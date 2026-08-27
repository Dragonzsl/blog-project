package presentation

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/zhushilin/blog-project/internal/discovery"
	"github.com/zhushilin/blog-project/internal/organization"
	"github.com/zhushilin/blog-project/internal/platform/database"
	platformslug "github.com/zhushilin/blog-project/internal/platform/slug"
	"github.com/zhushilin/blog-project/internal/publishing"
)

type ContentQueries interface {
	Article(context.Context, int64) (publishing.Article, error)
	Page(context.Context, int64) (publishing.Article, error)
	PublicArticle(context.Context, string) (publishing.Article, error)
	PublishedArticles(context.Context, int) ([]publishing.Article, error)
	PublicArticleByID(context.Context, int64) (publishing.Article, error)
	PublicPage(context.Context, string) (publishing.Article, error)
}

type OrganizationQueries interface {
	PublicNavigation(context.Context, string) ([]organization.NavigationItem, error)
	PublicCategory(context.Context, string, int) (organization.Category, []int64, error)
	PublicTag(context.Context, string, int) (organization.Tag, []int64, error)
}

type SiteNamer interface {
	SiteName(context.Context) (string, error)
}

type DiscoveryQueries interface {
	BaseURL() string
	AbsoluteURL(string) string
	Search(context.Context, discovery.SearchQuery) ([]discovery.SearchResult, error)
	Feed(context.Context) ([]discovery.FeedItem, error)
	Sitemap(context.Context) ([]discovery.SitemapEntry, error)
	ResolveRedirect(context.Context, string) (discovery.Redirect, error)
}

type StateRepository struct {
	database *database.DB
}

func NewStateRepository(database *database.DB) *StateRepository {
	return &StateRepository{database: database}
}

func (r *StateRepository) RenderEpoch(ctx context.Context) (int64, error) {
	var epoch int64
	if err := r.database.Reader.QueryRowContext(ctx, "SELECT render_epoch FROM system_state WHERE id = 1").Scan(&epoch); err != nil {
		return 0, fmt.Errorf("read render epoch: %w", err)
	}
	return epoch, nil
}

type HTTPHandler struct {
	content      ContentQueries
	siteNamer    SiteNamer
	state        *StateRepository
	theme        *Theme
	themeManager *ThemeManager
	cache        *PageCache
	logger       *slog.Logger
	organization OrganizationQueries
	discovery    DiscoveryQueries
}

func (h *HTTPHandler) SetDiscovery(service DiscoveryQueries) { h.discovery = service }

func (h *HTTPHandler) SetThemeManager(manager *ThemeManager) { h.themeManager = manager }

func (h *HTTPHandler) currentTheme() *Theme {
	if h.themeManager != nil {
		return h.themeManager.Current()
	}
	return h.theme
}

func NewHTTPHandler(content ContentQueries, siteNamer SiteNamer, state *StateRepository, theme *Theme, cache *PageCache, logger *slog.Logger, organizations ...OrganizationQueries) *HTTPHandler {
	handler := &HTTPHandler{content: content, siteNamer: siteNamer, state: state, theme: theme, cache: cache, logger: logger}
	if len(organizations) > 0 {
		handler.organization = organizations[0]
	}
	return handler
}

func (h *HTTPHandler) RegisterPublic(router chi.Router) {
	router.Get("/", h.home)
	router.Head("/", h.home)
	router.Get("/posts/{slug}", h.article)
	router.Head("/posts/{slug}", h.article)
	router.Get("/categories/{slug}", h.category)
	router.Head("/categories/{slug}", h.category)
	router.Get("/tags/{slug}", h.tag)
	router.Head("/tags/{slug}", h.tag)
	router.Get("/search", h.search)
	router.Head("/search", h.search)
	router.Get("/rss.xml", h.rss)
	router.Head("/rss.xml", h.rss)
	router.Get("/sitemap.xml", h.sitemap)
	router.Head("/sitemap.xml", h.sitemap)
	router.Get("/robots.txt", h.robots)
	router.Head("/robots.txt", h.robots)
	router.Get("/assets/theme/{themeID}/{fingerprint}/theme.css", h.asset)
	router.Head("/assets/theme/{themeID}/{fingerprint}/theme.css", h.asset)
	router.Get("/{slug}", h.page)
	router.Head("/{slug}", h.page)
}

