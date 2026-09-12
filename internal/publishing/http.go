package publishing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/zhushilin/blog-project/internal/media"
	"github.com/zhushilin/blog-project/internal/organization"
	"github.com/zhushilin/blog-project/internal/platform/pagination"
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
	media     MediaPicker
}

type MediaPicker interface {
	Items(context.Context) ([]media.Item, error)
}

type adminMediaOption struct {
	ID    int64
	Label string
	URL   string
}

type contentDescriptor struct {
	Kind            string
	Singular        string
	Plural          string
	ListURL         string
	CreateURL       string
	PermalinkPrefix string
}

type adminContentStatusTab struct {
	Key     string
	Label   string
	URL     string
	Current bool
}

type adminContentView struct {
	ID             int64
	LockVersion    int64
	Title          string
	Slug           string
	Excerpt        string
	Status         string
	StatusKey      string
	Published      bool
	UpdatedAt      string
	PublishedAt    string
	EditURL        string
	PreviewURL     string
	VersionsURL    string
	PublicURL      string
	HasPublicURL   bool
	CategoryName   string
	Tags           []string
	WordCount      int
	ReadingMinutes int
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

func (h *HTTPHandler) SetMediaPicker(picker MediaPicker) { h.media = picker }

func (h *HTTPHandler) RegisterAdmin(router chi.Router) {
	router.Get("/trash", h.trashList)
	router.Post("/trash/{contentID}/restore", h.trashRestore)
	router.Post("/trash/bulk", h.trashBulkRestore)
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
		router.Get(base+"/{"+parameter+"}/versions/compare", func(w http.ResponseWriter, r *http.Request) { h.contentCompare(w, r, kind) })
		router.Get(base+"/{"+parameter+"}/versions/{revisionID}", func(w http.ResponseWriter, r *http.Request) { h.contentVersion(w, r, kind) })
		router.Get(base+"/{"+parameter+"}/versions/{revisionID}/restore", func(w http.ResponseWriter, r *http.Request) { h.contentRestorePreview(w, r, kind) })
		router.Post(base+"/{"+parameter+"}/versions/{revisionID}/restore", func(w http.ResponseWriter, r *http.Request) { h.contentRestoreRevision(w, r, kind) })
		router.Post(base+"/{"+parameter+"}/snapshot", func(w http.ResponseWriter, r *http.Request) { h.contentSaveSnapshot(w, r, kind) })
		router.Post(base+"/bulk", func(w http.ResponseWriter, r *http.Request) { h.contentBulk(w, r, kind) })
		router.Get(base+"/calendar", func(w http.ResponseWriter, r *http.Request) { h.scheduleCalendar(w, r, kind) })
	}
}

