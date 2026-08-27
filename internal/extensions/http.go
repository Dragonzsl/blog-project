package extensions

import (
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"

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
	if err := h.templates.ExecuteTemplate(w, "plugins.html", map[string]any{"Plugins": states, "CSRF": h.security.CSRFToken(r)}); err != nil {
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
	if err != nil && !errors.Is(err, ErrPluginNotFound) {
		h.logger.ErrorContext(r.Context(), "change plugin state", "plugin", id, "error", err)
	}
	http.Redirect(w, r, "/admin/plugins", http.StatusSeeOther)
}
func (h *HTTPHandler) internalError(w http.ResponseWriter, r *http.Request, err error) {
	h.logger.ErrorContext(r.Context(), "plugin admin request failed", "error", err)
	http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
}
