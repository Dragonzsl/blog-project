package database

import (
	"context"
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
	if version != 1 {
		t.Fatalf("migration version = %d, want 1", version)
	}
	if got := db.Writer.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("writer connections = %d, want 1", got)
	}
	if got := db.Reader.Stats().MaxOpenConnections; got != 2 {
		t.Fatalf("reader connections = %d, want 2", got)
	}
}
