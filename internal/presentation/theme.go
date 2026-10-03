package presentation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html/template"
	"math"
	"strings"
	"time"
	"unicode"

	defaulttheme "github.com/zhushilin/blog-project/themes/default"
	xhtml "golang.org/x/net/html"
)

type ContentView struct {
	Kind         string
	Title        string
	Slug         string
	Excerpt      string
	BodyMarkdown string
	// BodyCacheKey is populated only for immutable public revisions. It lets
	// the theme reuse sanitized Markdown HTML without making that HTML an
	// authoritative content field.
	BodyCacheKey string
	PublishedAt  *time.Time
	UpdatedAt    *time.Time
	Cover        *MediaData
	Category     *TermData
	Tags         []TermData
	ReadingTime  int
	TOC          []HeadingData
	Previous     *ArticleCard
	Next         *ArticleCard
	Related      []ArticleCard
}

type ArticleData = ContentView

type MediaData struct {
	URL    string
	Alt    string
	Width  int
	Height int
	SrcSet string
}

type TermData struct{ Name, URL string }
type HeadingData struct {
	ID    string
	Text  string
	Level int
}

type FeatureFlags struct {
	Comments     bool
	CommentsMode string
	Newsletter   bool
	Analytics    bool
}

type FilterOption struct {
	Slug string
	Name string
}

type TermSummary struct {
	Name          string
	Slug          string
	Description   string
	URL           string
	ArticleCount  int
	LatestTitle   string
	LatestPath    string
	LatestPublish string
}

type ArchiveMonthView struct {
	Year  int
	Month int
	Label string
	Count int
	URL   string
}

type ArchiveYearView struct {
	Year   int
	Total  int
	Months []ArchiveMonthView
}

type HomePageData struct {
	Featured   *ArticleCard
	Recent     []ArticleCard
	Categories []TermSummary
	Archive    []ArchiveMonthView
	About      *ArticleCard
}

type DirectoryView struct {
	Title        string
	Description  string
	Categories   []TermSummary
	Tags         []TermSummary
	ArchiveYears []ArchiveYearView
}

type StatusView struct {
	Code    int
	Title   string
	Message string
}

type NavigationLink struct {
	Label, URL string
	External   bool
	Children   []NavigationLink
}

// SidebarItemView is the safe, render-ready representation used by the
// default theme's global navigation drawer. It deliberately exposes an icon
// key instead of SVG/HTML so theme templates cannot inject markup.
type SidebarItemView struct {
	Key      string
	Label    string
	URL      string
	Icon     string
	External bool
	Active   bool
	Expanded bool
	Children []SidebarItemView
}

type SidebarSectionView struct {
	Label string
	Items []SidebarItemView
}

type SidebarView struct {
	CurrentKey string
	Sections   []SidebarSectionView
}

type Navigation struct {
	Primary, Footer []NavigationLink
	CurrentPath     string
	Features        FeatureFlags
	Sidebar         SidebarView
}

type PageContext struct {
	CurrentPath string
	Features    FeatureFlags
}

type PageMetadata struct {
	Title, Description, CanonicalURL, OpenGraphType, RSSURL string
	SiteName, ImageURL, TwitterCard                         string
	PublishedAt, ModifiedAt                                 *time.Time
	NoIndex                                                 bool
	JSONLD                                                  template.JS
}

// SiteMetadata is the bounded public subset of site settings consumed by the
// renderer. Credentials, provider configuration, and private owner data never
// cross this boundary.
type SiteMetadata struct {
	Language              string
	Description           string
	DefaultSEOTitle       string
	DefaultSEODescription string
	FeedSummaryMode       string
	SocialLinks           []string
	DefaultSocialImageURL string
}

type ArticleCard struct {
	Kind               string
	Path               string
	Title              string
	Slug               string
	Excerpt            string
	PublishedAt        string
	PublishedISO       string
	UpdatedAt          string
	UpdatedISO         string
	ReadingTime        int
	Cover              *MediaData
	Category           *TermData
	HighlightedTitle   template.HTML
	HighlightedExcerpt template.HTML
}

