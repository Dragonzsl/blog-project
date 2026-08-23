package presentation

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/zhushilin/blog-project/internal/discovery"
	"github.com/zhushilin/blog-project/internal/organization"
	"github.com/zhushilin/blog-project/internal/platform/config"
	"github.com/zhushilin/blog-project/internal/platform/database"
	"github.com/zhushilin/blog-project/internal/publishing"
)

func TestDiscoveryHTTPSEOFeedSitemapSearchAndRedirect(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, config.Database{Path: filepath.Join(t.TempDir(), "blog.sqlite"), BusyTimeout: config.Duration{Duration: time.Second}, CacheSizeKiB: 4096, ReadConnections: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	publisher := publishing.NewService(publishing.NewRepository(db))
	article, err := publisher.CreateDraft(ctx, publishing.DraftInput{
		Title: "发现轻量博客", Slug: "discover", Excerpt: "简洁流畅",
		SEOTitle: "轻量博客搜索指南", SEODescription: "面向搜索引擎的独立摘要",
		BodyMarkdown: "中文全文搜索与 RSS",
	})
	if err != nil {
		t.Fatal(err)
	}
	article, err = publisher.Publish(ctx, article.ID, article.LockVersion)
	if err != nil {
		t.Fatal(err)
	}
	article, err = publisher.UpdateDraft(ctx, article.ID, article.LockVersion, publishing.DraftInput{
		Title: "发现轻量博客", Slug: "discover-new", Excerpt: "简洁流畅",
		SEOTitle: "轻量博客搜索指南", SEODescription: "面向搜索引擎的独立摘要",
		BodyMarkdown: "中文全文搜索与 RSS",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := publisher.Publish(ctx, article.ID, article.LockVersion); err != nil {
		t.Fatal(err)
	}
	discoveryService, err := discovery.NewService(discovery.NewRepository(db), discovery.Options{BaseURL: "https://blog.example", SyncBatchSize: 20, MaxResults: 50, FeedLimit: 50, SitemapLimit: 50000})
	if err != nil {
		t.Fatal(err)
	}
	theme, err := NewDefaultTheme(NewMarkdown())
	if err != nil {
		t.Fatal(err)
	}
	cache, err := NewPageCache(filepath.Join(t.TempDir(), "cache"), 16, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHTTPHandler(publisher, presentationSiteNamer{}, NewStateRepository(db), theme, cache, slog.New(slog.NewTextHandler(io.Discard, nil)), organization.NewService(db))
	handler.SetDiscovery(discoveryService)
	router := chi.NewRouter()
	handler.RegisterPublic(router)

	articleResponse := requestPublic(t, router, "/posts/discover-new")
	for _, expected := range []string{`<title>轻量博客搜索指南</title>`, `<meta name="description" content="面向搜索引擎的独立摘要">`, `rel="canonical" href="https://blog.example/posts/discover-new"`, `property="og:type" content="article"`, `application/ld+json`, `rel="alternate" type="application/rss+xml"`} {
		if !strings.Contains(articleResponse.Body.String(), expected) {
			t.Fatalf("article missing %q: %s", expected, articleResponse.Body.String())
		}
	}
	if !strings.Contains(articleResponse.Header().Get("Content-Security-Policy"), "sha256-") {
		t.Fatalf("JSON-LD CSP hash missing: %q", articleResponse.Header().Get("Content-Security-Policy"))
	}
	redirect := requestPublic(t, router, "/posts/discover")
	if redirect.Code != http.StatusMovedPermanently || redirect.Header().Get("Location") != "/posts/discover-new" {
		t.Fatalf("redirect status=%d location=%q", redirect.Code, redirect.Header().Get("Location"))
	}
	search := requestPublic(t, router, "/search?q=全文搜索")
	if search.Code != http.StatusOK || !strings.Contains(search.Body.String(), "发现轻量博客") || search.Header().Get("Cache-Control") != "no-store" || !strings.Contains(search.Body.String(), "noindex,nofollow") {
		t.Fatalf("search status=%d cache=%q body=%s", search.Code, search.Header().Get("Cache-Control"), search.Body.String())
	}
	if !strings.Contains(search.Header().Get("Content-Security-Policy"), "form-action 'self'") {
		t.Fatalf("search form blocked by CSP: %q", search.Header().Get("Content-Security-Policy"))
	}
	feed := requestPublic(t, router, "/rss.xml")
	if feed.Code != http.StatusOK || !strings.Contains(feed.Header().Get("Content-Type"), "application/rss+xml") || !strings.Contains(feed.Body.String(), "https://blog.example/posts/discover-new") {
		t.Fatalf("feed status=%d body=%s", feed.Code, feed.Body.String())
	}
	sitemap := requestPublic(t, router, "/sitemap.xml")
	if sitemap.Code != http.StatusOK || !strings.Contains(sitemap.Body.String(), "https://blog.example/posts/discover-new") {
		t.Fatalf("sitemap status=%d body=%s", sitemap.Code, sitemap.Body.String())
	}
	robots := requestPublic(t, router, "/robots.txt")
	if !strings.Contains(robots.Body.String(), "Disallow: /admin/") || !strings.Contains(robots.Body.String(), "https://blog.example/sitemap.xml") {
		t.Fatalf("robots body=%s", robots.Body.String())
	}
}

func requestPublic(t *testing.T, handler http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
	return response
}
