package presentation

import (
	"bytes"
	"fmt"
	"html/template"
	"regexp"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
)

type Markdown struct {
	engine goldmark.Markdown
	policy *bluemonday.Policy
}

func NewMarkdown() *Markdown {
	policy := bluemonday.NewPolicy()
	policy.AllowElements(
		"p", "br", "hr", "h1", "h2", "h3", "h4", "h5", "h6",
		"blockquote", "ul", "ol", "li", "pre", "code", "em", "strong",
		"del", "a", "img", "table", "thead", "tbody", "tfoot", "tr", "th", "td",
		"sup", "sub", "div",
	)
	policy.AllowAttrs("href", "title").OnElements("a")
	policy.AllowAttrs("src", "alt", "title", "width", "height").OnElements("img")
	policy.AllowAttrs("id").Matching(regexp.MustCompile(`^[\p{L}\p{N}_:.-]{1,160}$`)).Globally()
	policy.AllowAttrs("class").Matching(regexp.MustCompile(`^[A-Za-z0-9 _:-]{1,160}$`)).OnElements("code", "div", "ol", "li", "a")
	policy.AllowAttrs("role").Matching(regexp.MustCompile(`^doc-(endnotes|backlink|noteref)$`)).OnElements("div", "a")
	policy.AllowRelativeURLs(true)
	policy.AllowURLSchemes("http", "https", "mailto")
	policy.RequireNoReferrerOnLinks(true)

	return &Markdown{
		engine: goldmark.New(
			goldmark.WithExtensions(extension.GFM, extension.Footnote, extension.Typographer),
			goldmark.WithParserOptions(parser.WithAutoHeadingID()),
		),
		policy: policy,
	}
}

func (m *Markdown) Render(source string) (template.HTML, error) {
	var rendered bytes.Buffer
	if err := m.engine.Convert([]byte(source), &rendered); err != nil {
		return "", fmt.Errorf("render Markdown: %w", err)
	}
	sanitized := m.policy.SanitizeBytes(rendered.Bytes())
	return template.HTML(sanitized), nil
}
