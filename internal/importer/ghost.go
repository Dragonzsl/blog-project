package importer

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type ghostParser struct{}

func (ghostParser) Parse(data []byte, source string) ([]Item, []string, error) {
	var root any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, nil, fmt.Errorf("decode Ghost JSON: %w", err)
	}
	var result []Item
	var warnings []string
	walkJSON(root, "", func(value map[string]any, collection string) {
		if collection != "posts" && collection != "pages" {
			return
		}
		item := ghostItem(value, collection)
		if item.Title == "" && item.BodyMarkdown == "" {
			return
		}
		if item.Title == "" {
			warnings = append(warnings, "Ghost entry "+item.SourceID+" has no title and may conflict")
		}
		if strings.TrimSpace(asString(value["html"])) != "" || strings.TrimSpace(asString(value["mobiledoc"])) != "" {
			warnings = append(warnings, "Ghost HTML/Mobiledoc converted to Markdown; every item is imported as a draft")
		}
		if image := firstString(value, "feature_image", "og_image"); image != "" && len(item.CoverMediaPublicID) == 0 {
			warnings = append(warnings, "Ghost remote feature images are not downloaded; provide cover_media_public_id to preserve a local cover")
		}
		result = append(result, item)
	})
	return result, uniqueStrings(warnings), nil
}

func walkJSON(value any, collection string, visit func(map[string]any, string)) {
	switch current := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(current))
		for key := range current {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool {
			priority := func(key string) int {
				switch key {
				case "posts":
					return 0
				case "pages":
					return 1
				default:
					return 2
				}
			}
			left, right := priority(keys[i]), priority(keys[j])
			if left != right {
				return left < right
			}
			return keys[i] < keys[j]
		})
		for _, key := range keys {
			child := current[key]
			if key == "posts" || key == "pages" {
				if values, ok := child.([]any); ok {
					for _, entry := range values {
						if object, ok := entry.(map[string]any); ok {
							visit(object, key)
						}
					}
				}
				continue
			}
			walkJSON(child, key, visit)
		}
	case []any:
		for _, child := range current {
			walkJSON(child, collection, visit)
		}
	}
}

func ghostItem(value map[string]any, collection string) Item {
	item := Item{Kind: "article", SourceID: firstString(value, "uuid", "id", "slug"), Title: firstString(value, "title", "name"), Slug: importSlug(firstString(value, "slug", "title")), Excerpt: firstString(value, "custom_excerpt", "excerpt", "description"), SEOTitle: firstString(value, "meta_title"), SEODescription: firstString(value, "meta_description"), OriginalStatus: firstString(value, "status")}
	if publicID, err := decodeCoverMediaPublicID(firstString(value, "cover_media_public_id", "cover_media_id")); err == nil {
		item.CoverMediaPublicID = publicID
	}
	if collection == "pages" || strings.EqualFold(firstString(value, "type"), "page") {
		item.Kind = "page"
	}
	if markdown := strings.TrimSpace(firstString(value, "markdown")); markdown != "" {
		item.BodyMarkdown = markdown
	} else if bodyHTML := strings.TrimSpace(firstString(value, "html")); bodyHTML != "" {
		item.BodyMarkdown = htmlToMarkdown(bodyHTML)
	} else if mobiledoc := strings.TrimSpace(firstString(value, "mobiledoc")); mobiledoc != "" {
		item.BodyMarkdown = mobiledocToMarkdown(mobiledoc)
	}
	if item.BodyMarkdown == "" {
		item.BodyMarkdown = strings.TrimSpace(firstString(value, "lexical"))
	}
	if item.Excerpt == "" {
		item.Excerpt = firstParagraph(item.BodyMarkdown)
	}
	if parsed, err := parseImportTime(firstString(value, "published_at", "created_at")); err == nil {
		item.PublishedAt = &parsed
	}
	if tags, ok := value["tags"].([]any); ok {
		for _, raw := range tags {
			if object, ok := raw.(map[string]any); ok {
				if name := firstString(object, "name", "slug"); name != "" {
					item.Tags = append(item.Tags, name)
				}
			} else if name := strings.TrimSpace(asString(raw)); name != "" {
				item.Tags = append(item.Tags, name)
			}
		}
	}
	item.Tags = cleanNames(item.Tags)
	if primary, ok := value["primary_tag"].(map[string]any); ok {
		item.Tags = append(item.Tags, firstString(primary, "name", "slug"))
		item.Tags = cleanNames(item.Tags)
	}
	return item
}

func mobiledocToMarkdown(value string) string {
	var document any
	if json.Unmarshal([]byte(value), &document) != nil {
		return strings.TrimSpace(value)
	}
	var stringsFound []string
	collectJSONStrings(document, &stringsFound)
	return strings.TrimSpace(strings.Join(stringsFound, "\n\n"))
}

func collectJSONStrings(value any, result *[]string) {
	switch current := value.(type) {
	case string:
		if strings.TrimSpace(current) != "" && !strings.HasPrefix(current, "kg-card") {
			*result = append(*result, htmlToMarkdown(current))
		}
	case []any:
		for _, child := range current {
			collectJSONStrings(child, result)
		}
	case map[string]any:
		for _, child := range current {
			collectJSONStrings(child, result)
		}
	}
}

func firstString(value map[string]any, keys ...string) string {
	for _, key := range keys {
		if result := strings.TrimSpace(asString(value[key])); result != "" {
			return result
		}
	}
	return ""
}

func asString(value any) string {
	switch current := value.(type) {
	case string:
		return current
	case json.Number:
		return current.String()
	case float64:
		return fmt.Sprintf("%g", current)
	default:
		return ""
	}
}
