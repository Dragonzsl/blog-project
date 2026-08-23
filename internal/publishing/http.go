package publishing

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/zhushilin/blog-project/internal/organization"
	adminweb "github.com/zhushilin/blog-project/web/admin"
)

const maxContentFormBytes = (2 << 20) + (96 << 10)

type AdminSecurity interface {
	CSRFToken(*http.Request) string
	VerifyParsedCSRF(http.ResponseWriter, *http.Request) bool
}

type SiteNamer interface {
	SiteName(context.Context) (string, error)
	Timezone(context.Context) (string, error)
}

type HTTPHandler struct {
	service   *Service
	security  AdminSecurity
	siteNamer SiteNamer
	logger    *slog.Logger
	templates *template.Template
}

type contentDescriptor struct {
	Kind            string
	Singular        string
	Plural          string
	ListURL         string
	CreateURL       string
	PermalinkPrefix string
}

func describeContent(kind string) contentDescriptor {
	if kind == "page" {
		return contentDescriptor{Kind: "page", Singular: "页面", Plural: "页面", ListURL: "/admin/pages", CreateURL: "/admin/pages/new", PermalinkPrefix: "/"}
	}
	return contentDescriptor{Kind: "article", Singular: "文章", Plural: "文章", ListURL: "/admin/articles", CreateURL: "/admin/articles/new", PermalinkPrefix: "/posts/"}
}

func NewHTTPHandler(service *Service, security AdminSecurity, siteNamer SiteNamer, logger *slog.Logger) (*HTTPHandler, error) {
	templates, err := template.ParseFS(adminweb.Files, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse publishing templates: %w", err)
	}
	return &HTTPHandler{service: service, security: security, siteNamer: siteNamer, logger: logger, templates: templates}, nil
}

func (h *HTTPHandler) RegisterAdmin(router chi.Router) {
	router.Get("/trash", h.trashList)
	router.Post("/trash/{contentID}/restore", h.trashRestore)
	for _, kind := range []string{"article", "page"} {
		descriptor := describeContent(kind)
		kind := kind
		router.Get(descriptor.ListURL[len("/admin"):], func(w http.ResponseWriter, r *http.Request) { h.contentList(w, r, kind) })
		router.Get(descriptor.CreateURL[len("/admin"):], func(w http.ResponseWriter, r *http.Request) { h.contentNew(w, r, kind) })
		base := descriptor.ListURL[len("/admin"):]
		router.Post(base, func(w http.ResponseWriter, r *http.Request) { h.contentCreate(w, r, kind) })
		parameter := "articleID"
		if kind == "page" {
			parameter = "pageID"
		}
		router.Get(base+"/{"+parameter+"}/edit", func(w http.ResponseWriter, r *http.Request) { h.contentEdit(w, r, kind) })
		router.Post(base+"/{"+parameter+"}", func(w http.ResponseWriter, r *http.Request) { h.contentUpdate(w, r, kind) })
		router.Post(base+"/{"+parameter+"}/publish", func(w http.ResponseWriter, r *http.Request) { h.contentPublish(w, r, kind) })
		router.Post(base+"/{"+parameter+"}/schedule", func(w http.ResponseWriter, r *http.Request) { h.contentSchedule(w, r, kind) })
		router.Post(base+"/{"+parameter+"}/schedule/cancel", func(w http.ResponseWriter, r *http.Request) { h.contentCancelSchedule(w, r, kind) })
		router.Post(base+"/{"+parameter+"}/unpublish", func(w http.ResponseWriter, r *http.Request) { h.contentUnpublish(w, r, kind) })
		router.Post(base+"/{"+parameter+"}/trash", func(w http.ResponseWriter, r *http.Request) { h.contentTrash(w, r, kind) })
		router.Get(base+"/{"+parameter+"}/versions", func(w http.ResponseWriter, r *http.Request) { h.contentVersions(w, r, kind) })
		router.Get(base+"/{"+parameter+"}/versions/{revisionID}", func(w http.ResponseWriter, r *http.Request) { h.contentVersion(w, r, kind) })
		router.Post(base+"/{"+parameter+"}/versions/{revisionID}/restore", func(w http.ResponseWriter, r *http.Request) { h.contentRestoreRevision(w, r, kind) })
		router.Post(base+"/{"+parameter+"}/snapshot", func(w http.ResponseWriter, r *http.Request) { h.contentSaveSnapshot(w, r, kind) })
	}
}

