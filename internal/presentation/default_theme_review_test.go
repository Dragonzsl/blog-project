package presentation

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestDefaultThemeHomeAcceptsEmptyNavigationAndShortDates(t *testing.T) {
	theme, err := NewDefaultTheme(NewMarkdown())
	if err != nil {
		t.Fatal(err)
	}
	card := ArticleCard{Title: "短日期文章", Path: "/posts/short-date", PublishedAt: "今日", PublishedISO: ""}
	body, err := theme.RenderHomePageWithView("博客", HomePageData{Featured: &card, Recent: []ArticleCard{card, card}}, Navigation{}, PageMetadata{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "短日期文章") {
		t.Fatal("home content missing")
	}
}

func TestDefaultThemeFingerprintAssets(t *testing.T) {
	theme, err := NewDefaultTheme(NewMarkdown())
	if err != nil {
		t.Fatal(err)
	}
	handler := &HTTPHandler{theme: theme}
	router := chi.NewRouter()
	router.Get("/assets/theme/{themeID}/{fingerprint}/theme-search.js", handler.searchScriptAsset)
	router.Head("/assets/theme/{themeID}/{fingerprint}/theme-search.js", handler.searchScriptAsset)
	router.Get("/assets/theme/{themeID}/{fingerprint}/song-ink-landscape-background.webp", handler.landscapeAsset)
	router.Head("/assets/theme/{themeID}/{fingerprint}/song-ink-landscape-background.webp", handler.landscapeAsset)
	for _, asset := range []struct {
		path, mime string
		size       int
	}{
		{theme.SearchScriptURL(), "application/javascript; charset=utf-8", len(theme.SearchJS())},
		{"/assets/theme/default/" + theme.LandscapeHash() + "/song-ink-landscape-background.webp", "image/webp", len(theme.Landscape())},
	} {
		t.Run(asset.mime, func(t *testing.T) {
			get := httptest.NewRecorder()
			router.ServeHTTP(get, httptest.NewRequest("GET", asset.path, nil))
			if get.Code != 200 || get.Body.Len() != asset.size || get.Header().Get("Content-Type") != asset.mime || !strings.Contains(get.Header().Get("Cache-Control"), "immutable") {
				t.Fatalf("asset response: %d %v", get.Code, get.Header())
			}
			for _, method := range []string{"HEAD", "GET"} {
				request := httptest.NewRequest(method, asset.path, nil)
				if method == "GET" {
					request.Header.Set("If-None-Match", get.Header().Get("ETag"))
				}
				response := httptest.NewRecorder()
				router.ServeHTTP(response, request)
				expected := 200
				if method == "GET" {
					expected = 304
				}
				if response.Code != expected || response.Body.Len() != 0 {
					t.Fatalf("%s: %d body=%d", method, response.Code, response.Body.Len())
				}
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest("GET", strings.Replace(asset.path, "/default/", "/wrong/", 1), nil))
			if response.Code != 404 {
				t.Fatal("wrong theme fingerprint accepted")
			}
		})
	}
}

func TestThemeRevalidationETagPrecedesUnchangedTimestamp(t *testing.T) {
	handler := &HTTPHandler{}
	modified := "Wed, 01 Oct 2025 00:00:00 GMT"
	for _, cached := range []bool{true, false} {
		request := httptest.NewRequest("GET", "/", nil)
		request.Header.Set("If-None-Match", `"previous-theme"`)
		request.Header.Set("If-Modified-Since", modified)
		response := httptest.NewRecorder()
		if cached {
			handler.writeCacheEntry(response, request, CacheEntry{Body: []byte("new theme"), Status: 200, ContentType: "text/html; charset=utf-8", ETag: `"new-theme"`, LastModified: modified}, "HIT")
		} else {
			handler.writeGenerated(response, request, "text/plain", []byte("new theme"), modified)
		}
		if response.Code != http.StatusOK || response.Body.String() != "new theme" {
			t.Fatalf("cached=%v stale theme revalidation: %d", cached, response.Code)
		}
	}
}
