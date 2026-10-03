package presentation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadCelPanelTheme(t *testing.T) *Theme {
	t.Helper()
	root := filepath.Join("..", "..", "themes", "cel-panel")
	manifestBytes, err := os.ReadFile(filepath.Join(root, "theme.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest ThemeManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Version != "2.2.4" {
		t.Fatalf("theme version = %q, want 2.2.4", manifest.Version)
	}
	theme, err := NewThemeFromDirectory(root, manifest)
	if err != nil {
		t.Fatal(err)
	}
	return theme
}

func TestCelPanelThemeRendersEveryPublicView(t *testing.T) {
	theme := loadCelPanelTheme(t)
	navigation := Navigation{CurrentPath: "/posts/cel-panel", Features: FeatureFlags{Comments: true, CommentsMode: "local", Newsletter: true}}
	for _, expected := range []string{".prose h1", ".prose .code-block", ".prose .code-toolbar", ".prose .code-copy-button", ".hero-poster", ".poster-tv-body", ".signal-sticker", ".hero-speed-lines", ".signal-hero", ".signal-hero-poster", ".episode-grid", ".weekly-broadcast", ".signal-mode-preview", ".broadcast-badge", "--yellow: #ffd12e"} {
		if !strings.Contains(string(theme.CSS()), expected) {
			t.Errorf("theme CSS missing article formatting rule %q", expected)
		}
	}

	home, err := theme.RenderHomePageWithView("番剧信号站", HomePageData{Recent: []ArticleCard{{Path: "/posts/cel-panel", Title: "一集测试放送", Excerpt: "测试片段。", PublishedAt: "2026-08-30"}}}, navigation, PageMetadata{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	article, err := theme.RenderArticlePage("番剧信号站", ArticleData{Kind: "article", Title: "一篇测试文章", Slug: "cel-panel", BodyMarkdown: "## 第一格\n\n正文。\n\n### 细节\n\n更多正文。", Cover: &MediaData{URL: "/media/cover.jpg", Alt: "测试封面", Width: 1200, Height: 800}}, false, "/articles", navigation, PageMetadata{})
	if err != nil {
		t.Fatal(err)
	}
	listing, err := theme.RenderCollectionPage("番剧信号站", CollectionView{Title: "文章", Description: "所有文章", Items: []ArticleCard{{Path: "/posts/cel-panel", Title: "一集测试放送", PublishedAt: "2026年09月05日", ReadingTime: 1, Category: &TermData{Name: "工具效率", URL: "/categories/productivity"}}}}, navigation, PageMetadata{})
	if err != nil {
		t.Fatal(err)
	}
	search, err := theme.RenderSearch("番剧信号站", SearchPageData{Searched: true}, navigation, PageMetadata{})
	if err != nil {
		t.Fatal(err)
	}
	directory, err := theme.RenderDirectoryPage("番剧信号站", DirectoryView{Title: "站点索引"}, navigation, PageMetadata{})
	if err != nil {
		t.Fatal(err)
	}
	status, err := theme.RenderStatusPage("番剧信号站", StatusView{Code: 404, Title: "页面不存在", Message: "这格分镜还没有内容。"}, navigation, PageMetadata{})
	if err != nil {
		t.Fatal(err)
	}

	for name, body := range map[string][]byte{
		"home": home, "article": article, "listing": listing, "search": search, "directory": directory, "status": status,
	} {
		if !strings.Contains(string(body), theme.AssetURL()) {
			t.Errorf("%s page missing fingerprinted theme asset URL", name)
		}
	}
	for _, expected := range []string{"ANIME SIGNAL", "ON AIR", "TODAY'S EPISODE", "今天的故事", "signal-hero", "signal-hero-poster", "episode-grid", "weekly-broadcast", "WEEKLY", "BROADCAST", "data-theme-toggle", "data-newsletter-form"} {
		if !strings.Contains(string(home), expected) {
			t.Errorf("home page missing %q", expected)
		}
	}
	for _, expected := range []string{"data-comments-slot", `data-drawer-name="toc"`, "ON AIR", "EPISODE FILE", "第一格", "/media/cover.jpg"} {
		if !strings.Contains(string(article), expected) {
			t.Errorf("article page missing %q", expected)
		}
	}
	if !strings.Contains(string(listing), `class="article-reading-time">· 1 分钟</span>`) {
		t.Fatal("listing reading time is missing its non-breaking metadata unit")
	}
	if !strings.Contains(string(directory), "WORLD BUILDING") {
		t.Fatal("custom directory template was replaced by the fallback template")
	}
	if !strings.Contains(string(status), "NO EPISODE FOUND") {
		t.Fatal("custom status template was replaced by the fallback template")
	}
}