func (h *HTTPHandler) contentList(w http.ResponseWriter, r *http.Request, kind string) {
	contents, err := h.contents(r.Context(), kind)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	descriptor := describeContent(kind)
	views := make([]map[string]any, 0, len(contents))
	for _, content := range contents {
		views = append(views, map[string]any{
			"ID":        content.ID,
			"Title":     content.Title,
			"Slug":      content.Slug,
			"Status":    statusLabel(content.Status),
			"Published": content.Status == "published",
			"UpdatedAt": content.UpdatedAt.Format("2006-01-02 15:04 UTC"),
			"EditURL":   fmt.Sprintf("%s/%d/edit", descriptor.ListURL, content.ID),
		})
	}
	h.renderAdmin(w, r, "contents.html", map[string]any{"Contents": views, "Descriptor": descriptor})
}

func (h *HTTPHandler) contentNew(w http.ResponseWriter, r *http.Request, kind string) {
	h.renderEditor(w, r, kind, Article{Kind: kind, LockVersion: 1}, "", http.StatusOK)
}

func (h *HTTPHandler) contentCreate(w http.ResponseWriter, r *http.Request, kind string) {
	input, ok := h.parseContentForm(w, r, kind)
	if !ok {
		return
	}
	content, err := h.createDraft(r.Context(), kind, input)
	if err != nil {
		if !isUserFacingError(err) {
			h.internalError(w, r, err)
			return
		}
		h.renderEditor(w, r, kind, postedContent(kind, 0, 1, input), userMessage(err), statusForPublishingError(err))
		return
	}
	h.redirectToEditor(w, r, kind, content.ID)
}

func (h *HTTPHandler) contentEdit(w http.ResponseWriter, r *http.Request, kind string) {
	content, ok := h.loadContent(w, r, kind)
	if !ok {
		return
	}
	message := ""
	if r.URL.Query().Get("snapshot") == "load" {
		snapshot, err := h.service.EditingSnapshot(r.Context(), kind, content.ID)
		if err == nil && snapshot.BaseLockVersion == content.LockVersion {
			loaded := postedContent(kind, content.ID, content.LockVersion, snapshot.Input)
			loaded.Status = content.Status
			loaded.PublishedRevisionID = content.PublishedRevisionID
			loaded.PublishedAt = content.PublishedAt
			loaded.ScheduledAt = content.ScheduledAt
			content = loaded
			message = "已载入最近编辑快照；确认内容后请正式保存。"
		} else if errors.Is(err, ErrNotFound) || (err == nil && snapshot.BaseLockVersion != content.LockVersion) {
			h.redirectToEditor(w, r, kind, content.ID)
			return
		} else if err != nil && !errors.Is(err, ErrNotFound) {
			h.internalError(w, r, err)
			return
		}
	}
	h.renderEditor(w, r, kind, content, message, http.StatusOK)
}

func (h *HTTPHandler) contentUpdate(w http.ResponseWriter, r *http.Request, kind string) {
	input, ok := h.parseContentForm(w, r, kind)
	if !ok {
		return
	}
	id, err := contentID(r, kind)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	version, err := strconv.ParseInt(r.FormValue("lock_version"), 10, 64)
	if err != nil || version < 1 {
		http.Error(w, "Invalid editor version", http.StatusBadRequest)
		return
	}
	_, err = h.updateDraft(r.Context(), kind, id, version, input)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if !isUserFacingError(err) {
			h.internalError(w, r, err)
			return
		}
		h.renderEditor(w, r, kind, postedContent(kind, id, version, input), userMessage(err), statusForPublishingError(err))
		return
	}
	h.redirectToEditor(w, r, kind, id)
}

func (h *HTTPHandler) contentPublish(w http.ResponseWriter, r *http.Request, kind string) {
	if !h.parseActionForm(w, r) {
		return
	}
	id, err := contentID(r, kind)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	version, err := strconv.ParseInt(r.FormValue("lock_version"), 10, 64)
	if err != nil || version < 1 {
		http.Error(w, "Invalid editor version", http.StatusBadRequest)
		return
	}
	content, err := h.publish(r.Context(), kind, id, version)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if !isUserFacingError(err) {
			h.internalError(w, r, err)
			return
		}
		current, loadErr := h.content(r.Context(), kind, id)
		if loadErr != nil {
			h.handleReadError(w, r, loadErr)
			return
		}
		h.renderEditor(w, r, kind, current, userMessage(err), statusForPublishingError(err))
		return
	}
	http.Redirect(w, r, publicURL(content), http.StatusSeeOther)
}

