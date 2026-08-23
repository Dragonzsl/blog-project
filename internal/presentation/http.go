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
	"github.com/zhushilin/blog-project/internal/platform/database"
	"github.com/zhushilin/blog-project/internal/publishing"
)

type ContentQueries interface {
	Article(context.Context, int64) (publishing.Article, error)
	PublicArticle(context.Context, string) (publishing.Article, error)
	PublishedArticles(context.Context, int) ([]publishing.Article, error)
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
	content   ContentQueries
	siteNamer SiteNamer
	state     *StateRepository
	theme     *Theme
	cache     *PageCache
	logger    *slog.Logger
}

func NewHTTPHandler(content ContentQueries, siteNamer SiteNamer, state *StateRepository, theme *Theme, cache *PageCache, logger *slog.Logger) *HTTPHandler {
	return &HTTPHandler{content: content, siteNamer: siteNamer, state: state, theme: theme, cache: cache, logger: logger}
}

func (h *HTTPHandler) RegisterPublic(router chi.Router) {
	router.Get("/", h.home)
	router.Head("/", h.home)
	router.Get("/posts/{slug}", h.article)
	router.Head("/posts/{slug}", h.article)
	router.Get("/assets/theme/default/{fingerprint}/theme.css", h.asset)
	router.Head("/assets/theme/default/{fingerprint}/theme.css", h.asset)
}

func (h *HTTPHandler) RegisterAdmin(router chi.Router) {
	router.Get("/articles/{articleID}/preview", h.preview)
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
		body, err := h.theme.RenderHome(siteName, articleDataList(articles))
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
		body, err := h.theme.RenderArticle(siteName, articleData(article), false, "")
		lastModified := ""
		if article.PublishedRevisionAt != nil {
			lastModified = article.PublishedRevisionAt.UTC().Format(http.TimeFormat)
		}
		return body, lastModified, err
	})
}

func (h *HTTPHandler) preview(w http.ResponseWriter, r *http.Request) {
	id, err := publishingArticleID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	article, err := h.content.Article(r.Context(), id)
	if err != nil {
		h.handleRenderError(w, r, err)
		return
	}
	siteName, err := h.siteNamer.SiteName(r.Context())
	if err != nil {
		h.handleRenderError(w, r, err)
		return
	}
	body, err := h.theme.RenderArticle(siteName, articleData(article), true, fmt.Sprintf("/admin/articles/%d/edit", article.ID))
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
	return ArticleData{
		Title:        article.Title,
		Slug:         article.Slug,
		Excerpt:      article.Excerpt,
		BodyMarkdown: article.BodyMarkdown,
		PublishedAt:  article.PublishedAt,
	}
}

func articleDataList(articles []publishing.Article) []ArticleData {
	result := make([]ArticleData, 0, len(articles))
	for _, article := range articles {
		result = append(result, articleData(article))
	}
	return result
}

func publishingArticleID(r *http.Request) (int64, error) {
	value := chi.URLParam(r, "articleID")
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id < 1 {
		return 0, publishing.ErrNotFound
	}
	return id, nil
}
