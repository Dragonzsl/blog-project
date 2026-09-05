package presentation

import (
	"strings"
	"testing"
)

func TestStageSixAdaptiveReadingContracts(t *testing.T) {
	theme, err := NewDefaultTheme(NewMarkdown())
	if err != nil {
		t.Fatal(err)
	}

	withTOC, err := theme.RenderArticlePage("示例博客", ArticleData{
		Kind:         "article",
		Title:        "阶段六阅读布局",
		Slug:         "stage-six-reading",
		BodyMarkdown: "## 第一章\n\n正文。\n\n### 细节\n\n更多正文。",
	}, false, "/articles", Navigation{}, PageMetadata{})
	if err != nil {
		t.Fatal(err)
	}
	page := string(withTOC)
	for _, expected := range []string{
		`data-sidebar-breakpoint="1439"`,
		`data-sidebar-collapse`,
		`data-toc-collapse`,
		`data-back-to-top`,
		`data-theme-mode="system"`,
		`data-drawer-name="toc"`,
	} {
		if !strings.Contains(page, expected) {
			t.Fatalf("stage six article missing %q: %s", expected, page)
		}
	}
	readingIndex := strings.Index(page, `class="article-reading-column"`)
	tocIndex := strings.Index(page, `id="article-toc"`)
	if readingIndex < 0 || tocIndex < 0 || tocIndex < readingIndex {
		t.Fatalf("article directory is not rendered after the reading column: %s", page)
	}

	withoutTOC, err := theme.RenderArticlePage("示例博客", ArticleData{
		Kind:         "article",
		Title:        "单栏阅读",
		Slug:         "stage-six-without-toc",
		BodyMarkdown: "# 页面标题\n\n只有一段正文，没有目录标题。",
	}, false, "/articles", Navigation{}, PageMetadata{})
	if err != nil {
		t.Fatal(err)
	}
	plainPage := string(withoutTOC)
	if !strings.Contains(plainPage, `class="article article-without-toc"`) {
		t.Fatalf("article without headings did not use the single-column layout: %s", plainPage)
	}
	for _, forbidden := range []string{`id="article-toc"`, `data-toc-collapse`, "本文目录"} {
		if strings.Contains(plainPage, forbidden) {
			t.Fatalf("article without headings leaked directory control %q: %s", forbidden, plainPage)
		}
	}

	if len(theme.JS()) > 15<<10 {
		t.Fatalf("default theme JS is %d bytes, budget is 15 KiB", len(theme.JS()))
	}
	css := string(theme.CSS())
	for _, expected := range []string{
		`--article-toc-width`,
		`@media (min-width: 1440px)`,
		`data-site-sidebar`,
		`data-toc-collapsed`,
		`position: fixed`,
		`.site-header .sidebar-toggle`,
		`html[data-sidebar-open="toc"] .article-toc-trigger`,
		`.public-sidebar .sidebar-nav`,
		`overflow-y: auto`,
		`.back-to-top`,
		`.article-meta .article-reading-time`,
		`.related-articles .article-reading-time`,
		`.comments-form-actions`,
		`white-space: nowrap`,
	} {
		if !strings.Contains(css, expected) {
			t.Fatalf("stage six stylesheet missing %q", expected)
		}
	}
	script := string(theme.JS())
	for _, expected := range []string{
		"blog-theme",
		"prefers-color-scheme",
		"prefers-reduced-motion",
		"data-back-to-top",
		"data-toc-collapsed",
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("stage six script missing %q", expected)
		}
	}
}
