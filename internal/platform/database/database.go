package database

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/pressly/goose/v3"
	"github.com/zhushilin/blog-project/db/migrations"
	"github.com/zhushilin/blog-project/internal/platform/config"
)

type DB struct {
	Writer *sql.DB
	Reader *sql.DB
}

func Open(ctx context.Context, cfg config.Database) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(cfg.Path), 0o700); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}

	writer, err := sql.Open("sqlite3", dataSourceName(cfg, false))
	if err != nil {
		return nil, fmt.Errorf("open database writer: %w", err)
	}
	writer.SetMaxOpenConns(1)
	writer.SetMaxIdleConns(1)
	if err := writer.PingContext(ctx); err != nil {
		writer.Close()
		return nil, fmt.Errorf("connect database writer: %w", err)
	}
	if err := migrate(ctx, writer); err != nil {
		writer.Close()
		return nil, err
	}
	if err := verifyWriter(ctx, writer); err != nil {
		writer.Close()
		return nil, err
	}

	reader, err := sql.Open("sqlite3", dataSourceName(cfg, true))
	if err != nil {
		writer.Close()
		return nil, fmt.Errorf("open database reader: %w", err)
	}
	reader.SetMaxOpenConns(cfg.ReadConnections)
	reader.SetMaxIdleConns(cfg.ReadConnections)
	reader.SetConnMaxIdleTime(5 * time.Minute)
	if err := reader.PingContext(ctx); err != nil {
		reader.Close()
		writer.Close()
		return nil, fmt.Errorf("connect database reader: %w", err)
	}

	return &DB{Writer: writer, Reader: reader}, nil
}

func (db *DB) Close() error {
	var readerErr, writerErr error
	if db.Reader != nil {
		readerErr = db.Reader.Close()
	}
	if db.Writer != nil {
		writerErr = db.Writer.Close()
	}
	if readerErr != nil {
		return fmt.Errorf("close database reader: %w", readerErr)
	}
	if writerErr != nil {
		return fmt.Errorf("close database writer: %w", writerErr)
	}
	return nil
}

func (db *DB) Ready(ctx context.Context) error {
	if db == nil || db.Writer == nil || db.Reader == nil {
		return fmt.Errorf("database is not open")
	}
	if err := db.Writer.PingContext(ctx); err != nil {
		return fmt.Errorf("database writer: %w", err)
	}
	var epoch int64
	if err := db.Reader.QueryRowContext(ctx, "SELECT render_epoch FROM system_state WHERE id = 1").Scan(&epoch); err != nil {
		return fmt.Errorf("database schema: %w", err)
	}
	if epoch < 1 {
		return fmt.Errorf("database render epoch is invalid")
	}
	return nil
}

func (db *DB) MigrationVersion(ctx context.Context) (int64, error) {
	version, err := goose.GetDBVersionContext(ctx, db.Writer)
	if err != nil {
		return 0, fmt.Errorf("read migration version: %w", err)
	}
	return version, nil
}

func migrate(ctx context.Context, writer *sql.DB) error {
	goose.SetBaseFS(migrations.Files)
	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("configure migrations: %w", err)
	}
	if err := goose.UpContext(ctx, writer, "."); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

func verifyWriter(ctx context.Context, writer *sql.DB) error {
	var journalMode string
	if err := writer.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		return fmt.Errorf("verify SQLite journal mode: %w", err)
	}
	if journalMode != "wal" {
		return fmt.Errorf("verify SQLite journal mode: got %q, want wal", journalMode)
	}
	var foreignKeys int
	if err := writer.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return fmt.Errorf("verify SQLite foreign keys: %w", err)
	}
	if foreignKeys != 1 {
		return fmt.Errorf("verify SQLite foreign keys: disabled")
	}
	var fts5Enabled int
	if err := writer.QueryRowContext(ctx, "SELECT sqlite_compileoption_used('ENABLE_FTS5')").Scan(&fts5Enabled); err != nil {
		return fmt.Errorf("verify SQLite FTS5 support: %w", err)
	}
	if fts5Enabled != 1 {
		return fmt.Errorf("verify SQLite FTS5 support: binary was built without FTS5")
	}
	return nil
}

func dataSourceName(cfg config.Database, readOnly bool) string {
	query := url.Values{}
	query.Set("_busy_timeout", strconv.FormatInt(cfg.BusyTimeout.Milliseconds(), 10))
	query.Set("_cache_size", strconv.Itoa(-cfg.CacheSizeKiB))
	query.Set("_foreign_keys", "on")
	if readOnly {
		query.Set("mode", "ro")
		query.Set("_query_only", "on")
	} else {
		query.Set("_journal_mode", "WAL")
		query.Set("_synchronous", "NORMAL")
		query.Set("_txlock", "immediate")
	}
	return (&url.URL{Scheme: "file", Path: cfg.Path, RawQuery: query.Encode()}).String()
}
