package database

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/zhushilin/blog-project/db/migrations"
	"github.com/zhushilin/blog-project/internal/platform/config"
)

func TestOpenMigratesAndConfiguresSQLite(t *testing.T) {
	cfg := config.Database{
		Path:            filepath.Join(t.TempDir(), "nested", "blog.sqlite"),
		BusyTimeout:     config.Duration{Duration: time.Second},
		CacheSizeKiB:    4096,
		ReadConnections: 2,
	}
	db, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	if err := db.Ready(context.Background()); err != nil {
		t.Fatalf("Ready() error = %v", err)
	}
	version, err := db.MigrationVersion(context.Background())
	if err != nil {
		t.Fatalf("MigrationVersion() error = %v", err)
	}
	if version != LatestMigrationVersion {
		t.Fatalf("migration version = %d, want %d", version, LatestMigrationVersion)
	}
	if got := db.Writer.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("writer connections = %d, want 1", got)
	}
	if got := db.Reader.Stats().MaxOpenConnections; got != 2 {
		t.Fatalf("reader connections = %d, want 2", got)
	}
}

func TestRenderEpochHookTracksCommittedInvalidationsOnly(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, config.Database{
		Path: filepath.Join(t.TempDir(), "epoch.sqlite"), BusyTimeout: config.Duration{Duration: time.Second},
		CacheSizeKiB: 4096, ReadConnections: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	before := db.RenderEpoch()
	tx, err := db.Writer.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE system_state SET render_epoch=render_epoch+1 WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if got := db.RenderEpoch(); got != before {
		t.Fatalf("rolled-back render epoch=%d, want %d", got, before)
	}
	if _, err := db.Writer.ExecContext(ctx, "UPDATE system_state SET render_epoch=render_epoch+1 WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	if got := db.RenderEpoch(); got != before+1 {
		t.Fatalf("committed render epoch=%d, want %d", got, before+1)
	}
}

func TestPhaseOneMigrationUpgradesVersionTenDatabaseAndIsRestartSafe(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "upgrade.sqlite")
	cfg := config.Database{Path: path, BusyTimeout: config.Duration{Duration: time.Second}, CacheSizeKiB: 4096, ReadConnections: 2}
	db, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	goose.SetBaseFS(migrations.Files)
	if err := goose.DownTo(db.Writer, ".", 10); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if version, err := upgraded.MigrationVersion(ctx); err != nil || version != LatestMigrationVersion {
		upgraded.Close()
		t.Fatalf("version=%d err=%v", version, err)
	}
	for _, table := range []string{"request_idempotencies", "public_write_fingerprints", "newsletter_tokens", "event_outbox"} {
		var count int
		if err := upgraded.Reader.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil {
			upgraded.Close()
			t.Fatalf("table %s missing: %v", table, err)
		}
	}
	if err := upgraded.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if version, err := restarted.MigrationVersion(ctx); err != nil || version != LatestMigrationVersion {
		t.Fatalf("restart version=%d err=%v", version, err)
	}
}

func TestSnapshotIsConsistentAndDoesNotOverwrite(t *testing.T) {
	ctx := context.Background()
	cfg := config.Database{Path: filepath.Join(t.TempDir(), "live.sqlite"), BusyTimeout: config.Duration{Duration: time.Second}, CacheSizeKiB: 4096, ReadConnections: 2}
	db, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Writer.ExecContext(ctx, "UPDATE system_state SET render_epoch=42 WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(t.TempDir(), "snapshot.sqlite")
	if err := db.Snapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	if version, err := ValidateSnapshot(ctx, snapshot); err != nil || version != LatestMigrationVersion {
		t.Fatalf("snapshot version=%d err=%v", version, err)
	}
	info, err := os.Stat(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot mode=%v, want 0600", info.Mode().Perm())
	}
	if err := db.Snapshot(ctx, snapshot); err == nil {
		t.Fatal("snapshot overwrote an existing destination")
	}
}
