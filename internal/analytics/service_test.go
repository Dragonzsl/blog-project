package analytics

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhushilin/blog-project/internal/platform/config"
	"github.com/zhushilin/blog-project/internal/platform/database"
)

func TestRecordAggregatesAndPurges(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, config.Database{Path: filepath.Join(t.TempDir(), "blog.sqlite"), BusyTimeout: config.Duration{Duration: time.Second}, CacheSizeKiB: 4096, ReadConnections: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := NewService(db, []byte("secret"), 30)
	if err := service.Record(ctx, "/posts/hello", "127.0.0.1\x00ua"); err != nil {
		t.Fatal(err)
	}
	if err := service.Record(ctx, "/posts/hello", "127.0.0.1\x00ua"); err != nil {
		t.Fatal(err)
	}
	if err := service.Record(ctx, "/posts/hello", "127.0.0.2\x00ua"); err != nil {
		t.Fatal(err)
	}
	rows, err := service.Summary(ctx, 1)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	if rows[0].Views != 3 || rows[0].UniqueVisitors != 2 {
		t.Fatalf("row=%+v", rows[0])
	}
	if err := service.Record(ctx, "/posts/other", "127.0.0.1\x00ua"); err != nil {
		t.Fatal(err)
	}
	other, err := service.Summary(ctx, 1)
	if err != nil || len(other) != 2 {
		t.Fatalf("path-scoped rows=%v err=%v", other, err)
	}
	for _, row := range other {
		if row.Path == "/posts/other" && row.UniqueVisitors != 1 {
			t.Fatalf("path-scoped row=%+v", row)
		}
	}
	old := time.Now().UTC().AddDate(0, 0, -60).Format("2006-01-02")
	if _, err := db.Writer.ExecContext(ctx, "INSERT INTO analytics_daily(day,path,views,unique_visitors,updated_at) VALUES(?,?,?,?,?)", old, "/old", 1, 1, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if purged, err := service.Purge(ctx); err != nil || purged != 1 {
		t.Fatalf("purged=%d err=%v", purged, err)
	}
}
