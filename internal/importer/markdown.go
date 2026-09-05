package importer

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
	"time"
)

type markdownParser struct{}

func (markdownParser) Parse(data []byte, source string) ([]Item, []string, error) {
	parts := splitMarkdownFiles(data)
	items := make([]Item, 0, len(parts))
	var warnings []string
	for _, part := range parts {
		item, itemWarnings := parseMarkdownDocument(part.name, part.data)
		warnings = append(warnings, itemWarnings...)
		if item.Title == "" && strings.TrimSpace(item.BodyMarkdown) == "" {
			warnings = append(warnings, "empty markdown document skipped: "+part.name)
			continue
		}
		if item.Title == "" {
			item.Title = part.name
		}
		if item.Slug == "" {
			item.Slug = importSlug(strings.TrimSuffix(part.name, ".md"))
		}
		if item.BodyMarkdown == "" {
			item.BodyMarkdown = "# " + item.Title
			warnings = append(warnings, "markdown document had no body; a title placeholder was created: "+part.name)
		}
		item.SourceID = part.name
		items = append(items, item)
	}
	return items, warnings, nil
}

type markdownPart struct {
	name string
	data []byte
}

func splitMarkdownFiles(data []byte) []markdownPart {
	const marker = "\n---FILE "
	if !bytes.Contains(data, []byte(marker)) {
		return []markdownPart{{name: "content.md", data: data}}
	}
	var result []markdownPart
	rest := data
	for len(rest) > 0 {
		index := bytes.Index(rest, []byte(marker))
		if index < 0 {
			if len(bytes.TrimSpace(rest)) > 0 {
				result = append(result, markdownPart{name: "content.md", data: rest})
			}
			break
		}
		if index > 0 && len(bytes.TrimSpace(rest[:index])) > 0 {
			result = append(result, markdownPart{name: "content.md", data: rest[:index]})
		}
		rest = rest[index+len(marker):]
		end := bytes.Index(rest, []byte("---\n"))
		if end < 0 {
			result = append(result, markdownPart{name: "content.md", data: rest})
			break
		}
		name := strings.TrimSpace(string(rest[:end]))
		rest = rest[end+len("---\n"):]
		next := bytes.Index(rest, []byte(marker))
		if next < 0 {
			result = append(result, markdownPart{name: name, data: rest})
			break
		}
		result = append(result, markdownPart{name: name, data: rest[:next]})
		rest = rest[next:]
	}
	return result
}

func parseMarkdownDocument(name string, data []byte) (Item, []string) {
	item := Item{Kind: "article", Slug: "", BodyMarkdown: string(data)}
	var warnings []string
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if strings.HasPrefix(text, "---\n") {
		end := strings.Index(text[4:], "\n---")
		if end >= 0 {
			frontmatter := text[4 : 4+end]
			var frontmatterWarnings []string
			item, frontmatterWarnings = parseMarkdownFrontmatter(frontmatter)
			warnings = append(warnings, frontmatterWarnings...)
			item.BodyMarkdown = strings.TrimLeft(text[4+end+len("\n---"):], "\n")
		}
	}
	if item.Title == "" {
		scanner := bufio.NewScanner(strings.NewReader(item.BodyMarkdown))
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if strings.HasPrefix(line, "# ") {
				item.Title = strings.TrimSpace(strings.TrimPrefix(line, "# "))
				break
			}
		}
	}
	if item.Excerpt == "" {
		item.Excerpt = firstParagraph(item.BodyMarkdown)
	}
	if item.Kind == "" {
		item.Kind = "article"
	}
	if item.Kind != "article" && item.Kind != "page" {
		warnings = append(warnings, fmt.Sprintf("unknown markdown kind %q in %s; imported as article", item.Kind, name))
		item.Kind = "article"
	}
	return item, warnings
}

func parseMarkdownFrontmatter(raw string) (Item, []string) {
	item := Item{Kind: "article"}
	var warnings []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			key, value, ok = strings.Cut(line, "=")
		}
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.Trim(strings.TrimSpace(value), "\"'")
		switch key {
		case "title", "name":
			item.Title = value
		case "slug", "permalink":
			item.Slug = importSlug(value)
		case "excerpt", "summary", "description":
			item.Excerpt = value
		case "seo_title":
			item.SEOTitle = value
		case "seo_description":
			item.SEODescription = value
		case "type", "kind":
			item.Kind = strings.ToLower(value)
		case "category":
			item.Category = value
		case "tags", "tag":
			value = strings.Trim(value, "[]")
			for _, tag := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ';' }) {
				if tag = strings.Trim(strings.TrimSpace(tag), "\"'"); tag != "" {
					item.Tags = append(item.Tags, tag)
				}
			}
		case "date", "published_at", "published":
			if parsed, err := parseImportTime(value); err == nil {
				item.PublishedAt = &parsed
			}
		case "cover_media_public_id", "cover_media_id", "cover":
			if publicID, err := decodeCoverMediaPublicID(value); err == nil {
				item.CoverMediaPublicID = publicID
			} else {
				warnings = append(warnings, "invalid cover media public ID was ignored")
			}
		}
	}
	return item, warnings
}

func firstParagraph(body string) string {
	for _, paragraph := range strings.Split(body, "\n\n") {
		paragraph = strings.TrimSpace(paragraph)
		paragraph = strings.TrimLeft(paragraph, "# ")
		if paragraph != "" {
			if len([]rune(paragraph)) > 240 {
				return string([]rune(paragraph)[:240])
			}
			return paragraph
		}
	}
	return ""
}

func parseImportTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05 -0700", "2006-01-02 15:04:05", "2006-01-02"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported timestamp %q", value)
}
