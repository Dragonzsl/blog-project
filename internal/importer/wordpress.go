package importer

import (
	"encoding/xml"
	"fmt"
	"strings"
)

type wordpressParser struct{}

type wxrChannel struct {
	Items []wxrItem `xml:"item"`
}

type wxrDocument struct {
	Channel wxrChannel `xml:"channel"`
}

type wxrItem struct {
	ID       string
	Title    string
	Slug     string
	Status   string
	Kind     string
	Excerpt  string
	BodyHTML string
	Link     string
	PubDate  string
	Category string
	Tags     []string
}

func (item *wxrItem) UnmarshalXML(decoder *xml.Decoder, start xml.StartElement) error {
	for {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		switch value := token.(type) {
		case xml.StartElement:
			var text string
			switch value.Name.Local {
			case "title":
				if err := decoder.DecodeElement(&item.Title, &value); err != nil {
					return err
				}
			case "post_id":
				if err := decoder.DecodeElement(&item.ID, &value); err != nil {
					return err
				}
			case "post_name":
				if err := decoder.DecodeElement(&item.Slug, &value); err != nil {
					return err
				}
			case "post_status":
				if err := decoder.DecodeElement(&item.Status, &value); err != nil {
					return err
				}
			case "post_type":
				if err := decoder.DecodeElement(&item.Kind, &value); err != nil {
					return err
				}
			case "encoded":
				if err := decoder.DecodeElement(&item.BodyHTML, &value); err != nil {
					return err
				}
			case "excerpt":
				if err := decoder.DecodeElement(&item.Excerpt, &value); err != nil {
					return err
				}
			case "link":
				if err := decoder.DecodeElement(&item.Link, &value); err != nil {
					return err
				}
			case "pubDate":
				if err := decoder.DecodeElement(&item.PubDate, &value); err != nil {
					return err
				}
			case "category":
				if err := decoder.DecodeElement(&text, &value); err != nil {
					return err
				}
				domain := ""
				if value.Attr != nil {
					for _, attr := range value.Attr {
						if attr.Name.Local == "domain" {
							domain = attr.Value
							break
						}
					}
				}
				if domain == "post_tag" {
					item.Tags = append(item.Tags, strings.TrimSpace(text))
				} else if strings.TrimSpace(text) != "" {
					item.Category = strings.TrimSpace(text)
				}
			default:
				if err := decoder.Skip(); err != nil {
					return err
				}
			}
		case xml.EndElement:
			if value.Name == start.Name {
				return nil
			}
		}
	}
}

func (wordpressParser) Parse(data []byte, source string) ([]Item, []string, error) {
	var document wxrDocument
	decoder := xml.NewDecoder(strings.NewReader(string(data)))
	if err := decoder.Decode(&document); err != nil {
		return nil, nil, fmt.Errorf("decode WordPress WXR: %w", err)
	}
	var result []Item
	var warnings []string
	for index, value := range document.Channel.Items {
		kind := strings.ToLower(strings.TrimSpace(value.Kind))
		if kind != "post" && kind != "page" {
			continue
		}
		item := Item{SourceID: strings.TrimSpace(value.ID), Kind: "article", Title: strings.TrimSpace(value.Title), Slug: importSlug(value.Slug), Excerpt: strings.TrimSpace(value.Excerpt), Category: value.Category, Tags: cleanNames(value.Tags), OriginalStatus: strings.TrimSpace(value.Status)}
		if kind == "page" {
			item.Kind = "page"
		}
		if item.SourceID == "" {
			item.SourceID = fmt.Sprintf("item-%d", index+1)
		}
		if item.Slug == "" {
			item.Slug = importSlug(item.Title)
		}
		item.BodyMarkdown = htmlToMarkdown(value.BodyHTML)
		if item.BodyMarkdown == "" {
			item.BodyMarkdown = strings.TrimSpace(value.BodyHTML)
		}
		if item.Excerpt == "" {
			item.Excerpt = firstParagraph(item.BodyMarkdown)
		}
		if parsed, err := parseImportTime(value.PubDate); err == nil {
			item.PublishedAt = &parsed
		}
		if value.BodyHTML != "" {
			warnings = append(warnings, "WordPress HTML converted to Markdown; every item is imported as a draft")
		}
		if item.Title == "" {
			warnings = append(warnings, "WordPress item "+item.SourceID+" has no title and may conflict")
		}
		result = append(result, item)
	}
	return result, uniqueStrings(warnings), nil
}

func cleanNames(values []string) []string {
	seen := map[string]bool{}
	var result []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		key := strings.ToLower(value)
		if value != "" && !seen[key] {
			seen[key] = true
			result = append(result, value)
		}
	}
	return result
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	var result []string
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}
