package comments

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/zhushilin/blog-project/internal/operations"
	"github.com/zhushilin/blog-project/internal/platform/clientip"
	"github.com/zhushilin/blog-project/internal/platform/publicwrite"
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
	clientIP  *clientip.Resolver
}

type rateWindow struct {
	Started time.Time
	Count   int
}

type adminCommentFilterView struct {
	Status      string
	Query       string
	PreviousURL string
	NextURL     string
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
	return &HTTPHandler{service: service, lookup: lookup, security: security, logger: logger, templates: parsed, rate: make(map[string]rateWindow), clientIP: clientip.DirectPeerOnly()}
}

func (h *HTTPHandler) SetClientIPResolver(resolver *clientip.Resolver) {
	if resolver == nil {
		resolver = clientip.DirectPeerOnly()
	}
	h.clientIP = resolver
}

func (h *HTTPHandler) RegisterPublic(router chi.Router) {
	router.Get("/posts/{slug}/comments", h.list)
	router.Post("/posts/{slug}/comments", h.create)
}
func (h *HTTPHandler) RegisterAdmin(router chi.Router) {
	router.Get("/comments", h.pending)
	router.Post("/comments/{commentID}/moderate", h.moderate)
	router.Post("/comments/bulk", h.moderateBulk)
}

func (h *HTTPHandler) list(w http.ResponseWriter, r *http.Request) {
	article, err := h.lookup.PublicArticle(r.Context(), publicSlug(r))
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
	comment, err := h.service.CreateWithRequest(r.Context(), publicSlug(r), Input{DisplayName: r.FormValue("display_name"), Email: r.FormValue("email"), Website: r.FormValue("website"), Body: r.FormValue("body"), ParentID: parseID(r.FormValue("parent_id"))}, WriteRequest{IdempotencyKey: r.Header.Get("Idempotency-Key"), ClientIdentity: h.resolveClientIP(r)})
	if err != nil {
		status := http.StatusUnprocessableEntity
		if errors.Is(err, ErrNotFound) {
			status = http.StatusNotFound
		}
		if errors.Is(err, publicwrite.ErrIdempotencyConflict) || errors.Is(err, publicwrite.ErrIdempotencyPending) || errors.Is(err, publicwrite.ErrDuplicateRequest) {
			status = http.StatusConflict
		}
		message := "Invalid comment"
		if errors.Is(err, ErrNotFound) {
			message = "Comment target not found"
		} else if status == http.StatusConflict {
			message = "Duplicate comment submission"
		}
		http.Error(w, message, status)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": comment.ID, "public_id": fmt.Sprintf("%x", comment.PublicID), "status": comment.Status})
}

func (h *HTTPHandler) allowRequest(r *http.Request) bool {
	key := h.resolveClientIP(r)
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

func (h *HTTPHandler) resolveClientIP(r *http.Request) string {
	if h.clientIP == nil {
		return clientip.DirectPeerOnly().Resolve(r)
	}
	return h.clientIP.Resolve(r)
}

func (h *HTTPHandler) pending(w http.ResponseWriter, r *http.Request) {
	pageNumber := int(parseID(r.URL.Query().Get("page")))
	perPage := int(parseID(r.URL.Query().Get("per_page")))
	page, err := h.service.AdminComments(r.Context(), AdminCommentFilter{Status: r.URL.Query().Get("status"), Query: r.URL.Query().Get("q"), Page: pageNumber, PerPage: perPage})
	if err != nil {
		h.error(w, r, err)
		return
	}
	operationKey := newCommentOperationKey()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	notice, message := commentFeedback(r)
	filter := adminCommentFilterView{Status: r.URL.Query().Get("status"), Query: r.URL.Query().Get("q")}
	if filter.Status == "" {
		filter.Status = "pending"
	}
	if page.Page > 1 {
		filter.PreviousURL = commentPageURL(filter.Status, filter.Query, page.Page-1, page.PerPage)
	}
	if page.HasMore {
		filter.NextURL = commentPageURL(filter.Status, filter.Query, page.Page+1, page.PerPage)
	}
	_ = h.templates.ExecuteTemplate(w, "comments.html", map[string]any{"Comments": page.Comments, "Page": page, "Filter": filter, "OperationKey": operationKey, "CSRF": h.security.CSRFToken(r), "AdminSection": "plugins", "Notice": notice, "Error": message})
}

func commentPageURL(status, query string, page, perPage int) string {
	values := url.Values{}
	values.Set("status", status)
	if query != "" {
		values.Set("q", query)
	}
	values.Set("page", strconv.Itoa(page))
	if perPage > 0 {
		values.Set("per_page", strconv.Itoa(perPage))
	}
	return "/admin/plugins/comments.local/comments?" + values.Encode()
}

func (h *HTTPHandler) moderateBulk(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil || !h.security.VerifyParsedCSRF(w, r) {
		return
	}
	ids := make([]int64, 0, len(r.Form["comment_ids"]))
	for _, raw := range r.Form["comment_ids"] {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id < 1 {
			continue
		}
		ids = append(ids, id)
	}
	result, err := h.service.ModerateBulk(r.Context(), ids, r.FormValue("status"), r.FormValue("operation_key"))
	if err != nil {
		query := url.Values{"error": []string{"bulk"}}
		if errors.Is(err, ErrBulkInProgress) {
			query.Set("error", "bulk_in_progress")
		} else if errors.Is(err, ErrBulkKeyConflict) {
			query.Set("error", "bulk_conflict")
		} else if errors.Is(err, ErrInvalid) {
			query.Set("error", "invalid")
		}
		http.Redirect(w, r, "/admin/plugins/comments.local/comments?"+query.Encode(), http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = h.templates.ExecuteTemplate(w, "comment_bulk_result.html", map[string]any{"Result": result, "CSRF": h.security.CSRFToken(r), "AdminSection": "plugins"})
}

func newCommentOperationKey() string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(value)
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
		query := url.Values{"error": []string{"moderate"}}
		if errors.Is(err, ErrInvalid) {
			query.Set("error", "invalid")
		}
		http.Redirect(w, r, "/admin/plugins/comments.local/comments?"+query.Encode(), http.StatusSeeOther)
		return
	}
	status := r.FormValue("status")
	if status == "spam" {
		status = "spam"
	} else {
		status = "approved"
	}
	http.Redirect(w, r, "/admin/plugins/comments.local/comments?notice="+url.QueryEscape(status), http.StatusSeeOther)
}
func parseID(value string) int64 { id, _ := strconv.ParseInt(value, 10, 64); return id }
func (h *HTTPHandler) error(w http.ResponseWriter, r *http.Request, err error) {
	h.logger.ErrorContext(r.Context(), "comment request failed", "error", operations.SafeError(err))
	http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
}

func commentFeedback(r *http.Request) (string, string) {
	switch r.URL.Query().Get("notice") {
	case "approved":
		return "评论已通过并公开。", ""
	case "spam":
		return "评论已标记为垃圾。", ""
	case "bulk":
		return "批量评论操作已完成。", ""
	}
	switch r.URL.Query().Get("error") {
	case "invalid":
		return "", "评论操作无效，请刷新后重试。"
	case "moderate":
		return "", "评论状态更新失败，请稍后重试。"
	case "bulk_in_progress":
		return "相同的批量操作正在处理，请稍后查看结果。", ""
	case "bulk_conflict":
		return "批量操作编号已用于另一组评论，请刷新后重试。", ""
	case "bulk":
		return "批量操作失败，请刷新后重试。", ""
	}
	return "", ""
}

func publicSlug(r *http.Request) string {
	raw := chi.URLParam(r, "slug")
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		return raw
	}
	return decoded
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
	location := endpoint + "/" + url.PathEscape(publicSlug(r))
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, `<p>评论由外部服务托管：<a rel="nofollow" href="%s">打开评论</a></p>`, template.HTMLEscapeString(location))
}
