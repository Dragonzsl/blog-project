package media

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	adminweb "github.com/zhushilin/blog-project/web/admin"
)

type AdminSecurity interface {
	CSRFToken(*http.Request) string
	VerifyParsedCSRF(http.ResponseWriter, *http.Request) bool
}
type SiteNamer interface {
	SiteName(context.Context) (string, error)
}

type HTTPHandler struct {
	service   *Service
	security  AdminSecurity
	siteNamer SiteNamer
	logger    *slog.Logger
	templates *template.Template
}

func NewHTTPHandler(service *Service, security AdminSecurity, siteNamer SiteNamer, logger *slog.Logger) (*HTTPHandler, error) {
	templates, err := template.ParseFS(adminweb.Files, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse media templates: %w", err)
	}
	return &HTTPHandler{service: service, security: security, siteNamer: siteNamer, logger: logger, templates: templates}, nil
}

func (h *HTTPHandler) RegisterPublic(router chi.Router) {
	router.Get("/media/{mediaID}/{variant}/{filename}", h.asset)
	router.Head("/media/{mediaID}/{variant}/{filename}", h.asset)
}
func (h *HTTPHandler) RegisterAdmin(router chi.Router) {
	router.Get("/media", h.index)
	router.Post("/media", h.upload)
	router.Post("/media/{mediaRowID}/delete", h.delete)
}

func (h *HTTPHandler) index(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, "", http.StatusOK)
}
func (h *HTTPHandler) upload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, h.service.MaxUploadBytes()+(1<<20))
	if err := r.ParseMultipartForm(512 << 10); err != nil {
		http.Error(w, "Invalid upload", http.StatusBadRequest)
		return
	}
	defer r.MultipartForm.RemoveAll()
	if !h.security.VerifyParsedCSRF(w, r) {
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		h.render(w, r, "请选择要上传的文件。", http.StatusUnprocessableEntity)
		return
	}
	defer file.Close()
	if _, err := h.service.Upload(r.Context(), header.Filename, r.FormValue("alt_text"), file); err != nil {
		var validation ValidationError
		if errors.As(err, &validation) {
			h.render(w, r, validation.Message, http.StatusUnprocessableEntity)
			return
		}
		h.internalError(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/media", http.StatusSeeOther)
}
func (h *HTTPHandler) delete(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid media action", http.StatusBadRequest)
		return
	}
	if !h.security.VerifyParsedCSRF(w, r) {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "mediaRowID"), 10, 64)
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	err = h.service.Delete(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if errors.Is(err, ErrInUse) {
		h.render(w, r, "该媒体仍被文章或页面引用，解除引用后才能删除。", http.StatusConflict)
		return
	}
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/media", http.StatusSeeOther)
}

func (h *HTTPHandler) asset(w http.ResponseWriter, r *http.Request) {
	asset, err := h.service.Asset(r.Context(), chi.URLParam(r, "mediaID"), chi.URLParam(r, "variant"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	file, err := h.service.Open(asset)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	etag := `"` + hex.EncodeToString(asset.ContentHash[:16]) + `"`
	w.Header().Set("Content-Type", asset.MIMEType)
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("ETag", etag)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	if matchesETag(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if !strings.HasPrefix(asset.MIMEType, "image/") && asset.MIMEType != "application/pdf" {
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": asset.Item.OriginalName}))
	}
	http.ServeContent(w, r, asset.Item.OriginalName, asset.CreatedAt, file)
}

func matchesETag(header, etag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || candidate == etag || strings.TrimPrefix(candidate, "W/") == etag {
			return true
		}
	}
	return false
}

func (h *HTTPHandler) render(w http.ResponseWriter, r *http.Request, message string, status int) {
	items, err := h.service.Items(r.Context())
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	views := make([]map[string]any, 0, len(items))
	for _, item := range items {
		originalURL := item.OriginalURL()
		srcset := []string{}
		for _, variant := range item.Variants {
			srcset = append(srcset, fmt.Sprintf("%s %dw", item.VariantURL(variant.Key), variant.Width))
		}
		srcset = append(srcset, fmt.Sprintf("%s %dw", originalURL, item.Width))
		views = append(views, map[string]any{"ID": item.ID, "Name": item.OriginalName, "MIME": item.MIMEType, "Size": humanBytes(item.SizeBytes), "Width": item.Width, "Height": item.Height, "IsImage": strings.HasPrefix(item.MIMEType, "image/"), "URL": originalURL, "SrcSet": strings.Join(srcset, ", "), "Markdown": markdownFor(item, originalURL)})
	}
	siteName, err := h.siteNamer.SiteName(r.Context())
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	data := map[string]any{
		"SiteName":           siteName,
		"CSRF":               h.security.CSRFToken(r),
		"Items":              views,
		"Error":              message,
		"MaxUploadMiB":       fmt.Sprintf("%.1f", float64(h.service.options.MaxUploadBytes)/(1<<20)),
		"MaxImageMegapixels": fmt.Sprintf("%.1f", float64(h.service.options.MaxImagePixels)/1_000_000),
		"JPEGQuality":        h.service.options.JPEGQuality,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := h.templates.ExecuteTemplate(w, "media.html", data); err != nil {
		h.logger.ErrorContext(r.Context(), "render media template", "error", err)
	}
}
func markdownFor(item Item, url string) string {
	if strings.HasPrefix(item.MIMEType, "image/") {
		alt := item.AltText
		if alt == "" {
			alt = item.OriginalName
		}
		return "![" + strings.ReplaceAll(alt, "]", "\\]") + "](" + url + ")"
	}
	return "[" + strings.ReplaceAll(item.OriginalName, "]", "\\]") + "](" + url + ")"
}
func humanBytes(value int64) string {
	if value >= 1<<20 {
		return fmt.Sprintf("%.1f MiB", float64(value)/(1<<20))
	}
	if value >= 1<<10 {
		return fmt.Sprintf("%.1f KiB", float64(value)/(1<<10))
	}
	return fmt.Sprintf("%d B", value)
}
func (h *HTTPHandler) internalError(w http.ResponseWriter, r *http.Request, err error) {
	h.logger.ErrorContext(r.Context(), "media request failed", "error", err)
	http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
}
