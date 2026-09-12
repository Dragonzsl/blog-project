package publishing

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

type fakeAdminSecurity struct{}

func (fakeAdminSecurity) CSRFToken(*http.Request) string { return "test-csrf" }
func (fakeAdminSecurity) VerifyParsedCSRF(http.ResponseWriter, *http.Request) bool {
	return true
}

type fakeSiteNamer struct{}

func (fakeSiteNamer) SiteName(context.Context) (string, error) { return "纸上花园", nil }
func (fakeSiteNamer) Timezone(context.Context) (string, error) { return "Asia/Shanghai", nil }

func TestHTTPArticleCreatePreviewAndPublish(t *testing.T) {
	service, _ := newPublishingTestService(t)
	service.now = func() time.Time { return time.Date(2026, time.August, 23, 8, 0, 0, 0, time.UTC) }
	handler, err := NewHTTPHandler(
		service,
		fakeAdminSecurity{},
		fakeSiteNamer{},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Route("/admin", handler.RegisterAdmin)

	create := formRequest(http.MethodPost, "/admin/articles", url.Values{
		"csrf_token":    {"test-csrf"},
		"title":         {"安全发布"},
		"slug":          {"safe-publish"},
		"excerpt":       {"摘要"},
		"body_markdown": {"# Hello\n\n<script>alert(1)</script>"},
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, create)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/admin/articles/1/edit#content-form" {
		t.Fatalf("create status=%d location=%q body=%s", response.Code, response.Header().Get("Location"), response.Body.String())
	}

	publish := httptest.NewRecorder()
	router.ServeHTTP(publish, formRequest(http.MethodPost, "/admin/articles/1/publish", url.Values{
		"csrf_token":   {"test-csrf"},
		"lock_version": {"1"},
	}))
	if publish.Code != http.StatusSeeOther || publish.Header().Get("Location") != "/posts/safe-publish" {
		t.Fatalf("publish status=%d location=%q body=%s", publish.Code, publish.Header().Get("Location"), publish.Body.String())
	}

	pageCreate := httptest.NewRecorder()
	router.ServeHTTP(pageCreate, formRequest(http.MethodPost, "/admin/pages", url.Values{"csrf_token": {"test-csrf"}, "title": {"关于"}, "slug": {"about"}, "body_markdown": {"关于页面"}}))
	if pageCreate.Code != http.StatusSeeOther || pageCreate.Header().Get("Location") != "/admin/pages/2/edit#content-form" {
		t.Fatalf("page create status=%d location=%q", pageCreate.Code, pageCreate.Header().Get("Location"))
	}
	pagePublish := httptest.NewRecorder()
	router.ServeHTTP(pagePublish, formRequest(http.MethodPost, "/admin/pages/2/publish", url.Values{"csrf_token": {"test-csrf"}, "lock_version": {"1"}}))
	if pagePublish.Code != http.StatusSeeOther || pagePublish.Header().Get("Location") != "/about" {
		t.Fatalf("page publish status=%d location=%q", pagePublish.Code, pagePublish.Header().Get("Location"))
	}
	articleEditor := httptest.NewRecorder()
	router.ServeHTTP(articleEditor, httptest.NewRequest(http.MethodGet, "/admin/articles/new", nil))
	if !strings.Contains(articleEditor.Body.String(), "分类") || !strings.Contains(articleEditor.Body.String(), "标签") {
		t.Fatalf("article editor missing taxonomy: %s", articleEditor.Body.String())
	}
	pageEditor := httptest.NewRecorder()
	router.ServeHTTP(pageEditor, httptest.NewRequest(http.MethodGet, "/admin/pages/new", nil))
	if strings.Contains(pageEditor.Body.String(), "tag_ids") {
		t.Fatal("page editor exposed article tags")
	}
	articleList := httptest.NewRecorder()
	router.ServeHTTP(articleList, httptest.NewRequest(http.MethodGet, "/admin/articles?status=published&q=%E5%AE%89%E5%85%A8", nil))
	if articleList.Code != http.StatusOK || !strings.Contains(articleList.Body.String(), "内容状态") || !strings.Contains(articleList.Body.String(), "安全发布") || strings.Contains(articleList.Body.String(), "关于") {
		t.Fatalf("article list status=%d body=%s", articleList.Code, articleList.Body.String())
	}
	if !strings.Contains(articleEditor.Body.String(), "editor-rail") || !strings.Contains(articleEditor.Body.String(), "editor-panel") || !strings.Contains(articleEditor.Body.String(), "字数") {
		t.Fatalf("editor workspace missing phase three structure: %s", articleEditor.Body.String())
	}

	versions := httptest.NewRecorder()
	router.ServeHTTP(versions, httptest.NewRequest(http.MethodGet, "/admin/articles/1/versions", nil))
	if versions.Code != http.StatusOK || !strings.Contains(versions.Body.String(), "发布检查点") || !strings.Contains(versions.Body.String(), "安全发布") {
		t.Fatalf("versions status=%d body=%s", versions.Code, versions.Body.String())
	}
	versionDetail := httptest.NewRecorder()
	router.ServeHTTP(versionDetail, httptest.NewRequest(http.MethodGet, "/admin/articles/1/versions/1", nil))
	if versionDetail.Code != http.StatusOK || !strings.Contains(versionDetail.Body.String(), "# Hello") {
		t.Fatalf("version detail status=%d body=%s", versionDetail.Code, versionDetail.Body.String())
	}
	snapshot := httptest.NewRecorder()
	router.ServeHTTP(snapshot, formRequest(http.MethodPost, "/admin/articles/1/snapshot", url.Values{"csrf_token": {"test-csrf"}, "lock_version": {"2"}, "browser_version": {"100"}, "title": {"断线编辑"}, "slug": {"safe-publish"}, "body_markdown": {"尚未正式保存"}}))
	if snapshot.Code != http.StatusNoContent || snapshot.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("snapshot status=%d body=%s", snapshot.Code, snapshot.Body.String())
	}
	editWithSnapshot := httptest.NewRecorder()
	router.ServeHTTP(editWithSnapshot, httptest.NewRequest(http.MethodGet, "/admin/articles/1/edit", nil))
	if !strings.Contains(editWithSnapshot.Body.String(), "检测到") || !strings.Contains(editWithSnapshot.Body.String(), "editor.js") {
		t.Fatalf("editor snapshot recovery missing: %s", editWithSnapshot.Body.String())
	}
	loadedSnapshot := httptest.NewRecorder()
	router.ServeHTTP(loadedSnapshot, httptest.NewRequest(http.MethodGet, "/admin/articles/1/edit?snapshot=load", nil))
	if loadedSnapshot.Code != http.StatusOK || !strings.Contains(loadedSnapshot.Body.String(), "尚未正式保存") || !strings.Contains(loadedSnapshot.Body.String(), "先正式保存") || strings.Contains(loadedSnapshot.Body.String(), `action="/admin/articles/1/publish"`) {
		t.Fatalf("loaded snapshot status=%d body=%s", loadedSnapshot.Code, loadedSnapshot.Body.String())
	}
	unpublish := httptest.NewRecorder()
	router.ServeHTTP(unpublish, formRequest(http.MethodPost, "/admin/articles/1/unpublish", url.Values{"csrf_token": {"test-csrf"}, "lock_version": {"2"}}))
	if unpublish.Code != http.StatusSeeOther {
		t.Fatalf("unpublish status=%d body=%s", unpublish.Code, unpublish.Body.String())
	}
	schedule := httptest.NewRecorder()
	router.ServeHTTP(schedule, formRequest(http.MethodPost, "/admin/articles/1/schedule", url.Values{"csrf_token": {"test-csrf"}, "lock_version": {"3"}, "scheduled_at": {"2026-08-23T18:00"}}))
	if schedule.Code != http.StatusSeeOther {
		t.Fatalf("schedule status=%d body=%s", schedule.Code, schedule.Body.String())
	}
	scheduledEditor := httptest.NewRecorder()
	router.ServeHTTP(scheduledEditor, httptest.NewRequest(http.MethodGet, "/admin/articles/1/edit", nil))
	if !strings.Contains(scheduledEditor.Body.String(), "取消定时，保留草稿") || !strings.Contains(scheduledEditor.Body.String(), "Asia/Shanghai") {
		t.Fatalf("scheduled editor body=%s", scheduledEditor.Body.String())
	}
	trash := httptest.NewRecorder()
	router.ServeHTTP(trash, formRequest(http.MethodPost, "/admin/articles/1/trash", url.Values{"csrf_token": {"test-csrf"}, "lock_version": {"4"}, "confirm_action": {"1"}}))
	if trash.Code != http.StatusSeeOther || trash.Header().Get("Location") != "/admin/trash#trash-results" {
		t.Fatalf("trash status=%d location=%q", trash.Code, trash.Header().Get("Location"))
	}
	trashPage := httptest.NewRecorder()
	router.ServeHTTP(trashPage, httptest.NewRequest(http.MethodGet, "/admin/trash", nil))
	if trashPage.Code != http.StatusOK || !strings.Contains(trashPage.Body.String(), "安全发布") {
		t.Fatalf("trash page status=%d body=%s", trashPage.Code, trashPage.Body.String())
	}
	restoreTrash := httptest.NewRecorder()
	router.ServeHTTP(restoreTrash, formRequest(http.MethodPost, "/admin/trash/1/restore", url.Values{"csrf_token": {"test-csrf"}, "confirm_action": {"1"}}))
	if restoreTrash.Code != http.StatusSeeOther || restoreTrash.Header().Get("Location") != "/admin/articles/1/edit#content-form" {
		t.Fatalf("restore trash status=%d location=%q", restoreTrash.Code, restoreTrash.Header().Get("Location"))
	}

}

