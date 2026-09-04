package database

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const LatestMigrationVersion int64 = 13

// Snapshot writes a transactionally consistent, compact SQLite copy. VACUUM
// INTO reads through SQLite instead of copying the live database and ignoring
// committed pages that may still reside in the WAL.
func (db *DB) Snapshot(ctx context.Context, destination string) error {
	if db == nil || db.Writer == nil {
		return fmt.Errorf("database is not open")
	}
	destination, err := filepath.Abs(destination)
	if err != nil {
		return fmt.Errorf("resolve database snapshot path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return fmt.Errorf("create database snapshot directory: %w", err)
	}
	if _, err := os.Lstat(destination); err == nil {
		return fmt.Errorf("database snapshot destination already exists")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect database snapshot destination: %w", err)
	}
	quoted := "'" + strings.ReplaceAll(destination, "'", "''") + "'"
	if _, err := db.Writer.ExecContext(ctx, "VACUUM INTO "+quoted); err != nil {
		return fmt.Errorf("create consistent database snapshot: %w", err)
	}
	if err := os.Chmod(destination, 0o600); err != nil {
		return fmt.Errorf("protect database snapshot: %w", err)
	}
	if _, err := ValidateSnapshot(ctx, destination); err != nil {
		return err
	}
	return nil
}

// ValidateSnapshot verifies SQLite page integrity and returns the schema
// migration version without changing the snapshot.
func ValidateSnapshot(ctx context.Context, path string) (int64, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return 0, fmt.Errorf("resolve database snapshot path: %w", err)
	}
	query := url.Values{}
	query.Set("mode", "ro")
	query.Set("immutable", "1")
	query.Set("_query_only", "on")
	handle, err := sql.Open("sqlite3", (&url.URL{Scheme: "file", Path: absolute, RawQuery: query.Encode()}).String())
	if err != nil {
		return 0, fmt.Errorf("open database snapshot: %w", err)
	}
	defer handle.Close()
	var integrity string
	if err := handle.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
		return 0, fmt.Errorf("check database snapshot integrity: %w", err)
	}
	if integrity != "ok" {
		return 0, fmt.Errorf("database snapshot integrity check returned %q", integrity)
	}
	var version int64
	if err := handle.QueryRowContext(ctx, "SELECT version_id FROM goose_db_version WHERE is_applied=1 ORDER BY id DESC LIMIT 1").Scan(&version); err != nil {
		return 0, fmt.Errorf("read database snapshot migration version: %w", err)
	}
	return version, nil
}