func (h *HTTPHandler) contentSchedule(w http.ResponseWriter, r *http.Request, kind string) {
	if !h.parseActionForm(w, r) {
		return
	}
	id, version, ok := lifecycleTarget(w, r, kind)
	if !ok {
		return
	}
	timezone, err := h.siteNamer.Timezone(r.Context())
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		h.internalError(w, r, fmt.Errorf("load site timezone: %w", err))
		return
	}
	publishAt, err := time.ParseInLocation("2006-01-02T15:04", r.FormValue("scheduled_at"), location)
	if err != nil {
		h.renderLifecycleError(w, r, kind, id, ValidationError{Message: "请选择有效的定时发布时间"})
		return
	}
	if _, err := h.service.Schedule(r.Context(), kind, id, version, publishAt); err != nil {
		h.renderLifecycleError(w, r, kind, id, err)
		return
	}
	h.redirectToEditor(w, r, kind, id)
}

func (h *HTTPHandler) contentUnpublish(w http.ResponseWriter, r *http.Request, kind string) {
	if !h.parseActionForm(w, r) {
		return
	}
	id, version, ok := lifecycleTarget(w, r, kind)
	if !ok {
		return
	}
	if _, err := h.service.Unpublish(r.Context(), kind, id, version); err != nil {
		h.renderLifecycleError(w, r, kind, id, err)
		return
	}
	h.redirectToEditor(w, r, kind, id)
}

func (h *HTTPHandler) contentCancelSchedule(w http.ResponseWriter, r *http.Request, kind string) {
	if !h.parseActionForm(w, r) {
		return
	}
	id, version, ok := lifecycleTarget(w, r, kind)
	if !ok {
		return
	}
	if _, err := h.service.CancelSchedule(r.Context(), kind, id, version); err != nil {
		h.renderLifecycleError(w, r, kind, id, err)
		return
	}
	h.redirectToEditor(w, r, kind, id)
}

func (h *HTTPHandler) contentTrash(w http.ResponseWriter, r *http.Request, kind string) {
	if !h.parseActionForm(w, r) {
		return
	}
	id, version, ok := lifecycleTarget(w, r, kind)
	if !ok {
		return
	}
	if err := h.service.Trash(r.Context(), kind, id, version); err != nil {
		h.renderLifecycleError(w, r, kind, id, err)
		return
	}
	http.Redirect(w, r, "/admin/trash", http.StatusSeeOther)
}

func (h *HTTPHandler) contentVersions(w http.ResponseWriter, r *http.Request, kind string) {
	content, ok := h.loadContent(w, r, kind)
	if !ok {
		return
	}
	revisions, err := h.service.Revisions(r.Context(), kind, content.ID)
	if err != nil {
		h.handleReadError(w, r, err)
		return
	}
	views := make([]map[string]any, 0, len(revisions))
	for _, revision := range revisions {
		views = append(views, map[string]any{
			"ID": revision.ID, "Number": revision.Number, "Title": revision.Title,
			"Slug": revision.Slug, "Excerpt": revision.Excerpt, "Body": revision.BodyMarkdown,
			"Reason": revisionReasonLabel(revision.Reason), "Checkpoint": revision.IsPublicationCheckpoint,
			"Current": revision.ID == content.CurrentRevisionID, "CreatedAt": revision.CreatedAt.Format("2006-01-02 15:04 UTC"),
		})
	}
	h.renderAdmin(w, r, "versions.html", map[string]any{"Content": content, "Revisions": views, "Descriptor": describeContent(kind)})
}

func (h *HTTPHandler) contentRestoreRevision(w http.ResponseWriter, r *http.Request, kind string) {
	if !h.parseActionForm(w, r) {
		return
	}
	id, version, ok := lifecycleTarget(w, r, kind)
	if !ok {
		return
	}
	revisionID, err := strconv.ParseInt(chi.URLParam(r, "revisionID"), 10, 64)
	if err != nil || revisionID < 1 {
		http.NotFound(w, r)
		return
	}
	if _, err := h.service.RestoreRevision(r.Context(), kind, id, revisionID, version); err != nil {
		h.renderLifecycleError(w, r, kind, id, err)
		return
	}
	h.redirectToEditor(w, r, kind, id)
}

