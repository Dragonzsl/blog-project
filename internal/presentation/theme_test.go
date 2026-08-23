package presentation

import (
	"strings"
	"testing"
)

func TestDefaultThemeRendersSanitizedArticle(t *testing.T) {
	theme, err := NewDefaultTheme(NewMarkdown())
	if err != nil {
		t.Fatal(err)
	}
	body, err := theme.RenderArticle("纸上花园", ArticleData{
		Title:        "现代文章",
		Slug:         "modern-article",
		Excerpt:      "一段摘要",
		BodyMarkdown: "## 正文\n\n**安全内容**\n\n<script>alert(1)</script>",
	}, false, "")
	if err != nil {
		t.Fatal(err)
	}
	html := string(body)
	if !strings.Contains(html, "现代文章") || !strings.Contains(html, "<strong>安全内容</strong>") {
		t.Fatalf("article content missing: %s", html)
	}
	if strings.Contains(html, "<script") {
		t.Fatalf("unsafe script rendered: %s", html)
	}
	if !strings.Contains(html, theme.AssetURL()) {
		t.Fatalf("fingerprinted asset URL missing: %s", html)
	}
}
