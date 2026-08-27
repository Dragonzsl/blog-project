// Package contentapi contains the deliberately small, read-only content API.
// It is an extension rather than a core route so a minimal installation pays
// no routing, JSON or cache cost until the site owner enables it.
package contentapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/zhushilin/blog-project/internal/extensions"
	"github.com/zhushilin/blog-project/internal/publishing"
)

const (
	PluginID   = "contentapi.readonly"
	APIVersion = 1
)

type ContentQueries interface {
	PublishedArticles(context.Context, int) ([]publishing.Article, error)
	PublishedPages(context.Context, int) ([]publishing.Article, error)
	PublicArticle(context.Context, string) (publishing.Article, error)
	PublicPage(context.Context, string) (publishing.Article, error)
}

type SiteQueries interface {
	SiteName(context.Context) (string, error)
}

type URLQueries interface {
	AbsoluteURL(string) string
}

type Config struct {
	// Token is optional. With an empty token the API follows ADR-0032 and is
	// same-origin/public-read by default. When set, callers must send a Bearer
	// token; no write endpoint is ever registered.
	Token string
}

type Plugin struct {
	handler *HTTPHandler
}

func NewPlugin(content ContentQueries, site SiteQueries, urls URLQueries, cfg Config) *Plugin {
	return &Plugin{handler: &HTTPHandler{content: content, site: site, urls: urls, token: strings.TrimSpace(cfg.Token)}}
}

func (p *Plugin) Manifest() extensions.Manifest {
	return extensions.Manifest{ID: PluginID, Name: "只读内容 API", Version: "1.0.0", APIVersion: extensions.HostAPIVersion, Kind: "content_api", Capabilities: []string{"public_route", "read_only"}}
}

func (p *Plugin) Register(host *extensions.Host) error {
	if p == nil || p.handler == nil || p.handler.content == nil || p.handler.site == nil {
		return errors.New("content API dependencies are required")
	}
	if err := host.RegisterMenu(extensions.MenuItem{Label: "内容 API", Path: "/admin/plugins/" + PluginID + "/status", Section: "settings", Order: 80}); err != nil {
		return err
	}
	if err := host.Route(http.MethodGet, "/api/v1/site", p.handler.siteInfo); err != nil {
		return err
	}
	if err := host.Route(http.MethodGet, "/api/v1/posts", p.handler.posts); err != nil {
		return err
	}
	if err := host.Route(http.MethodGet, "/api/v1/pages", p.handler.pages); err != nil {
		return err
	}
	if err := host.Route(http.MethodGet, "/api/v1/posts/{slug}", p.handler.post); err != nil {
		return err
	}
	if err := host.Route(http.MethodGet, "/api/v1/pages/{slug}", p.handler.page); err != nil {
		return err
	}
	return host.AdminRoute(http.MethodGet, "/status", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"id": PluginID, "api_version": APIVersion, "read_only": true})
	})
}

type HTTPHandler struct {
	content ContentQueries
	site    SiteQueries
	urls    URLQueries
	token   string
}

type item struct {
	ID           string     `json:"id"`
	Kind         string     `json:"kind"`
	Title        string     `json:"title"`
	Slug         string     `json:"slug"`
	URL          string     `json:"url"`
	Excerpt      string     `json:"excerpt"`
	BodyMarkdown string     `json:"body_markdown"`
	PublishedAt  *time.Time `json:"published_at,omitempty"`
	Category     *term      `json:"category,omitempty"`
	Tags         []term     `json:"tags,omitempty"`
	SEO          seo        `json:"seo"`
}

type term struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

type seo struct {
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
}

