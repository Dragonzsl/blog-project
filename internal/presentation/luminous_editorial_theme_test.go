package presentation

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadLuminousEditorialTheme(t *testing.T) *Theme {
	t.Helper()
	root := filepath.Join("..", "..", "themes", "luminous-editorial")
	manifestBytes, err := os.ReadFile(filepath.Join(root, "theme.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest ThemeManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.ID != "luminous-editorial" {
		t.Fatalf("theme id = %q, want luminous-editorial", manifest.ID)
	}
	theme, err := NewThemeFromDirectory(root, manifest)
	if err != nil {
		t.Fatal(err)
	}
	return theme
}

func TestLuminousEditorialThemeRendersEveryPublicView(t *testing.T) {
	theme := loadLuminousEditorialTheme(t)
	navigation := Navigation{CurrentPath: "/posts/luminous-editorial", Features: FeatureFlags{Comments: true, CommentsMode: "local", Newsletter: true}}
	view := HomePageData{
		Featured:   &ArticleCard{Path: "/posts/featured", Title: "一篇值得先读的文章", Excerpt: "从这里开始阅读。", PublishedAt: "2026年09月18日", PublishedISO: "2026-09-18T00:00:00Z", Category: &TermData{Name: "设计", URL: "/categories/design"}},
		Recent:     []ArticleCard{{Path: "/posts/recent", Title: "最近的一篇文章", Excerpt: "最近的文字。", PublishedAt: "2026年09月17日", PublishedISO: "2026-09-17T00:00:00Z", ReadingTime: 4, Category: &TermData{Name: "阅读", URL: "/categories/reading"}}},
		Categories: []TermSummary{{Name: "设计", URL: "/categories/design", ArticleCount: 2, Description: "设计与系统。"}},
		Archive:    []ArchiveMonthView{{Label: "2026年09月", Count: 2, URL: "/archive/2026/09"}},
	}
	home, err := theme.RenderHomePageWithView("澄光编辑室", view, navigation, PageMetadata{Description: "文章与思考。"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	article, err := theme.RenderArticlePage("澄光编辑室", ArticleData{Kind: "article", Title: "一篇完整的文章", Slug: "complete-article", Excerpt: "文章摘要。", BodyMarkdown: "## 第一节\n\n正文。\n\n### 细节\n\n更多正文。", TOC: []HeadingData{{ID: "第一节", Text: "第一节", Level: 2}}, Cover: &MediaData{URL: "/media/cover.jpg", Alt: "测试封面", Width: 1200, Height: 800}}, false, "/articles", navigation, PageMetadata{CanonicalURL: "https://example.test/posts/complete-article"})
	if err != nil {
		t.Fatal(err)
	}
	listing, err := theme.RenderCollectionPage("澄光编辑室", CollectionView{Title: "文章", Description: "所有文章", Items: view.Recent}, navigation, PageMetadata{})
	if err != nil {
		t.Fatal(err)
	}
	search, err := theme.RenderSearch("澄光编辑室", SearchPageData{Searched: true, Query: "文章"}, navigation, PageMetadata{})
	if err != nil {
		t.Fatal(err)
	}
	directory, err := theme.RenderDirectoryPage("澄光编辑室", DirectoryView{Title: "内容索引", Categories: view.Categories, ArchiveYears: []ArchiveYearView{{Year: 2026, Total: 2, Months: view.Archive}}}, navigation, PageMetadata{})
	if err != nil {
		t.Fatal(err)
	}
	status, err := theme.RenderStatusPage("澄光编辑室", StatusView{Code: 404, Title: "页面不存在", Message: "这条路径还没有内容。"}, navigation, PageMetadata{})
	if err != nil {
		t.Fatal(err)
	}

	for name, body := range map[string][]byte{"home": home, "article": article, "listing": listing, "search": search, "directory": directory, "status": status} {
		if !strings.Contains(string(body), theme.AssetURL()) {
			t.Errorf("%s page missing fingerprinted theme asset URL", name)
		}
		style := "body { --accent: #6C5CE7; }"
		if !strings.Contains(string(body), "<style>"+style+"</style>") {
			t.Errorf("%s page missing theme settings stylesheet", name)
		}
		response := httptest.NewRecorder()
		setPublicSecurityHeaders(response, body)
		digest := sha256.Sum256([]byte(style))
		allowed := "style-src 'self' 'sha256-" + base64.StdEncoding.EncodeToString(digest[:]) + "'"
		if !strings.Contains(response.Header().Get("Content-Security-Policy"), allowed) {
			t.Errorf("%s page blocks its theme settings stylesheet", name)
		}
	}
	for _, expected := range []string{"内容光谱", "从这里开始", "最近文章", "data-theme-toggle", "data-newsletter-form"} {
		if !strings.Contains(string(home), expected) {
			t.Errorf("home page missing %q", expected)
		}
	}
	for _, expected := range []string{"data-comments-slot", `data-drawer-name="toc"`, "第一节", "/media/cover.jpg", "data-reading-progress"} {
		if !strings.Contains(string(article), expected) {
			t.Errorf("article page missing %q", expected)
		}
	}
	if !strings.Contains(string(listing), "最近的一篇文章") {
		t.Error("listing page did not render its article content")
	}
	if !strings.Contains(string(directory), "设计") {
		t.Error("directory page did not render its category")
	}
	if !strings.Contains(string(status), "页面不存在") {
		t.Error("status page did not render its error title")
	}
}

func TestLuminousThemeAccentIsHashedWithoutAllowingStyleAttributes(t *testing.T) {
	theme := loadLuminousEditorialTheme(t).WithSettings(map[string]any{"accent": "#125634"})
	body, err := theme.RenderStatusPage("测试站点", StatusView{Code: 404, Title: "不存在"}, Navigation{}, PageMetadata{})
	if err != nil {
		t.Fatal(err)
	}
	style := "body { --accent: #125634; }"
	if !strings.Contains(string(body), "<style>"+style+"</style>") {
		t.Fatal("custom accent did not reach the rendered stylesheet")
	}
	response := httptest.NewRecorder()
	setPublicSecurityHeaders(response, body)
	policy := response.Header().Get("Content-Security-Policy")
	digest := sha256.Sum256([]byte(style))
	if !strings.Contains(policy, "style-src 'self' 'sha256-"+base64.StdEncoding.EncodeToString(digest[:])+"';") {
		t.Fatalf("stylesheet hash missing from CSP: %s", policy)
	}
	if strings.Contains(policy, "unsafe-inline") || strings.Contains(policy, "unsafe-hashes") {
		t.Fatal("CSP permits unrestricted styles or style attributes")
	}
}