func (h *HTTPHandler) contentVersion(w http.ResponseWriter, r *http.Request, kind string) {
	content, ok := h.loadContent(w, r, kind)
	if !ok {
		return
	}
	revisionID, err := strconv.ParseInt(chi.URLParam(r, "revisionID"), 10, 64)
	if err != nil || revisionID < 1 {
		http.NotFound(w, r)
		return
	}
	revision, err := h.service.Revision(r.Context(), kind, content.ID, revisionID)
	if err != nil {
		h.handleReadError(w, r, err)
		return
	}
	h.renderAdmin(w, r, "version.html", map[string]any{
		"Content": content, "Revision": revision, "Descriptor": describeContent(kind),
		"Reason": revisionReasonLabel(revision.Reason), "Current": revision.ID == content.CurrentRevisionID,
		"CreatedAt": revision.CreatedAt.Format("2006-01-02 15:04 UTC"),
	})
}

func (h *HTTPHandler) contentSaveSnapshot(w http.ResponseWriter, r *http.Request, kind string) {
	input, ok := h.parseContentForm(w, r, kind)
	if !ok {
		return
	}
	id, err := contentID(r, kind)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	baseVersion, err := strconv.ParseInt(r.FormValue("lock_version"), 10, 64)
	if err != nil || baseVersion < 1 {
		http.Error(w, "Invalid editor version", http.StatusBadRequest)
		return
	}
	browserVersion, err := strconv.ParseInt(r.FormValue("browser_version"), 10, 64)
	if err != nil || browserVersion < 1 {
		http.Error(w, "Invalid snapshot version", http.StatusBadRequest)
		return
	}
	err = h.service.SaveEditingSnapshot(r.Context(), kind, EditingSnapshot{ContentID: id, BaseLockVersion: baseVersion, BrowserVersion: browserVersion, Input: input})
	if errors.Is(err, ErrConflict) {
		http.Error(w, http.StatusText(http.StatusConflict), http.StatusConflict)
		return
	}
	if err != nil {
		if isUserFacingError(err) {
			http.Error(w, userMessage(err), http.StatusUnprocessableEntity)
			return
		}
		h.internalError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (h *HTTPHandler) trashList(w http.ResponseWriter, r *http.Request) {
	contents, err := h.service.TrashedContents(r.Context())
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	views := make([]map[string]any, 0, len(contents))
	for _, content := range contents {
		views = append(views, map[string]any{"ID": content.ID, "Title": content.Title, "Kind": describeContent(content.Kind).Singular, "TrashedAt": content.TrashedAt.Format("2006-01-02 15:04 UTC")})
	}
	h.renderAdmin(w, r, "trash.html", map[string]any{"Contents": views})
}

func (h *HTTPHandler) trashRestore(w http.ResponseWriter, r *http.Request) {
	if !h.parseActionForm(w, r) {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "contentID"), 10, 64)
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	content, err := h.service.RestoreFromTrash(r.Context(), id)
	if err != nil {
		h.handleReadError(w, r, err)
		return
	}
	h.redirectToEditor(w, r, content.Kind, content.ID)
}

func (h *HTTPHandler) renderLifecycleError(w http.ResponseWriter, r *http.Request, kind string, id int64, err error) {
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if !isUserFacingError(err) {
		h.internalError(w, r, err)
		return
	}
	content, loadErr := h.content(r.Context(), kind, id)
	if loadErr != nil {
		h.handleReadError(w, r, loadErr)
		return
	}
	h.renderEditor(w, r, kind, content, userMessage(err), statusForPublishingError(err))
}

func lifecycleTarget(w http.ResponseWriter, r *http.Request, kind string) (int64, int64, bool) {
	id, err := contentID(r, kind)
	if err != nil {
		http.NotFound(w, r)
		return 0, 0, false
	}
	version, err := strconv.ParseInt(r.FormValue("lock_version"), 10, 64)
	if err != nil || version < 1 {
		http.Error(w, "Invalid editor version", http.StatusBadRequest)
		return 0, 0, false
	}
	return id, version, true
}

func (h *HTTPHandler) loadContent(w http.ResponseWriter, r *http.Request, kind string) (Article, bool) {
	id, err := contentID(r, kind)
	if err != nil {
		http.NotFound(w, r)
		return Article{}, false
	}
	content, err := h.content(r.Context(), kind, id)
	if err != nil {
		h.handleReadError(w, r, err)
		return Article{}, false
	}
	return content, true
}

func (h *HTTPHandler) parseContentForm(w http.ResponseWriter, r *http.Request, kind string) (DraftInput, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxContentFormBytes)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid content form", http.StatusBadRequest)
		return DraftInput{}, false
	}
	if !h.security.VerifyParsedCSRF(w, r) {
		return DraftInput{}, false
	}
	input := DraftInput{Title: r.FormValue("title"), Slug: r.FormValue("slug"), Excerpt: r.FormValue("excerpt"), BodyMarkdown: r.FormValue("body_markdown")}
	if kind == "article" {
		if value := r.FormValue("category_id"); value != "" {
			categoryID, err := strconv.ParseInt(value, 10, 64)
			if err != nil || categoryID < 1 {
				http.Error(w, "Invalid category", http.StatusBadRequest)
				return DraftInput{}, false
			}
			input.CategoryID = categoryID
		}
		for _, value := range r.Form["tag_ids"] {
			tagID, err := strconv.ParseInt(value, 10, 64)
			if err != nil || tagID < 1 {
				http.Error(w, "Invalid tag", http.StatusBadRequest)
				return DraftInput{}, false
			}
			input.TagIDs = append(input.TagIDs, tagID)
		}
	}
	return input, true
}

