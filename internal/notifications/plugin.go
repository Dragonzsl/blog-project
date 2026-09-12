package notifications

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/zhushilin/blog-project/internal/extensions"
	"github.com/zhushilin/blog-project/internal/platform/clientip"
	"github.com/zhushilin/blog-project/internal/platform/publicwrite"
	adminweb "github.com/zhushilin/blog-project/web/admin"
)

type NewsletterHTTPHandler struct {
	service   *NewsletterService
	clientIP  *clientip.Resolver
	security  NewsletterAdminSecurity
	adminPath string
	templates *template.Template
}

type NewsletterAdminSecurity interface {
	CSRFToken(*http.Request) string
	VerifyParsedCSRF(http.ResponseWriter, *http.Request) bool
}

// NewNewsletterHTTPHandler keeps the original value-based constructor for
// small integrations and tests. NewNewsletterHTTPHandlerFromService should be
// used by the application so the handler and plugin share the same service.
func NewNewsletterHTTPHandler(service NewsletterService) *NewsletterHTTPHandler {
	return NewNewsletterHTTPHandlerFromService(&service)
}

func NewNewsletterHTTPHandlerFromService(service *NewsletterService) *NewsletterHTTPHandler {
	templates, err := template.ParseFS(adminweb.Files, "templates/*.html")
	if err != nil {
		templates = template.New("newsletter_subscribers.html")
	}
	return &NewsletterHTTPHandler{service: service, clientIP: clientip.DirectPeerOnly(), templates: templates}
}

func (h *NewsletterHTTPHandler) SetClientIPResolver(resolver *clientip.Resolver) {
	if resolver == nil {
		resolver = clientip.DirectPeerOnly()
	}
	h.clientIP = resolver
}

func (h *NewsletterHTTPHandler) SetSecurity(security NewsletterAdminSecurity) { h.security = security }

func (h *NewsletterHTTPHandler) subscribe(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.service == nil {
		writeNewsletterJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "newsletter is unavailable"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := r.ParseForm(); err != nil {
		writeNewsletterJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid form"})
		return
	}
	identity := clientip.DirectPeerOnly().Resolve(r)
	if h.clientIP != nil {
		identity = h.clientIP.Resolve(r)
	}
	err := h.service.RequestSubscribe(r.Context(), r.FormValue("email"), NewsletterRequest{
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
		ClientIdentity: identity,
	})
	if err != nil {
		if errors.Is(err, publicwrite.ErrDuplicateRequest) && r.Header.Get("Idempotency-Key") == "" {
			writeNewsletterJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
			return
		}
		status := http.StatusUnprocessableEntity
		if errors.Is(err, publicwrite.ErrIdempotencyConflict) || errors.Is(err, publicwrite.ErrDuplicateRequest) {
			status = http.StatusConflict
		}
		writeNewsletterJSON(w, status, map[string]string{"error": "subscription request could not be accepted"})
		return
	}
	// Do not reveal whether the address already exists or was previously
	// unsubscribed. Confirmation is delivered asynchronously when applicable.
	writeNewsletterJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

func (h *NewsletterHTTPHandler) confirm(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.service == nil {
		writeNewsletterJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "newsletter is unavailable"})
		return
	}
	result, err := h.service.Confirm(r.Context(), r.URL.Query().Get("token"))
	if err != nil {
		writeNewsletterJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid or expired newsletter token"})
		return
	}
	writeNewsletterJSON(w, http.StatusOK, map[string]string{"status": "confirmed", "unsubscribe_url": result.UnsubscribeURL})
}

