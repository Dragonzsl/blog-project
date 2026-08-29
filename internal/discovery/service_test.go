package discovery

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhushilin/blog-project/internal/organization"
	"github.com/zhushilin/blog-project/internal/platform/config"
	"github.com/zhushilin/blog-project/internal/platform/database"
	"github.com/zhushilin/blog-project/internal/publishing"
)

func TestSearchProjectionLifecycleChineseAndFilters(t *testing.T) {
	ctx := context.Background()
	db := openDiscoveryTestDatabase(t)
	organizations := organization.NewService(db)
	category, err := organizations.CreateCategory(ctx, organization.TermInput{Name: "轻量架构", Slug: "architecture"})
	if err != nil {
		t.Fatal(err)
	}
	tag, err := organizations.CreateTag(ctx, organization.TermInput{Name: "Golang", Slug: "golang"})
	if err != nil {
		t.Fatal(err)
	}
	publisher := publishing.NewService(publishing.NewRepository(db))
	article, err := publisher.CreateDraft(ctx, publishing.DraftInput{
		Title: "低开销博客系统", Slug: "small-blog", Excerpt: "保持流畅与克制",
		BodyMarkdown: "## 设计\n\nGolang 与 SQLite 构成低内存方案。", CategoryID: category.ID, TagIDs: []int64{tag.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	article, err = publisher.Publish(ctx, article.ID, article.LockVersion)
	if err != nil {
		t.Fatal(err)
	}
	page, err := publisher.CreatePageDraft(ctx, publishing.DraftInput{Title: "博客关于页面", Slug: "about", BodyMarkdown: "保持低开销"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := publisher.PublishPage(ctx, page.ID, page.LockVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := publisher.CreateDraft(ctx, publishing.DraftInput{Title: "低开销私密草稿", Slug: "private"}); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(NewRepository(db), Options{BaseURL: "https://blog.example", SyncBatchSize: 2, MaxResults: 50, FeedLimit: 50, SitemapLimit: 50000})
	if err != nil {
		t.Fatal(err)
	}

	results, err := service.Search(ctx, SearchQuery{Text: "低开销", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Path != "/posts/small-blog" {
		t.Fatalf("Chinese results = %+v", results)
	}
	results, err = service.Search(ctx, SearchQuery{Text: "Golan", Kind: "article", CategorySlug: "architecture", TagSlug: "golang", Limit: 20})
	if err != nil || len(results) != 1 || results[0].Title != "低开销博客系统" {
		t.Fatalf("filtered prefix results=%+v err=%v", results, err)
	}
	results, err = service.Search(ctx, SearchQuery{Text: "低开销 SQLite", Kind: "article", Limit: 20})
	if err != nil || len(results) != 1 || results[0].Path != "/posts/small-blog" {
		t.Fatalf("mixed-language results=%+v err=%v", results, err)
	}
	searchPage, err := service.SearchPage(ctx, SearchQuery{Text: "低开销", PerPage: 1, Page: 2})
	if err != nil || searchPage.Pagination.Total != 2 || searchPage.Pagination.Page != 2 || len(searchPage.Results) != 1 {
		t.Fatalf("paginated search=%+v err=%v", searchPage, err)
	}
	if _, err := service.Search(ctx, SearchQuery{Text: "低", Limit: 20}); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("one-character query error=%v", err)
	}

	if _, err := publisher.Unpublish(ctx, "article", article.ID, article.LockVersion); err != nil {
		t.Fatal(err)
	}
	results, err = service.Search(ctx, SearchQuery{Text: "Golang", Limit: 20})
	if err != nil || len(results) != 0 {
		t.Fatalf("unpublished article remained searchable: results=%+v err=%v", results, err)
	}
}

func TestFeedSitemapAndRedirectProjection(t *testing.T) {
	ctx := context.Background()
	db := openDiscoveryTestDatabase(t)
	publisher := publishing.NewService(publishing.NewRepository(db))
	article, err := publisher.CreateDraft(ctx, publishing.DraftInput{Title: "订阅文章", Slug: "feed", Excerpt: "摘要"})
	if err != nil {
		t.Fatal(err)
	}
	article, err = publisher.Publish(ctx, article.ID, article.LockVersion)
	if err != nil {
		t.Fatal(err)
	}
	article, err = publisher.UpdateDraft(ctx, article.ID, article.LockVersion, publishing.DraftInput{Title: "订阅文章", Slug: "feed-new", Excerpt: "摘要"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := publisher.Publish(ctx, article.ID, article.LockVersion); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(NewRepository(db), Options{BaseURL: "https://blog.example/base", SyncBatchSize: 20, MaxResults: 50, FeedLimit: 50, SitemapLimit: 50000})
	if err != nil {
		t.Fatal(err)
	}
	feed, err := service.Feed(ctx)
	if err != nil || len(feed) != 1 || feed[0].Path != "/posts/feed-new" {
		t.Fatalf("feed=%+v err=%v", feed, err)
	}
	entries, err := service.Sitemap(ctx)
	if err != nil || !hasSitemapPath(entries, "/") || !hasSitemapPath(entries, "/posts/feed-new") {
		t.Fatalf("sitemap=%+v err=%v", entries, err)
	}
	redirect, err := service.ResolveRedirect(ctx, "/posts/feed")
	if err != nil || redirect.TargetPath != "/posts/feed-new" || redirect.StatusCode != 301 {
		t.Fatalf("redirect=%+v err=%v", redirect, err)
	}
	if service.AbsoluteURL("/rss.xml") != "https://blog.example/base/rss.xml" {
		t.Fatalf("absolute URL=%q", service.AbsoluteURL("/rss.xml"))
	}
}

func openDiscoveryTestDatabase(t *testing.T) *database.DB {
	t.Helper()
	db, err := database.Open(context.Background(), config.Database{
		Path: filepath.Join(t.TempDir(), "blog.sqlite"), BusyTimeout: config.Duration{Duration: time.Second},
		CacheSizeKiB: 4096, ReadConnections: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func hasSitemapPath(entries []SitemapEntry, path string) bool {
	for _, entry := range entries {
		if entry.Path == path {
			return true
		}
	}
	return false
}

func TestNewServiceRejectsCredentialBearingBaseURL(t *testing.T) {
	db := openDiscoveryTestDatabase(t)
	if _, err := NewService(NewRepository(db), Options{BaseURL: "https://owner:secret@blog.example"}); err == nil {
		t.Fatal("credential-bearing discovery base URL was accepted")
	}
}
