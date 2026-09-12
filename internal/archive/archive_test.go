package archive

import (
	"context"
	"crypto/sha256"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhushilin/blog-project/internal/media"
	"github.com/zhushilin/blog-project/internal/organization"
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
	if manifest.Version != FormatVersion || len(manifest.Entries[0].Revisions) == 0 || len(manifest.MediaReferences) != 0 {
		t.Fatalf("manifest=%+v", manifest)
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

func TestArchiveV2PreservesRevisionsRedirectsAndPublicSiteMetadata(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	open := func(name string) *database.DB {
		db, err := database.Open(ctx, config.Database{Path: filepath.Join(root, name), BusyTimeout: config.Duration{Duration: time.Second}, CacheSizeKiB: 4096, ReadConnections: 2})
		if err != nil {
			t.Fatal(err)
		}
		return db
	}
	sourceDB := open("source.sqlite")
	defer sourceDB.Close()
	source := publishing.NewService(publishing.NewRepository(sourceDB))
	article, err := source.CreateDraft(ctx, publishing.DraftInput{Title: "多版本归档", Slug: "archive-revisions", BodyMarkdown: "初始正文"})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := source.UpdateDraft(ctx, article.ID, article.LockVersion, publishing.DraftInput{Title: "多版本归档", Slug: "archive-revisions", BodyMarkdown: "更新后的正文"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Publish(ctx, updated.ID, updated.LockVersion); err != nil {
		t.Fatal(err)
	}
	if err := source.CreateRedirect(ctx, organization.RedirectInput{SourcePath: "/old-archive", TargetPath: "/posts/archive-revisions", StatusCode: 301}); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(root, "v2.zip")
	manifest, err := ExportWithOptions(ctx, source, ExportOptions{
		ApplicationVersion: "test-version", MigrationVersion: 19,
		Site: SiteManifest{Name: "测试站点", PrimaryLanguage: "zh-CN", Timezone: "Asia/Shanghai", BaseURL: "https://blog.example", Description: "公开描述", SocialLinks: []string{"https://example.com/author"}},
	}, archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Entries) != 1 || len(manifest.Entries[0].Revisions) < 2 || len(manifest.Redirects) != 1 {
		t.Fatalf("manifest=%+v", manifest)
	}
	if len(manifest.MediaReferences) != 0 || manifest.Site.Name != "测试站点" {
		t.Fatalf("manifest metadata=%+v", manifest)
	}
	if _, err := Verify(ctx, archivePath); err != nil {
		t.Fatal(err)
	}

	destinationDB := open("destination.sqlite")
	defer destinationDB.Close()
	destination := publishing.NewService(publishing.NewRepository(destinationDB))
	var applied SiteManifest
	result, err := ImportWithOptions(ctx, destination, archivePath, ImportOptions{ApplySite: func(_ context.Context, site SiteManifest) error {
		applied = site
		return nil
	}})
	if err != nil || result.Created != 1 || result.Conflicts != 0 || applied.Name != "测试站点" {
		t.Fatalf("result=%+v applied=%+v err=%v", result, applied, err)
	}
	items, err := destination.Articles(ctx)
	if err != nil || len(items) != 1 || items[0].BodyMarkdown != "更新后的正文" {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	revisions, err := destination.RevisionsWithBodies(ctx, "article", items[0].ID)
	if err != nil || len(revisions) != len(manifest.Entries[0].Revisions) {
		t.Fatalf("revisions=%d want=%d err=%v", len(revisions), len(manifest.Entries[0].Revisions), err)
	}
	redirects, err := destination.Redirects(ctx, 10)
	if err != nil || len(redirects) != 1 || redirects[0].SourcePath != "/old-archive" {
		t.Fatalf("redirects=%+v err=%v", redirects, err)
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
	applied := false
	result, err = ImportWithOptions(ctx, missing, archivePath, ImportOptions{
		ApplySite: func(_ context.Context, _ SiteManifest) error {
			applied = true
			return nil
		},
	})
	if err != nil || result.Created != 0 || result.Conflicts != 1 || len(result.Warnings) != 1 {
		t.Fatalf("missing media result=%+v err=%v", result, err)
	}
	if applied {
		t.Fatal("site settings applied after an archive conflict")
	}
}