func (h *HTTPHandler) RegisterAdmin(router chi.Router) {
	router.Get("/articles/{articleID}/preview", h.preview)
	router.Get("/pages/{pageID}/preview", h.preview)
}

func (h *HTTPHandler) home(w http.ResponseWriter, r *http.Request) {
	h.serveCached(w, r, "home", func(ctx context.Context) ([]byte, string, error) {
		articles, err := h.content.PublishedArticles(ctx, 20)
		if err != nil {
			return nil, "", err
		}
		siteName, err := h.siteNamer.SiteName(ctx)
		if err != nil {
			return nil, "", err
		}
		navigation, err := h.navigation(ctx)
		if err != nil {
			return nil, "", err
		}
		metadata := h.metadata(siteName, siteName, siteName+"的文章与思考。", "/", "website", map[string]any{
			"@context": "https://schema.org", "@type": "WebSite", "name": siteName,
			"url": h.absoluteURL("/"), "potentialAction": map[string]any{
				"@type": "SearchAction", "target": h.absoluteURL("/search") + "?q={search_term_string}",
				"query-input": "required name=search_term_string",
			},
		})
		theme := h.currentTheme()
		body, err := theme.RenderHomePage(siteName, articleDataList(articles), navigation, metadata)
		lastModified := ""
		if len(articles) > 0 && articles[0].PublishedRevisionAt != nil {
			lastModified = articles[0].PublishedRevisionAt.UTC().Format(http.TimeFormat)
		}
		return body, lastModified, err
	})
}

func (h *HTTPHandler) article(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	h.serveCached(w, r, "article:"+slug, func(ctx context.Context) ([]byte, string, error) {
		article, err := h.content.PublicArticle(ctx, slug)
		if err != nil {
			return nil, "", err
		}
		siteName, err := h.siteNamer.SiteName(ctx)
		if err != nil {
			return nil, "", err
		}
		navigation, err := h.navigation(ctx)
		if err != nil {
			return nil, "", err
		}
		path := "/posts/" + article.Slug
		metadata := h.articleMetadata(siteName, article, path)
		theme := h.currentTheme()
		body, err := theme.RenderArticlePage(siteName, articleData(article), false, "", navigation, metadata)
		lastModified := ""
		if article.PublishedRevisionAt != nil {
			lastModified = article.PublishedRevisionAt.UTC().Format(http.TimeFormat)
		}
		return body, lastModified, err
	})
}

func (h *HTTPHandler) page(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	h.serveCached(w, r, "page:"+slug, func(ctx context.Context) ([]byte, string, error) {
		page, err := h.content.PublicPage(ctx, slug)
		if err != nil {
			return nil, "", err
		}
		siteName, err := h.siteNamer.SiteName(ctx)
		if err != nil {
			return nil, "", err
		}
		navigation, err := h.navigation(ctx)
		if err != nil {
			return nil, "", err
		}
		path := "/" + page.Slug
		metadata := h.articleMetadata(siteName, page, path)
		theme := h.currentTheme()
		body, err := theme.RenderArticlePage(siteName, articleData(page), false, "", navigation, metadata)
		lastModified := ""
		if page.PublishedRevisionAt != nil {
			lastModified = page.PublishedRevisionAt.UTC().Format(http.TimeFormat)
		}
		return body, lastModified, err
	})
}

func (h *HTTPHandler) category(w http.ResponseWriter, r *http.Request) {
	h.taxonomyListing(w, r, "category")
}
func (h *HTTPHandler) tag(w http.ResponseWriter, r *http.Request) { h.taxonomyListing(w, r, "tag") }