func (h *HTTPHandler) parseActionForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid action form", http.StatusBadRequest)
		return false
	}
	return h.security.VerifyParsedCSRF(w, r)
}

func (h *HTTPHandler) renderEditor(w http.ResponseWriter, r *http.Request, kind string, content Article, errorMessage string, status int) {
	descriptor := describeContent(kind)
	action := descriptor.ListURL
	if content.ID > 0 {
		action = fmt.Sprintf("%s/%d", descriptor.ListURL, content.ID)
	}
	selectedTags := make(map[int64]bool)
	for _, tag := range content.Tags {
		selectedTags[tag.ID] = true
	}
	for _, tagID := range contentTagIDs(content) {
		selectedTags[tagID] = true
	}
	data := map[string]any{
		"Content":                      content,
		"Descriptor":                   descriptor,
		"Action":                       action,
		"IsNew":                        content.ID == 0,
		"Published":                    content.Status == "published",
		"Scheduled":                    content.Status == "scheduled",
		"EverPublished":                content.PublishedRevisionID > 0,
		"Status":                       statusLabel(content.Status),
		"Error":                        errorMessage,
		"PublicURL":                    publicURL(content),
		"SelectedTags":                 selectedTags,
		"SnapshotIntervalMilliseconds": h.service.SnapshotInterval().Milliseconds(),
		"SnapshotIntervalSeconds":      int(h.service.SnapshotInterval().Seconds()),
		"SnapshotLoaded":               r.URL.Query().Get("snapshot") == "load",
	}
	var siteLocation *time.Location
	if content.ID > 0 {
		timezone, err := h.siteNamer.Timezone(r.Context())
		if err != nil {
			h.internalError(w, r, err)
			return
		}
		siteLocation, err = time.LoadLocation(timezone)
		if err != nil {
			h.internalError(w, r, err)
			return
		}
		data["Timezone"] = timezone
	}
	if content.ScheduledAt != nil {
		data["ScheduledAt"] = content.ScheduledAt.In(siteLocation).Format("2006-01-02T15:04")
		data["ScheduledAtDisplay"] = content.ScheduledAt.In(siteLocation).Format("2006-01-02 15:04 MST")
	}
	if content.ID > 0 {
		snapshot, err := h.service.EditingSnapshot(r.Context(), kind, content.ID)
		if err == nil && snapshot.BaseLockVersion == content.LockVersion {
			data["SnapshotAvailable"] = true
			data["SnapshotUpdatedAt"] = snapshot.UpdatedAt.Format("2006-01-02 15:04 UTC")
		} else if err != nil && !errors.Is(err, ErrNotFound) {
			h.internalError(w, r, err)
			return
		}
	}
	if kind == "article" {
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
		data["Categories"] = categories
		data["Tags"] = tags
	}
	h.renderAdminWithStatus(w, r, "content_edit.html", data, status)
}

func contentTagIDs(content Article) []int64 {
	ids := make([]int64, 0, len(content.Tags))
	for _, tag := range content.Tags {
		ids = append(ids, tag.ID)
	}
	return ids
}

func postedContent(kind string, id, version int64, input DraftInput) Article {
	content := Article{ID: id, Kind: kind, Title: input.Title, Slug: input.Slug, Excerpt: input.Excerpt, BodyMarkdown: input.BodyMarkdown, LockVersion: version}
	if input.CategoryID > 0 {
		content.Category = &organization.Category{ID: input.CategoryID}
	}
	for _, tagID := range input.TagIDs {
		content.Tags = append(content.Tags, organization.Tag{ID: tagID})
	}
	return content
}

