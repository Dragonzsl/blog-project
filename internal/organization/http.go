package organization

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"

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
type ContentOptions interface {
	NavigationContentOptions(context.Context) ([]ContentOption, error)
}

type HTTPHandler struct {
	service   *Service
	content   ContentOptions
	security  AdminSecurity
	siteNamer SiteNamer
	logger    *slog.Logger
	templates *template.Template
}

func NewHTTPHandler(service *Service, content ContentOptions, security AdminSecurity, siteNamer SiteNamer, logger *slog.Logger) (*HTTPHandler, error) {
	templates, err := template.ParseFS(adminweb.Files, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse organization templates: %w", err)
	}
	return &HTTPHandler{service: service, content: content, security: security, siteNamer: siteNamer, logger: logger, templates: templates}, nil
}

func (h *HTTPHandler) RegisterAdmin(router chi.Router) {
	router.Get("/organization", h.index)
	router.Post("/organization/categories", h.createCategory)
	router.Post("/organization/categories/{categoryID}", h.updateCategory)
	router.Post("/organization/categories/{categoryID}/delete", h.deleteCategory)
	router.Post("/organization/tags", h.createTag)
	router.Post("/organization/tags/{tagID}", h.updateTag)
	router.Post("/organization/tags/{tagID}/delete", h.deleteTag)
	router.Post("/organization/navigation", h.createNavigation)
	router.Post("/organization/navigation/{navigationID}", h.updateNavigation)
	router.Post("/organization/navigation/{navigationID}/delete", h.deleteNavigation)
}

func (h *HTTPHandler) index(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, "", http.StatusOK)
}

func (h *HTTPHandler) createCategory(w http.ResponseWriter, r *http.Request) {
	if !h.parse(w, r) {
		return
	}
	_, err := h.service.CreateCategory(r.Context(), termInput(r))
	h.finish(w, r, err)
}
func (h *HTTPHandler) updateCategory(w http.ResponseWriter, r *http.Request) {
	if !h.parse(w, r) {
		return
	}
	id, ok := routeID(w, r, "categoryID")
	if !ok {
		return
	}
	_, err := h.service.UpdateCategory(r.Context(), id, termInput(r))
	h.finish(w, r, err)
}
func (h *HTTPHandler) deleteCategory(w http.ResponseWriter, r *http.Request) {
	if !h.parse(w, r) {
		return
	}
	id, ok := routeID(w, r, "categoryID")
	if !ok {
		return
	}
	h.finish(w, r, h.service.DeleteCategory(r.Context(), id))
}
func (h *HTTPHandler) createTag(w http.ResponseWriter, r *http.Request) {
	if !h.parse(w, r) {
		return
	}
	_, err := h.service.CreateTag(r.Context(), termInput(r))
	h.finish(w, r, err)
}
func (h *HTTPHandler) updateTag(w http.ResponseWriter, r *http.Request) {
	if !h.parse(w, r) {
		return
	}
	id, ok := routeID(w, r, "tagID")
	if !ok {
		return
	}
	_, err := h.service.UpdateTag(r.Context(), id, termInput(r))
	h.finish(w, r, err)
}
func (h *HTTPHandler) deleteTag(w http.ResponseWriter, r *http.Request) {
	if !h.parse(w, r) {
		return
	}
	id, ok := routeID(w, r, "tagID")
	if !ok {
		return
	}
	h.finish(w, r, h.service.DeleteTag(r.Context(), id))
}

func (h *HTTPHandler) createNavigation(w http.ResponseWriter, r *http.Request) {
	if !h.parse(w, r) {
		return
	}
	input, err := navigationInput(r)
	if err == nil {
		_, err = h.service.CreateNavigationItem(r.Context(), input)
	}
	h.finish(w, r, err)
}
func (h *HTTPHandler) updateNavigation(w http.ResponseWriter, r *http.Request) {
	if !h.parse(w, r) {
		return
	}
	id, ok := routeID(w, r, "navigationID")
	if !ok {
		return
	}
	input, err := navigationInput(r)
	if err == nil {
		_, err = h.service.UpdateNavigationItem(r.Context(), id, input)
	}
	h.finish(w, r, err)
}
func (h *HTTPHandler) deleteNavigation(w http.ResponseWriter, r *http.Request) {
	if !h.parse(w, r) {
		return
	}
	id, ok := routeID(w, r, "navigationID")
	if !ok {
		return
	}
	h.finish(w, r, h.service.DeleteNavigationItem(r.Context(), id))
}