func TestHighImpactPublishingActionsRequireExplicitConfirmation(t *testing.T) {
	ctx := context.Background()
	service, _ := newPublishingTestService(t)
	draft, err := service.CreateDraft(ctx, DraftInput{Title: "确认保护", Slug: "confirmation-guard", BodyMarkdown: "第一版"})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := service.UpdateDraft(ctx, draft.ID, draft.LockVersion, DraftInput{Title: "确认保护", Slug: draft.Slug, BodyMarkdown: "第二版"})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHTTPHandler(service, fakeAdminSecurity{}, fakeSiteNamer{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Route("/admin", handler.RegisterAdmin)

	missingBulk := httptest.NewRecorder()
	router.ServeHTTP(missingBulk, formRequest(http.MethodPost, "/admin/articles/bulk", url.Values{"csrf_token": {"test-csrf"}, "content_ids": {"1"}, "content_versions": {"1:2"}, "bulk_action": {"publish"}, "operation_key": {"content-confirmation"}}))
	if missingBulk.Code != http.StatusBadRequest {
		t.Fatalf("bulk content without confirmation status=%d body=%s", missingBulk.Code, missingBulk.Body.String())
	}
	current, err := service.Article(ctx, draft.ID)
	if err != nil || current.Status != "draft" || current.LockVersion != updated.LockVersion {
		t.Fatalf("bulk content without confirmation changed content=%+v err=%v", current, err)
	}

	missingRestore := httptest.NewRecorder()
	router.ServeHTTP(missingRestore, formRequest(http.MethodPost, "/admin/articles/1/versions/1/restore", url.Values{"csrf_token": {"test-csrf"}, "lock_version": {"2"}}))
	if missingRestore.Code != http.StatusBadRequest {
		t.Fatalf("restore without confirmation status=%d body=%s", missingRestore.Code, missingRestore.Body.String())
	}
	current, err = service.Article(ctx, draft.ID)
	if err != nil || current.LockVersion != updated.LockVersion {
		t.Fatalf("restore without confirmation changed content=%+v err=%v", current, err)
	}

	restore := httptest.NewRecorder()
	router.ServeHTTP(restore, formRequest(http.MethodPost, "/admin/articles/1/versions/1/restore", url.Values{"csrf_token": {"test-csrf"}, "lock_version": {"2"}, "confirm_action": {"1"}}))
	if restore.Code != http.StatusSeeOther {
		t.Fatalf("restore with confirmation status=%d body=%s", restore.Code, restore.Body.String())
	}
	current, err = service.Article(ctx, draft.ID)
	if err != nil || current.LockVersion != 3 {
		t.Fatalf("restored content=%+v err=%v", current, err)
	}

	missingTrash := httptest.NewRecorder()
	router.ServeHTTP(missingTrash, formRequest(http.MethodPost, "/admin/articles/1/trash", url.Values{"csrf_token": {"test-csrf"}, "lock_version": {"3"}}))
	if missingTrash.Code != http.StatusBadRequest {
		t.Fatalf("trash without confirmation status=%d body=%s", missingTrash.Code, missingTrash.Body.String())
	}
	trashed := httptest.NewRecorder()
	router.ServeHTTP(trashed, formRequest(http.MethodPost, "/admin/articles/1/trash", url.Values{"csrf_token": {"test-csrf"}, "lock_version": {"3"}, "confirm_action": {"1"}}))
	if trashed.Code != http.StatusSeeOther {
		t.Fatalf("trash with confirmation status=%d body=%s", trashed.Code, trashed.Body.String())
	}

	missingBulkRestore := httptest.NewRecorder()
	router.ServeHTTP(missingBulkRestore, formRequest(http.MethodPost, "/admin/trash/bulk", url.Values{"csrf_token": {"test-csrf"}, "trash_items": {"article:1:4"}, "operation_key": {"restore-confirmation"}}))
	if missingBulkRestore.Code != http.StatusBadRequest {
		t.Fatalf("bulk restore without confirmation status=%d body=%s", missingBulkRestore.Code, missingBulkRestore.Body.String())
	}
	bulkRestore := httptest.NewRecorder()
	router.ServeHTTP(bulkRestore, formRequest(http.MethodPost, "/admin/trash/bulk", url.Values{"csrf_token": {"test-csrf"}, "trash_items": {"article:1:4"}, "operation_key": {"restore-confirmation"}, "confirm_action": {"1"}}))
	if bulkRestore.Code != http.StatusOK || !strings.Contains(bulkRestore.Body.String(), "批量恢复结果") {
		t.Fatalf("bulk restore with confirmation status=%d body=%s", bulkRestore.Code, bulkRestore.Body.String())
	}
}

func formRequest(method, target string, values url.Values) *http.Request {
	request := httptest.NewRequest(method, target, strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return request
}