func (h *HTTPHandler) contentList(w http.ResponseWriter, r *http.Request, kind string) {
	descriptor := describeContent(kind)
	filter, request := parseAdminContentRequest(r)
	page, err := h.service.AdminContentsPage(r.Context(), kind, filter, request)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	views := make([]adminContentView, 0, len(page.Contents))
	for _, content := range page.Contents {
		views = append(views, newAdminContentView(descriptor, content))
	}
	data := map[string]any{
		"Contents":         views,
		"Descriptor":       descriptor,
		"Pagination":       page.Pagination,
		"Filter":           filter,
		"StatusTabs":       adminContentStatusTabs(descriptor, filter),
		"AdminSection":     kind + "s",
		"PreviousURL":      adminContentURL(descriptor.ListURL, filter, filter.Status, page.Pagination.Page-1),
		"NextURL":          adminContentURL(descriptor.ListURL, filter, filter.Status, page.Pagination.Page+1),
		"BulkOperationKey": newBulkOperationKey(),
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
	h.renderAdmin(w, r, "contents.html", data)
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
			loaded.PublishedSlug = content.PublishedSlug
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
	if !requireAdminConfirmation(w, r) {
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
	http.Redirect(w, r, "/admin/trash#trash-results", http.StatusSeeOther)
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
	h.renderAdmin(w, r, "versions.html", map[string]any{"Content": content, "Revisions": views, "Descriptor": describeContent(kind), "AdminSection": kind + "s"})
}

func (h *HTTPHandler) contentCompare(w http.ResponseWriter, r *http.Request, kind string) {
	id, err := contentID(r, kind)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	fromID, _ := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
	toID, _ := strconv.ParseInt(r.URL.Query().Get("to"), 10, 64)
	if fromID < 1 || toID < 1 || fromID == toID {
		http.Error(w, "请选择两个不同的版本", http.StatusBadRequest)
		return
	}
	content, err := h.content(r.Context(), kind, id)
	if err != nil {
		h.handleReadError(w, r, err)
		return
	}
	comparison, err := h.service.CompareRevisions(r.Context(), kind, id, fromID, toID)
	if err != nil {
		if errors.Is(err, ErrDiffTooLarge) {
			http.Error(w, "版本正文过大，暂不生成比较结果", http.StatusRequestEntityTooLarge)
			return
		}
		h.handleReadError(w, r, err)
		return
	}
	h.renderAdmin(w, r, "revision_compare.html", map[string]any{"Content": content, "Comparison": comparison, "Descriptor": describeContent(kind), "AdminSection": kind + "s"})
}

func (h *HTTPHandler) contentRestorePreview(w http.ResponseWriter, r *http.Request, kind string) {
	id, err := contentID(r, kind)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	revisionID, err := strconv.ParseInt(chi.URLParam(r, "revisionID"), 10, 64)
	if err != nil || revisionID < 1 {
		http.NotFound(w, r)
		return
	}
	content, err := h.content(r.Context(), kind, id)
	if err != nil {
		h.handleReadError(w, r, err)
		return
	}
	revision, err := h.service.Revision(r.Context(), kind, id, revisionID)
	if err != nil {
		h.handleReadError(w, r, err)
		return
	}
	snapshot, snapshotErr := h.service.EditingSnapshot(r.Context(), kind, id)
	if snapshotErr != nil && !errors.Is(snapshotErr, ErrNotFound) {
		h.handleReadError(w, r, snapshotErr)
		return
	}
	h.renderAdmin(w, r, "restore_preview.html", map[string]any{"Content": content, "Revision": revision, "Snapshot": snapshot, "HasSnapshot": snapshotErr == nil, "Descriptor": describeContent(kind), "AdminSection": kind + "s"})
}

func (h *HTTPHandler) contentBulk(w http.ResponseWriter, r *http.Request, kind string) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil || !h.security.VerifyParsedCSRF(w, r) {
		return
	}
	if !requireAdminConfirmation(w, r) {
		return
	}
	ids := make([]int64, 0, len(r.Form["content_ids"]))
	for _, value := range r.Form["content_ids"] {
		if id, err := strconv.ParseInt(value, 10, 64); err == nil && id > 0 {
			ids = append(ids, id)
		}
	}
	expectedVersions, err := parseExpectedVersions(r.Form["content_versions"])
	if err != nil {
		http.Error(w, "Invalid content version", http.StatusBadRequest)
		return
	}
	action := strings.TrimSpace(r.FormValue("bulk_action"))
	var scheduleAt *time.Time
	if action == "schedule" {
		timezone, err := h.siteNamer.Timezone(r.Context())
		if err != nil {
			h.internalError(w, r, err)
			return
		}
		location, err := time.LoadLocation(timezone)
		if err != nil {
			http.Error(w, "站点时区无效", http.StatusBadRequest)
			return
		}
		value, err := time.ParseInLocation("2006-01-02T15:04", r.FormValue("scheduled_at"), location)
		if err != nil || !value.After(time.Now().In(location)) {
			http.Error(w, "定时发布时间必须是未来的有效时间", http.StatusBadRequest)
			return
		}
		value = value.UTC()
		scheduleAt = &value
	}
	key := strings.TrimSpace(r.FormValue("operation_key"))
	if key == "" {
		raw := make([]byte, 16)
		if _, err := rand.Read(raw); err != nil {
			h.internalError(w, r, err)
			return
		}
		key = "admin-bulk-" + hex.EncodeToString(raw)
	}
	result, err := h.service.ApplyBulk(r.Context(), BulkActionRequest{Kind: kind, Action: action, IDs: ids, ExpectedVersion: expectedVersions, ScheduleAt: scheduleAt, OperationKey: key})
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, ErrBulkInProgress) {
			status = http.StatusConflict
		}
		http.Error(w, err.Error(), status)
		return
	}
	h.renderAdmin(w, r, "bulk_result.html", map[string]any{"Descriptor": describeContent(kind), "Result": result, "AdminSection": kind + "s"})
}

func parseExpectedVersions(values []string) (map[int64]int64, error) {
	result := make(map[int64]int64, len(values))
	for _, raw := range values {
		parts := strings.Split(raw, ":")
		if len(parts) != 2 {
			return nil, ErrBulkInvalid
		}
		id, idErr := strconv.ParseInt(parts[0], 10, 64)
		version, versionErr := strconv.ParseInt(parts[1], 10, 64)
		if idErr != nil || versionErr != nil || id < 1 || version < 1 {
			return nil, ErrBulkInvalid
		}
		result[id] = version
	}
	return result, nil
}

