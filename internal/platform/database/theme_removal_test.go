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

func TestThemeRemovalMigrationPreservesExistingStateAndRestarts(t *testing.T) {
	ctx := context.Background()
	cfg := config.Database{Path: filepath.Join(t.TempDir(), "upgrade.sqlite"), BusyTimeout: config.Duration{Duration: time.Second}, CacheSizeKiB: 4096, ReadConnections: 2}
	db, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	goose.SetBaseFS(migrations.Files)
	if err := goose.DownTo(db.Writer, ".", 21); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Writer.Exec(`INSERT INTO themes(theme_id,name,version,api_version,path,checksum,validation_status,active,installed_at,updated_at) VALUES('existing','Existing','1.0.0',1,'/test',zeroblob(32),'valid',1,0,0)`); err != nil {
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
	var active, removed bool
	if err := db.Reader.QueryRow("SELECT active,removed_at IS NOT NULL FROM themes WHERE theme_id='existing'").Scan(&active, &removed); err != nil || !active || removed {
		t.Fatalf("upgrade changed existing state: %v", err)
	}
	if _, err := db.Writer.Exec("UPDATE themes SET removed_at=1 WHERE theme_id='existing'"); err == nil {
		t.Fatal("active and removed state accepted")
	}
	if _, err := db.Writer.Exec("UPDATE themes SET active=0,removed_at=-1 WHERE theme_id='existing'"); err == nil {
		t.Fatal("negative removal time accepted")
	}
	if _, err := db.Writer.Exec("UPDATE themes SET active=0,removed_at=1 WHERE theme_id='existing'"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Reader.QueryRow("SELECT active,removed_at IS NOT NULL FROM themes WHERE theme_id='existing'").Scan(&active, &removed); err != nil || active || !removed {
		t.Fatalf("restart changed removal: %v", err)
	}
	if err := goose.DownTo(db.Writer, ".", 21); err != nil {
		t.Fatal(err)
	}
	if err := goose.Up(db.Writer, "."); err != nil {
		t.Fatal(err)
	}
	if err := db.Ready(ctx); err != nil {
		t.Fatal(err)
	}
}
