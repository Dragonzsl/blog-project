package presentation

import (
	"strings"
	"testing"
)

func TestDefaultThemeSidebarAndArticleDirectoryContracts(t *testing.T) {
	theme, err := NewDefaultTheme(NewMarkdown())
	if err != nil {
		t.Fatal(err)
	}

	navigation := Navigation{
		CurrentPath: "/posts/sidebar-contract",
		Primary: []NavigationLink{
			{Label: "关于本站", URL: "/about"},
			{Label: "工程笔记", URL: "/notes", Children: []NavigationLink{
				{Label: "侧边栏设计", URL: "/notes/sidebar-contract"},
			}},
		},
		Footer: []NavigationLink{{Label: "作者主页", URL: "https://example.com/author", External: true}},
	}

	withTOC, err := theme.RenderArticlePage("示例博客", ArticleData{
		Kind:         "article",
		Title:        "侧边栏导航契约",
		Slug:         "sidebar-contract",
		BodyMarkdown: "## 第一章\n\n正文。\n\n### 细节\n\n更多正文。",
	}, false, "/articles", navigation, PageMetadata{})
	if err != nil {
		t.Fatal(err)
	}
	page := string(withTOC)
	for _, expected := range []string{
		`id="public-sidebar"`,
		`data-drawer-name="site"`,
		`aria-label="网站导航"`,
		`aria-hidden="false"`,
		`data-sidebar-toggle`,
		`aria-current="page"`,
		"工程笔记",
		"侧边栏设计",
		`data-drawer-name="toc"`,
		`本文目录`,
		`data-sidebar-scrim="toc"`,
	} {
		if !strings.Contains(page, expected) {
			t.Fatalf("article with toc missing %q: %s", expected, page)
		}
	}
	if strings.Contains(page, `<details class="article-toc"`) || strings.Contains(page, "展开 / 收起") {
		t.Fatalf("article still uses the legacy top directory control: %s", page)
	}

	withoutTOC, err := theme.RenderArticlePage("示例博客", ArticleData{
		Kind:         "article",
		Title:        "没有目录的文章",
		Slug:         "without-toc",
		BodyMarkdown: "只有一段正文，没有二级标题。",
	}, false, "/articles", navigation, PageMetadata{})
	if err != nil {
		t.Fatal(err)
	}
	plainPage := string(withoutTOC)
	if !strings.Contains(plainPage, `class="article article-without-toc"`) {
		t.Fatalf("article without toc did not use the single-column layout: %s", plainPage)
	}
	for _, forbidden := range []string{`data-drawer-name="toc"`, "本文目录", "article-toc-trigger"} {
		if strings.Contains(plainPage, forbidden) {
			t.Fatalf("article without toc leaked directory control %q: %s", forbidden, plainPage)
		}
	}

	if len(theme.JS()) > 15<<10 {
		t.Fatalf("default theme JS is %d bytes, budget is 15 KiB", len(theme.JS()))
	}
	css := string(theme.CSS())
	for _, expected := range []string{
		`.js .public-sidebar[aria-hidden="false"]`,
		`html:not(.js) .public-sidebar`,
		`html:not(.js) .sidebar-toggle`,
	} {
		if !strings.Contains(css, expected) {
			t.Fatalf("default theme CSS missing sidebar fallback contract %q", expected)
		}
	}
}
