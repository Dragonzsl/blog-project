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
	"github.com/zhushilin/blog-project/internal/organization"
	"github.com/zhushilin/blog-project/internal/platform/config"
	"github.com/zhushilin/blog-project/internal/platform/database"
	"github.com/zhushilin/blog-project/internal/publishing"
)

type presentationSiteNamer struct{}

func (presentationSiteNamer) SiteName(context.Context) (string, error) { return "纸上花园", nil }

func TestPublicThemeCacheETagAndPreviewIsolation(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, config.Database{
		Path:            filepath.Join(t.TempDir(), "blog.sqlite"),
		BusyTimeout:     config.Duration{Duration: time.Second},
		CacheSizeKiB:    4096,
		ReadConnections: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	publishingService := publishing.NewService(publishing.NewRepository(db))
	draft, err := publishingService.CreateDraft(ctx, publishing.DraftInput{
		Title:        "缓存文章",
		Slug:         "cached-article",
		Excerpt:      "摘要",
		BodyMarkdown: "## 第一版\n\n**安全** <script>alert(1)</script>",
	})
	if err != nil {
		t.Fatal(err)
	}
	published, err := publishingService.Publish(ctx, draft.ID, draft.LockVersion)
	if err != nil {
		t.Fatal(err)
	}
	theme, err := NewDefaultTheme(NewMarkdown())
	if err != nil {
		t.Fatal(err)
	}
	cache, err := NewPageCache(filepath.Join(t.TempDir(), "cache"), 4, 256<<10)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHTTPHandler(
		publishingService,
		presentationSiteNamer{},
		NewStateRepository(db),
		theme,
		cache,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	router := chi.NewRouter()
	handler.RegisterPublic(router)
	router.Route("/admin", handler.RegisterAdmin)

	first := httptest.NewRecorder()
	router.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/posts/cached-article", nil))
	if first.Code != http.StatusOK || first.Header().Get("X-Page-Cache") != "MISS" {
		t.Fatalf("first response status=%d cache=%q body=%s", first.Code, first.Header().Get("X-Page-Cache"), first.Body.String())
	}
	if !strings.Contains(first.Body.String(), "<strong>安全</strong>") || strings.Contains(first.Body.String(), "<script") {
		t.Fatalf("unsafe or unrendered Markdown: %s", first.Body.String())
	}
	if first.Header().Get("Last-Modified") == "" {
		t.Fatal("published revision did not produce Last-Modified")
	}
	etag := first.Header().Get("ETag")
	second := httptest.NewRecorder()
	router.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/posts/cached-article", nil))
	if second.Header().Get("X-Page-Cache") != "HIT" || second.Header().Get("ETag") != etag {
		t.Fatalf("second response cache=%q etag=%q", second.Header().Get("X-Page-Cache"), second.Header().Get("ETag"))
	}
	conditionalRequest := httptest.NewRequest(http.MethodGet, "/posts/cached-article", nil)
	conditionalRequest.Header.Set("If-None-Match", etag)
	conditional := httptest.NewRecorder()
	router.ServeHTTP(conditional, conditionalRequest)
	if conditional.Code != http.StatusNotModified || conditional.Body.Len() != 0 {
		t.Fatalf("conditional status=%d body=%q", conditional.Code, conditional.Body.String())
	}
	head := httptest.NewRecorder()
	router.ServeHTTP(head, httptest.NewRequest(http.MethodHead, "/posts/cached-article", nil))
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("ETag") != etag {
		t.Fatalf("HEAD status=%d body=%q etag=%q", head.Code, head.Body.String(), head.Header().Get("ETag"))
	}

	updated, err := publishingService.UpdateDraft(ctx, published.ID, published.LockVersion, publishing.DraftInput{
		Title:        "缓存文章",
		Slug:         "cached-article",
		Excerpt:      "摘要",
		BodyMarkdown: "## 尚未发布的第二版",
	})
	if err != nil {
		t.Fatal(err)
	}
	preview := httptest.NewRecorder()
	router.ServeHTTP(preview, httptest.NewRequest(http.MethodGet, "/admin/articles/1/preview", nil))
	if !strings.Contains(preview.Body.String(), "尚未发布的第二版") || preview.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("preview response = %s", preview.Body.String())
	}
	stillPublic := httptest.NewRecorder()
	router.ServeHTTP(stillPublic, httptest.NewRequest(http.MethodGet, "/posts/cached-article", nil))
	if strings.Contains(stillPublic.Body.String(), "尚未发布的第二版") {
		t.Fatal("preview revision leaked into public cache")
	}
	if _, err := publishingService.Publish(ctx, updated.ID, updated.LockVersion); err != nil {
		t.Fatal(err)
	}
	invalidated := httptest.NewRecorder()
	router.ServeHTTP(invalidated, httptest.NewRequest(http.MethodGet, "/posts/cached-article", nil))
	if invalidated.Header().Get("X-Page-Cache") != "MISS" || !strings.Contains(invalidated.Body.String(), "尚未发布的第二版") {
		t.Fatalf("invalidated response cache=%q body=%s", invalidated.Header().Get("X-Page-Cache"), invalidated.Body.String())
	}
	if invalidated.Header().Get("ETag") == etag {
		t.Fatal("ETag did not change after publication")
	}

	home := httptest.NewRecorder()
	router.ServeHTTP(home, httptest.NewRequest(http.MethodGet, "/", nil))
	if home.Code != http.StatusOK || !strings.Contains(home.Body.String(), "缓存文章") {
		t.Fatalf("home response status=%d body=%s", home.Code, home.Body.String())
	}
	asset := httptest.NewRecorder()
	router.ServeHTTP(asset, httptest.NewRequest(http.MethodGet, theme.AssetURL(), nil))
	if asset.Code != http.StatusOK || !strings.Contains(asset.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("asset response status=%d cache=%q", asset.Code, asset.Header().Get("Cache-Control"))
	}
}

func TestPagesTaxonomyAndNavigationRenderPublishedRevisions(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, config.Database{Path: filepath.Join(t.TempDir(), "blog.sqlite"), BusyTimeout: config.Duration{Duration: time.Second}, CacheSizeKiB: 4096, ReadConnections: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	organizationService := organization.NewService(db)
	category, err := organizationService.CreateCategory(ctx, organization.TermInput{Name: "技术", Slug: "tech", Description: "技术写作"})
	if err != nil {
		t.Fatal(err)
	}
	tag, err := organizationService.CreateTag(ctx, organization.TermInput{Name: "Go", Slug: "go"})
	if err != nil {
		t.Fatal(err)
	}
	publisher := publishing.NewService(publishing.NewRepository(db))
	article, err := publisher.CreateDraft(ctx, publishing.DraftInput{Title: "公开标题", Slug: "organized", BodyMarkdown: "公开正文", CategoryID: category.ID, TagIDs: []int64{tag.ID}})
	if err != nil {
		t.Fatal(err)
	}
	article, err = publisher.Publish(ctx, article.ID, article.LockVersion)
	if err != nil {
		t.Fatal(err)
	}
	page, err := publisher.CreatePageDraft(ctx, publishing.DraftInput{Title: "关于", Slug: "about", BodyMarkdown: "关于页面"})
	if err != nil {
		t.Fatal(err)
	}
	page, err = publisher.PublishPage(ctx, page.ID, page.LockVersion)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := organizationService.CreateNavigationItem(ctx, organization.NavigationInput{Location: "primary", Label: "关于", TargetKind: "content", TargetID: page.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := organizationService.CreateNavigationItem(ctx, organization.NavigationInput{Location: "primary", ParentID: parent.ID, Label: "项目", TargetKind: "external", ExternalURL: "https://example.com/projects"}); err != nil {
		t.Fatal(err)
	}
	updated, err := publisher.UpdateDraft(ctx, article.ID, article.LockVersion, publishing.DraftInput{Title: "未发布标题", Slug: "organized", BodyMarkdown: "未发布正文", CategoryID: category.ID, TagIDs: []int64{tag.ID}})
	if err != nil {
		t.Fatal(err)
	}
	_ = updated
	theme, err := NewDefaultTheme(NewMarkdown())
	if err != nil {
		t.Fatal(err)
	}
	cache, err := NewPageCache(filepath.Join(t.TempDir(), "cache"), 8, 512<<10)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHTTPHandler(publisher, presentationSiteNamer{}, NewStateRepository(db), theme, cache, slog.New(slog.NewTextHandler(io.Discard, nil)), organizationService)
	router := chi.NewRouter()
	handler.RegisterPublic(router)
	for target, checks := range map[string][]string{"/about": {"关于页面", "关于", "项目"}, "/categories/tech": {"公开标题", "技术写作", "项目"}, "/tags/go": {"公开标题", "# Go", "项目"}, "/": {"关于", "项目"}} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", target, response.Code, response.Body.String())
		}
		for _, check := range checks {
			if !strings.Contains(response.Body.String(), check) {
				t.Fatalf("%s missing %q: %s", target, check, response.Body.String())
			}
		}
		if strings.Contains(response.Body.String(), "未发布标题") || strings.Contains(response.Body.String(), "未发布正文") {
			t.Fatalf("%s leaked current draft", target)
		}
	}
}