func newBulkOperationKey() string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "admin-bulk-fallback"
	}
	return "admin-bulk-" + hex.EncodeToString(raw)
}

func (h *HTTPHandler) scheduleCalendar(w http.ResponseWriter, r *http.Request, kind string) {
	timezone, err := h.siteNamer.Timezone(r.Context())
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	view := r.URL.Query().Get("view")
	if view != "week" {
		view = "month"
	}
	var start, end time.Time
	if view == "week" {
		start = scheduleWeekStart(r.URL.Query().Get("week"), location)
		end = start.AddDate(0, 0, 7)
	} else {
		start, err = time.ParseInLocation("2006-01", r.URL.Query().Get("month"), location)
		if err != nil {
			now := time.Now().In(location)
			start = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, location)
		}
		end = start.AddDate(0, 1, 0)
	}
	items, hasMore, err := h.service.ScheduledContents(r.Context(), kind, start.UTC(), end.UTC(), 200)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	views := make([]map[string]any, 0, len(items))
	for _, item := range items {
		at := ""
		if item.ScheduledAt != nil {
			at = item.ScheduledAt.In(location).Format("2006-01-02 15:04 MST")
		}
		views = append(views, map[string]any{"ID": item.ID, "Title": item.Title, "ScheduledAt": at, "EditURL": fmt.Sprintf("/admin/%ss/%d/edit", kind, item.ID)})
	}
	previous := start.AddDate(0, 0, -1)
	next := end
	if view == "month" {
		previous = start.AddDate(0, -1, 0)
		next = start.AddDate(0, 1, 0)
	}
	previousValues := url.Values{"view": []string{view}}
	nextValues := url.Values{"view": []string{view}}
	if view == "week" {
		previousValues.Set("week", previous.Format("2006-01-02"))
		nextValues.Set("week", next.Format("2006-01-02"))
	} else {
		previousValues.Set("month", previous.Format("2006-01"))
		nextValues.Set("month", next.Format("2006-01"))
	}
	rangeLabel := start.Format("2006-01")
	if view == "week" {
		rangeLabel = start.Format("2006-01-02") + " ～ " + end.AddDate(0, 0, -1).Format("2006-01-02")
	}
	descriptor := describeContent(kind)
	h.renderAdmin(w, r, "schedule_calendar.html", map[string]any{"Descriptor": descriptor, "Month": start.Format("2006-01"), "RangeLabel": rangeLabel, "View": view, "PreviousURL": descriptor.ListURL + "/calendar?" + previousValues.Encode(), "NextURL": descriptor.ListURL + "/calendar?" + nextValues.Encode(), "Items": views, "HasMore": hasMore, "Timezone": timezone, "AdminSection": kind + "s"})
}

func scheduleWeekStart(raw string, location *time.Location) time.Time {
	value, err := time.ParseInLocation("2006-01-02", raw, location)
	if err != nil {
		now := time.Now().In(location)
		value = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location)
	}
	offset := (int(value.Weekday()) + 6) % 7
	return value.AddDate(0, 0, -offset)
}

func (h *HTTPHandler) contentRestoreRevision(w http.ResponseWriter, r *http.Request, kind string) {
	if !h.parseActionForm(w, r) {
		return
	}
	if !requireAdminConfirmation(w, r) {
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
		"CreatedAt":    revision.CreatedAt.Format("2006-01-02 15:04 UTC"),
		"AdminSection": kind + "s",
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
		views = append(views, map[string]any{"ID": content.ID, "KindKey": content.Kind, "Title": content.Title, "Kind": describeContent(content.Kind).Singular, "LockVersion": content.LockVersion, "TrashedAt": content.TrashedAt.Format("2006-01-02 15:04 UTC")})
	}
	h.renderAdmin(w, r, "trash.html", map[string]any{"Contents": views, "BulkOperationKey": newBulkOperationKey(), "CSRF": h.security.CSRFToken(r), "AdminSection": "trash"})
}

type trashBulkResult struct {
	OperationKey string
	Succeeded    int
	Skipped      int
	Conflicts    int
	Failed       int
	Items        []BulkItemResult
}

