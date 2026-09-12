package contentapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/zhushilin/blog-project/internal/extensions"
	"github.com/zhushilin/blog-project/internal/platform/config"
	"github.com/zhushilin/blog-project/internal/platform/database"
	"github.com/zhushilin/blog-project/internal/publishing"
)

type fakeContent struct{ articles, pages []publishing.Article }

func (f fakeContent) PublishedArticles(context.Context, int) ([]publishing.Article, error) {
	return f.articles, nil
}
func (f fakeContent) PublishedPages(context.Context, int) ([]publishing.Article, error) {
	return f.pages, nil
}
func (f fakeContent) PublicArticle(context.Context, string) (publishing.Article, error) {
	if len(f.articles) == 0 {
		return publishing.Article{}, publishing.ErrNotFound
	}
	return f.articles[0], nil
}
func (f fakeContent) PublicPage(context.Context, string) (publishing.Article, error) {
	if len(f.pages) == 0 {
		return publishing.Article{}, publishing.ErrNotFound
	}
	return f.pages[0], nil
}

type fakeSite struct{}

func (fakeSite) SiteName(context.Context) (string, error) { return "测试站", nil }

type fakeURLs struct{}

func (fakeURLs) AbsoluteURL(path string) string { return "https://example.test" + path }

func TestReadOnlyAPIRequiresEnableAndReturnsPublishedItems(t *testing.T) {
	db, err := database.Open(context.Background(), config.Database{Path: filepath.Join(t.TempDir(), "blog.sqlite"), BusyTimeout: config.Duration{Duration: time.Second}, CacheSizeKiB: 4096, ReadConnections: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	router := chi.NewRouter()
	registry := extensions.NewRegistry(db, router, nil)
	article := publishing.Article{PublicID: bytesOf(1), Kind: "article", PublishedSlug: "hello", Title: "你好", BodyMarkdown: "正文"}
	if err := registry.Register(NewPlugin(fakeContent{articles: []publishing.Article{article}}, fakeSite{}, fakeURLs{}, Config{Token: "token"})); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/posts", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("disabled status=%d", response.Code)
	}
	if err := registry.Enable(context.Background(), PluginID); err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/posts", nil)
	request.Header.Set("Authorization", "Bearer token")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("enabled status=%d body=%s", response.Code, response.Body.String())
	}
	var decoded map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["read_only"] != nil {
		t.Fatalf("list unexpectedly has read_only marker: %v", decoded)
	}
	if response.Header().Get("ETag") == "" {
		t.Fatal("missing ETag")
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/posts", nil)
	request.Header.Set("Authorization", "Bearer token")
	request.Header.Set("If-None-Match", response.Header().Get("ETag"))
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNotModified {
		t.Fatalf("conditional status=%d", response.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/posts", nil)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", response.Code)
	}
}

type cursorContent struct {
	items []publishing.Article
}

func (f cursorContent) PublishedArticles(context.Context, int) ([]publishing.Article, error) {
	return f.items, nil
}
func (f cursorContent) PublishedPages(context.Context, int) ([]publishing.Article, error) {
	return nil, nil
}
func (f cursorContent) PublicArticle(context.Context, string) (publishing.Article, error) {
	return f.items[0], nil
}
func (f cursorContent) PublicPage(context.Context, string) (publishing.Article, error) {
	return publishing.Article{}, publishing.ErrNotFound
}
func (f cursorContent) PublishedContentCursor(_ context.Context, query publishing.PublicContentQuery) (publishing.PublicContentPage, error) {
	items := make([]publishing.Article, 0, len(f.items))
	for _, item := range f.items {
		if query.UpdatedSince != nil && (item.PublishedAt == nil || item.PublishedAt.Before(*query.UpdatedSince)) {
			continue
		}
		if query.Cursor != nil && item.PublishedAt != nil {
			if item.PublishedAt.After(query.Cursor.PublishedAt) || (item.PublishedAt.Equal(query.Cursor.PublishedAt) && strings.Compare(string(item.PublicID), string(query.Cursor.PublicID)) >= 0) {
				continue
			}
		}
		items = append(items, item)
	}
	limit := query.Limit
	if len(items) > limit {
		return publishing.PublicContentPage{Contents: items[:limit], HasMore: true}, nil
	}
	return publishing.PublicContentPage{Contents: items}, nil
}

func TestCursorAPIHasStableContinuationAndPrivateValidators(t *testing.T) {
	base := time.Date(2026, time.August, 23, 14, 0, 0, 0, time.UTC)
	items := make([]publishing.Article, 0, 5)
	for index := 0; index < 5; index++ {
		published := base.Add(-time.Duration(index) * time.Minute)
		items = append(items, publishing.Article{PublicID: bytesOf(byte(index + 1)), Kind: "article", PublishedSlug: "post-" + string(rune('a'+index)), Title: "文章", PublishedAt: &published, PublishedRevisionAt: &published})
	}
	handler := &HTTPHandler{content: cursorContent{items: items}, site: fakeSite{}, urls: fakeURLs{}, token: "secret"}
	router := chi.NewRouter()
	router.Get("/posts", handler.posts)
	first := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/posts?per_page=2", nil)
	request.Header.Set("Authorization", "Bearer secret")
	router.ServeHTTP(first, request)
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), `"next_cursor"`) {
		t.Fatalf("first page status=%d body=%s", first.Code, first.Body.String())
	}
	if first.Header().Get("Last-Modified") == "" || first.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("headers=%v", first.Header())
	}
	var page struct {
		Items      []item `json:"items"`
		NextCursor string `json:"next_cursor"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.NextCursor == "" {
		t.Fatalf("page=%+v", page)
	}
	second := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/posts?per_page=2&cursor="+page.NextCursor, nil)
	request.Header.Set("Authorization", "Bearer secret")
	router.ServeHTTP(second, request)
	if second.Code != http.StatusOK || strings.Contains(second.Body.String(), page.Items[0].Slug) || strings.Contains(second.Body.String(), page.Items[1].Slug) {
		t.Fatalf("continuation status=%d body=%s", second.Code, second.Body.String())
	}
	bad := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/posts?cursor=not-a-cursor", nil)
	request.Header.Set("Authorization", "Bearer secret")
	router.ServeHTTP(bad, request)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("bad cursor status=%d", bad.Code)
	}
	pageAndCursor := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/posts?page=2&cursor="+page.NextCursor, nil)
	request.Header.Set("Authorization", "Bearer secret")
	router.ServeHTTP(pageAndCursor, request)
	if pageAndCursor.Code != http.StatusBadRequest {
		t.Fatalf("page and cursor status=%d", pageAndCursor.Code)
	}
	conditional := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/posts?per_page=2", nil)
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("If-None-Match", first.Header().Get("ETag"))
	router.ServeHTTP(conditional, request)
	if conditional.Code != http.StatusNotModified {
		t.Fatalf("conditional status=%d", conditional.Code)
	}
}

func bytesOf(value byte) []byte {
	return []byte{value, value, value, value, value, value, value, value, value, value, value, value, value, value, value, value}
}
