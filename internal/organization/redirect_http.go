package organization

import (
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	adminweb "github.com/zhushilin/blog-project/web/admin"
)

type RedirectHTTPHandler struct {
	service   *Service
	security  AdminSecurity
	logger    *slog.Logger
	templates *template.Template
}

func NewRedirectHTTPHandler(service *Service, security AdminSecurity, logger *slog.Logger) (*RedirectHTTPHandler, error) {
	templates, err := template.ParseFS(adminweb.Files, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse redirect templates: %w", err)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &RedirectHTTPHandler{service: service, security: security, logger: logger, templates: templates}, nil
}

func (h *RedirectHTTPHandler) RegisterAdmin(router chi.Router) {
	router.Get("/redirects", h.index)
	router.Post("/redirects", h.create)
	router.Post("/redirects/{redirectID}/delete", h.delete)
}

func (h *RedirectHTTPHandler) index(w http.ResponseWriter, r *http.Request) {
	items, err := h.service.Redirects(r.Context(), 200)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := h.templates.ExecuteTemplate(w, "redirects.html", map[string]any{"Redirects": items, "CSRF": h.security.CSRFToken(r)}); err != nil {
		h.logger.ErrorContext(r.Context(), "render redirects", "error", err)
	}
}

func (h *RedirectHTTPHandler) create(w http.ResponseWriter, r *http.Request) {
	if !h.parse(w, r) {
		return
	}
	status, _ := strconv.Atoi(r.FormValue("status_code"))
	err := h.service.CreateRedirect(r.Context(), RedirectInput{SourcePath: r.FormValue("source_path"), TargetPath: r.FormValue("target_path"), StatusCode: status})
	if err == nil {
		http.Redirect(w, r, "/admin/redirects", http.StatusSeeOther)
		return
	}
	if errors.Is(err, ErrSlugUnavailable) || IsUserFacing(err) {
		h.renderError(w, r, err)
		return
	}
	h.internalError(w, r, err)
}

func (h *RedirectHTTPHandler) delete(w http.ResponseWriter, r *http.Request) {
	if !h.parse(w, r) {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "redirectID"), 10, 64)
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	if err := h.service.DeleteRedirect(r.Context(), id); err != nil && !errors.Is(err, ErrNotFound) {
		h.internalError(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/redirects", http.StatusSeeOther)
}

func (h *RedirectHTTPHandler) parse(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid redirect form", http.StatusBadRequest)
		return false
	}
	return h.security.VerifyParsedCSRF(w, r)
}
func (h *RedirectHTTPHandler) renderError(w http.ResponseWriter, r *http.Request, err error) {
	h.logger.InfoContext(r.Context(), "redirect validation failed", "error", err)
	items, _ := h.service.Redirects(r.Context(), 200)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusUnprocessableEntity)
	_ = h.templates.ExecuteTemplate(w, "redirects.html", map[string]any{"Redirects": items, "Error": err.Error(), "CSRF": h.security.CSRFToken(r)})
}
func (h *RedirectHTTPHandler) internalError(w http.ResponseWriter, r *http.Request, err error) {
	h.logger.ErrorContext(r.Context(), "redirect request failed", "error", err)
	http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
}