func (h *HTTPHandler) finish(w http.ResponseWriter, r *http.Request, err error) {
	if err == nil {
		http.Redirect(w, r, "/admin/organization", http.StatusSeeOther)
		return
	}
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if IsUserFacing(err) {
		h.render(w, r, userMessage(err), http.StatusUnprocessableEntity)
		return
	}
	h.internalError(w, r, err)
}

func (h *HTTPHandler) render(w http.ResponseWriter, r *http.Request, message string, status int) {
	categories, err := h.service.Categories(r.Context())
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	tags, err := h.service.Tags(r.Context())
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	navigation, err := h.service.NavigationItems(r.Context())
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	content, err := h.content.NavigationContentOptions(r.Context())
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	siteName, err := h.siteNamer.SiteName(r.Context())
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	topLevel := make([]NavigationItem, 0, len(navigation))
	for _, item := range navigation {
		if item.ParentID == 0 {
			topLevel = append(topLevel, item)
		}
	}
	data := map[string]any{"SiteName": siteName, "CSRF": h.security.CSRFToken(r), "Categories": categories, "Tags": tags, "Navigation": navigation, "TopLevel": topLevel, "ContentOptions": content, "Error": message, "AdminSection": "organization"}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := h.templates.ExecuteTemplate(w, "organization.html", data); err != nil {
		h.logger.ErrorContext(r.Context(), "render organization template", "error", err)
	}
}

func (h *HTTPHandler) parse(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid organization form", http.StatusBadRequest)
		return false
	}
	return h.security.VerifyParsedCSRF(w, r)
}
func termInput(r *http.Request) TermInput {
	sortOrder, _ := strconv.Atoi(r.FormValue("sort_order"))
	return TermInput{Name: r.FormValue("name"), Slug: r.FormValue("slug"), Description: r.FormValue("description"), SortOrder: sortOrder}
}
func navigationInput(r *http.Request) (NavigationInput, error) {
	targetID, err := optionalInt(r.FormValue("target_id"))
	if err != nil {
		return NavigationInput{}, ValidationError{Message: "导航目标无效"}
	}
	parentID, err := optionalInt(r.FormValue("parent_id"))
	if err != nil {
		return NavigationInput{}, ValidationError{Message: "父级导航无效"}
	}
	sortOrder, err := strconv.Atoi(r.FormValue("sort_order"))
	if err != nil && r.FormValue("sort_order") != "" {
		return NavigationInput{}, ValidationError{Message: "导航顺序必须是整数"}
	}
	return NavigationInput{Location: r.FormValue("location"), ParentID: parentID, Label: r.FormValue("label"), TargetKind: r.FormValue("target_kind"), TargetID: targetID, ExternalURL: r.FormValue("external_url"), SortOrder: sortOrder}, nil
}
func optionalInt(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id < 1 {
		return 0, ErrInvalidTarget
	}
	return id, nil
}
func routeID(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, name), 10, 64)
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return 0, false
	}
	return id, true
}
func userMessage(err error) string {
	var validation ValidationError
	if errors.As(err, &validation) {
		return validation.Message
	}
	if errors.Is(err, ErrSlugUnavailable) {
		return "这个 Slug 已被使用。"
	}
	if errors.Is(err, ErrInvalidTarget) {
		return "导航目标不存在或层级无效。"
	}
	return err.Error()
}
func (h *HTTPHandler) internalError(w http.ResponseWriter, r *http.Request, err error) {
	h.logger.ErrorContext(r.Context(), "organization request failed", "error", err)
	http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
}