type PageLink struct {
	Number  int
	URL     string
	Current bool
}

type PaginationView struct {
	Page        int
	PerPage     int
	Total       int
	PageCount   int
	HasPrevious bool
	HasNext     bool
	PreviousURL string
	NextURL     string
	Pages       []PageLink
}

type CollectionView struct {
	Title        string
	Description  string
	Items        []ArticleCard
	Pagination   PaginationView
	CanonicalURL string
}

type SearchPageData struct {
	Query, Kind, Category, Tag, Sort string
	Searched, Invalid                bool
	Results                          []ArticleCard
	Total                            int
	Pagination                       PaginationView
	CategoryOptions                  []FilterOption
	TagOptions                       []FilterOption
}

type Theme struct {
	templates       *template.Template
	markdown        *Markdown
	css             []byte
	js              []byte
	searchJS        []byte
	landscape       []byte
	landscapeHash   string
	assetHash       string
	scriptHash      string
	searchHash      string
	templateHash    string
	assetURL        string
	scriptURL       string
	searchScriptURL string
	id              string
	version         string
	assetRoot       string
	settings        map[string]any
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
	stageSixCSS, err := defaulttheme.Files.ReadFile("assets/theme-stage-six.css")
	if err != nil {
		return nil, fmt.Errorf("read default theme stage six stylesheet: %w", err)
	}
	songScrollCSS, err := defaulttheme.Files.ReadFile("assets/theme-song-scroll.css")
	if err != nil {
		return nil, fmt.Errorf("read default theme Song scroll stylesheet: %w", err)
	}
	roundedCSS, err := defaulttheme.Files.ReadFile("assets/theme-rounded.css")
	if err != nil {
		return nil, fmt.Errorf("read default theme rounded stylesheet: %w", err)
	}
	landscape, err := defaulttheme.Files.ReadFile("assets/song-ink-landscape-background.webp")
	if err != nil {
		return nil, fmt.Errorf("read default theme landscape: %w", err)
	}
	landscapeDigest := sha256.Sum256(landscape)
	landscapeHash := hex.EncodeToString(landscapeDigest[:8])
	landscapeURL := "/assets/theme/default/" + landscapeHash + "/song-ink-landscape-background.webp"
	songScrollCSS = bytes.ReplaceAll(songScrollCSS, []byte("__SONG_LANDSCAPE_URL__"), []byte(landscapeURL))
	css = append(css, '\n')
	css = append(css, stageSixCSS...)
	css = append(css, '\n')
	css = append(css, songScrollCSS...)
	css = append(css, '\n')
	css = append(css, roundedCSS...)
	js, err := defaulttheme.Files.ReadFile("assets/theme.js")
	if err != nil {
		return nil, fmt.Errorf("read default theme script: %w", err)
	}
	searchJS, err := defaulttheme.Files.ReadFile("assets/theme-search.js")
	if err != nil {
		return nil, fmt.Errorf("read default theme search script: %w", err)
	}
	hash := sha256.Sum256(css)
	assetHash := hex.EncodeToString(hash[:8])
	scriptDigest := sha256.Sum256(js)
	scriptHash := hex.EncodeToString(scriptDigest[:8])
	searchDigest := sha256.Sum256(searchJS)
	searchHash := hex.EncodeToString(searchDigest[:8])
	templateHasher := sha256.New()
	entries, err := defaulttheme.Files.ReadDir("templates")
	if err != nil {
		return nil, fmt.Errorf("list default theme templates: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := "templates/" + entry.Name()
		contents, err := defaulttheme.Files.ReadFile(name)
		if err != nil {
			return nil, fmt.Errorf("read default theme template %q: %w", entry.Name(), err)
		}
		_, _ = templateHasher.Write([]byte(name + "\x00"))
		_, _ = templateHasher.Write(contents)
	}
	templateDigest := templateHasher.Sum(nil)
	return &Theme{
		templates:       templates,
		markdown:        markdown,
		css:             css,
		js:              js,
		searchJS:        searchJS,
		landscape:       landscape,
		landscapeHash:   landscapeHash,
		assetHash:       assetHash,
		scriptHash:      scriptHash,
		searchHash:      searchHash,
		templateHash:    hex.EncodeToString(templateDigest[:8]),
		assetURL:        "/assets/theme/default/" + assetHash + "/theme.css",
		scriptURL:       "/assets/theme/default/" + scriptHash + "/theme.js",
		searchScriptURL: "/assets/theme/default/" + searchHash + "/theme-search.js",
		id:              DefaultThemeID,
		version:         defaulttheme.Version,
	}, nil
}

func (t *Theme) Version() string {
	version := t.version
	if version == "" {
		version = defaulttheme.Version
	}
	searchVersion := ""
	if t.searchHash != "" {
		searchVersion = "+" + t.searchHash
	}
	if t.templateHash == "" {
		if t.scriptHash == "" {
			return version + "+" + t.assetHash
		}
		return version + "+" + t.assetHash + "+" + t.scriptHash + searchVersion
	}
	return version + "+" + t.assetHash + "+" + t.scriptHash + searchVersion + "+" + t.templateHash
}

func (t *Theme) AssetURL() string         { return t.assetURL }
func (t *Theme) AssetHash() string        { return t.assetHash }
func (t *Theme) CSS() []byte              { return t.css }
func (t *Theme) Landscape() []byte        { return t.landscape }
func (t *Theme) LandscapeHash() string    { return t.landscapeHash }
func (t *Theme) ScriptURL() string        { return t.scriptURL }
func (t *Theme) ScriptHash() string       { return t.scriptHash }
func (t *Theme) JS() []byte               { return t.js }
func (t *Theme) SearchScriptURL() string  { return t.searchScriptURL }
func (t *Theme) SearchScriptHash() string { return t.searchHash }
func (t *Theme) SearchJS() []byte         { return t.searchJS }

func (t *Theme) Settings() map[string]any { return cloneSettings(t.settings) }

func (t *Theme) WithSettings(values map[string]any) *Theme {
	clone := *t
	clone.settings = cloneSettings(values)
	return &clone
}

func cloneSettings(values map[string]any) map[string]any {
	if len(values) == 0 {
		return map[string]any{}
	}
	clone := make(map[string]any, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}

func (t *Theme) RenderArticle(siteName string, article ArticleData, preview bool, backURL string, navigation ...Navigation) ([]byte, error) {
	return t.RenderArticlePage(siteName, article, preview, backURL, firstNavigation(navigation), PageMetadata{})
}

func (t *Theme) RenderArticlePage(siteName string, article ArticleData, preview bool, backURL string, navigation Navigation, metadata PageMetadata) ([]byte, error) {
	navigation = withSidebar(navigation)
	body, err := t.markdown.RenderCached(article.BodyCacheKey, article.BodyMarkdown)
	if err != nil {
		return nil, err
	}
	readingTime := article.ReadingTime
	if readingTime < 1 {
		readingTime = readingMinutes(article.BodyMarkdown)
	}
	toc := article.TOC
	if len(toc) == 0 {
		toc = tableOfContents(body)
	}
	view := articleTemplateView{Kind: article.Kind, Title: article.Title, Slug: article.Slug, Excerpt: article.Excerpt, BodyHTML: body, Cover: article.Cover, Category: article.Category, Tags: article.Tags, ReadingTime: readingTime, TOC: toc, Previous: article.Previous, Next: article.Next, Related: article.Related}
	if article.PublishedAt != nil {
		view.PublishedAt = article.PublishedAt.UTC().Format("2006年01月02日")
		view.PublishedISO = article.PublishedAt.UTC().Format(time.RFC3339)
	}
	if article.UpdatedAt != nil {
		view.UpdatedAt = article.UpdatedAt.UTC().Format("2006年01月02日")
		view.UpdatedISO = article.UpdatedAt.UTC().Format(time.RFC3339)
	}
	data := map[string]any{
		"SiteName":        siteName,
		"AssetURL":        t.assetURL,
		"ScriptURL":       t.scriptURL,
		"SearchScriptURL": t.searchScriptURL,
		"Preview":         preview,
		"BackURL":         backURL,
		"Article":         view,
		"Navigation":      navigation,
		"Context":         pageContext(navigation),
		"Features":        navigation.Features,
		"Settings":        t.settings,
		"Search":          SearchPageData{},
		"Meta":            metadata,
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
	cards := articleCards(articles)
	view := HomePageData{}
	if len(cards) > 0 {
		view.Featured = &cards[0]
		if len(cards) > 1 {
			view.Recent = cards[1:]
		}
	} else {
		view.Recent = cards
	}
	return t.RenderHomePageWithView(siteName, view, navigation, metadata, cards)
}

func (t *Theme) RenderHomePageWithView(siteName string, view HomePageData, navigation Navigation, metadata PageMetadata, legacyArticles []ArticleCard) ([]byte, error) {
	navigation = withSidebar(navigation)
	if legacyArticles == nil {
		legacyArticles = view.Recent
	}
	var output bytes.Buffer
	if err := t.templates.ExecuteTemplate(&output, "home.html", map[string]any{
		"SiteName":        siteName,
		"AssetURL":        t.assetURL,
		"ScriptURL":       t.scriptURL,
		"SearchScriptURL": t.searchScriptURL,
		"Articles":        legacyArticles,
		"Home":            view,
		"Navigation":      navigation,
		"Context":         pageContext(navigation),
		"Features":        navigation.Features,
		"Settings":        t.settings,
		"Search":          SearchPageData{},
		"Meta":            metadata,
	}); err != nil {
		return nil, fmt.Errorf("render default home theme: %w", err)
	}
	return output.Bytes(), nil
}

func (t *Theme) RenderListing(siteName, title, description string, articles []ArticleData, navigation Navigation) ([]byte, error) {
	return t.RenderListingPage(siteName, title, description, articles, navigation, PageMetadata{})
}

func (t *Theme) RenderListingPage(siteName, title, description string, articles []ArticleData, navigation Navigation, metadata PageMetadata) ([]byte, error) {
	return t.RenderCollectionPage(siteName, CollectionView{Title: title, Description: description, Items: articleCards(articles)}, navigation, metadata)
}

func (t *Theme) RenderCollectionPage(siteName string, collection CollectionView, navigation Navigation, metadata PageMetadata) ([]byte, error) {
	navigation = withSidebar(navigation)
	var output bytes.Buffer
	if err := t.templates.ExecuteTemplate(&output, "listing.html", map[string]any{
		"SiteName": siteName, "AssetURL": t.assetURL, "ScriptURL": t.scriptURL, "SearchScriptURL": t.searchScriptURL, "Title": collection.Title,
		"Description": collection.Description, "Articles": collection.Items, "Collection": collection,
		"Pagination": collection.Pagination, "Navigation": navigation, "Context": pageContext(navigation),
		"Features": navigation.Features, "Settings": t.settings, "Search": SearchPageData{}, "Meta": metadata,
	}); err != nil {
		return nil, fmt.Errorf("render default listing theme: %w", err)
	}
	return output.Bytes(), nil
}

func (t *Theme) RenderDirectoryPage(siteName string, directory DirectoryView, navigation Navigation, metadata PageMetadata) ([]byte, error) {
	navigation = withSidebar(navigation)
	var output bytes.Buffer
	if err := t.templates.ExecuteTemplate(&output, "directory.html", map[string]any{
		"SiteName": siteName, "AssetURL": t.assetURL, "ScriptURL": t.scriptURL, "SearchScriptURL": t.searchScriptURL, "Title": directory.Title,
		"Description": directory.Description, "Directory": directory, "Navigation": navigation,
		"Context": pageContext(navigation), "Features": navigation.Features, "Settings": t.settings, "Search": SearchPageData{}, "Meta": metadata,
	}); err != nil {
		return nil, fmt.Errorf("render directory theme: %w", err)
	}
	return output.Bytes(), nil
}

func (t *Theme) RenderStatusPage(siteName string, status StatusView, navigation Navigation, metadata PageMetadata) ([]byte, error) {
	navigation = withSidebar(navigation)
	var output bytes.Buffer
	if err := t.templates.ExecuteTemplate(&output, "status.html", map[string]any{
		"SiteName": siteName, "AssetURL": t.assetURL, "ScriptURL": t.scriptURL, "SearchScriptURL": t.searchScriptURL, "Status": status,
		"Navigation": navigation, "Context": pageContext(navigation),
		"Features": navigation.Features, "Settings": t.settings, "Search": SearchPageData{}, "Meta": metadata,
	}); err != nil {
		return nil, fmt.Errorf("render status theme: %w", err)
	}
	return output.Bytes(), nil
}

func (t *Theme) RenderSearch(siteName string, search SearchPageData, navigation Navigation, metadata PageMetadata) ([]byte, error) {
	navigation = withSidebar(navigation)
	var output bytes.Buffer
	if err := t.templates.ExecuteTemplate(&output, "search.html", map[string]any{
		"SiteName": siteName, "AssetURL": t.assetURL, "ScriptURL": t.scriptURL, "SearchScriptURL": t.searchScriptURL, "Search": search,
		"Navigation": navigation, "Context": pageContext(navigation), "Features": navigation.Features, "Settings": t.settings, "Meta": metadata,
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

type articleTemplateView struct {
	Kind         string
	Title        string
	Slug         string
	Excerpt      string
	BodyHTML     template.HTML
	Cover        *MediaData
	PublishedAt  string
	PublishedISO string
	UpdatedAt    string
	UpdatedISO   string
	ReadingTime  int
	Category     *TermData
	Tags         []TermData
	TOC          []HeadingData
	Previous     *ArticleCard
	Next         *ArticleCard
	Related      []ArticleCard
}

func pageContext(navigation Navigation) PageContext {
	return PageContext{CurrentPath: navigation.CurrentPath, Features: navigation.Features}
}

func withSidebar(navigation Navigation) Navigation {
	if len(navigation.Sidebar.Sections) == 0 {
		navigation.Sidebar = sidebarView(navigation.CurrentPath, navigation.Primary, navigation.Footer)
	}
	return navigation
}

func sidebarView(currentPath string, primary, footer []NavigationLink) SidebarView {
	sections := []SidebarSectionView{
		{Label: "内容", Items: []SidebarItemView{
			staticSidebarItem("home", "首页", "/", "home", currentPath == "/"),
			staticSidebarItem("articles", "文章", "/articles", "articles", currentPath == "/articles" || strings.HasPrefix(currentPath, "/posts/")),
			staticSidebarItem("archive", "归档", "/archive", "archive", strings.HasPrefix(currentPath, "/archive")),
		}},
		{Label: "探索", Items: []SidebarItemView{
			staticSidebarItem("categories", "分类", "/categories", "categories", strings.HasPrefix(currentPath, "/categories")),
			staticSidebarItem("tags", "标签", "/tags", "tags", strings.HasPrefix(currentPath, "/tags")),
			staticSidebarItem("search", "搜索", "/search", "search", strings.HasPrefix(currentPath, "/search")),
		}},
	}

	if items := sidebarItemsFromLinks(primary, currentPath, "link"); len(items) > 0 {
		sections = append(sections, SidebarSectionView{Label: "站点", Items: items})
	}
	more := sidebarItemsFromLinks(footer, currentPath, "link")
	more = append(more, staticSidebarItem("rss", "RSS", "/rss.xml", "rss", false))
	sections = append(sections, SidebarSectionView{Label: "更多", Items: more})

	currentKey := ""
	for _, section := range sections {
		for _, item := range section.Items {
			if item.Active {
				currentKey = item.Key
				break
			}
		}
		if currentKey != "" {
			break
		}
	}
	return SidebarView{CurrentKey: currentKey, Sections: sections}
}

func staticSidebarItem(key, label, target, icon string, active bool) SidebarItemView {
	return SidebarItemView{Key: key, Label: label, URL: target, Icon: icon, Active: active}
}

func sidebarItemsFromLinks(links []NavigationLink, currentPath, fallbackIcon string) []SidebarItemView {
	items := make([]SidebarItemView, 0, len(links))
	for index, link := range links {
		key := fmt.Sprintf("custom-%d", index)
		children := sidebarItemsFromLinks(link.Children, currentPath, fallbackIcon)
		active := sidebarPathActive(currentPath, link.URL)
		for _, child := range children {
			if child.Active || child.Expanded {
				active = true
				break
			}
		}
		items = append(items, SidebarItemView{
			Key: key, Label: link.Label, URL: link.URL, Icon: fallbackIcon, External: link.External,
			Active: active, Expanded: active && len(children) > 0, Children: children,
		})
	}
	return items
}

func sidebarPathActive(currentPath, target string) bool {
	if target == "" || strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") || strings.HasPrefix(target, "//") {
		return false
	}
	if target == "/" {
		return currentPath == "/"
	}
	target = strings.TrimRight(target, "/")
	return currentPath == target || strings.HasPrefix(currentPath, target+"/")
}

func articleCards(articles []ArticleData) []ArticleCard {
	cards := make([]ArticleCard, 0, len(articles))
	for _, article := range articles {
		card := ArticleCard{Kind: article.Kind, Path: contentPath(article.Kind, article.Slug), Title: article.Title, Slug: article.Slug, Excerpt: article.Excerpt, ReadingTime: article.ReadingTime, Cover: article.Cover, Category: article.Category}
		if article.PublishedAt != nil {
			card.PublishedAt = article.PublishedAt.UTC().Format("2006年01月02日")
			card.PublishedISO = article.PublishedAt.UTC().Format(time.RFC3339)
		}
		if article.UpdatedAt != nil {
			card.UpdatedAt = article.UpdatedAt.UTC().Format("2006年01月02日")
			card.UpdatedISO = article.UpdatedAt.UTC().Format(time.RFC3339)
		}
		cards = append(cards, card)
	}
	return cards
}

func contentPath(kind, slug string) string {
	if kind == "page" {
		return "/" + slug
	}
	return "/posts/" + slug
}

func toArticleCard(article ArticleData) *ArticleCard {
	cards := articleCards([]ArticleData{article})
	if len(cards) == 0 {
		return nil
	}
	return &cards[0]
}

func readingMinutes(source string) int {
	chinese := 0
	latinWords := 0
	inWord := false
	for _, value := range source {
		switch {
		case unicode.Is(unicode.Han, value):
			chinese++
			inWord = false
		case unicode.IsLetter(value) || unicode.IsNumber(value):
			inWord = true
		default:
			if inWord {
				latinWords++
				inWord = false
			}
		}
	}
	if inWord {
		latinWords++
	}
	units := chinese + latinWords*5
	return maxInt(1, int(math.Ceil(float64(units)/400)))
}

func tableOfContents(body template.HTML) []HeadingData {
	root, err := xhtml.Parse(strings.NewReader("<body>" + string(body) + "</body>"))
	if err != nil {
		return nil
	}
	var result []HeadingData
	var visit func(*xhtml.Node)
	visit = func(node *xhtml.Node) {
		if node.Type == xhtml.ElementNode && (node.Data == "h2" || node.Data == "h3") {
			id := ""
			for _, attr := range node.Attr {
				if attr.Key == "id" {
					id = attr.Val
					break
				}
			}
			if id != "" {
				level := 2
				if node.Data == "h3" {
					level = 3
				}
				result = append(result, HeadingData{ID: id, Text: nodeText(node), Level: level})
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(root)
	return result
}

func nodeText(node *xhtml.Node) string {
	if node.Type == xhtml.TextNode {
		return node.Data
	}
	var builder strings.Builder
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		builder.WriteString(nodeText(child))
	}
	return strings.Join(strings.Fields(builder.String()), " ")
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}
