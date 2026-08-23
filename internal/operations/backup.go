package operations

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zhushilin/blog-project/internal/platform/database"
	platformid "github.com/zhushilin/blog-project/internal/platform/id"
)

type BackupOptions struct {
	DataDir            string
	DatabasePath       string
	BackupDir          string
	ApplicationVersion string
	ApplicationCommit  string
	Interval           time.Duration
	DailyRetention     int
	WeeklyRetention    int
}

type BackupResult struct {
	Path      string
	Manifest  BackupManifest
	SizeBytes int64
}

type BackupService struct {
	database *database.DB
	options  BackupOptions
	now      func() time.Time
}

func NewBackupService(db *database.DB, options BackupOptions) (*BackupService, error) {
	if db == nil {
		return nil, fmt.Errorf("backup database is required")
	}
	var err error
	options.DataDir, err = filepath.Abs(options.DataDir)
	if err != nil || options.DataDir == string(filepath.Separator) {
		return nil, fmt.Errorf("backup data directory is invalid")
	}
	options.DatabasePath, err = filepath.Abs(options.DatabasePath)
	if err != nil {
		return nil, fmt.Errorf("backup database path is invalid")
	}
	databaseRelative, err := filepath.Rel(options.DataDir, options.DatabasePath)
	if err != nil || databaseRelative == "." || strings.HasPrefix(databaseRelative, ".."+string(filepath.Separator)) || databaseRelative == ".." {
		return nil, fmt.Errorf("backup database must be inside the data directory")
	}
	if options.BackupDir == "" {
		options.BackupDir = filepath.Join(options.DataDir, "backups")
	}
	options.BackupDir, err = filepath.Abs(options.BackupDir)
	if err != nil {
		return nil, fmt.Errorf("backup destination is invalid")
	}
	if options.Interval <= 0 {
		options.Interval = 24 * time.Hour
	}
	if options.DailyRetention < 1 {
		options.DailyRetention = 7
	}
	if options.WeeklyRetention < 0 {
		options.WeeklyRetention = 4
	}
	return &BackupService{database: db, options: options, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (s *BackupService) Create(ctx context.Context, reason, output string) (BackupResult, error) {
	if !validBackupReason(reason) {
		return BackupResult{}, fmt.Errorf("backup reason must be manual, scheduled, or pre_upgrade")
	}
	now := s.now().UTC()
	publicID, err := platformid.NewPublicID(now)
	if err != nil {
		return BackupResult{}, err
	}
	encodedID, err := platformid.EncodePublicID(publicID)
	if err != nil {
		return BackupResult{}, err
	}
	if err := os.MkdirAll(s.options.BackupDir, 0o700); err != nil {
		return BackupResult{}, fmt.Errorf("create backup directory: %w", err)
	}
	if output == "" {
		output = filepath.Join(s.options.BackupDir, "blog-"+now.Format("20060102T150405Z")+"-"+encodedID[:12]+".tar.gz")
	} else {
		output, err = filepath.Abs(output)
		if err != nil {
			return BackupResult{}, fmt.Errorf("resolve backup output: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(output), 0o700); err != nil {
			return BackupResult{}, fmt.Errorf("create backup output directory: %w", err)
		}
	}
	if !strings.HasSuffix(strings.ToLower(output), ".tar.gz") {
		return BackupResult{}, fmt.Errorf("backup output must end in .tar.gz")
	}
	temporary, err := os.MkdirTemp(s.options.BackupDir, ".backup-work-")
	if err != nil {
		return BackupResult{}, fmt.Errorf("create backup work directory: %w", err)
	}
	defer os.RemoveAll(temporary)
	snapshotPath := filepath.Join(temporary, "blog.sqlite")
	if err := s.database.Snapshot(ctx, snapshotPath); err != nil {
		return BackupResult{}, err
	}
	migrationVersion, err := database.ValidateSnapshot(ctx, snapshotPath)
	if err != nil {
		return BackupResult{}, err
	}
	databaseRelative, _ := filepath.Rel(s.options.DataDir, s.options.DatabasePath)
	databaseArchivePath := filepath.ToSlash(databaseRelative)
	snapshotInfo, err := os.Stat(snapshotPath)
	if err != nil {
		return BackupResult{}, err
	}
	sources := []archiveSource{{archivePath: databaseArchivePath, filePath: snapshotPath, size: snapshotInfo.Size()}}
	for _, directory := range []string{"media", "secrets", "themes", "plugins"} {
		tree, err := collectBackupTree(s.options.DataDir, directory)
		if err != nil {
			return BackupResult{}, err
		}
		sources = append(sources, tree...)
	}
	manifest := BackupManifest{
		Format: backupFormat, FormatVersion: backupFormatVersion, PublicID: encodedID, Reason: reason,
		CreatedAt: now, ApplicationVersion: s.options.ApplicationVersion, ApplicationCommit: s.options.ApplicationCommit,
		MigrationVersion: migrationVersion, DatabasePath: databaseArchivePath,
	}
	partial := output + ".partial-" + encodedID[:12]
	if err := writeBackupArchive(ctx, partial, manifest, sources); err != nil {
		return BackupResult{}, err
	}
	if _, err := os.Lstat(output); err == nil {
		_ = os.Remove(partial)
		return BackupResult{}, fmt.Errorf("backup output already exists")
	} else if !os.IsNotExist(err) {
		_ = os.Remove(partial)
		return BackupResult{}, fmt.Errorf("inspect backup output: %w", err)
	}
	if err := os.Rename(partial, output); err != nil {
		_ = os.Remove(partial)
		return BackupResult{}, fmt.Errorf("publish backup archive: %w", err)
	}
	if err := syncDirectory(filepath.Dir(output)); err != nil {
		return BackupResult{}, err
	}
	verified, err := VerifyBackup(ctx, output)
	if err != nil {
		return BackupResult{}, fmt.Errorf("verify created backup: %w", err)
	}
	if verified.Manifest.PublicID != encodedID {
		return BackupResult{}, fmt.Errorf("created backup identity changed during verification")
	}
	if err := s.recordBackup(ctx, publicID, output, verified, reason, now); err != nil {
		return BackupResult{}, err
	}
	return BackupResult{Path: output, Manifest: verified.Manifest, SizeBytes: verified.SizeBytes}, nil
}

func (s *BackupService) CreateScheduledIfDue(ctx context.Context) (*BackupResult, error) {
	var lastCreated sql.NullInt64
	if err := s.database.Reader.QueryRowContext(ctx, "SELECT MAX(created_at) FROM backups WHERE reason='scheduled' AND checksum_status='valid'").Scan(&lastCreated); err != nil {
		return nil, fmt.Errorf("read last scheduled backup: %w", err)
	}
	if lastCreated.Valid && s.now().UTC().Sub(time.UnixMilli(lastCreated.Int64).UTC()) < s.options.Interval {
		return nil, nil
	}
	result, err := s.Create(ctx, "scheduled", "")
	if err != nil {
		return nil, err
	}
	if err := s.PruneScheduled(ctx); err != nil {
		return &result, err
	}
	return &result, nil
}

func (s *BackupService) MarkRestoreTested(ctx context.Context, manifest BackupManifest) error {
	publicID, err := platformid.DecodePublicID(manifest.PublicID)
	if err != nil {
		return err
	}
	now := s.now().UTC()
	result, err := s.database.Writer.ExecContext(ctx, "UPDATE backups SET restore_tested_at=? WHERE public_id=?", now.UnixMilli(), publicID)
	if err != nil {
		return fmt.Errorf("mark backup restore test: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return fmt.Errorf("backup is not registered in this instance")
	}
	contextJSON, _ := json.Marshal(map[string]any{"backup_id": manifest.PublicID})
	_, err = s.database.Writer.ExecContext(ctx, `INSERT INTO audit_entries(action,object_kind,object_public_id,result,context_json,created_at) VALUES('operations.backup.restore_tested','backup',?,'succeeded',?,?)`, publicID, string(contextJSON), now.UnixMilli())
	return err
}

func (s *BackupService) PruneScheduled(ctx context.Context) error {
	rows, err := s.database.Reader.QueryContext(ctx, "SELECT id,public_id,manifest_path,created_at FROM backups WHERE reason='scheduled' AND checksum_status='valid' ORDER BY created_at DESC,id DESC")
	if err != nil {
		return fmt.Errorf("list scheduled backups: %w", err)
	}
	type record struct {
		id       int64
		publicID []byte
		path     string
		created  time.Time
	}
	var records []record
	for rows.Next() {
		var value record
		var created int64
		if err := rows.Scan(&value.id, &value.publicID, &value.path, &created); err != nil {
			rows.Close()
			return err
		}
		value.created = time.UnixMilli(created).UTC()
		records = append(records, value)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	days := make(map[string]struct{})
	weeks := make(map[string]struct{})
	for _, value := range records {
		keep := false
		day := value.created.Format("2006-01-02")
		if _, exists := days[day]; !exists && len(days) < s.options.DailyRetention {
			days[day] = struct{}{}
			keep = true
		}
		year, week := value.created.ISOWeek()
		weekKey := fmt.Sprintf("%04d-%02d", year, week)
		if _, exists := weeks[weekKey]; !exists && len(weeks) < s.options.WeeklyRetention {
			weeks[weekKey] = struct{}{}
			keep = true
		}
		if keep {
			continue
		}
		inside, err := pathInside(s.options.BackupDir, value.path)
		if err != nil || !inside {
			return fmt.Errorf("refuse to prune backup outside configured directory: %q", value.path)
		}
		if err := os.Remove(value.path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove expired backup: %w", err)
		}
		if _, err := s.database.Writer.ExecContext(ctx, "DELETE FROM backups WHERE id=?", value.id); err != nil {
			return fmt.Errorf("remove expired backup record: %w", err)
		}
		if _, err := s.database.Writer.ExecContext(ctx, `INSERT INTO audit_entries(action,object_kind,object_public_id,result,context_json,created_at) VALUES('operations.backup.pruned','backup',?,'succeeded','{}',?)`, value.publicID, s.now().UTC().UnixMilli()); err != nil {
			return fmt.Errorf("audit expired backup removal: %w", err)
		}
	}
	return nil
}

func (s *BackupService) recordBackup(ctx context.Context, publicID []byte, path string, verified VerifiedBackup, reason string, now time.Time) error {
	tx, err := s.database.Writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO backups(public_id,manifest_path,destination,size_bytes,checksum_status,encrypted,created_at,reason,application_version,migration_version) VALUES(?,?,'local',?,'valid',0,?,?,?,?)`, publicID, path, verified.SizeBytes, now.UnixMilli(), reason, verified.Manifest.ApplicationVersion, verified.Manifest.MigrationVersion); err != nil {
		return fmt.Errorf("record backup: %w", err)
	}
	contextJSON, _ := json.Marshal(map[string]any{"reason": reason, "size_bytes": verified.SizeBytes, "migration_version": verified.Manifest.MigrationVersion})
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_entries(action,object_kind,object_public_id,result,context_json,created_at) VALUES('operations.backup.created','backup',?,'succeeded',?,?)`, publicID, string(contextJSON), now.UnixMilli()); err != nil {
		return fmt.Errorf("audit backup: %w", err)
	}
	return tx.Commit()
}

func collectBackupTree(dataDir, directory string) ([]archiveSource, error) {
	root := filepath.Join(dataDir, directory)
	info, err := os.Lstat(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect backup source %q: %w", directory, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("backup source %q must be a real directory", directory)
	}
	var sources []archiveSource
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("backup source contains symbolic link %q", path)
		}
		if entry.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("backup source contains non-regular file %q", path)
		}
		relative, err := filepath.Rel(dataDir, path)
		if err != nil {
			return err
		}
		sources = append(sources, archiveSource{archivePath: filepath.ToSlash(relative), filePath: path, size: info.Size()})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("collect backup source %q: %w", directory, err)
	}
	return sources, nil
}

func pathInside(root, path string) (bool, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return false, err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return false, err
	}
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return false, err
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)), nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open directory for sync: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync directory: %w", err)
	}
	return nil
}
