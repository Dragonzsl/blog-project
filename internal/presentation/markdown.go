package presentation

import (
	"bytes"
	"fmt"
	"html/template"
	"regexp"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
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
	enhanced, err := enhanceCodeBlocks(sanitized)
	if err != nil {
		return "", fmt.Errorf("enhance Markdown code blocks: %w", err)
	}
	return template.HTML(enhanced), nil
}

var codeLanguagePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_+#.-]{0,31}$`)

func enhanceCodeBlocks(source []byte) ([]byte, error) {
	root := &xhtml.Node{Type: xhtml.ElementNode, DataAtom: atom.Div, Data: "div"}
	nodes, err := xhtml.ParseFragment(bytes.NewReader(source), root)
	if err != nil {
		return nil, err
	}
	for _, node := range nodes {
		root.AppendChild(node)
	}
	enhanceCodeBlockNodes(root)

	var output bytes.Buffer
	for node := root.FirstChild; node != nil; node = node.NextSibling {
		if err := xhtml.Render(&output, node); err != nil {
			return nil, err
		}
	}
	return output.Bytes(), nil
}

func enhanceCodeBlockNodes(parent *xhtml.Node) {
	for child := parent.FirstChild; child != nil; {
		next := child.NextSibling
		if child.Type == xhtml.ElementNode && child.Data == "pre" {
			if code := directCodeChild(child); code != nil {
				wrapCodeBlock(parent, child, code)
			}
		} else if child.Type == xhtml.ElementNode {
			enhanceCodeBlockNodes(child)
		}
		child = next
	}
}

func directCodeChild(pre *xhtml.Node) *xhtml.Node {
	for child := pre.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == xhtml.ElementNode && child.Data == "code" {
			return child
		}
	}
	return nil
}

func wrapCodeBlock(parent, pre, code *xhtml.Node) {
	language := codeLanguage(code)
	label := codeLanguageLabel(language)
	wrapper := &xhtml.Node{
		Type:     xhtml.ElementNode,
		DataAtom: atom.Div,
		Data:     "div",
		Attr: []xhtml.Attribute{
			{Key: "class", Val: "code-block"},
			{Key: "data-code-block", Val: ""},
		},
	}
	toolbar := &xhtml.Node{Type: xhtml.ElementNode, DataAtom: atom.Div, Data: "div", Attr: []xhtml.Attribute{{Key: "class", Val: "code-toolbar"}}}
	languageNode := &xhtml.Node{Type: xhtml.ElementNode, DataAtom: atom.Span, Data: "span", Attr: []xhtml.Attribute{{Key: "class", Val: "code-language"}, {Key: "data-code-language", Val: language}}}
	languageNode.AppendChild(&xhtml.Node{Type: xhtml.TextNode, Data: label})
	copyButton := &xhtml.Node{
		Type:     xhtml.ElementNode,
		DataAtom: atom.Button,
		Data:     "button",
		Attr: []xhtml.Attribute{
			{Key: "type", Val: "button"},
			{Key: "class", Val: "code-copy-button"},
			{Key: "data-copy-code", Val: ""},
			{Key: "aria-label", Val: "复制" + label + "代码"},
		},
	}
	copyButton.AppendChild(&xhtml.Node{Type: xhtml.TextNode, Data: "复制代码"})
	status := &xhtml.Node{Type: xhtml.ElementNode, DataAtom: atom.Span, Data: "span", Attr: []xhtml.Attribute{{Key: "class", Val: "code-copy-status"}, {Key: "data-code-copy-status", Val: ""}, {Key: "role", Val: "status"}, {Key: "aria-live", Val: "polite"}}}
	toolbar.AppendChild(languageNode)
	toolbar.AppendChild(copyButton)
	toolbar.AppendChild(status)
	parent.InsertBefore(wrapper, pre)
	parent.RemoveChild(pre)
	wrapper.AppendChild(toolbar)
	wrapper.AppendChild(pre)
}

func codeLanguage(code *xhtml.Node) string {
	for _, attr := range code.Attr {
		if attr.Key != "class" {
			continue
		}
		for _, token := range strings.Fields(attr.Val) {
			if strings.HasPrefix(token, "language-") {
				language := strings.TrimPrefix(token, "language-")
				if codeLanguagePattern.MatchString(language) {
					return strings.ToLower(language)
				}
			}
		}
	}
	return "text"
}

func codeLanguageLabel(language string) string {
	labels := map[string]string{
		"bash": "Bash", "c": "C", "cpp": "C++", "c++": "C++", "css": "CSS",
		"go": "Go", "golang": "Go", "html": "HTML", "java": "Java", "javascript": "JavaScript",
		"js": "JavaScript", "json": "JSON", "markdown": "Markdown", "md": "Markdown",
		"py": "Python", "python": "Python", "rust": "Rust", "ruby": "Ruby", "shell": "Shell",
		"sh": "Shell", "sql": "SQL", "svg": "SVG", "ts": "TypeScript", "typescript": "TypeScript",
		"xml": "XML", "yaml": "YAML", "yml": "YAML",
	}
	if label, ok := labels[language]; ok {
		return label
	}
	if language == "text" {
		return "纯文本"
	}
	return strings.ToUpper(language[:1]) + language[1:]
}
