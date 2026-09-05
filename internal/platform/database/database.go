package database

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/mattn/go-sqlite3"
	"github.com/pressly/goose/v3"
	"github.com/zhushilin/blog-project/db/migrations"
	"github.com/zhushilin/blog-project/internal/platform/config"
)

type DB struct {
	Writer      *sql.DB
	Reader      *sql.DB
	renderEpoch atomic.Int64
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

	db := &DB{Writer: writer, Reader: reader}
	var epoch int64
	if err := reader.QueryRowContext(ctx, "SELECT render_epoch FROM system_state WHERE id = 1").Scan(&epoch); err != nil || epoch < 1 {
		reader.Close()
		writer.Close()
		if err != nil {
			return nil, fmt.Errorf("initialize render epoch: %w", err)
		}
		return nil, fmt.Errorf("initialize render epoch: invalid value %d", epoch)
	}
	db.renderEpoch.Store(epoch)
	if err := db.registerRenderEpochHooks(ctx); err != nil {
		reader.Close()
		writer.Close()
		return nil, err
	}
	return db, nil
}

// RenderEpoch is maintained in-process by SQLite's commit hook. Public page
// reads therefore do not need a system_state query on every request, while a
// rolled-back transaction cannot invalidate the page cache.
func (db *DB) RenderEpoch() int64 {
	if db == nil {
		return 0
	}
	return db.renderEpoch.Load()
}

func (db *DB) registerRenderEpochHooks(ctx context.Context) error {
	connection, err := db.Writer.Conn(ctx)
	if err != nil {
		return fmt.Errorf("open render epoch hook connection: %w", err)
	}
	defer connection.Close()
	if err := connection.Raw(func(driverConnection any) error {
		sqliteConnection, ok := driverConnection.(*sqlite3.SQLiteConn)
		if !ok {
			return fmt.Errorf("unexpected SQLite driver connection %T", driverConnection)
		}
		pending := false
		sqliteConnection.RegisterUpdateHook(func(_ int, _ string, table string, _ int64) {
			if table == "system_state" {
				pending = true
			}
		})
		sqliteConnection.RegisterCommitHook(func() int {
			if pending {
				db.renderEpoch.Add(1)
				pending = false
			}
			return 0
		})
		sqliteConnection.RegisterRollbackHook(func() { pending = false })
		return nil
	}); err != nil {
		return fmt.Errorf("register render epoch hooks: %w", err)
	}
	return nil
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