func (h *HTTPHandler) createDraft(ctx context.Context, kind string, input DraftInput) (Article, error) {
	if kind == "page" {
		return h.service.CreatePageDraft(ctx, input)
	}
	return h.service.CreateDraft(ctx, input)
}
func (h *HTTPHandler) updateDraft(ctx context.Context, kind string, id, version int64, input DraftInput) (Article, error) {
	if kind == "page" {
		return h.service.UpdatePageDraft(ctx, id, version, input)
	}
	return h.service.UpdateDraft(ctx, id, version, input)
}
func (h *HTTPHandler) publish(ctx context.Context, kind string, id, version int64) (Article, error) {
	if kind == "page" {
		return h.service.PublishPage(ctx, id, version)
	}
	return h.service.Publish(ctx, id, version)
}
func (h *HTTPHandler) content(ctx context.Context, kind string, id int64) (Article, error) {
	if kind == "page" {
		return h.service.Page(ctx, id)
	}
	return h.service.Article(ctx, id)
}
func (h *HTTPHandler) contents(ctx context.Context, kind string) ([]Article, error) {
	if kind == "page" {
		return h.service.Pages(ctx)
	}
	return h.service.Articles(ctx)
}

func (h *HTTPHandler) redirectToEditor(w http.ResponseWriter, r *http.Request, kind string, id int64) {
	http.Redirect(w, r, fmt.Sprintf("%s/%d/edit", describeContent(kind).ListURL, id), http.StatusSeeOther)
}
func publicURL(content Article) string {
	if content.Kind == "page" {
		return "/" + content.Slug
	}
	return "/posts/" + content.Slug
}

func (h *HTTPHandler) renderAdmin(w http.ResponseWriter, r *http.Request, name string, data map[string]any) {
	h.renderAdminWithStatus(w, r, name, data, http.StatusOK)
}
func (h *HTTPHandler) renderAdminWithStatus(w http.ResponseWriter, r *http.Request, name string, data map[string]any, status int) {
	siteName, err := h.siteNamer.SiteName(r.Context())
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	data["SiteName"] = siteName
	data["CSRF"] = h.security.CSRFToken(r)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := h.templates.ExecuteTemplate(w, name, data); err != nil {
		h.logger.ErrorContext(r.Context(), "render publishing template", "template", name, "error", err)
	}
}
func (h *HTTPHandler) handleReadError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	h.internalError(w, r, err)
}
func (h *HTTPHandler) internalError(w http.ResponseWriter, r *http.Request, err error) {
	h.logger.ErrorContext(r.Context(), "publishing request failed", "error", err)
	http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
}

func contentID(r *http.Request, kind string) (int64, error) {
	parameter := "articleID"
	if kind == "page" {
		parameter = "pageID"
	}
	id, err := strconv.ParseInt(chi.URLParam(r, parameter), 10, 64)
	if err != nil || id < 1 {
		return 0, ErrNotFound
	}
	return id, nil
}
func userMessage(err error) string {
	var validation ValidationError
	if errors.As(err, &validation) {
		return validation.Message
	}
	switch {
	case errors.Is(err, ErrConflict):
		return "内容已在其他页面被修改，请刷新后合并更改。"
	case errors.Is(err, ErrSlugUnavailable):
		return "这个固定链接已经被使用或保留。"
	case errors.Is(err, ErrPublishedSlugImmutable):
		return "已发布内容的固定链接不可直接修改。"
	case errors.Is(err, ErrInvalidTransition):
		return "当前状态不支持这项操作，请刷新后重试。"
	default:
		return err.Error()
	}
}
func isUserFacingError(err error) bool {
	var validation ValidationError
	return errors.As(err, &validation) || errors.Is(err, ErrConflict) || errors.Is(err, ErrSlugUnavailable) || errors.Is(err, ErrPublishedSlugImmutable) || errors.Is(err, ErrInvalidTransition)
}

func revisionReasonLabel(reason string) string {
	switch reason {
	case "create":
		return "创建"
	case "restore":
		return "恢复版本"
	case "import":
		return "导入"
	default:
		return "正式保存"
	}
}
func statusForPublishingError(err error) int {
	if errors.Is(err, ErrConflict) {
		return http.StatusConflict
	}
	return http.StatusUnprocessableEntity
}
func statusLabel(status string) string {
	switch status {
	case "published":
		return "已发布"
	case "scheduled":
		return "待定时发布"
	default:
		return "草稿"
	}
}
