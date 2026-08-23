package presentation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/zhushilin/blog-project/internal/organization"
	"github.com/zhushilin/blog-project/internal/platform/database"
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
	cache        *PageCache
	logger       *slog.Logger
	organization OrganizationQueries
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
	router.Get("/assets/theme/default/{fingerprint}/theme.css", h.asset)
	router.Head("/assets/theme/default/{fingerprint}/theme.css", h.asset)
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
		body, err := h.theme.RenderHome(siteName, articleDataList(articles), navigation)
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
		body, err := h.theme.RenderArticle(siteName, articleData(article), false, "", navigation)
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
		body, err := h.theme.RenderArticle(siteName, articleData(page), false, "", navigation)
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
		var title, description string
		var ids []int64
		var err error
		if kind == "category" {
			var term organization.Category
			term, ids, err = h.organization.PublicCategory(ctx, slug, 50)
			title, description = term.Name, term.Description
		} else {
			var term organization.Tag
			term, ids, err = h.organization.PublicTag(ctx, slug, 50)
			title, description = "# "+term.Name, term.Description
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
		body, err := h.theme.RenderListing(siteName, title, description, articleDataList(articles), navigation)
		lastModified := ""
		if len(articles) > 0 && articles[0].PublishedRevisionAt != nil {
			lastModified = articles[0].PublishedRevisionAt.UTC().Format(http.TimeFormat)
		}
		return body, lastModified, err
	})
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
	body, err := h.theme.RenderArticle(siteName, articleData(article), true, backURL, navigation)
	if err != nil {
		h.handleRenderError(w, r, err)
		return
	}
	setPublicSecurityHeaders(w)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

func (h *HTTPHandler) asset(w http.ResponseWriter, r *http.Request) {
	if chi.URLParam(r, "fingerprint") != h.theme.AssetHash() {
		http.NotFound(w, r)
		return
	}
	etag := `"` + h.theme.AssetHash() + `"`
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("ETag", etag)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if matchesETag(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if r.Method != http.MethodHead {
		_, _ = w.Write(h.theme.CSS())
	}
}

func (h *HTTPHandler) serveCached(w http.ResponseWriter, r *http.Request, semanticKey string, render func(context.Context) ([]byte, string, error)) {
	epoch, err := h.state.RenderEpoch(r.Context())
	if err != nil {
		h.handleRenderError(w, r, err)
		return
	}
	key := h.theme.Version() + "|" + semanticKey
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
		ContentType:  "text/html; charset=utf-8",
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
	setPublicSecurityHeaders(w)
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

func (h *HTTPHandler) handleRenderError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, publishing.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if errors.Is(err, organization.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	h.logger.ErrorContext(r.Context(), "render public page", "error", err)
	http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
}

func setPublicSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; img-src 'self' data: https:; form-action 'none'; frame-ancestors 'none'; base-uri 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
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
