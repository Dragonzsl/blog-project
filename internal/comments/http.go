package comments

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	adminweb "github.com/zhushilin/blog-project/web/admin"
)

type Security interface {
	CSRFToken(*http.Request) string
	VerifyParsedCSRF(http.ResponseWriter, *http.Request) bool
}

type HTTPHandler struct {
	service   *Service
	lookup    ContentLookup
	security  Security
	logger    *slog.Logger
	templates *template.Template
	rateMu    sync.Mutex
	rate      map[string]rateWindow
}

type rateWindow struct {
	Started time.Time
	Count   int
}

func NewHTTPHandler(service *Service, lookup ContentLookup, security Security, logger *slog.Logger, templates ...*template.Template) *HTTPHandler {
	var parsed *template.Template
	if len(templates) > 0 {
		parsed = templates[0]
	}
	if parsed == nil {
		var err error
		parsed, err = template.ParseFS(adminweb.Files, "templates/*.html")
		if err != nil {
			if logger != nil {
				logger.Error("parse comment templates", "error", err)
			}
			parsed = template.New("comments.html")
		}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &HTTPHandler{service: service, lookup: lookup, security: security, logger: logger, templates: parsed, rate: make(map[string]rateWindow)}
}

func (h *HTTPHandler) RegisterPublic(router chi.Router) {
	router.Get("/posts/{slug}/comments", h.list)
	router.Post("/posts/{slug}/comments", h.create)
}
func (h *HTTPHandler) RegisterAdmin(router chi.Router) {
	router.Get("/comments", h.pending)
	router.Post("/comments/{commentID}/moderate", h.moderate)
}

func (h *HTTPHandler) list(w http.ResponseWriter, r *http.Request) {
	article, err := h.lookup.PublicArticle(r.Context(), chi.URLParam(r, "slug"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	comments, err := h.service.Approved(r.Context(), article.ID)
	if err != nil {
		h.error(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"comments": comments})
}

func (h *HTTPHandler) create(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid comment", http.StatusBadRequest)
		return
	}
	if r.FormValue("website_confirm") != "" {
		http.Error(w, "Invalid comment", http.StatusUnprocessableEntity)
		return
	}
	if !h.allowRequest(r) {
		http.Error(w, "Too many comments", http.StatusTooManyRequests)
		return
	}
	comment, err := h.service.Create(r.Context(), chi.URLParam(r, "slug"), Input{DisplayName: r.FormValue("display_name"), Email: r.FormValue("email"), Website: r.FormValue("website"), Body: r.FormValue("body"), ParentID: parseID(r.FormValue("parent_id"))})
	if err != nil {
		status := http.StatusUnprocessableEntity
		if errors.Is(err, ErrNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": comment.ID, "status": comment.Status})
}

func (h *HTTPHandler) allowRequest(r *http.Request) bool {
	key := strings.TrimSpace(r.RemoteAddr)
	now := time.Now()
	h.rateMu.Lock()
	defer h.rateMu.Unlock()
	if len(h.rate) > 1024 {
		for candidate, window := range h.rate {
			if now.Sub(window.Started) > time.Minute {
				delete(h.rate, candidate)
			}
		}
	}
	window := h.rate[key]
	if now.Sub(window.Started) >= time.Minute || window.Started.IsZero() {
		window = rateWindow{Started: now}
	}
	window.Count++
	h.rate[key] = window
	return window.Count <= 5
}

func (h *HTTPHandler) pending(w http.ResponseWriter, r *http.Request) {
	comments, err := h.service.Pending(r.Context(), 100)
	if err != nil {
		h.error(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = h.templates.ExecuteTemplate(w, "comments.html", map[string]any{"Comments": comments, "CSRF": h.security.CSRFToken(r)})
}
func (h *HTTPHandler) moderate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil || !h.security.VerifyParsedCSRF(w, r) {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "commentID"), 10, 64)
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	if err := h.service.Moderate(r.Context(), id, r.FormValue("status")); err != nil {
		if errors.Is(err, ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		h.error(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/plugins/comments.local/comments", http.StatusSeeOther)
}
func parseID(value string) int64 { id, _ := strconv.ParseInt(value, 10, 64); return id }
func (h *HTTPHandler) error(w http.ResponseWriter, r *http.Request, err error) {
	h.logger.ErrorContext(r.Context(), "comment request failed", "error", err)
	http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// ExternalHTTPHandler exposes a provider-hosted discussion page without
// coupling the core article renderer to a specific vendor.
type ExternalHTTPHandler struct {
	Endpoint string
}

func (h ExternalHTTPHandler) Register(router chi.Router) {
	router.Get("/posts/{slug}/comments", h.show)
}

func (h ExternalHTTPHandler) show(w http.ResponseWriter, r *http.Request) {
	endpoint := strings.TrimRight(strings.TrimSpace(h.Endpoint), "/")
	if endpoint == "" {
		http.NotFound(w, r)
		return
	}
	location := endpoint + "/" + chi.URLParam(r, "slug")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, `<p>评论由外部服务托管：<a rel="nofollow" href="%s">打开评论</a></p>`, template.HTMLEscapeString(location))
}
