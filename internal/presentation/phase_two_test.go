package presentation

import (
	"context"
	"fmt"
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

func TestPhaseTwoPublicDiscoveryAndReadingExperience(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, config.Database{
		Path: filepath.Join(t.TempDir(), "blog.sqlite"), BusyTimeout: config.Duration{Duration: time.Second},
		CacheSizeKiB: 4096, ReadConnections: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	organizations := organization.NewService(db)
	category, err := organizations.CreateCategory(ctx, organization.TermInput{Name: "设计系统", Slug: "design", Description: "长期维护的界面与内容设计。"})
	if err != nil {
		t.Fatal(err)
	}
	tag, err := organizations.CreateTag(ctx, organization.TermInput{Name: "出版", Slug: "publishing"})
	if err != nil {
		t.Fatal(err)
	}
	publisher := publishing.NewService(publishing.NewRepository(db))
	first, err := publisher.CreateDraft(ctx, publishing.DraftInput{
		Title: "高级博客的阅读路径", Slug: "reading-path", Excerpt: "让发现、阅读与继续阅读连成一条路径。",
		BodyMarkdown: "## 阅读入口\n\n高级博客需要清晰的信息架构。\n\n### 继续阅读\n\n把分类、标签与归档放回读者视线。",
		CategoryID:   category.ID, TagIDs: []int64{tag.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err = publisher.Publish(ctx, first.ID, first.LockVersion)
	if err != nil {
		t.Fatal(err)
	}
	second, err := publisher.CreateDraft(ctx, publishing.DraftInput{
		Title: "低噪音的博客首页", Slug: "quiet-home", Excerpt: "首页应该帮助读者找到下一篇文章。",
		BodyMarkdown: "## 首页\n\n把精选、最近、分类与归档组织成安静的入口。",
		CategoryID:   category.ID, TagIDs: []int64{tag.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := publisher.Publish(ctx, second.ID, second.LockVersion); err != nil {
		t.Fatal(err)
	}
	about, err := publisher.CreatePageDraft(ctx, publishing.DraftInput{Title: "关于这个地方", Slug: "about", Excerpt: "一个用来长期写作和整理想法的地方。", BodyMarkdown: "## 关于\n\n这里记录设计、出版与日常思考。"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := publisher.PublishPage(ctx, about.ID, about.LockVersion); err != nil {
		t.Fatal(err)
	}

	discoveryService, err := discovery.NewService(discovery.NewRepository(db), discovery.Options{
		BaseURL: "https://blog.example", SyncBatchSize: 20, MaxResults: 50, FeedLimit: 50, SitemapLimit: 50000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := discoveryService.SyncAllDirty(ctx); err != nil {
		t.Fatal(err)
	}
	theme, err := NewDefaultTheme(NewMarkdown())
	if err != nil {
		t.Fatal(err)
	}
	cache, err := NewPageCache(filepath.Join(t.TempDir(), "cache"), 32, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHTTPHandler(
		publisher, presentationSiteNamer{}, NewStateRepository(db), theme, cache,
		slog.New(slog.NewTextHandler(io.Discard, nil)), organizations,
	)
	handler.SetDiscovery(discoveryService)
	router := chi.NewRouter()
	handler.RegisterPublic(router)

	get := func(target string) *httptest.ResponseRecorder {
		t.Helper()
		return requestPublic(t, router, target)
	}
	assertPage := func(target string, expected ...string) string {
		t.Helper()
		response := get(target)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", target, response.Code, response.Body.String())
		}
		for _, value := range expected {
			if !strings.Contains(response.Body.String(), value) {
				t.Fatalf("%s missing %q: %s", target, value, response.Body.String())
			}
		}
		return response.Body.String()
	}

	assertPage("/", "推荐文章", "最新文章", "文章归档", "spotlight-search")
	assertPage("/categories", "分类", "设计系统", "2 篇文章")
	assertPage("/tags", "标签", "# 出版", "2 篇")
	archive := assertPage("/archive", "归档", "按时间浏览", "篇")
	if !strings.Contains(archive, fmt.Sprintf("/archive/%04d/%02d", first.PublishedAt.UTC().Year(), int(first.PublishedAt.UTC().Month()))) {
		t.Fatalf("archive month link missing: %s", archive)
	}
	monthPath := fmt.Sprintf("/archive/%04d/%02d", first.PublishedAt.UTC().Year(), int(first.PublishedAt.UTC().Month()))
	assertPage(monthPath, "高级博客的阅读路径", "这一时间段共发布")
	search := assertPage("/search?q=高级博客&category=design&tag=publishing", "找到 1 条结果", "<mark>高级博客</mark>")
	if !strings.Contains(search, `data-search-results`) || !strings.Contains(search, `name="q"`) {
		t.Fatal("search response must retain the accessible keyword form and results")
	}

	article := assertPage("/posts/"+first.PublishedSlug, "reading-progress", "data-reading-progress", "data-copy-link", "data-share-link", "data-print-article", "本文目录", "data-drawer-name=\"toc\"", "article-layout", "blog-theme", "data-theme-toggle")
	if !strings.Contains(article, theme.ScriptURL()) {
		t.Fatalf("article script URL missing: %s", article)
	}
	withoutTOC, err := theme.RenderArticlePage("示例博客", ArticleData{Kind: "article", Title: "没有目录的文章", Slug: "without-toc", BodyMarkdown: "# 文章内部标题\n\n这是一段没有二级目录的正文。"}, false, "/articles", Navigation{}, PageMetadata{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(withoutTOC), "article-without-toc") || strings.Contains(string(withoutTOC), "article-toc") {
		t.Fatalf("article without toc did not use the single-column layout: %s", withoutTOC)
	}
	if !strings.Contains(get("/posts/"+first.PublishedSlug).Header().Get("Content-Security-Policy"), "script-src 'self'") {
		t.Fatalf("article CSP does not allow same-origin enhancement script")
	}
	script := get(theme.ScriptURL())
	if script.Code != http.StatusOK || !strings.Contains(script.Header().Get("Content-Type"), "javascript") || !strings.Contains(script.Body.String(), "data-reading-progress") {
		t.Fatalf("script asset status=%d type=%q body=%s", script.Code, script.Header().Get("Content-Type"), script.Body.String())
	}

	missing := get("/does-not-exist")
	if missing.Code != http.StatusNotFound || !strings.Contains(missing.Body.String(), "页面不存在") || missing.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("missing page status=%d cache=%q body=%s", missing.Code, missing.Header().Get("Cache-Control"), missing.Body.String())
	}
	method := httptest.NewRecorder()
	router.ServeHTTP(method, httptest.NewRequest(http.MethodPost, "/articles", nil))
	if method.Code != http.StatusMethodNotAllowed || !strings.Contains(method.Body.String(), "暂不支持此操作") {
		t.Fatalf("method status=%d body=%s", method.Code, method.Body.String())
	}
}
