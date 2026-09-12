// Package contentapi contains the deliberately small, read-only content API.
// It is an extension rather than a core route so a minimal installation pays
// no routing, JSON or cache cost until the site owner enables it.
package contentapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/zhushilin/blog-project/internal/extensions"
	"github.com/zhushilin/blog-project/internal/media"
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

type CursorContentQueries interface {
	PublishedContentCursor(context.Context, publishing.PublicContentQuery) (publishing.PublicContentPage, error)
}

type PagedContentQueries interface {
	PublishedContentPage(context.Context, string, int, int) (publishing.PublicContentPage, error)
}

type SiteQueries interface {
	SiteName(context.Context) (string, error)
}

type URLQueries interface {
	AbsoluteURL(string) string
}

type MediaQueries interface {
	PublicItem(context.Context, []byte) (media.Item, error)
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

func (p *Plugin) SetMediaQueries(queries MediaQueries) {
	if p != nil && p.handler != nil {
		p.handler.media = queries
	}
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
	if err := host.RouteSlot("content_api", http.MethodGet, "/api/v1/site", p.handler.siteInfo); err != nil {
		return err
	}
	if err := host.RouteSlot("content_api", http.MethodGet, "/api/v1/posts", p.handler.posts); err != nil {
		return err
	}
	if err := host.RouteSlot("content_api", http.MethodGet, "/api/v1/pages", p.handler.pages); err != nil {
		return err
	}
	if err := host.RouteSlot("content_api", http.MethodGet, "/api/v1/posts/{slug}", p.handler.post); err != nil {
		return err
	}
	if err := host.RouteSlot("content_api", http.MethodGet, "/api/v1/pages/{slug}", p.handler.page); err != nil {
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
	media   MediaQueries
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
	UpdatedAt    *time.Time `json:"updated_at,omitempty"`
	Cover        *mediaView `json:"cover,omitempty"`
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

type mediaView struct {
	URL    string `json:"url"`
	Alt    string `json:"alt,omitempty"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
	SrcSet string `json:"srcset,omitempty"`
}

func (h *HTTPHandler) authorize(w http.ResponseWriter, r *http.Request) bool {
	if h.token == "" {
		return true
	}
	const prefix = "Bearer "
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(value) <= len(prefix) || !strings.EqualFold(value[:len(prefix)], prefix) || subtle.ConstantTimeCompare([]byte(value[len(prefix):]), []byte(h.token)) != 1 {
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
	query, err := parseListQuery(r)
	if err != nil {
		writeJSONRequest(w, r, http.StatusBadRequest, map[string]string{"error": "invalid_query"})
		return
	}
	private := h.token != ""
	if cursorQueries, ok := h.content.(CursorContentQueries); ok && (!query.PageProvided || query.Cursor != nil || query.CursorText != "" || query.CategorySlug != "" || query.TagSlug != "" || query.UpdatedSince != nil) {
		result, err := cursorQueries.PublishedContentCursor(r.Context(), publishing.PublicContentQuery{Kind: kind, Limit: query.Limit, Cursor: query.Cursor, CategorySlug: query.CategorySlug, TagSlug: query.TagSlug, UpdatedSince: query.UpdatedSince})
		if err != nil {
			writeJSONRequest(w, r, http.StatusInternalServerError, map[string]string{"error": "content unavailable"})
			return
		}
		items := make([]item, 0, len(result.Contents))
		for _, value := range result.Contents {
			items = append(items, h.toItem(r.Context(), value))
		}
		response := map[string]any{"items": items, "per_page": query.Limit, "has_more": result.HasMore, "protocol_version": APIVersion}
		if result.HasMore && len(result.Contents) > 0 {
			response["next_cursor"] = encodeCursor(result.Contents[len(result.Contents)-1])
		}
		writeJSONRequestWithOptions(w, r, http.StatusOK, response, private, latestModified(result.Contents))
		return
	}
	var result publishing.PublicContentPage
	if paged, ok := h.content.(PagedContentQueries); ok {
		result, err = paged.PublishedContentPage(r.Context(), kind, query.Page, query.Limit)
	} else {
		var values []publishing.Article
		fetch := query.Limit * query.Page
		if kind == "article" {
			values, err = h.content.PublishedArticles(r.Context(), fetch)
		} else {
			values, err = h.content.PublishedPages(r.Context(), fetch)
		}
		start := (query.Page - 1) * query.Limit
		if start > len(values) {
			start = len(values)
		}
		end := start + query.Limit
		if end > len(values) {
			end = len(values)
		}
		result = publishing.PublicContentPage{Contents: values[start:end], HasMore: len(values) > end}
	}
	if err != nil {
		writeJSONRequest(w, r, http.StatusInternalServerError, map[string]string{"error": "content unavailable"})
		return
	}
	items := make([]item, 0, len(result.Contents))
	for _, value := range result.Contents {
		items = append(items, h.toItem(r.Context(), value))
	}
	writeJSONRequestWithOptions(w, r, http.StatusOK, map[string]any{"items": items, "page": query.Page, "per_page": query.Limit, "has_more": result.HasMore, "protocol_version": APIVersion}, private, latestModified(result.Contents))
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
	writeJSONRequestWithOptions(w, r, http.StatusOK, h.toItem(r.Context(), value), h.token != "", articleModified(value))
}

func (h *HTTPHandler) toItem(ctx context.Context, value publishing.Article) item {
	result := item{ID: hex.EncodeToString(value.PublicID), Kind: value.Kind, Title: value.Title, Slug: value.PublishedSlug, Excerpt: value.Excerpt, BodyMarkdown: value.BodyMarkdown, PublishedAt: value.PublishedAt, UpdatedAt: value.PublishedRevisionAt, SEO: seo{Title: value.SEOTitle, Description: value.SEODescription}}
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
	if h.media != nil && len(value.CoverMediaPublicID) > 0 {
		if mediaItem, err := h.media.PublicItem(ctx, value.CoverMediaPublicID); err == nil {
			if view, err := mediaItem.PublicView(); err == nil {
				result.Cover = &mediaView{URL: view.URL, Alt: view.Alt, Width: view.Width, Height: view.Height, SrcSet: view.SrcSet}
			}
		}
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

type listQuery struct {
	Limit        int
	Page         int
	PageProvided bool
	CursorText   string
	Cursor       *publishing.PublicContentCursor
	CategorySlug string
	TagSlug      string
	UpdatedSince *time.Time
}

func parseListQuery(r *http.Request) (listQuery, error) {
	limit, page := parsePage(r)
	query := r.URL.Query()
	result := listQuery{Limit: limit, Page: page, PageProvided: query.Get("page") != "", CursorText: strings.TrimSpace(query.Get("cursor")), CategorySlug: strings.TrimSpace(query.Get("category")), TagSlug: strings.TrimSpace(query.Get("tag"))}
	if result.CursorText != "" {
		if query.Get("page") != "" {
			return listQuery{}, errors.New("page and cursor cannot be combined")
		}
		cursor, err := decodeCursor(result.CursorText)
		if err != nil {
			return listQuery{}, err
		}
		result.Cursor = &cursor
		result.Page = 0
	}
	if len(result.CategorySlug) > 120 || len(result.TagSlug) > 120 {
		return listQuery{}, errors.New("filter is too long")
	}
	if raw := strings.TrimSpace(query.Get("updated_since")); raw != "" {
		value, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return listQuery{}, err
		}
		value = value.UTC()
		result.UpdatedSince = &value
	}
	return result, nil
}

type cursorPayload struct {
	Version     int    `json:"v"`
	PublishedAt int64  `json:"published_at"`
	PublicID    string `json:"public_id"`
}

func encodeCursor(value publishing.Article) string {
	publishedAt := int64(0)
	if value.PublishedAt != nil {
		publishedAt = value.PublishedAt.UnixMilli()
	}
	payload, _ := json.Marshal(cursorPayload{Version: 1, PublishedAt: publishedAt, PublicID: hex.EncodeToString(value.PublicID)})
	return base64.RawURLEncoding.EncodeToString(payload)
}

func decodeCursor(value string) (publishing.PublicContentCursor, error) {
	if len(value) > 512 {
		return publishing.PublicContentCursor{}, errors.New("cursor is too long")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return publishing.PublicContentCursor{}, err
	}
	var payload cursorPayload
	if json.Unmarshal(decoded, &payload) != nil || payload.Version != 1 || payload.PublishedAt < 1 {
		return publishing.PublicContentCursor{}, errors.New("cursor is invalid")
	}
	publicID, err := hex.DecodeString(payload.PublicID)
	if err != nil || len(publicID) != 16 {
		return publishing.PublicContentCursor{}, errors.New("cursor is invalid")
	}
	return publishing.PublicContentCursor{PublishedAt: time.UnixMilli(payload.PublishedAt).UTC(), PublicID: publicID}, nil
}

func articleModified(value publishing.Article) string {
	if value.PublishedRevisionAt != nil {
		return value.PublishedRevisionAt.UTC().Format(http.TimeFormat)
	}
	if value.PublishedAt != nil {
		return value.PublishedAt.UTC().Format(http.TimeFormat)
	}
	return ""
}

func latestModified(values []publishing.Article) string {
	var latest time.Time
	for _, value := range values {
		candidate := value.PublishedRevisionAt
		if candidate == nil {
			candidate = value.PublishedAt
		}
		if candidate != nil && candidate.After(latest) {
			latest = *candidate
		}
	}
	if latest.IsZero() {
		return ""
	}
	return latest.UTC().Format(http.TimeFormat)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	writeJSONRequest(w, nil, status, value)
}

func writeJSONRequest(w http.ResponseWriter, r *http.Request, status int, value any) {
	writeJSONRequestWithOptions(w, r, status, value, false, "")
}

func writeJSONRequestWithOptions(w http.ResponseWriter, r *http.Request, status int, value any, private bool, lastModified string) {
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
	} else if private {
		w.Header().Set("Cache-Control", "private, no-store")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=60, must-revalidate")
	}
	w.Header().Set("ETag", etag)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if lastModified != "" {
		w.Header().Set("Last-Modified", lastModified)
	}
	if r != nil && r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if r != nil && lastModified != "" {
		if modified, err := http.ParseTime(lastModified); err == nil {
			if since, err := http.ParseTime(r.Header.Get("If-Modified-Since")); err == nil && !modified.After(since) {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
