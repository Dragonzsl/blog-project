package database

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

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
