package operations

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	Destination        string
	ObjectKey          string
	Reason             string
	SizeBytes          int64
	Encrypted          bool
	ApplicationVersion string
	MigrationVersion   int64
	VerifiedAt         *time.Time
	RestoreTestedAt    *time.Time
	CreatedAt          time.Time
}

type Status struct {
	MigrationVersion int64
	PendingJobs      int
	RunningJobs      int
	FailedJobs       int
	OldestFailedAt   *time.Time
	LastTaskError    string
	ValidBackups     int
	LastBackupAt     *time.Time
	LastRestoreTest  *time.Time
}

type OperationsSummary struct {
	MigrationVersion       int64
	Ready                  bool
	Tasks                  TaskSnapshot
	ValidBackups           int
	LastBackupAt           *time.Time
	LastBackupDestination  string
	LastBackupEncrypted    bool
	LastBackupVerifiedAt   *time.Time
	LastRestoreTest        *time.Time
	MediaCount             int
	VariantCount           int
	StorageMigrationState  string
	StorageMigrationCopied int
	StorageMigrationTotal  int
	StorageMigrationError  string
	CacheEntries           int
	CacheBytes             int64
	CacheEpoch             int64
	CacheScanLimited       bool
}

// ReadOperationsSummary gathers only bounded aggregates. It intentionally
// returns target type and relative timestamps, never backup paths, object
// keys, endpoints, payloads, or credentials.
func ReadOperationsSummary(ctx context.Context, db *database.DB, queue *TaskQueue, cacheDir string) (OperationsSummary, error) {
	if db == nil || db.Reader == nil {
		return OperationsSummary{}, fmt.Errorf("operations database is not configured")
	}
	version, err := db.MigrationVersion(ctx)
	if err != nil {
		return OperationsSummary{}, err
	}
	summary := OperationsSummary{MigrationVersion: version, Ready: db.Ready(ctx) == nil}
	if queue != nil {
		summary.Tasks, err = queue.Snapshot(ctx)
		if err != nil {
			return OperationsSummary{}, err
		}
	}
	var encrypted int
	var backupAt, verifiedAt, restoreAt sql.NullInt64
	err = db.Reader.QueryRowContext(ctx, `SELECT count(*),MAX(created_at),
		COALESCE((SELECT destination FROM backups WHERE checksum_status='valid' ORDER BY created_at DESC,id DESC LIMIT 1),''),
		COALESCE((SELECT encrypted FROM backups WHERE checksum_status='valid' ORDER BY created_at DESC,id DESC LIMIT 1),0),
		(SELECT verified_at FROM backups WHERE checksum_status='valid' ORDER BY created_at DESC,id DESC LIMIT 1),
		(SELECT restore_tested_at FROM backups WHERE checksum_status='valid' ORDER BY created_at DESC,id DESC LIMIT 1)
		FROM backups WHERE checksum_status='valid'`).Scan(&summary.ValidBackups, &backupAt, &summary.LastBackupDestination, &encrypted, &verifiedAt, &restoreAt)
	if err != nil {
		return OperationsSummary{}, err
	}
	summary.LastBackupEncrypted = encrypted != 0
	summary.LastBackupAt = nullableMillisTime(backupAt)
	summary.LastBackupVerifiedAt = nullableMillisTime(verifiedAt)
	summary.LastRestoreTest = nullableMillisTime(restoreAt)
	if err := db.Reader.QueryRowContext(ctx, "SELECT count(*),(SELECT count(*) FROM media_variants WHERE status='ready') FROM media").Scan(&summary.MediaCount, &summary.VariantCount); err != nil {
		return OperationsSummary{}, err
	}
	var migrationError string
	_ = db.Reader.QueryRowContext(ctx, `SELECT status,copied_objects,total_objects,COALESCE(last_error,'') FROM storage_migrations ORDER BY id DESC LIMIT 1`, &summary.StorageMigrationState, &summary.StorageMigrationCopied, &summary.StorageMigrationTotal, &migrationError)
	summary.StorageMigrationError = truncateError(migrationError)
	_ = db.Reader.QueryRowContext(ctx, "SELECT render_epoch FROM system_state WHERE id=1").Scan(&summary.CacheEpoch)
	summary.CacheEntries, summary.CacheBytes, summary.CacheScanLimited = scanCacheSummary(cacheDir)
	return summary, nil
}

func scanCacheSummary(directory string) (int, int64, bool) {
	if strings.TrimSpace(directory) == "" {
		return 0, 0, false
	}
	entries, err := os.ReadDir(filepath.Clean(directory))
	if err != nil {
		return 0, 0, false
	}
	const maxEntries = 4096
	count := 0
	var bytes int64
	limited := false
	for _, entry := range entries {
		if count >= maxEntries {
			limited = true
			break
		}
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "epoch-") || !strings.HasSuffix(entry.Name(), ".cache") {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		count++
		bytes += info.Size()
	}
	return count, bytes, limited
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
	rows, err := db.Reader.QueryContext(ctx, `SELECT public_id,manifest_path,destination,object_key,reason,size_bytes,encrypted,application_version,migration_version,verified_at,restore_tested_at,created_at FROM backups WHERE checksum_status='valid' ORDER BY created_at DESC,id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list backups: %w", err)
	}
	defer rows.Close()
	var records []BackupRecord
	for rows.Next() {
		var record BackupRecord
		var publicID []byte
		var encrypted int
		var verifiedAt, restoreTested sql.NullInt64
		var created int64
		if err := rows.Scan(&publicID, &record.Path, &record.Destination, &record.ObjectKey, &record.Reason, &record.SizeBytes, &encrypted, &record.ApplicationVersion, &record.MigrationVersion, &verifiedAt, &restoreTested, &created); err != nil {
			return nil, err
		}
		record.PublicID = hex.EncodeToString(publicID)
		record.Encrypted = encrypted != 0
		record.CreatedAt = time.UnixMilli(created).UTC()
		if verifiedAt.Valid {
			value := time.UnixMilli(verifiedAt.Int64).UTC()
			record.VerifiedAt = &value
		}
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
	var oldestFailed, lastBackup, lastRestore sql.NullInt64
	var lastTaskError sql.NullString
	if err := db.Reader.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM jobs WHERE status='pending'),
		(SELECT count(*) FROM jobs WHERE status='running'),
		(SELECT count(*) FROM jobs WHERE status='failed'),
		(SELECT MIN(updated_at) FROM jobs WHERE status='failed'),
		(SELECT COALESCE(last_error,'') FROM jobs WHERE status='failed' ORDER BY updated_at DESC,id DESC LIMIT 1),
		(SELECT count(*) FROM backups WHERE checksum_status='valid'),
		(SELECT MAX(created_at) FROM backups WHERE checksum_status='valid'),
		(SELECT MAX(restore_tested_at) FROM backups WHERE restore_tested_at IS NOT NULL)
	`).Scan(&status.PendingJobs, &status.RunningJobs, &status.FailedJobs, &oldestFailed, &lastTaskError, &status.ValidBackups, &lastBackup, &lastRestore); err != nil {
		return Status{}, fmt.Errorf("read operational status: %w", err)
	}
	status.LastTaskError = truncateError(lastTaskError.String)
	if oldestFailed.Valid {
		value := time.UnixMilli(oldestFailed.Int64).UTC()
		status.OldestFailedAt = &value
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
