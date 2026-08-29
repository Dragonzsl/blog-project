package extensions

import (
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"
	adminweb "github.com/zhushilin/blog-project/web/admin"
)

type AdminSecurity interface {
	CSRFToken(*http.Request) string
	VerifyParsedCSRF(http.ResponseWriter, *http.Request) bool
}

type HTTPHandler struct {
	registry  *Registry
	security  AdminSecurity
	logger    *slog.Logger
	templates *template.Template
}

func NewHTTPHandler(registry *Registry, security AdminSecurity, logger *slog.Logger) (*HTTPHandler, error) {
	templates, err := template.ParseFS(adminweb.Files, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse plugin templates: %w", err)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &HTTPHandler{registry: registry, security: security, logger: logger, templates: templates}, nil
}

func (h *HTTPHandler) RegisterAdmin(router chi.Router) {
	router.Get("/plugins", h.index)
	router.Post("/plugins/{pluginID}/enable", h.enable)
	router.Post("/plugins/{pluginID}/disable", h.disable)
}

func (h *HTTPHandler) index(w http.ResponseWriter, r *http.Request) {
	states, err := h.registry.States(r.Context())
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	notice, message := pluginFeedback(r)
	if err := h.templates.ExecuteTemplate(w, "plugins.html", map[string]any{"Plugins": states, "CSRF": h.security.CSRFToken(r), "AdminSection": "plugins", "Notice": notice, "Error": message}); err != nil {
		h.internalError(w, r, err)
	}
}

func (h *HTTPHandler) enable(w http.ResponseWriter, r *http.Request)  { h.change(w, r, true) }
func (h *HTTPHandler) disable(w http.ResponseWriter, r *http.Request) { h.change(w, r, false) }
func (h *HTTPHandler) change(w http.ResponseWriter, r *http.Request, enabled bool) {
	if err := r.ParseForm(); err != nil || !h.security.VerifyParsedCSRF(w, r) {
		return
	}
	id := chi.URLParam(r, "pluginID")
	var err error
	if enabled {
		err = h.registry.Enable(r.Context(), id)
	} else {
		err = h.registry.Disable(r.Context(), id)
	}
	if err != nil {
		if !errors.Is(err, ErrPluginNotFound) {
			h.logger.ErrorContext(r.Context(), "change plugin state", "plugin", id, "error", err)
		}
		query := url.Values{"error": []string{"change"}}
		if errors.Is(err, ErrProviderConflict) {
			query.Set("error", "conflict")
		} else if errors.Is(err, ErrPluginNotFound) {
			query.Set("error", "missing")
		}
		http.Redirect(w, r, "/admin/plugins?"+query.Encode(), http.StatusSeeOther)
		return
	}
	status := "disabled"
	if enabled {
		status = "enabled"
	}
	http.Redirect(w, r, "/admin/plugins?notice="+status, http.StatusSeeOther)
}

func pluginFeedback(r *http.Request) (string, string) {
	switch r.URL.Query().Get("notice") {
	case "enabled":
		return "插件已启用。", ""
	case "disabled":
		return "插件已停用。", ""
	}
	switch r.URL.Query().Get("error") {
	case "conflict":
		return "", "同类评论插件已经启用，请先停用当前插件。"
	case "missing":
		return "", "插件不存在或已被移除。"
	case "change":
		return "", "插件状态更新失败，请稍后重试。"
	}
	return "", ""
}
func (h *HTTPHandler) internalError(w http.ResponseWriter, r *http.Request, err error) {
	h.logger.ErrorContext(r.Context(), "plugin admin request failed", "error", err)
	http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
}
