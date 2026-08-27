package organization

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhushilin/blog-project/internal/platform/config"
	"github.com/zhushilin/blog-project/internal/platform/database"
)

func TestManualRedirectsValidateAndList(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, config.Database{Path: filepath.Join(t.TempDir(), "blog.sqlite"), BusyTimeout: config.Duration{Duration: time.Second}, CacheSizeKiB: 4096, ReadConnections: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := NewService(db)
	if err := service.CreateRedirect(ctx, RedirectInput{SourcePath: "/old", TargetPath: "/new", StatusCode: 301}); err != nil {
		t.Fatal(err)
	}
	items, err := service.Redirects(ctx, 10)
	if err != nil || len(items) != 1 || items[0].Reason != "manual" {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	if err := service.CreateRedirect(ctx, RedirectInput{SourcePath: "https://evil.example", TargetPath: "/new", StatusCode: 301}); err == nil {
		t.Fatal("external source accepted")
	}
	if err := service.CreateRedirect(ctx, RedirectInput{SourcePath: "/admin/x", TargetPath: "/new", StatusCode: 301}); err == nil {
		t.Fatal("admin source accepted")
	}
	if err := service.DeleteRedirect(ctx, items[0].ID); err != nil {
		t.Fatal(err)
	}
}