func (h *HTTPHandler) authorize(w http.ResponseWriter, r *http.Request) bool {
	if h.token == "" {
		return true
	}
	const prefix = "Bearer "
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(value) <= len(prefix) || !strings.EqualFold(value[:len(prefix)], prefix) || value[len(prefix):] != h.token {
		w.Header().Set("WWW-Authenticate", `Bearer realm="content-api"`)
		writeJSONRequest(w, r, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return false
	}
	return true
}

func (h *HTTPHandler) siteInfo(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	name, err := h.site.SiteName(r.Context())
	if err != nil {
		writeJSONRequest(w, r, http.StatusInternalServerError, map[string]string{"error": "site unavailable"})
		return
	}
	writeJSONRequest(w, r, http.StatusOK, map[string]any{"name": name, "api_version": APIVersion, "read_only": true})
}

func (h *HTTPHandler) posts(w http.ResponseWriter, r *http.Request) {
	h.list(w, r, "article")
}

func (h *HTTPHandler) pages(w http.ResponseWriter, r *http.Request) {
	h.list(w, r, "page")
}

func (h *HTTPHandler) list(w http.ResponseWriter, r *http.Request, kind string) {
	if !h.authorize(w, r) {
		return
	}
	limit, page := parsePage(r)
	fetch := limit * page
	var values []publishing.Article
	var err error
	if kind == "article" {
		values, err = h.content.PublishedArticles(r.Context(), fetch)
	} else {
		values, err = h.content.PublishedPages(r.Context(), fetch)
	}
	if err != nil {
		writeJSONRequest(w, r, http.StatusInternalServerError, map[string]string{"error": "content unavailable"})
		return
	}
	start := (page - 1) * limit
	if start > len(values) {
		start = len(values)
	}
	end := start + limit
	if end > len(values) {
		end = len(values)
	}
	items := make([]item, 0, end-start)
	for _, value := range values[start:end] {
		items = append(items, h.toItem(value))
	}
	writeJSONRequest(w, r, http.StatusOK, map[string]any{"items": items, "page": page, "per_page": limit, "has_more": len(values) > end})
}

func (h *HTTPHandler) post(w http.ResponseWriter, r *http.Request) {
	h.single(w, r, true)
}

func (h *HTTPHandler) page(w http.ResponseWriter, r *http.Request) {
	h.single(w, r, false)
}

func (h *HTTPHandler) single(w http.ResponseWriter, r *http.Request, article bool) {
	if !h.authorize(w, r) {
		return
	}
	slug := chi.URLParam(r, "slug")
	var value publishing.Article
	var err error
	if article {
		value, err = h.content.PublicArticle(r.Context(), slug)
	} else {
		value, err = h.content.PublicPage(r.Context(), slug)
	}
	if err != nil {
		if errors.Is(err, publishing.ErrNotFound) {
			writeJSONRequest(w, r, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		writeJSONRequest(w, r, http.StatusInternalServerError, map[string]string{"error": "content unavailable"})
		return
	}
	writeJSONRequest(w, r, http.StatusOK, h.toItem(value))
}

func (h *HTTPHandler) toItem(value publishing.Article) item {
	result := item{ID: hex.EncodeToString(value.PublicID), Kind: value.Kind, Title: value.Title, Slug: value.PublishedSlug, Excerpt: value.Excerpt, BodyMarkdown: value.BodyMarkdown, PublishedAt: value.PublishedAt, SEO: seo{Title: value.SEOTitle, Description: value.SEODescription}}
	if result.Slug == "" {
		result.Slug = value.Slug
	}
	if h.urls != nil {
		if value.Kind == "article" {
			result.URL = h.urls.AbsoluteURL("/posts/" + result.Slug)
		} else {
			result.URL = h.urls.AbsoluteURL("/" + result.Slug)
		}
	}
	if value.Category != nil {
		result.Category = &term{ID: hex.EncodeToString(value.Category.PublicID), Slug: value.Category.Slug, Name: value.Category.Name}
	}
	for _, tag := range value.Tags {
		result.Tags = append(result.Tags, term{ID: hex.EncodeToString(tag.PublicID), Slug: tag.Slug, Name: tag.Name})
	}
	return result
}

func parsePage(r *http.Request) (int, int) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	if page < 1 {
		page = 1
	}
	if page > 100 {
		page = 100
	}
	return limit, page
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	writeJSONRequest(w, nil, status, value)
}

func writeJSONRequest(w http.ResponseWriter, r *http.Request, status int, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	digest := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(digest[:16]) + `"`
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if status >= 400 {
		w.Header().Set("Cache-Control", "no-store")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=60, must-revalidate")
	}
	w.Header().Set("ETag", etag)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r != nil && r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