func (h *HTTPHandler) trashBulkRestore(w http.ResponseWriter, r *http.Request) {
	if !h.parseActionForm(w, r) {
		return
	}
	if !requireAdminConfirmation(w, r) {
		return
	}
	if single := strings.TrimSpace(r.FormValue("single_item")); single != "" {
		r.Form["trash_items"] = []string{single}
	}
	if len(r.Form["trash_items"]) == 0 || len(r.Form["trash_items"]) > maxBulkItems {
		http.Error(w, "没有选择要恢复的内容", http.StatusBadRequest)
		return
	}
	groups := make(map[string][]int64)
	versions := make(map[string]map[int64]int64)
	seen := make(map[string]struct{})
	for _, raw := range r.Form["trash_items"] {
		parts := strings.Split(raw, ":")
		if len(parts) != 3 || (parts[0] != "article" && parts[0] != "page") {
			http.Error(w, "Invalid trash item", http.StatusBadRequest)
			return
		}
		id, idErr := strconv.ParseInt(parts[1], 10, 64)
		version, versionErr := strconv.ParseInt(parts[2], 10, 64)
		if idErr != nil || versionErr != nil || id < 1 || version < 1 {
			http.Error(w, "Invalid trash item", http.StatusBadRequest)
			return
		}
		key := parts[0] + ":" + parts[1]
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		groups[parts[0]] = append(groups[parts[0]], id)
		if versions[parts[0]] == nil {
			versions[parts[0]] = make(map[int64]int64)
		}
		versions[parts[0]][id] = version
	}
	operationKey := strings.TrimSpace(r.FormValue("operation_key"))
	if operationKey == "" {
		operationKey = newBulkOperationKey()
	}
	combined := trashBulkResult{OperationKey: operationKey}
	for _, kind := range []string{"article", "page"} {
		if len(groups[kind]) == 0 {
			continue
		}
		result, err := h.service.ApplyBulk(r.Context(), BulkActionRequest{Kind: kind, Action: "restore", IDs: groups[kind], ExpectedVersion: versions[kind], OperationKey: operationKey + "-" + kind})
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, ErrBulkInProgress) {
				status = http.StatusConflict
			}
			http.Error(w, err.Error(), status)
			return
		}
		combined.Succeeded += result.Succeeded
		combined.Skipped += result.Skipped
		combined.Conflicts += result.Conflicts
		combined.Failed += result.Failed
		combined.Items = append(combined.Items, result.Items...)
	}
	h.renderAdmin(w, r, "trash_bulk_result.html", map[string]any{"Result": combined, "CSRF": h.security.CSRFToken(r), "AdminSection": "trash"})
}

