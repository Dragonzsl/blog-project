package presentation

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"path/filepath"

	"github.com/go-chi/chi/v5"
	adminweb "github.com/zhushilin/blog-project/web/admin"
)

type ThemeAdminSecurity interface {
	CSRFToken(*http.Request) string
	VerifyParsedCSRF(http.ResponseWriter, *http.Request) bool
}

type ThemeHTTPHandler struct {
	catalog   *ThemeCatalog
	manager   *ThemeManager
	security  ThemeAdminSecurity
	siteNamer interface {
		SiteName(context.Context) (string, error)
	}
	logger    *slog.Logger
	templates *template.Template
	options   ThemeInstallOptions
}

func NewThemeHTTPHandler(catalog *ThemeCatalog, manager *ThemeManager, security ThemeAdminSecurity, siteNamer interface {
	SiteName(context.Context) (string, error)
}, logger *slog.Logger, options ThemeInstallOptions) (*ThemeHTTPHandler, error) {
	templates, err := template.ParseFS(adminweb.Files, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse theme templates: %w", err)
	}
	return &ThemeHTTPHandler{catalog: catalog, manager: manager, security: security, siteNamer: siteNamer, logger: logger, templates: templates, options: options}, nil
}

func (h *ThemeHTTPHandler) RegisterAdmin(router chi.Router) {
	router.Get("/themes", h.index)
	router.Post("/themes/upload", h.upload)
	router.Post("/themes/{themeID}/{version}/activate", h.activate)
	router.Post("/themes/rollback", h.rollback)
}

func (h *ThemeHTTPHandler) index(w http.ResponseWriter, r *http.Request) {
	records, err := h.catalog.List(r.Context())
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	views := make([]map[string]any, 0, len(records))
	for _, record := range records {
		views = append(views, map[string]any{"ID": record.Manifest.ID, "Name": record.Manifest.Name, "Version": record.Manifest.Version, "Active": record.Active, "Valid": record.ValidationStatus == "valid", "Path": filepath.Base(record.Path)})
	}
	siteName, err := h.siteNamer.SiteName(r.Context())
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	h.render(w, r, map[string]any{"SiteName": siteName, "CSRF": h.security.CSRFToken(r), "Themes": views, "Fallback": h.manager.IsFallback(), "ActiveID": h.manager.Current().ThemeID(), "ActiveVersion": h.manager.Current().ThemeVersion(), "MaxBytes": h.options.MaxBytes / (1 << 20)})
}

func (h *ThemeHTTPHandler) upload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, h.options.MaxBytes+1<<20)
	if err := r.ParseMultipartForm(512 << 10); err != nil {
		http.Error(w, "Invalid theme upload", http.StatusBadRequest)
		return
	}
	defer r.MultipartForm.RemoveAll()
	if !h.security.VerifyParsedCSRF(w, r) {
		return
	}
	file, _, err := r.FormFile("package")
	if err != nil {
		h.renderError(w, r, "请选择主题 ZIP 包。", http.StatusUnprocessableEntity)
		return
	}
	defer file.Close()
	if _, err := h.catalog.Install(r.Context(), file, h.options); err != nil {
		h.renderError(w, r, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	h.redirect(w, r)
}

func (h *ThemeHTTPHandler) activate(w http.ResponseWriter, r *http.Request) {
	if !h.verifyAction(w, r) {
		return
	}
	if err := h.catalog.Activate(r.Context(), chi.URLParam(r, "themeID"), chi.URLParam(r, "version")); err != nil {
		h.renderError(w, r, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	h.redirect(w, r)
}

func (h *ThemeHTTPHandler) rollback(w http.ResponseWriter, r *http.Request) {
	if !h.verifyAction(w, r) {
		return
	}
	if err := h.catalog.Rollback(r.Context()); err != nil {
		h.internalError(w, r, err)
		return
	}
	h.redirect(w, r)
}

func (h *ThemeHTTPHandler) verifyAction(w http.ResponseWriter, r *http.Request) bool {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid theme action", http.StatusBadRequest)
		return false
	}
	return h.security.VerifyParsedCSRF(w, r)
}

func (h *ThemeHTTPHandler) render(w http.ResponseWriter, r *http.Request, data map[string]any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := h.templates.ExecuteTemplate(w, "themes.html", data); err != nil {
		h.logger.ErrorContext(r.Context(), "render themes", "error", err)
	}
}

func (h *ThemeHTTPHandler) renderError(w http.ResponseWriter, r *http.Request, message string, status int) {
	records, _ := h.catalog.List(r.Context())
	views := make([]map[string]any, 0, len(records))
	for _, record := range records {
		views = append(views, map[string]any{"ID": record.Manifest.ID, "Name": record.Manifest.Name, "Version": record.Manifest.Version, "Active": record.Active, "Valid": record.ValidationStatus == "valid"})
	}
	siteName, _ := h.siteNamer.SiteName(r.Context())
	w.WriteHeader(status)
	h.render(w, r, map[string]any{"SiteName": siteName, "CSRF": h.security.CSRFToken(r), "Themes": views, "Error": message, "Fallback": h.manager.IsFallback(), "MaxBytes": h.options.MaxBytes / (1 << 20)})
}

func (h *ThemeHTTPHandler) redirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/admin/themes", http.StatusSeeOther)
}
func (h *ThemeHTTPHandler) internalError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	h.logger.ErrorContext(r.Context(), "theme request failed", "error", err)
	http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
}
