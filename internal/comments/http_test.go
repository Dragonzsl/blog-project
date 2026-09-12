package comments

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/zhushilin/blog-project/internal/platform/config"
	"github.com/zhushilin/blog-project/internal/platform/database"
	"github.com/zhushilin/blog-project/internal/presentation"
	"github.com/zhushilin/blog-project/internal/publishing"
)

type fakeCommentAdminSecurity struct{}

func (fakeCommentAdminSecurity) CSRFToken(*http.Request) string { return "test-csrf" }
func (fakeCommentAdminSecurity) VerifyParsedCSRF(http.ResponseWriter, *http.Request) bool {
	return true
}

func TestCommentModerationRequiresExplicitConfirmation(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, config.Database{Path: filepath.Join(t.TempDir(), "blog.sqlite"), BusyTimeout: config.Duration{Duration: time.Second}, CacheSizeKiB: 4096, ReadConnections: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	publishingService := publishing.NewService(publishing.NewRepository(db))
	article, err := publishingService.CreateDraft(ctx, publishing.DraftInput{Title: "评论确认", Slug: "comment-confirmation", BodyMarkdown: "正文"})
	if err != nil {
		t.Fatal(err)
	}
	article, err = publishingService.Publish(ctx, article.ID, article.LockVersion)
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(db, publishingService, presentation.NewMarkdown(), true)
	comment, err := service.Create(ctx, article.Slug, Input{DisplayName: "访客", Body: "待审核评论"})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHTTPHandler(service, publishingService, fakeCommentAdminSecurity{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	router := chi.NewRouter()
	router.Route("/admin/plugins/comments.local", handler.RegisterAdmin)

	missing := httptest.NewRecorder()
	router.ServeHTTP(missing, commentFormRequest(http.MethodPost, "/admin/plugins/comments.local/comments/1/moderate", url.Values{"csrf_token": {"test-csrf"}, "status": {"approved"}}))
	if missing.Code != http.StatusBadRequest {
		t.Fatalf("moderation without confirmation status=%d body=%s", missing.Code, missing.Body.String())
	}
	current, err := service.Get(ctx, comment.ID)
	if err != nil || current.Status != "pending" {
		t.Fatalf("moderation without confirmation changed comment=%+v err=%v", current, err)
	}

	approved := httptest.NewRecorder()
	router.ServeHTTP(approved, commentFormRequest(http.MethodPost, "/admin/plugins/comments.local/comments/1/moderate", url.Values{"csrf_token": {"test-csrf"}, "status": {"approved"}, "confirm_action": {"1"}}))
	if approved.Code != http.StatusSeeOther {
		t.Fatalf("moderation with confirmation status=%d body=%s", approved.Code, approved.Body.String())
	}

	bulkComment, err := service.Create(ctx, article.Slug, Input{DisplayName: "另一位访客", Body: "另一条待审核评论"})
	if err != nil {
		t.Fatal(err)
	}
	missingBulk := httptest.NewRecorder()
	router.ServeHTTP(missingBulk, commentFormRequest(http.MethodPost, "/admin/plugins/comments.local/comments/bulk", url.Values{"csrf_token": {"test-csrf"}, "comment_ids": {strconvFormatID(bulkComment.ID)}, "status": {"spam"}, "operation_key": {"comment-confirmation"}}))
	if missingBulk.Code != http.StatusBadRequest {
		t.Fatalf("bulk moderation without confirmation status=%d body=%s", missingBulk.Code, missingBulk.Body.String())
	}
	bulk := httptest.NewRecorder()
	router.ServeHTTP(bulk, commentFormRequest(http.MethodPost, "/admin/plugins/comments.local/comments/bulk", url.Values{"csrf_token": {"test-csrf"}, "comment_ids": {strconvFormatID(bulkComment.ID)}, "status": {"spam"}, "operation_key": {"comment-confirmation"}, "confirm_action": {"1"}}))
	if bulk.Code != http.StatusOK || !strings.Contains(bulk.Body.String(), "批量操作结果") {
		t.Fatalf("bulk moderation with confirmation status=%d location=%q body=%s", bulk.Code, bulk.Header().Get("Location"), bulk.Body.String())
	}
}

func commentFormRequest(method, target string, values url.Values) *http.Request {
	request := httptest.NewRequest(method, target, strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return request
}

func strconvFormatID(id int64) string {
	return strconv.FormatInt(id, 10)
}
