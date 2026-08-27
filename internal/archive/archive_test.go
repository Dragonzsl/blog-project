package archive

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhushilin/blog-project/internal/platform/config"
	"github.com/zhushilin/blog-project/internal/platform/database"
	"github.com/zhushilin/blog-project/internal/publishing"
)

func TestExportVerifyImportRoundTrip(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	open := func(path string) *database.DB {
		db, err := database.Open(ctx, config.Database{Path: filepath.Join(root, path), BusyTimeout: config.Duration{Duration: time.Second}, CacheSizeKiB: 4096, ReadConnections: 2})
		if err != nil {
			t.Fatal(err)
		}
		return db
	}
	sourceDB := open("source.sqlite")
	defer sourceDB.Close()
	source := publishing.NewService(publishing.NewRepository(sourceDB))
	article, err := source.CreateDraft(ctx, publishing.DraftInput{Title: "归档文章", Slug: "archive-post", Excerpt: "摘要", BodyMarkdown: "# 内容\n\n往返。"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Publish(ctx, article.ID, article.LockVersion); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(root, "content.zip")
	manifest, err := Export(ctx, source, archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Entries) != 1 {
		t.Fatalf("entries=%d", len(manifest.Entries))
	}
	verified, err := Verify(ctx, archivePath)
	if err != nil || len(verified.Manifest.Entries) != 1 {
		t.Fatalf("verified=%+v err=%v", verified, err)
	}
	destinationDB := open("destination.sqlite")
	defer destinationDB.Close()
	destination := publishing.NewService(publishing.NewRepository(destinationDB))
	count, err := Import(ctx, destination, archivePath)
	if err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	items, err := destination.Articles(ctx)
	if err != nil || len(items) != 1 || items[0].BodyMarkdown != "# 内容\n\n往返。" {
		t.Fatalf("items=%+v err=%v", items, err)
	}
}
