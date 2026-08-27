package presentation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html/template"
	"time"

	defaulttheme "github.com/zhushilin/blog-project/themes/default"
)

type ArticleData struct {
	Kind         string
	Title        string
	Slug         string
	Excerpt      string
	BodyMarkdown string
	PublishedAt  *time.Time
	Category     *TermData
	Tags         []TermData
}

type TermData struct{ Name, URL string }
type NavigationLink struct {
	Label, URL string
	External   bool
	Children   []NavigationLink
}
type Navigation struct{ Primary, Footer []NavigationLink }

type PageMetadata struct {
	Title, Description, CanonicalURL, OpenGraphType, RSSURL string
	NoIndex                                                 bool
	JSONLD                                                  template.JS
}

type ArticleCard struct {
	Kind         string
	Path         string
	Title        string
	Slug         string
	Excerpt      string
	PublishedAt  string
	PublishedISO string
}

type SearchPageData struct {
	Query, Kind, Category, Tag, Sort string
	Searched, Invalid                bool
	Results                          []ArticleCard
}

type Theme struct {
	templates *template.Template
	markdown  *Markdown
	css       []byte
	assetHash string
	assetURL  string
	id        string
	version   string
	assetRoot string
}

func NewDefaultTheme(markdown *Markdown) (*Theme, error) {
	templates, err := template.ParseFS(defaulttheme.Files, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse default theme: %w", err)
	}
	css, err := defaulttheme.Files.ReadFile("assets/theme.css")
	if err != nil {
		return nil, fmt.Errorf("read default theme stylesheet: %w", err)
	}
	hash := sha256.Sum256(css)
	assetHash := hex.EncodeToString(hash[:8])
	return &Theme{
		templates: templates,
		markdown:  markdown,
		css:       css,
		assetHash: assetHash,
		assetURL:  "/assets/theme/default/" + assetHash + "/theme.css",
		id:        DefaultThemeID,
		version:   defaulttheme.Version,
	}, nil
}

func (t *Theme) Version() string {
	return defaulttheme.Version + "+" + t.assetHash
}

func (t *Theme) AssetURL() string  { return t.assetURL }
func (t *Theme) AssetHash() string { return t.assetHash }
func (t *Theme) CSS() []byte       { return t.css }

func (t *Theme) RenderArticle(siteName string, article ArticleData, preview bool, backURL string, navigation ...Navigation) ([]byte, error) {
	return t.RenderArticlePage(siteName, article, preview, backURL, firstNavigation(navigation), PageMetadata{})
}

func (t *Theme) RenderArticlePage(siteName string, article ArticleData, preview bool, backURL string, navigation Navigation, metadata PageMetadata) ([]byte, error) {
	body, err := t.markdown.Render(article.BodyMarkdown)
	if err != nil {
		return nil, err
	}
	view := struct {
		Kind         string
		Title        string
		Slug         string
		Excerpt      string
		BodyHTML     template.HTML
		PublishedAt  string
		PublishedISO string
		Category     *TermData
		Tags         []TermData
	}{
		Kind:     article.Kind,
		Title:    article.Title,
		Slug:     article.Slug,
		Excerpt:  article.Excerpt,
		BodyHTML: body,
		Category: article.Category,
		Tags:     article.Tags,
	}
	if article.PublishedAt != nil {
		view.PublishedAt = article.PublishedAt.UTC().Format("2006年01月02日")
		view.PublishedISO = article.PublishedAt.UTC().Format(time.RFC3339)
	}
	data := map[string]any{
		"SiteName":   siteName,
		"AssetURL":   t.assetURL,
		"Preview":    preview,
		"BackURL":    backURL,
		"Article":    view,
		"Navigation": navigation,
		"Meta":       metadata,
	}
	var output bytes.Buffer
	if err := t.templates.ExecuteTemplate(&output, "article.html", data); err != nil {
		return nil, fmt.Errorf("render default article theme: %w", err)
	}
	return output.Bytes(), nil
}

func (t *Theme) RenderHome(siteName string, articles []ArticleData, navigation ...Navigation) ([]byte, error) {
	return t.RenderHomePage(siteName, articles, firstNavigation(navigation), PageMetadata{})
}

func (t *Theme) RenderHomePage(siteName string, articles []ArticleData, navigation Navigation, metadata PageMetadata) ([]byte, error) {
	cards := make([]ArticleCard, 0, len(articles))
	for _, article := range articles {
		card := ArticleCard{Title: article.Title, Slug: article.Slug, Excerpt: article.Excerpt}
		if article.PublishedAt != nil {
			card.PublishedAt = article.PublishedAt.UTC().Format("2006年01月02日")
			card.PublishedISO = article.PublishedAt.UTC().Format(time.RFC3339)
		}
		cards = append(cards, card)
	}
	var output bytes.Buffer
	if err := t.templates.ExecuteTemplate(&output, "home.html", map[string]any{
		"SiteName":   siteName,
		"AssetURL":   t.assetURL,
		"Articles":   cards,
		"Navigation": navigation,
		"Meta":       metadata,
	}); err != nil {
		return nil, fmt.Errorf("render default home theme: %w", err)
	}
	return output.Bytes(), nil
}

func (t *Theme) RenderListing(siteName, title, description string, articles []ArticleData, navigation Navigation) ([]byte, error) {
	return t.RenderListingPage(siteName, title, description, articles, navigation, PageMetadata{})
}

func (t *Theme) RenderListingPage(siteName, title, description string, articles []ArticleData, navigation Navigation, metadata PageMetadata) ([]byte, error) {
	cards := make([]ArticleCard, 0, len(articles))
	for _, article := range articles {
		card := ArticleCard{Title: article.Title, Slug: article.Slug, Excerpt: article.Excerpt}
		if article.PublishedAt != nil {
			card.PublishedAt = article.PublishedAt.UTC().Format("2006年01月02日")
			card.PublishedISO = article.PublishedAt.UTC().Format(time.RFC3339)
		}
		cards = append(cards, card)
	}
	var output bytes.Buffer
	if err := t.templates.ExecuteTemplate(&output, "listing.html", map[string]any{
		"SiteName": siteName, "AssetURL": t.assetURL, "Title": title,
		"Description": description, "Articles": cards, "Navigation": navigation, "Meta": metadata,
	}); err != nil {
		return nil, fmt.Errorf("render default listing theme: %w", err)
	}
	return output.Bytes(), nil
}

func (t *Theme) RenderSearch(siteName string, search SearchPageData, navigation Navigation, metadata PageMetadata) ([]byte, error) {
	var output bytes.Buffer
	if err := t.templates.ExecuteTemplate(&output, "search.html", map[string]any{
		"SiteName": siteName, "AssetURL": t.assetURL, "Search": search,
		"Navigation": navigation, "Meta": metadata,
	}); err != nil {
		return nil, fmt.Errorf("render default search theme: %w", err)
	}
	return output.Bytes(), nil
}

func firstNavigation(values []Navigation) Navigation {
	if len(values) == 0 {
		return Navigation{}
	}
	return values[0]
}
