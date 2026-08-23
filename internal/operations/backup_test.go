package operations

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhushilin/blog-project/internal/platform/config"
	"github.com/zhushilin/blog-project/internal/platform/database"
	platformid "github.com/zhushilin/blog-project/internal/platform/id"
)

func TestBackupVerifyRestoreAndAuditRoundTrip(t *testing.T) {
	ctx := context.Background()
	sourceDir := t.TempDir()
	dbPath := filepath.Join(sourceDir, "db", "blog.sqlite")
	db := openOperationsDatabase(t, dbPath)
	if _, err := db.Writer.ExecContext(ctx, "UPDATE system_state SET render_epoch=99 WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(sourceDir, "media", "nested", "photo.jpg"), []byte("original-media"))
	writeTestFile(t, filepath.Join(sourceDir, "secrets", "auth.key"), []byte("01234567890123456789012345678901"))
	service, err := NewBackupService(db, BackupOptions{
		DataDir: sourceDir, DatabasePath: dbPath, ApplicationVersion: "test", ApplicationCommit: "abc123",
		Interval: 24 * time.Hour, DailyRetention: 7, WeeklyRetention: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return time.Date(2026, time.August, 23, 10, 0, 0, 0, time.UTC) }
	created, err := service.Create(ctx, "manual", "")
	if err != nil {
		t.Fatal(err)
	}
	if created.Manifest.MigrationVersion != database.LatestMigrationVersion || !hasBackupEntry(created.Manifest, "db/blog.sqlite") || !hasBackupEntry(created.Manifest, "media/nested/photo.jpg") || !hasBackupEntry(created.Manifest, "secrets/auth.key") {
		t.Fatalf("backup manifest=%+v", created.Manifest)
	}
	backupInfo, err := os.Stat(created.Path)
	if err != nil {
		t.Fatal(err)
	}
	if backupInfo.Mode().Perm() != 0o600 {
		t.Fatalf("backup permissions=%v", backupInfo.Mode().Perm())
	}
	verified, err := VerifyBackup(ctx, created.Path)
	if err != nil || verified.Manifest.PublicID != created.Manifest.PublicID {
		t.Fatalf("verified=%+v err=%v", verified, err)
	}
	var backupRecords, backupAudits int
	if err := db.Reader.QueryRowContext(ctx, "SELECT count(*) FROM backups WHERE public_id=? AND checksum_status='valid'", mustDecodeID(t, created.Manifest.PublicID)).Scan(&backupRecords); err != nil {
		t.Fatal(err)
	}
	if err := db.Reader.QueryRowContext(ctx, "SELECT count(*) FROM audit_entries WHERE action='operations.backup.created'").Scan(&backupAudits); err != nil {
		t.Fatal(err)
	}
	if backupRecords != 1 || backupAudits != 1 {
		t.Fatalf("backup records=%d audits=%d", backupRecords, backupAudits)
	}
	if _, err := db.Writer.ExecContext(ctx, "UPDATE system_state SET render_epoch=100 WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(sourceDir, "media", "nested", "photo.jpg"), []byte("changed-media"))

	targetDir := filepath.Join(t.TempDir(), "restored-data")
	restored, err := RestoreBackup(ctx, created.Path, targetDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if restored.RollbackDir != "" || restored.RestoredFiles != len(created.Manifest.Entries) {
		t.Fatalf("restore result=%+v", restored)
	}
	contents, err := os.ReadFile(filepath.Join(targetDir, "media", "nested", "photo.jpg"))
	if err != nil || string(contents) != "original-media" {
		t.Fatalf("restored media=%q err=%v", contents, err)
	}
	restoredDB := openOperationsDatabase(t, filepath.Join(targetDir, "db", "blog.sqlite"))
	var epoch, restoreAudits int
	if err := restoredDB.Reader.QueryRowContext(ctx, "SELECT render_epoch FROM system_state WHERE id=1").Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	if err := restoredDB.Reader.QueryRowContext(ctx, "SELECT count(*) FROM audit_entries WHERE action='operations.restore.completed'").Scan(&restoreAudits); err != nil {
		t.Fatal(err)
	}
	if epoch != 99 || restoreAudits != 1 {
		t.Fatalf("restored epoch=%d restore audits=%d", epoch, restoreAudits)
	}
	if err := service.MarkRestoreTested(ctx, created.Manifest); err != nil {
		t.Fatal(err)
	}
	var testedAt int64
	if err := db.Reader.QueryRowContext(ctx, "SELECT restore_tested_at FROM backups WHERE public_id=?", mustDecodeID(t, created.Manifest.PublicID)).Scan(&testedAt); err != nil || testedAt == 0 {
		t.Fatalf("restore tested at=%d err=%v", testedAt, err)
	}
	backups, err := ListBackups(ctx, db, 20)
	if err != nil || len(backups) != 1 || backups[0].RestoreTestedAt == nil {
		t.Fatalf("backup list=%+v err=%v", backups, err)
	}
	audit, err := RecentAudit(ctx, db, 20)
	if err != nil || len(audit) < 2 || audit[0].Action != "operations.backup.restore_tested" {
		t.Fatalf("audit=%+v err=%v", audit, err)
	}
	status, err := ReadStatus(ctx, db)
	if err != nil || status.MigrationVersion != database.LatestMigrationVersion || status.ValidBackups != 1 || status.LastRestoreTest == nil {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}

func TestBackupCorruptionSymlinkAndRestoreLockAreRejected(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "db", "blog.sqlite")
	db := openOperationsDatabase(t, dbPath)
	service, err := NewBackupService(db, BackupOptions{DataDir: dataDir, DatabasePath: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(ctx, "manual", "")
	if err != nil {
		t.Fatal(err)
	}
	corrupted := filepath.Join(t.TempDir(), "corrupted.tar.gz")
	archive, err := os.ReadFile(created.Path)
	if err != nil {
		t.Fatal(err)
	}
	archive[len(archive)/2] ^= 0xff
	if err := os.WriteFile(corrupted, archive, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBackup(ctx, corrupted); err == nil {
		t.Fatal("corrupted backup passed verification")
	}
	target := filepath.Join(t.TempDir(), "locked-data")
	lock, err := AcquireDataLock(target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreBackup(ctx, created.Path, target, true); !errors.Is(err, ErrDataLocked) {
		t.Fatalf("restore lock error=%v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "media"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dataDir, "db"), filepath.Join(dataDir, "media", "unsafe")); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(ctx, "manual", ""); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("symlink backup error=%v", err)
	}
}

func TestRestorePreservesExistingTargetAsRollback(t *testing.T) {
	ctx := context.Background()
	sourceDir := t.TempDir()
	dbPath := filepath.Join(sourceDir, "db", "blog.sqlite")
	db := openOperationsDatabase(t, dbPath)
	service, err := NewBackupService(db, BackupOptions{DataDir: sourceDir, DatabasePath: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(ctx, "manual", "")
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "data")
	writeTestFile(t, filepath.Join(target, "marker"), []byte("previous-data"))
	if _, err := RestoreBackup(ctx, created.Path, target, false); err == nil {
		t.Fatal("restore replaced an existing target without confirmation")
	}
	result, err := RestoreBackup(ctx, created.Path, target, true)
	if err != nil {
		t.Fatal(err)
	}
	marker, err := os.ReadFile(filepath.Join(result.RollbackDir, "marker"))
	if err != nil || string(marker) != "previous-data" {
		t.Fatalf("rollback marker=%q err=%v", marker, err)
	}
}

func TestScheduledBackupDueCheckAndRetention(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "db", "blog.sqlite")
	db := openOperationsDatabase(t, dbPath)
	service, err := NewBackupService(db, BackupOptions{DataDir: dataDir, DatabasePath: dbPath, Interval: 24 * time.Hour, DailyRetention: 2, WeeklyRetention: 1})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.August, 1, 2, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	first, err := service.CreateScheduledIfDue(ctx)
	if err != nil || first == nil {
		t.Fatalf("first scheduled backup=%+v err=%v", first, err)
	}
	if duplicate, err := service.CreateScheduledIfDue(ctx); err != nil || duplicate != nil {
		t.Fatalf("duplicate scheduled backup=%+v err=%v", duplicate, err)
	}
	for _, day := range []int{2, 3, 10} {
		now = time.Date(2026, time.August, day, 2, 0, 0, 0, time.UTC)
		if result, err := service.CreateScheduledIfDue(ctx); err != nil || result == nil {
			t.Fatalf("day %d scheduled backup=%+v err=%v", day, result, err)
		}
	}
	var scheduled int
	if err := db.Reader.QueryRowContext(ctx, "SELECT count(*) FROM backups WHERE reason='scheduled'").Scan(&scheduled); err != nil {
		t.Fatal(err)
	}
	if scheduled != 2 {
		t.Fatalf("scheduled backups after retention=%d, want 2", scheduled)
	}
}

func openOperationsDatabase(t *testing.T, path string) *database.DB {
	t.Helper()
	db, err := database.Open(context.Background(), config.Database{Path: path, BusyTimeout: config.Duration{Duration: time.Second}, CacheSizeKiB: 4096, ReadConnections: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func writeTestFile(t *testing.T, path string, contents []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
}

func hasBackupEntry(manifest BackupManifest, path string) bool {
	for _, entry := range manifest.Entries {
		if entry.Path == path {
			return true
		}
	}
	return false
}

func mustDecodeID(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := platformid.DecodePublicID(value)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}
