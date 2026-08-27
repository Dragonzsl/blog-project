package importer

import (
	"bytes"
	"html"
	"net/url"
	"strings"

	xhtml "golang.org/x/net/html"
)

// htmlToMarkdown is intentionally conservative: it maps common editorial
// elements and leaves unsupported text intact. It never downloads referenced
// media, which keeps offline imports bounded and reproducible.
func htmlToMarkdown(source string) string {
	source = strings.TrimSpace(source)
	if source == "" {
		return ""
	}
	root, err := xhtml.Parse(strings.NewReader(source))
	if err != nil {
		return strings.TrimSpace(html.UnescapeString(source))
	}
	var output bytes.Buffer
	var walk func(*xhtml.Node, int)
	walk = func(node *xhtml.Node, listDepth int) {
		if node == nil {
			return
		}
		if node.Type == xhtml.TextNode {
			output.WriteString(html.UnescapeString(node.Data))
			return
		}
		if node.Type != xhtml.ElementNode {
			for child := node.FirstChild; child != nil; child = child.NextSibling {
				walk(child, listDepth)
			}
			return
		}
		tag := strings.ToLower(node.Data)
		if tag == "script" || tag == "style" || tag == "noscript" {
			return
		}
		if strings.HasPrefix(tag, "h") && len(tag) == 2 && tag[1] >= '1' && tag[1] <= '6' {
			level := int(tag[1] - '0')
			output.WriteString("\n\n" + strings.Repeat("#", level) + " ")
			for child := node.FirstChild; child != nil; child = child.NextSibling {
				walk(child, listDepth)
			}
			output.WriteString("\n\n")
			return
		}
		switch tag {
		case "p", "div", "section", "article", "blockquote", "figure", "header", "footer":
			output.WriteString("\n\n")
			for child := node.FirstChild; child != nil; child = child.NextSibling {
				walk(child, listDepth)
			}
			output.WriteString("\n\n")
		case "br":
			output.WriteString("\n")
		case "strong", "b":
			output.WriteString("**")
			for child := node.FirstChild; child != nil; child = child.NextSibling {
				walk(child, listDepth)
			}
			output.WriteString("**")
		case "em", "i":
			output.WriteString("*")
			for child := node.FirstChild; child != nil; child = child.NextSibling {
				walk(child, listDepth)
			}
			output.WriteString("*")
		case "code":
			output.WriteString("`")
			for child := node.FirstChild; child != nil; child = child.NextSibling {
				walk(child, listDepth)
			}
			output.WriteString("`")
		case "pre":
			output.WriteString("\n\n```")
			for child := node.FirstChild; child != nil; child = child.NextSibling {
				walk(child, listDepth)
			}
			output.WriteString("```\n\n")
		case "ul", "ol":
			output.WriteString("\n\n")
			for child := node.FirstChild; child != nil; child = child.NextSibling {
				walk(child, listDepth+1)
			}
			output.WriteString("\n")
		case "li":
			output.WriteString(strings.Repeat("  ", max(0, listDepth-1)) + "- ")
			for child := node.FirstChild; child != nil; child = child.NextSibling {
				walk(child, listDepth)
			}
			output.WriteString("\n")
		case "a":
			href := ""
			for _, attr := range node.Attr {
				if attr.Key == "href" {
					href = strings.TrimSpace(attr.Val)
					break
				}
			}
			if parsed, err := url.Parse(href); err != nil || parsed.Scheme != "" && parsed.Scheme != "http" && parsed.Scheme != "https" {
				href = ""
			}
			output.WriteString("[")
			for child := node.FirstChild; child != nil; child = child.NextSibling {
				walk(child, listDepth)
			}
			if href != "" {
				output.WriteString("](" + href + ")")
			} else {
				output.WriteString("]")
			}
		case "img":
			src, alt := "", ""
			for _, attr := range node.Attr {
				switch attr.Key {
				case "src":
					src = strings.TrimSpace(attr.Val)
				case "alt":
					alt = strings.TrimSpace(attr.Val)
				}
			}
			if parsed, err := url.Parse(src); err != nil || parsed.Scheme != "" && parsed.Scheme != "http" && parsed.Scheme != "https" {
				src = ""
			}
			if src != "" {
				output.WriteString("![" + alt + "](" + src + ")")
			}
		default:
			for child := node.FirstChild; child != nil; child = child.NextSibling {
				walk(child, listDepth)
			}
		}
	}
	walk(root, 0)
	result := strings.TrimSpace(output.String())
	result = strings.ReplaceAll(result, "\n\n\n", "\n\n")
	return result
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
