package contentapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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

func bytesOf(value byte) []byte {
	return []byte{value, value, value, value, value, value, value, value, value, value, value, value, value, value, value, value}
}
