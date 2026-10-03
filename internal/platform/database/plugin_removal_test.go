package database

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/zhushilin/blog-project/db/migrations"
	"github.com/zhushilin/blog-project/internal/platform/config"
)

func TestPluginRemovalMigrationPreservesExistingStateAndRestarts(t *testing.T) {
	ctx := context.Background()
	cfg := config.Database{Path: filepath.Join(t.TempDir(), "upgrade.sqlite"), BusyTimeout: config.Duration{Duration: time.Second}, CacheSizeKiB: 4096, ReadConnections: 2}
	db, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	goose.SetBaseFS(migrations.Files)
	if err := goose.DownTo(db.Writer, ".", 20); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Writer.Exec(`INSERT INTO plugin_states(plugin_id,name,version,api_version,enabled,updated_at) VALUES('existing','Existing','1',1,1,0)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var enabled, removed bool
	if err := db.Reader.QueryRow("SELECT enabled,removed_at IS NOT NULL FROM plugin_states WHERE plugin_id='existing'").Scan(&enabled, &removed); err != nil || !enabled || removed {
		t.Fatalf("upgrade changed existing state: %v", err)
	}
	if _, err := db.Writer.Exec("UPDATE plugin_states SET removed_at=1 WHERE plugin_id='existing'"); err == nil {
		t.Fatal("enabled and removed state accepted")
	}
	if _, err := db.Writer.Exec("UPDATE plugin_states SET enabled=0,removed_at=-1 WHERE plugin_id='existing'"); err == nil {
		t.Fatal("negative removal time accepted")
	}
	if _, err := db.Writer.Exec("UPDATE plugin_states SET enabled=0,removed_at=1 WHERE plugin_id='existing'"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Reader.QueryRow("SELECT enabled,removed_at IS NOT NULL FROM plugin_states WHERE plugin_id='existing'").Scan(&enabled, &removed); err != nil || enabled || !removed {
		t.Fatalf("restart changed removal: %v", err)
	}
	if err := goose.DownTo(db.Writer, ".", 20); err != nil {
		t.Fatal(err)
	}
	if err := goose.Up(db.Writer, "."); err != nil {
		t.Fatal(err)
	}
	if err := db.Ready(ctx); err != nil {
		t.Fatal(err)
	}
}