func (h *HTTPHandler) taxonomyListing(w http.ResponseWriter, r *http.Request, kind string) {
	if h.organization == nil {
		http.NotFound(w, r)
		return
	}
	slug := chi.URLParam(r, "slug")
	h.serveCached(w, r, kind+":"+slug, func(ctx context.Context) ([]byte, string, error) {
		var title, description, canonicalSlug string
		var ids []int64
		var err error
		if kind == "category" {
			var term organization.Category
			term, ids, err = h.organization.PublicCategory(ctx, slug, 50)
			title, description = term.Name, term.Description
			canonicalSlug = term.Slug
		} else {
			var term organization.Tag
			term, ids, err = h.organization.PublicTag(ctx, slug, 50)
			title, description = "# "+term.Name, term.Description
			canonicalSlug = term.Slug
		}
		if err != nil {
			return nil, "", err
		}
		articles := make([]publishing.Article, 0, len(ids))
		for _, id := range ids {
			article, err := h.content.PublicArticleByID(ctx, id)
			if err != nil {
				return nil, "", err
			}
			articles = append(articles, article)
		}
		siteName, err := h.siteNamer.SiteName(ctx)
		if err != nil {
			return nil, "", err
		}
		navigation, err := h.navigation(ctx)
		if err != nil {
			return nil, "", err
		}
		path := "/" + kind + "s/" + canonicalSlug
		metadata := h.metadata(siteName, title+" · "+siteName, description, path, "website", map[string]any{
			"@context": "https://schema.org", "@type": "CollectionPage", "name": title,
			"url": h.absoluteURL(path), "description": description,
		})
		theme := h.currentTheme()
		body, err := theme.RenderListingPage(siteName, title, description, articleDataList(articles), navigation, metadata)
		lastModified := ""
		if len(articles) > 0 && articles[0].PublishedRevisionAt != nil {
			lastModified = articles[0].PublishedRevisionAt.UTC().Format(http.TimeFormat)
		}
		return body, lastModified, err
	})
}

