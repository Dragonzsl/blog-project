package archive

import (
	"context"
	"crypto/sha256"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhushilin/blog-project/internal/media"
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

func TestArchivePreservesCoverAndReportsMissingMedia(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	open := func(name string) *database.DB {
		db, err := database.Open(ctx, config.Database{Path: filepath.Join(root, name), BusyTimeout: config.Duration{Duration: time.Second}, CacheSizeKiB: 4096, ReadConnections: 2})
		if err != nil {
			t.Fatal(err)
		}
		return db
	}
	publicID := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	createMedia := func(db *database.DB) media.Item {
		hash := sha256.Sum256([]byte("archive-cover"))
		item, err := media.NewRepository(db).Create(ctx, media.Item{PublicID: publicID, OriginalName: "cover.jpg", MIMEType: "image/jpeg", SizeBytes: 128, Width: 1200, Height: 630, ContentHash: hash[:], AltText: "归档封面", ObjectKey: "original/cover.jpg"}, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		return item
	}
	sourceDB := open("source.sqlite")
	defer sourceDB.Close()
	cover := createMedia(sourceDB)
	source := publishing.NewService(publishing.NewRepository(sourceDB))
	article, err := source.CreateDraft(ctx, publishing.DraftInput{Title: "带封面归档", Slug: "archive-cover", BodyMarkdown: "正文", CoverMediaID: cover.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Publish(ctx, article.ID, article.LockVersion); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(root, "cover.zip")
	manifest, err := Export(ctx, source, archivePath)
	if err != nil || len(manifest.Entries) != 1 || manifest.Entries[0].CoverMediaPublicID == "" {
		t.Fatalf("manifest=%+v err=%v", manifest, err)
	}
	verified, err := Verify(ctx, archivePath)
	if err != nil || verified.Manifest.Entries[0].CoverMediaPublicID != manifest.Entries[0].CoverMediaPublicID {
		t.Fatalf("verified=%+v err=%v", verified, err)
	}

	destinationDB := open("destination.sqlite")
	defer destinationDB.Close()
	createMedia(destinationDB)
	destination := publishing.NewService(publishing.NewRepository(destinationDB))
	result, err := ImportWithReport(ctx, destination, archivePath)
	if err != nil || result.Created != 1 || result.Conflicts != 0 {
		t.Fatalf("import result=%+v err=%v", result, err)
	}
	items, err := destination.Articles(ctx)
	if err != nil || len(items) != 1 || string(items[0].CoverMediaPublicID) != string(publicID) {
		t.Fatalf("destination=%+v err=%v", items, err)
	}

	missingDB := open("missing.sqlite")
	defer missingDB.Close()
	missing := publishing.NewService(publishing.NewRepository(missingDB))
	result, err = ImportWithReport(ctx, missing, archivePath)
	if err != nil || result.Created != 0 || result.Conflicts != 1 || len(result.Warnings) != 1 {
		t.Fatalf("missing media result=%+v err=%v", result, err)
	}
}
