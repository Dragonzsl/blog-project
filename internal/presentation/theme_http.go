package presentation

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"time"

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
	router.Get("/themes/{themeID}/{version}/preview", h.preview)
	router.Get("/themes/{themeID}/{version}/preview-image", h.previewImage)
	router.Post("/themes/rollback", h.rollback)
}

func (h *ThemeHTTPHandler) index(w http.ResponseWriter, r *http.Request) {
	records, err := h.catalog.List(r.Context())
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	views := themeViews(records)
	siteName, err := h.siteNamer.SiteName(r.Context())
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	notice, message := themeFeedback(r)
	h.render(w, r, map[string]any{"SiteName": siteName, "CSRF": h.security.CSRFToken(r), "Themes": views, "Fallback": h.manager.IsFallback(), "ActiveID": h.manager.Current().ThemeID(), "ActiveVersion": h.manager.Current().ThemeVersion(), "MaxBytes": h.options.MaxBytes / (1 << 20), "AdminSection": "themes", "Notice": notice, "Error": message})
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
	h.redirect(w, r, "uploaded")
}

func (h *ThemeHTTPHandler) activate(w http.ResponseWriter, r *http.Request) {
	if !h.verifyAction(w, r) {
		return
	}
	if err := h.catalog.Activate(r.Context(), chi.URLParam(r, "themeID"), chi.URLParam(r, "version")); err != nil {
		h.renderError(w, r, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	h.redirect(w, r, "activated")
}

func (h *ThemeHTTPHandler) rollback(w http.ResponseWriter, r *http.Request) {
	if !h.verifyAction(w, r) {
		return
	}
	if err := h.catalog.Rollback(r.Context()); err != nil {
		h.internalError(w, r, err)
		return
	}
	h.redirect(w, r, "rollback")
}

func (h *ThemeHTTPHandler) preview(w http.ResponseWriter, r *http.Request) {
	record, err := h.catalog.Resolve(chi.URLParam(r, "themeID"), chi.URLParam(r, "version"))
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	theme, err := NewThemeFromDirectory(record.Path, record.Manifest)
	if err != nil {
		h.renderError(w, r, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	siteName, err := h.siteNamer.SiteName(r.Context())
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	body, err := theme.RenderHomePage(siteName, nil, Navigation{}, PageMetadata{NoIndex: true})
	if err != nil {
		h.renderError(w, r, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	setPublicSecurityHeaders(w, body)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

func (h *ThemeHTTPHandler) previewImage(w http.ResponseWriter, r *http.Request) {
	record, err := h.catalog.Resolve(chi.URLParam(r, "themeID"), chi.URLParam(r, "version"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	file, name, err := openThemePreview(record.Path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", mime.TypeByExtension(filepath.Ext(name)))
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, name, time.Time{}, file)
}

func openThemePreview(root string) (*os.File, string, error) {
	for _, name := range []string{"preview.webp", "preview.png", "preview.jpg", "preview.jpeg"} {
		path := filepath.Join(root, name)
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		info, statErr := file.Stat()
		if statErr == nil && info.Mode().IsRegular() {
			return file, name, nil
		}
		_ = file.Close()
	}
	return nil, "", os.ErrNotExist
}

func (h *ThemeHTTPHandler) verifyAction(w http.ResponseWriter, r *http.Request) bool {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid theme action", http.StatusBadRequest)
		return false
	}
	return h.security.VerifyParsedCSRF(w, r)
}

func (h *ThemeHTTPHandler) render(w http.ResponseWriter, r *http.Request, data map[string]any, statuses ...int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	status := http.StatusOK
	if len(statuses) > 0 {
		status = statuses[0]
	}
	w.WriteHeader(status)
	if err := h.templates.ExecuteTemplate(w, "themes.html", data); err != nil {
		h.logger.ErrorContext(r.Context(), "render themes", "error", err)
	}
}

func (h *ThemeHTTPHandler) renderError(w http.ResponseWriter, r *http.Request, message string, status int) {
	records, _ := h.catalog.List(r.Context())
	views := themeViews(records)
	siteName, _ := h.siteNamer.SiteName(r.Context())
	h.render(w, r, map[string]any{"SiteName": siteName, "CSRF": h.security.CSRFToken(r), "Themes": views, "Error": message, "Fallback": h.manager.IsFallback(), "MaxBytes": h.options.MaxBytes / (1 << 20), "AdminSection": "themes"}, status)
}

func themeViews(records []ThemeRecord) []map[string]any {
	views := make([]map[string]any, 0, len(records))
	for _, record := range records {
		file, _, err := openThemePreview(record.Path)
		if err == nil {
			_ = file.Close()
		}
		preview := "/admin/themes/" + record.Manifest.ID + "/" + record.Manifest.Version + "/preview-image"
		views = append(views, map[string]any{"ID": record.Manifest.ID, "Name": record.Manifest.Name, "Version": record.Manifest.Version, "Active": record.Active, "Valid": record.ValidationStatus == "valid", "Path": filepath.Base(record.Path), "PreviewURL": preview, "PreviewAvailable": err == nil})
	}
	return views
}

func (h *ThemeHTTPHandler) redirect(w http.ResponseWriter, r *http.Request, notice string) {
	http.Redirect(w, r, "/admin/themes?notice="+notice, http.StatusSeeOther)
}

func themeFeedback(r *http.Request) (string, string) {
	switch r.URL.Query().Get("notice") {
	case "uploaded":
		return "主题已上传并通过验证。", ""
	case "activated":
		return "主题已启用。", ""
	case "rollback":
		return "已回退到默认主题。", ""
	}
	return "", ""
}
func (h *ThemeHTTPHandler) internalError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	h.logger.ErrorContext(r.Context(), "theme request failed", "error", err)
	http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
}
