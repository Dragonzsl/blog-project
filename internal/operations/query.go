package operations

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/zhushilin/blog-project/internal/platform/database"
)

type AuditEntry struct {
	ID          int64
	Action      string
	ObjectKind  string
	ObjectID    string
	Result      string
	RequestID   string
	ContextJSON string
	CreatedAt   time.Time
}

type BackupRecord struct {
	PublicID           string
	Path               string
	Reason             string
	SizeBytes          int64
	ApplicationVersion string
	MigrationVersion   int64
	RestoreTestedAt    *time.Time
	CreatedAt          time.Time
}

type Status struct {
	MigrationVersion int64
	FailedJobs       int
	ValidBackups     int
	LastBackupAt     *time.Time
	LastRestoreTest  *time.Time
}

func RecentAudit(ctx context.Context, db *database.DB, limit int) ([]AuditEntry, error) {
	if limit < 1 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	rows, err := db.Reader.QueryContext(ctx, `SELECT id,action,COALESCE(object_kind,''),COALESCE(object_public_id,x''),result,COALESCE(request_id,''),context_json,created_at FROM audit_entries ORDER BY created_at DESC,id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list audit entries: %w", err)
	}
	defer rows.Close()
	var entries []AuditEntry
	for rows.Next() {
		var entry AuditEntry
		var objectID []byte
		var created int64
		if err := rows.Scan(&entry.ID, &entry.Action, &entry.ObjectKind, &objectID, &entry.Result, &entry.RequestID, &entry.ContextJSON, &created); err != nil {
			return nil, err
		}
		entry.ObjectID = hex.EncodeToString(objectID)
		entry.CreatedAt = time.UnixMilli(created).UTC()
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func ListBackups(ctx context.Context, db *database.DB, limit int) ([]BackupRecord, error) {
	if limit < 1 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}
	rows, err := db.Reader.QueryContext(ctx, `SELECT public_id,manifest_path,reason,size_bytes,application_version,migration_version,restore_tested_at,created_at FROM backups WHERE checksum_status='valid' ORDER BY created_at DESC,id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list backups: %w", err)
	}
	defer rows.Close()
	var records []BackupRecord
	for rows.Next() {
		var record BackupRecord
		var publicID []byte
		var restoreTested sql.NullInt64
		var created int64
		if err := rows.Scan(&publicID, &record.Path, &record.Reason, &record.SizeBytes, &record.ApplicationVersion, &record.MigrationVersion, &restoreTested, &created); err != nil {
			return nil, err
		}
		record.PublicID = hex.EncodeToString(publicID)
		record.CreatedAt = time.UnixMilli(created).UTC()
		if restoreTested.Valid {
			value := time.UnixMilli(restoreTested.Int64).UTC()
			record.RestoreTestedAt = &value
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func ReadStatus(ctx context.Context, db *database.DB) (Status, error) {
	version, err := db.MigrationVersion(ctx)
	if err != nil {
		return Status{}, err
	}
	status := Status{MigrationVersion: version}
	var lastBackup, lastRestore sql.NullInt64
	if err := db.Reader.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM jobs WHERE status='failed'),
		(SELECT count(*) FROM backups WHERE checksum_status='valid'),
		(SELECT MAX(created_at) FROM backups WHERE checksum_status='valid'),
		(SELECT MAX(restore_tested_at) FROM backups WHERE restore_tested_at IS NOT NULL)
	`).Scan(&status.FailedJobs, &status.ValidBackups, &lastBackup, &lastRestore); err != nil {
		return Status{}, fmt.Errorf("read operational status: %w", err)
	}
	if lastBackup.Valid {
		value := time.UnixMilli(lastBackup.Int64).UTC()
		status.LastBackupAt = &value
	}
	if lastRestore.Valid {
		value := time.UnixMilli(lastRestore.Int64).UTC()
		status.LastRestoreTest = &value
	}
	return status, nil
}