func (h *HTTPHandler) trashRestore(w http.ResponseWriter, r *http.Request) {
	if !h.parseActionForm(w, r) {
		return
	}
	if !requireAdminConfirmation(w, r) {
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
	input := DraftInput{
		Title: r.FormValue("title"), Slug: r.FormValue("slug"), AutoSlug: r.FormValue("auto_slug") == "1", Excerpt: r.FormValue("excerpt"),
		SEOTitle: r.FormValue("seo_title"), SEODescription: r.FormValue("seo_description"),
		BodyMarkdown: r.FormValue("body_markdown"),
	}
	if value := strings.TrimSpace(r.FormValue("cover_media_id")); value != "" {
		coverMediaID, err := strconv.ParseInt(value, 10, 64)
		if err != nil || coverMediaID < 1 {
			http.Error(w, "Invalid cover media", http.StatusBadRequest)
			return DraftInput{}, false
		}
		input.CoverMediaID = coverMediaID
	}
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

func requireAdminConfirmation(w http.ResponseWriter, r *http.Request) bool {
	if r.FormValue("confirm_action") == "1" {
		return true
	}
	http.Error(w, "请确认此管理操作", http.StatusBadRequest)
	return false
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
		"WordCount":                    editorialWordCount(content.BodyMarkdown),
		"ReadingMinutes":               editorialReadingMinutes(content.BodyMarkdown),
		"AdminSection":                 kind + "s",
		"MediaOptions":                 []adminMediaOption{},
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
	if h.media != nil {
		items, err := h.media.Items(r.Context())
		if err != nil {
			h.internalError(w, r, err)
			return
		}
		options := make([]adminMediaOption, 0, len(items))
		for _, item := range items {
			if item.MIMEType != "image/jpeg" && item.MIMEType != "image/png" {
				continue
			}
			options = append(options, adminMediaOption{ID: item.ID, Label: item.OriginalName, URL: item.OriginalURL()})
		}
		data["MediaOptions"] = options
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
	content := Article{ID: id, Kind: kind, Title: input.Title, Slug: input.Slug, Excerpt: input.Excerpt, SEOTitle: input.SEOTitle, SEODescription: input.SEODescription, BodyMarkdown: input.BodyMarkdown, CoverMediaID: input.CoverMediaID, LockVersion: version}
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

func parseAdminContentRequest(r *http.Request) (AdminContentFilter, pagination.Request) {
	query := r.URL.Query()
	filter := AdminContentFilter{
		Query:    strings.TrimSpace(query.Get("q")),
		Status:   normalizeAdminContentStatus(query.Get("status")),
		Category: strings.TrimSpace(query.Get("category")),
		Tag:      strings.TrimSpace(query.Get("tag")),
		Sort:     normalizeAdminContentSort(query.Get("sort")),
	}
	page, err := strconv.Atoi(query.Get("page"))
	if err != nil || page < 1 {
		page = 1
	}
	return filter, pagination.Request{Page: page, PerPage: 20}
}

func adminContentStatusTabs(descriptor contentDescriptor, filter AdminContentFilter) []adminContentStatusTab {
	tabs := []adminContentStatusTab{
		{Key: "all", Label: "全部", Current: filter.Status == "all"},
		{Key: "draft", Label: "草稿", Current: filter.Status == "draft"},
		{Key: "scheduled", Label: "定时发布", Current: filter.Status == "scheduled"},
		{Key: "published", Label: "已发布", Current: filter.Status == "published"},
	}
	for index := range tabs {
		tabs[index].URL = adminContentURL(descriptor.ListURL, filter, tabs[index].Key, 1)
	}
	return tabs
}

func adminContentURL(base string, filter AdminContentFilter, status string, page int) string {
	values := url.Values{}
	if filter.Query != "" {
		values.Set("q", filter.Query)
	}
	if status != "" && status != "all" {
		values.Set("status", status)
	}
	if filter.Category != "" {
		values.Set("category", filter.Category)
	}
	if filter.Tag != "" {
		values.Set("tag", filter.Tag)
	}
	if filter.Sort != "" && filter.Sort != "updated" {
		values.Set("sort", filter.Sort)
	}
	if page > 1 {
		values.Set("page", strconv.Itoa(page))
	}
	if encoded := values.Encode(); encoded != "" {
		return base + "?" + encoded
	}
	return base
}

func newAdminContentView(descriptor contentDescriptor, content Article) adminContentView {
	view := adminContentView{
		ID:             content.ID,
		LockVersion:    content.LockVersion,
		Title:          content.Title,
		Slug:           content.Slug,
		Excerpt:        content.Excerpt,
		Status:         statusLabel(content.Status),
		StatusKey:      content.Status,
		Published:      content.Status == "published",
		UpdatedAt:      content.UpdatedAt.Format("2006-01-02 15:04 UTC"),
		EditURL:        fmt.Sprintf("%s/%d/edit", descriptor.ListURL, content.ID),
		PreviewURL:     fmt.Sprintf("%s/%d/preview", descriptor.ListURL, content.ID),
		VersionsURL:    fmt.Sprintf("%s/%d/versions", descriptor.ListURL, content.ID),
		WordCount:      editorialWordCount(content.BodyMarkdown),
		ReadingMinutes: editorialReadingMinutes(content.BodyMarkdown),
	}
	if content.Category != nil {
		view.CategoryName = content.Category.Name
	}
	for _, tag := range content.Tags {
		view.Tags = append(view.Tags, tag.Name)
	}
	if content.PublishedAt != nil {
		view.PublishedAt = content.PublishedAt.Format("2006-01-02 15:04 UTC")
	}
	if view.Published {
		view.PublicURL = publicURL(content)
		view.HasPublicURL = content.Slug != ""
	}
	return view
}

func editorialWordCount(markdown string) int {
	return utf8.RuneCountInString(strings.TrimSpace(markdown))
}

func editorialReadingMinutes(markdown string) int {
	count := editorialWordCount(markdown)
	if count == 0 {
		return 0
	}
	return (count + 399) / 400
}

func (h *HTTPHandler) redirectToEditor(w http.ResponseWriter, r *http.Request, kind string, id int64) {
	http.Redirect(w, r, fmt.Sprintf("%s/%d/edit#content-form", describeContent(kind).ListURL, id), http.StatusSeeOther)
}
func publicURL(content Article) string {
	slug := content.PublishedSlug
	if slug == "" {
		slug = content.Slug
	}
	if content.Kind == "page" {
		return "/" + slug
	}
	return "/posts/" + slug
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
	case errors.Is(err, ErrInvalidTransition):
		return "当前状态不支持这项操作，请刷新后重试。"
	default:
		return err.Error()
	}
}
func isUserFacingError(err error) bool {
	var validation ValidationError
	return errors.As(err, &validation) || errors.Is(err, ErrConflict) || errors.Is(err, ErrSlugUnavailable) || errors.Is(err, ErrInvalidTransition)
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