func (h *HTTPHandler) search(w http.ResponseWriter, r *http.Request) {
	if h.discovery == nil {
		http.NotFound(w, r)
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	kind := strings.TrimSpace(r.URL.Query().Get("type"))
	categoryRaw := strings.TrimSpace(r.URL.Query().Get("category"))
	tagRaw := strings.TrimSpace(r.URL.Query().Get("tag"))
	sortOrder := strings.TrimSpace(r.URL.Query().Get("sort"))
	if sortOrder == "" {
		sortOrder = "relevance"
	}
	category, categoryValid := normalizedFilter(categoryRaw)
	tag, tagValid := normalizedFilter(tagRaw)
	page := SearchPageData{Query: query, Kind: kind, Category: categoryRaw, Tag: tagRaw, Sort: sortOrder}
	if query != "" {
		if !categoryValid || !tagValid {
			page.Invalid = true
		} else {
			results, err := h.discovery.Search(r.Context(), discovery.SearchQuery{
				Text: query, Kind: kind, CategorySlug: category, TagSlug: tag, Sort: sortOrder, Limit: 50,
			})
			if errors.Is(err, discovery.ErrInvalidQuery) {
				page.Invalid = true
			} else if err != nil {
				h.handleRenderError(w, r, err)
				return
			} else {
				page.Searched = true
				for _, result := range results {
					page.Results = append(page.Results, ArticleCard{
						Kind: result.Kind, Path: result.Path, Title: result.Title, Excerpt: result.Excerpt,
						PublishedAt: result.PublishedAt.Format("2006年01月02日"), PublishedISO: result.PublishedAt.Format(time.RFC3339),
					})
				}
			}
		}
	}
	siteName, err := h.siteNamer.SiteName(r.Context())
	if err != nil {
		h.handleRenderError(w, r, err)
		return
	}
	navigation, err := h.navigation(r.Context())
	if err != nil {
		h.handleRenderError(w, r, err)
		return
	}
	metadata := h.metadata(siteName, "搜索 · "+siteName, "搜索"+siteName+"的公开文章与页面。", "/search", "website", nil)
	metadata.NoIndex = true
	body, err := h.currentTheme().RenderSearch(siteName, page, navigation, metadata)
	if err != nil {
		h.handleRenderError(w, r, err)
		return
	}
	setPublicSecurityHeaders(w, body)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

type rssDocument struct {
	XMLName xml.Name   `xml:"rss"`
	Version string     `xml:"version,attr"`
	Channel rssChannel `xml:"channel"`
}
type rssChannel struct {
	Title         string    `xml:"title"`
	Link          string    `xml:"link"`
	Description   string    `xml:"description"`
	Language      string    `xml:"language"`
	LastBuildDate string    `xml:"lastBuildDate,omitempty"`
	Items         []rssItem `xml:"item"`
}
type rssItem struct {
	Title       string  `xml:"title"`
	Link        string  `xml:"link"`
	Description string  `xml:"description"`
	Published   string  `xml:"pubDate"`
	GUID        rssGUID `xml:"guid"`
}
type rssGUID struct {
	Permalink string `xml:"isPermaLink,attr"`
	Value     string `xml:",chardata"`
}

func (h *HTTPHandler) rss(w http.ResponseWriter, r *http.Request) {
	if h.discovery == nil {
		http.NotFound(w, r)
		return
	}
	h.serveCachedDocument(w, r, "rss", "application/rss+xml; charset=utf-8", func(ctx context.Context) ([]byte, string, error) {
		items, err := h.discovery.Feed(ctx)
		if err != nil {
			return nil, "", err
		}
		siteName, err := h.siteNamer.SiteName(ctx)
		if err != nil {
			return nil, "", err
		}
		document := rssDocument{Version: "2.0", Channel: rssChannel{
			Title: siteName, Link: h.absoluteURL("/"), Description: siteName + "的最新文章。", Language: "zh-CN",
		}}
		lastModified := ""
		var latest time.Time
		for _, item := range items {
			absolute := h.absoluteURL(item.Path)
			description := item.Excerpt
			if description == "" {
				description = item.Title
			}
			document.Channel.Items = append(document.Channel.Items, rssItem{
				Title: item.Title, Link: absolute, Description: description,
				Published: item.PublishedAt.Format(time.RFC1123Z), GUID: rssGUID{Permalink: "true", Value: absolute},
			})
			if item.UpdatedAt.After(latest) {
				latest = item.UpdatedAt
			}
		}
		if !latest.IsZero() {
			document.Channel.LastBuildDate = latest.Format(time.RFC1123Z)
			lastModified = latest.Format(http.TimeFormat)
		}
		body, err := xml.Marshal(document)
		return append([]byte(xml.Header), body...), lastModified, err
	})
}

type sitemapDocument struct {
	XMLName xml.Name     `xml:"urlset"`
	XMLNS   string       `xml:"xmlns,attr"`
	URLs    []sitemapURL `xml:"url"`
}
type sitemapURL struct {
	Location     string `xml:"loc"`
	LastModified string `xml:"lastmod,omitempty"`
}

func (h *HTTPHandler) sitemap(w http.ResponseWriter, r *http.Request) {
	if h.discovery == nil {
		http.NotFound(w, r)
		return
	}
	h.serveCachedDocument(w, r, "sitemap", "application/xml; charset=utf-8", func(ctx context.Context) ([]byte, string, error) {
		entries, err := h.discovery.Sitemap(ctx)
		if err != nil {
			return nil, "", err
		}
		document := sitemapDocument{XMLNS: "http://www.sitemaps.org/schemas/sitemap/0.9"}
		lastModified := ""
		var latest time.Time
		for _, entry := range entries {
			item := sitemapURL{Location: h.absoluteURL(entry.Path)}
			if !entry.LastModified.IsZero() {
				item.LastModified = entry.LastModified.Format("2006-01-02T15:04:05Z07:00")
				if entry.LastModified.After(latest) {
					latest = entry.LastModified
					lastModified = entry.LastModified.Format(http.TimeFormat)
				}
			}
			document.URLs = append(document.URLs, item)
		}
		body, err := xml.Marshal(document)
		return append([]byte(xml.Header), body...), lastModified, err
	})
}

func (h *HTTPHandler) robots(w http.ResponseWriter, r *http.Request) {
	if h.discovery == nil {
		http.NotFound(w, r)
		return
	}
	body := []byte("User-agent: *\nAllow: /\nDisallow: /admin/\nDisallow: /search\nSitemap: " + h.absoluteURL("/sitemap.xml") + "\n")
	h.writeGenerated(w, r, "text/plain; charset=utf-8", body, "")
}

func (h *HTTPHandler) preview(w http.ResponseWriter, r *http.Request) {
	id, kind, err := publishingContentID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	article, err := h.content.Article(r.Context(), id)
	if kind == "page" {
		article, err = h.content.Page(r.Context(), id)
	}
	if err != nil {
		h.handleRenderError(w, r, err)
		return
	}
	siteName, err := h.siteNamer.SiteName(r.Context())
	if err != nil {
		h.handleRenderError(w, r, err)
		return
	}
	navigation, err := h.navigation(r.Context())
	if err != nil {
		h.handleRenderError(w, r, err)
		return
	}
	backURL := fmt.Sprintf("/admin/articles/%d/edit", article.ID)
	if kind == "page" {
		backURL = fmt.Sprintf("/admin/pages/%d/edit", article.ID)
	}
	body, err := h.currentTheme().RenderArticlePage(siteName, articleData(article), true, backURL, navigation, PageMetadata{NoIndex: true})
	if err != nil {
		h.handleRenderError(w, r, err)
		return
	}
	setPublicSecurityHeaders(w)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

func (h *HTTPHandler) asset(w http.ResponseWriter, r *http.Request) {
	theme := h.currentTheme()
	if chi.URLParam(r, "themeID") != theme.ThemeID() || chi.URLParam(r, "fingerprint") != theme.AssetHash() {
		http.NotFound(w, r)
		return
	}
	etag := `"` + theme.AssetHash() + `"`
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("ETag", etag)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if matchesETag(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if r.Method != http.MethodHead {
		_, _ = w.Write(theme.CSS())
	}
}

func (h *HTTPHandler) serveCached(w http.ResponseWriter, r *http.Request, semanticKey string, render func(context.Context) ([]byte, string, error)) {
	h.serveCachedDocument(w, r, semanticKey, "text/html; charset=utf-8", render)
}

func (h *HTTPHandler) serveCachedDocument(w http.ResponseWriter, r *http.Request, semanticKey, contentType string, render func(context.Context) ([]byte, string, error)) {
	epoch, err := h.state.RenderEpoch(r.Context())
	if err != nil {
		h.handleRenderError(w, r, err)
		return
	}
	key := h.currentTheme().Version() + "|" + semanticKey
	if cached, ok := h.cache.Get(key, epoch); ok {
		h.writeCacheEntry(w, r, cached, "HIT")
		return
	}
	body, lastModified, err := render(r.Context())
	if err != nil {
		h.handleRenderError(w, r, err)
		return
	}
	digest := sha256.Sum256(body)
	entry := CacheEntry{
		Key:          key,
		Epoch:        epoch,
		Status:       http.StatusOK,
		ContentType:  contentType,
		ETag:         `"` + hex.EncodeToString(digest[:16]) + `"`,
		LastModified: lastModified,
		Body:         body,
	}
	if err := h.cache.Put(entry); err != nil {
		h.logger.WarnContext(r.Context(), "store public page cache", "error", err, "key", semanticKey)
	}
	if err := h.cache.Prune(epoch); err != nil {
		h.logger.WarnContext(r.Context(), "prune public page cache", "error", err)
	}
	h.writeCacheEntry(w, r, entry, "MISS")
}

func (h *HTTPHandler) writeCacheEntry(w http.ResponseWriter, r *http.Request, entry CacheEntry, cacheStatus string) {
	if strings.HasPrefix(entry.ContentType, "text/html") {
		setPublicSecurityHeaders(w, entry.Body)
	} else {
		w.Header().Set("X-Content-Type-Options", "nosniff")
	}
	w.Header().Set("Content-Type", entry.ContentType)
	w.Header().Set("Cache-Control", "public, max-age=0, must-revalidate")
	w.Header().Set("ETag", entry.ETag)
	w.Header().Set("X-Page-Cache", cacheStatus)
	if entry.LastModified != "" {
		w.Header().Set("Last-Modified", entry.LastModified)
	}
	if matchesETag(r.Header.Get("If-None-Match"), entry.ETag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(entry.Status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(entry.Body)
	}
}

func (h *HTTPHandler) writeGenerated(w http.ResponseWriter, r *http.Request, contentType string, body []byte, lastModified string) {
	digest := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(digest[:16]) + `"`
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=0, must-revalidate")
	w.Header().Set("ETag", etag)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if lastModified != "" {
		w.Header().Set("Last-Modified", lastModified)
	}
	if matchesETag(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

func (h *HTTPHandler) handleRenderError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, publishing.ErrNotFound) {
		if h.tryRedirect(w, r) {
			return
		}
		http.NotFound(w, r)
		return
	}
	if errors.Is(err, organization.ErrNotFound) {
		if h.tryRedirect(w, r) {
			return
		}
		http.NotFound(w, r)
		return
	}
	h.logger.ErrorContext(r.Context(), "render public page", "error", err)
	http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
}

func setPublicSecurityHeaders(w http.ResponseWriter, bodies ...[]byte) {
	scriptPolicy := "script-src 'none'; "
	if len(bodies) > 0 {
		const startMarker = `<script type="application/ld+json">`
		if start := strings.Index(string(bodies[0]), startMarker); start >= 0 {
			scriptStart := start + len(startMarker)
			if end := strings.Index(string(bodies[0][scriptStart:]), "</script>"); end >= 0 {
				digest := sha256.Sum256(bodies[0][scriptStart : scriptStart+end])
				scriptPolicy = "script-src 'sha256-" + base64.StdEncoding.EncodeToString(digest[:]) + "'; "
			}
		}
	}
	w.Header().Set("Content-Security-Policy", "default-src 'none'; "+scriptPolicy+"style-src 'self'; img-src 'self' data: https:; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
}

func (h *HTTPHandler) absoluteURL(path string) string {
	if h.discovery == nil {
		return ""
	}
	return h.discovery.AbsoluteURL(path)
}

func (h *HTTPHandler) metadata(siteName, title, description, path, openGraphType string, schema map[string]any) PageMetadata {
	metadata := PageMetadata{Title: title, Description: description, OpenGraphType: openGraphType}
	if h.discovery == nil {
		return metadata
	}
	metadata.CanonicalURL = h.absoluteURL(path)
	metadata.RSSURL = h.absoluteURL("/rss.xml")
	if schema != nil {
		if encoded, err := json.Marshal(schema); err == nil {
			metadata.JSONLD = template.JS(encoded)
		}
	}
	_ = siteName
	return metadata
}

func (h *HTTPHandler) articleMetadata(siteName string, article publishing.Article, path string) PageMetadata {
	title := article.SEOTitle
	if title == "" {
		title = article.Title + " · " + siteName
	}
	description := article.SEODescription
	if description == "" {
		description = article.Excerpt
	}
	if description == "" {
		description = article.Title
	}
	schemaType, openGraphType := "WebPage", "website"
	if article.Kind == "article" {
		schemaType, openGraphType = "BlogPosting", "article"
	}
	schema := map[string]any{
		"@context": "https://schema.org", "@type": schemaType, "headline": article.Title,
		"description": description, "url": h.absoluteURL(path),
		"isPartOf": map[string]any{"@type": "WebSite", "name": siteName, "url": h.absoluteURL("/")},
	}
	if article.PublishedAt != nil {
		schema["datePublished"] = article.PublishedAt.Format(time.RFC3339)
	}
	if article.PublishedRevisionAt != nil {
		schema["dateModified"] = article.PublishedRevisionAt.Format(time.RFC3339)
	}
	return h.metadata(siteName, title, description, path, openGraphType, schema)
}

func normalizedFilter(value string) (string, bool) {
	if value == "" {
		return "", true
	}
	_, key, err := platformslug.Normalize(value)
	return key, err == nil
}

func (h *HTTPHandler) tryRedirect(w http.ResponseWriter, r *http.Request) bool {
	if h.discovery == nil {
		return false
	}
	pathKey, ok := normalizedPathKey(r.URL.Path)
	if !ok {
		return false
	}
	redirect, err := h.discovery.ResolveRedirect(r.Context(), pathKey)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			h.logger.ErrorContext(r.Context(), "resolve public redirect", "error", err, "path", r.URL.Path)
		}
		return false
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	http.Redirect(w, r, redirect.TargetPath, redirect.StatusCode)
	return true
}

func normalizedPathKey(path string) (string, bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 1 && parts[0] != "" {
		_, key, err := platformslug.Normalize(parts[0])
		return "/" + key, err == nil
	}
	if len(parts) == 2 && (parts[0] == "posts" || parts[0] == "categories" || parts[0] == "tags") {
		_, key, err := platformslug.Normalize(parts[1])
		return "/" + parts[0] + "/" + key, err == nil
	}
	return "", false
}

func matchesETag(header, etag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || candidate == etag || strings.TrimPrefix(candidate, "W/") == etag {
			return true
		}
	}
	return false
}

func articleData(article publishing.Article) ArticleData {
	data := ArticleData{
		Kind:         article.Kind,
		Title:        article.Title,
		Slug:         article.Slug,
		Excerpt:      article.Excerpt,
		BodyMarkdown: article.BodyMarkdown,
		PublishedAt:  article.PublishedAt,
	}
	if article.Category != nil {
		data.Category = &TermData{Name: article.Category.Name, URL: "/categories/" + article.Category.Slug}
	}
	for _, tag := range article.Tags {
		data.Tags = append(data.Tags, TermData{Name: tag.Name, URL: "/tags/" + tag.Slug})
	}
	return data
}

func articleDataList(articles []publishing.Article) []ArticleData {
	result := make([]ArticleData, 0, len(articles))
	for _, article := range articles {
		result = append(result, articleData(article))
	}
	return result
}

func publishingContentID(r *http.Request) (int64, string, error) {
	kind := "article"
	value := chi.URLParam(r, "articleID")
	if value == "" {
		kind = "page"
		value = chi.URLParam(r, "pageID")
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id < 1 {
		return 0, "", publishing.ErrNotFound
	}
	return id, kind, nil
}

func (h *HTTPHandler) navigation(ctx context.Context) (Navigation, error) {
	if h.organization == nil {
		return Navigation{}, nil
	}
	primary, err := h.organization.PublicNavigation(ctx, "primary")
	if err != nil {
		return Navigation{}, err
	}
	footer, err := h.organization.PublicNavigation(ctx, "footer")
	if err != nil {
		return Navigation{}, err
	}
	return Navigation{Primary: navigationLinks(primary), Footer: navigationLinks(footer)}, nil
}

func navigationLinks(items []organization.NavigationItem) []NavigationLink {
	links := make(map[int64]*NavigationLink, len(items))
	roots := make([]*NavigationLink, 0, len(items))
	for _, item := range items {
		link := &NavigationLink{Label: item.Label, URL: item.URL, External: item.TargetKind == "external"}
		links[item.ID] = link
		if item.ParentID == 0 {
			roots = append(roots, link)
		} else if parent := links[item.ParentID]; parent != nil {
			parent.Children = append(parent.Children, *link)
		}
	}
	result := make([]NavigationLink, 0, len(roots))
	for _, link := range roots {
		result = append(result, *link)
	}
	return result
}
