package presentation

import (
	"strings"
	"testing"
)

func TestMarkdownRendersGFMAndRemovesUnsafeHTML(t *testing.T) {
	rendered, err := NewMarkdown().Render(`# 标题

| A | B |
|---|---|
| 1 | 2 |

[safe](https://example.com) [unsafe](javascript:alert(1))

<script>alert("x")</script>

<img src="javascript:alert(1)" onerror="alert(2)">
`)
	if err != nil {
		t.Fatal(err)
	}
	html := string(rendered)
	for _, forbidden := range []string{"<script", "javascript:", "onerror"} {
		if strings.Contains(strings.ToLower(html), forbidden) {
			t.Fatalf("rendered HTML contains %q: %s", forbidden, html)
		}
	}
	for _, expected := range []string{`<h1 id="heading">标题</h1>`, "<table>", `<a href="https://example.com"`} {
		if !strings.Contains(html, expected) {
			t.Fatalf("rendered HTML missing %q: %s", expected, html)
		}
	}
}

func TestMarkdownAllowsRelativeImages(t *testing.T) {
	rendered, err := NewMarkdown().Render(`![替代文字](/media/example.jpg "标题")`)
	if err != nil {
		t.Fatal(err)
	}
	html := string(rendered)
	if !strings.Contains(html, `src="/media/example.jpg"`) || !strings.Contains(html, `alt="替代文字"`) {
		t.Fatalf("relative image was removed: %s", html)
	}
}

func TestMarkdownEnhancesCodeBlocksWithLanguageAndCopyControl(t *testing.T) {
	rendered, err := NewMarkdown().Render("```go\nfmt.Println(\"hello\")\n```")
	if err != nil {
		t.Fatal(err)
	}
	html := string(rendered)
	for _, expected := range []string{
		`class="code-block"`,
		`data-code-language="go"`,
		">Go</span>",
		`data-copy-code`,
		`复制代码`,
		`fmt.Println(&#34;hello&#34;)`,
	} {
		if !strings.Contains(html, expected) {
			t.Fatalf("enhanced code block missing %q: %s", expected, html)
		}
	}
}
