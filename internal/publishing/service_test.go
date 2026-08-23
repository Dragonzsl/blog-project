package publishing

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhushilin/blog-project/internal/platform/config"
	"github.com/zhushilin/blog-project/internal/platform/database"
)

func TestArticleDraftPreviewPublishAndStablePermalink(t *testing.T) {
	ctx := context.Background()
	service, db := newPublishingTestService(t)
	fixedTime := time.Date(2026, time.August, 23, 15, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return fixedTime }

	draft, err := service.CreateDraft(ctx, DraftInput{
		Title:        "第一篇文章",
		Slug:         "first-post",
		Excerpt:      "一段摘要",
		BodyMarkdown: "# 初稿\n\n正文。",
	})
	if err != nil {
		t.Fatalf("CreateDraft() error = %v", err)
	}
	if draft.Status != "draft" || draft.CurrentRevisionID == 0 || draft.LockVersion != 1 {
		t.Fatalf("draft = %+v", draft)
	}
	if _, err := service.PublicArticle(ctx, "first-post"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("draft is public: %v", err)
	}

	updated, err := service.UpdateDraft(ctx, draft.ID, draft.LockVersion, DraftInput{
		Title:        "第一篇文章",
		Slug:         "first-post",
		Excerpt:      "更新后的摘要",
		BodyMarkdown: "# 可发布版本\n\n正文。",
	})
	if err != nil {
		t.Fatalf("UpdateDraft() error = %v", err)
	}
	if updated.LockVersion != 2 || updated.CurrentRevisionID == draft.CurrentRevisionID {
		t.Fatalf("updated = %+v", updated)
	}
	if _, err := service.UpdateDraft(ctx, draft.ID, draft.LockVersion, DraftInput{Title: "冲突", Slug: "first-post"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update error = %v", err)
	}

	published, err := service.Publish(ctx, updated.ID, updated.LockVersion)
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if published.Status != "published" || published.PublishedRevisionID != updated.CurrentRevisionID || published.PublishedAt == nil {
		t.Fatalf("published = %+v", published)
	}
	public, err := service.PublicArticle(ctx, "first-post")
	if err != nil {
		t.Fatalf("PublicArticle() error = %v", err)
	}
	if public.BodyMarkdown != "# 可发布版本\n\n正文。" {
		t.Fatalf("public body = %q", public.BodyMarkdown)
	}

	newDraft, err := service.UpdateDraft(ctx, published.ID, published.LockVersion, DraftInput{
		Title:        "第一篇文章（修订中）",
		Slug:         "first-post",
		Excerpt:      "尚未发布",
		BodyMarkdown: "# 尚未发布的修订",
	})
	if err != nil {
		t.Fatalf("UpdateDraft(published) error = %v", err)
	}
	public, err = service.PublicArticle(ctx, "first-post")
	if err != nil {
		t.Fatal(err)
	}
	if public.Title != "第一篇文章" || public.BodyMarkdown != "# 可发布版本\n\n正文。" {
		t.Fatalf("unpublished edit leaked publicly: %+v", public)
	}
	if _, err := service.UpdateDraft(ctx, newDraft.ID, newDraft.LockVersion, DraftInput{
		Title: "改链接", Slug: "changed-post",
	}); !errors.Is(err, ErrPublishedSlugImmutable) {
		t.Fatalf("published slug change error = %v", err)
	}

	var epoch int64
	if err := db.Reader.QueryRow("SELECT render_epoch FROM system_state WHERE id = 1").Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	if epoch != 2 {
		t.Fatalf("render epoch = %d, want 2", epoch)
	}
	var revisions, checkpoints int
	if err := db.Reader.QueryRow("SELECT count(*), sum(is_publication_checkpoint) FROM content_revisions WHERE content_id = ?", draft.ID).Scan(&revisions, &checkpoints); err != nil {
		t.Fatal(err)
	}
	if revisions != 3 || checkpoints != 1 {
		t.Fatalf("revisions = %d, checkpoints = %d", revisions, checkpoints)
	}
}

func TestArticleSlugIsUniqueAndReserved(t *testing.T) {
	ctx := context.Background()
	service, _ := newPublishingTestService(t)
	input := DraftInput{Title: "文章", Slug: "reserved-post"}
	if _, err := service.CreateDraft(ctx, input); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateDraft(ctx, input); !errors.Is(err, ErrSlugUnavailable) {
		t.Fatalf("duplicate slug error = %v", err)
	}
}

func TestPageUsesRootPermalinkAndSystemPathsStayReserved(t *testing.T) {
	ctx := context.Background()
	service, _ := newPublishingTestService(t)
	page, err := service.CreatePageDraft(ctx, DraftInput{Title: "关于", Slug: "关于-me", BodyMarkdown: "## 你好"})
	if err != nil {
		t.Fatal(err)
	}
	if page.Kind != "page" {
		t.Fatalf("kind=%q", page.Kind)
	}
	if _, err := service.PublicPage(ctx, "关于-me"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("draft public error=%v", err)
	}
	published, err := service.PublishPage(ctx, page.ID, page.LockVersion)
	if err != nil {
		t.Fatal(err)
	}
	public, err := service.PublicPage(ctx, "关于-me")
	if err != nil || public.Title != "关于" || published.PublishedRevisionID == 0 {
		t.Fatalf("public=%+v err=%v", public, err)
	}
	if _, err := service.CreatePageDraft(ctx, DraftInput{Title: "后台", Slug: "admin", BodyMarkdown: "reserved"}); !errors.Is(err, ErrSlugUnavailable) {
		t.Fatalf("reserved page slug error=%v", err)
	}
}

func newPublishingTestService(t *testing.T) (*Service, *database.DB) {
	t.Helper()
	db, err := database.Open(context.Background(), config.Database{
		Path:            filepath.Join(t.TempDir(), "blog.sqlite"),
		BusyTimeout:     config.Duration{Duration: time.Second},
		CacheSizeKiB:    4096,
		ReadConnections: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})
	return NewService(NewRepository(db)), db
}