func (h *NewsletterHTTPHandler) unsubscribe(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.service == nil {
		writeNewsletterJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "newsletter is unavailable"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := r.ParseForm(); err != nil {
		writeNewsletterJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid form"})
		return
	}
	if err := h.service.UnsubscribeToken(r.Context(), r.FormValue("token")); err != nil {
		writeNewsletterJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid or expired newsletter token"})
		return
	}
	writeNewsletterJSON(w, http.StatusAccepted, map[string]string{"status": "unsubscribed"})
}

func (h *NewsletterHTTPHandler) adminSubscribers(w http.ResponseWriter, r *http.Request) {
	if h.service == nil || h.security == nil {
		http.NotFound(w, r)
		return
	}
	page, err := h.service.AdminSubscribers(r.Context(), r.URL.Query().Get("status"), "", parseNewsletterPage(r.URL.Query().Get("page")), 20)
	if err != nil {
		http.Error(w, "newsletter subscribers unavailable", http.StatusInternalServerError)
		return
	}
	adminPath := h.adminPath
	if adminPath == "" {
		adminPath = "/admin/plugins/newsletter.local/subscribers"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	status := normalizedNewsletterStatus(r.URL.Query().Get("status"))
	operationKey := newNewsletterOperationKey()
	data := map[string]any{"Page": page, "Status": status, "BasePath": adminPath, "CSRF": h.security.CSRFToken(r), "AdminSection": "plugins", "OperationKey": operationKey, "PreviousURL": newsletterPageURL(adminPath, status, page.Page-1, page.PerPage), "NextURL": newsletterPageURL(adminPath, status, page.Page+1, page.PerPage)}
	if !page.HasMore {
		data["NextURL"] = ""
	}
	if page.Page <= 1 {
		data["PreviousURL"] = ""
	}
	switch r.URL.Query().Get("notice") {
	case "resend":
		data["Notice"] = "确认邮件任务已重新排队。"
	}
	switch r.URL.Query().Get("error") {
	case "cooldown":
		data["Error"] = "该订阅地址最近已经重发过确认邮件，请稍后再试。"
	case "in_progress":
		data["Error"] = "相同的重发请求正在处理，请稍后刷新。"
	case "resend":
		data["Error"] = "确认邮件重发失败，请检查后台任务。"
	}
	_ = h.templates.ExecuteTemplate(w, "newsletter_subscribers.html", data)
}

func (h *NewsletterHTTPHandler) adminResend(w http.ResponseWriter, r *http.Request) {
	if h.security == nil {
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := r.ParseForm(); err != nil || !h.security.VerifyParsedCSRF(w, r) {
		return
	}
	if r.FormValue("confirm_action") != "1" {
		http.Error(w, "请确认此管理操作", http.StatusBadRequest)
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "subscriberID"), 10, 64)
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	operationKey := strings.TrimSpace(r.FormValue("operation_key"))
	if operationKey == "" {
		operationKey = newNewsletterOperationKey()
	}
	err = h.service.ResendConfirmation(r.Context(), id, operationKey)
	query := url.Values{}
	if err == nil {
		query.Set("notice", "resend")
	} else if errors.Is(err, ErrNewsletterResendCooldown) {
		query.Set("error", "cooldown")
	} else if errors.Is(err, publicwrite.ErrIdempotencyPending) {
		query.Set("error", "in_progress")
	} else {
		query.Set("error", "resend")
	}
	path := h.adminPath
	if path == "" {
		path = "/admin/plugins/newsletter.local/subscribers"
	}
	http.Redirect(w, r, path+"?"+query.Encode(), http.StatusSeeOther)
}

func parseNewsletterPage(value string) int {
	page, _ := strconv.Atoi(value)
	if page < 1 {
		page = 1
	}
	return page
}

func newsletterPageURL(basePath, status string, page, perPage int) string {
	if page < 1 {
		return ""
	}
	values := url.Values{}
	if status != "" && status != "all" {
		values.Set("status", status)
	}
	values.Set("page", strconv.Itoa(page))
	if perPage > 0 {
		values.Set("per_page", strconv.Itoa(perPage))
	}
	return basePath + "?" + values.Encode()
}

func normalizedNewsletterStatus(value string) string {
	if value == "pending" || value == "active" || value == "unsubscribed" {
		return value
	}
	return "all"
}

func newNewsletterOperationKey() string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "newsletter-resend"
	}
	return hex.EncodeToString(value)
}

func writeNewsletterJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

type NewsletterPlugin struct {
	ID      string
	Name    string
	Handler *NewsletterHTTPHandler
	Service *NewsletterService
}

func (p *NewsletterPlugin) Manifest() extensions.Manifest {
	return extensions.Manifest{ID: p.ID, Name: p.Name, Version: "1.0.0", APIVersion: extensions.HostAPIVersion, Kind: "newsletter", Capabilities: []string{"public_route", "persistent_task", "admin_menu"}}
}

func (p *NewsletterPlugin) Register(host *extensions.Host) error {
	if p == nil || p.Handler == nil {
		return extensions.ErrCapabilityDenied
	}
	service := p.Service
	if service == nil {
		service = p.Handler.service
	}
	if service == nil {
		return errors.New("newsletter service is required")
	}
	p.Service = service
	p.Handler.adminPath = "/admin/plugins/" + p.ID + "/subscribers"
	service.SetTaskEnqueuer(func(ctx context.Context, tx *sql.Tx, kind string, payload any, idempotencyKey string, availableAt time.Time) error {
		return host.EnqueueTaskTx(ctx, tx, kind, payload, idempotencyKey, availableAt)
	})
	if err := host.RegisterSettings(extensions.SettingsSchema{"enabled": {Type: "boolean", Default: true}}); err != nil {
		return err
	}
	if err := host.RegisterMenu(extensions.MenuItem{Label: "Newsletter", Path: p.Handler.adminPath, Section: "content", Order: 40}); err != nil {
		return err
	}
	if err := host.RouteSlot("newsletter", http.MethodPost, "/newsletter/subscribe", p.Handler.subscribe); err != nil {
		return err
	}
	if err := host.RouteSlot("newsletter", http.MethodGet, "/newsletter/confirm", p.Handler.confirm); err != nil {
		return err
	}
	if err := host.RouteSlot("newsletter", http.MethodGet, "/newsletter/unsubscribe", p.Handler.unsubscribe); err != nil {
		return err
	}
	if err := host.RouteSlot("newsletter", http.MethodPost, "/newsletter/unsubscribe", p.Handler.unsubscribe); err != nil {
		return err
	}
	if err := host.AdminRoute(http.MethodGet, "/subscribers", p.Handler.adminSubscribers); err != nil {
		return err
	}
	if err := host.AdminRoute(http.MethodPost, "/subscribers/{subscriberID}/resend", p.Handler.adminResend); err != nil {
		return err
	}
	if err := host.RegisterTask("send_confirmation", service.ProcessConfirmationTask); err != nil {
		return err
	}
	return host.RegisterTask("sync", service.ProcessSyncTask)
}
