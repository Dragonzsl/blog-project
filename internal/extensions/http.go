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
	router.Post("/plugins/{pluginID}/delete", h.remove)
	router.Post("/plugins/{pluginID}/restore", h.restore)
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
	var installed, removed []PluginState
	for _, state := range states {
		if state.Removed {
			removed = append(removed, state)
		} else {
			installed = append(installed, state)
		}
	}
	if err := h.templates.ExecuteTemplate(w, "plugins.html", map[string]any{"Plugins": installed, "RemovedPlugins": removed, "CSRF": h.security.CSRFToken(r), "AdminSection": "plugins", "Notice": notice, "Error": message}); err != nil {
		h.internalError(w, r, err)
	}
}

func (h *HTTPHandler) enable(w http.ResponseWriter, r *http.Request)  { h.change(w, r, "enable") }
func (h *HTTPHandler) disable(w http.ResponseWriter, r *http.Request) { h.change(w, r, "disable") }
func (h *HTTPHandler) remove(w http.ResponseWriter, r *http.Request)  { h.change(w, r, "delete") }
func (h *HTTPHandler) restore(w http.ResponseWriter, r *http.Request) { h.change(w, r, "restore") }
func (h *HTTPHandler) change(w http.ResponseWriter, r *http.Request, action string) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}
	if !h.security.VerifyParsedCSRF(w, r) {
		return
	}
	if action == "delete" && r.PostFormValue("confirm_action") != "1" {
		http.Redirect(w, r, "/admin/plugins?error=confirm", http.StatusSeeOther)
		return
	}
	id := chi.URLParam(r, "pluginID")
	var err error
	switch action {
	case "enable":
		err = h.registry.Enable(r.Context(), id)
	case "disable":
		err = h.registry.Disable(r.Context(), id)
	case "delete":
		err = h.registry.Remove(r.Context(), id)
	case "restore":
		err = h.registry.Restore(r.Context(), id)
	}
	if err != nil {
		if !errors.Is(err, ErrPluginNotFound) && !errors.Is(err, ErrPluginRemoved) {
			h.logger.ErrorContext(r.Context(), "change plugin state", "plugin", id, "error", err)
		}
		query := url.Values{"error": []string{"change"}}
		if errors.Is(err, ErrProviderConflict) {
			query.Set("error", "conflict")
		} else if errors.Is(err, ErrPluginNotFound) || errors.Is(err, ErrPluginRemoved) {
			query.Set("error", "missing")
		}
		http.Redirect(w, r, "/admin/plugins?"+query.Encode(), http.StatusSeeOther)
		return
	}
	status := map[string]string{"enable": "enabled", "disable": "disabled", "delete": "removed", "restore": "restored"}[action]
	http.Redirect(w, r, "/admin/plugins?notice="+status, http.StatusSeeOther)
}

func pluginFeedback(r *http.Request) (string, string) {
	switch r.URL.Query().Get("notice") {
	case "enabled":
		return "插件已启用。", ""
	case "disabled":
		return "插件已停用。", ""
	case "removed":
		return "插件已删除，配置和数据已保留，可在下方重新添加。", ""
	case "restored":
		return "插件已重新添加，当前为停用状态。", ""
	}
	switch r.URL.Query().Get("error") {
	case "conflict":
		return "", "同类评论插件已经启用，请先停用当前插件。"
	case "missing":
		return "", "插件不存在或已被移除。"
	case "change":
		return "", "插件状态更新失败，请稍后重试。"
	case "confirm":
		return "", "请先勾选确认删除插件。"
	}
	return "", ""
}
func (h *HTTPHandler) internalError(w http.ResponseWriter, r *http.Request, err error) {
	h.logger.ErrorContext(r.Context(), "plugin admin request failed", "error", err)
	http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
}
