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
		Title:          "第一篇文章",
		Slug:           "first-post",
		Excerpt:        "一段摘要",
		SEOTitle:       "独立 SEO 标题",
		SEODescription: "独立 SEO 摘要",
		BodyMarkdown:   "# 初稿\n\n正文。",
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
		Title:          "第一篇文章",
		Slug:           "first-post",
		Excerpt:        "更新后的摘要",
		SEOTitle:       "更新 SEO 标题",
		SEODescription: "更新 SEO 摘要",
		BodyMarkdown:   "# 可发布版本\n\n正文。",
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
	if public.BodyMarkdown != "# 可发布版本\n\n正文。" || public.SEOTitle != "更新 SEO 标题" || public.SEODescription != "更新 SEO 摘要" {
		t.Fatalf("public content = %+v", public)
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
	renamed, err := service.UpdateDraft(ctx, newDraft.ID, newDraft.LockVersion, DraftInput{
		Title: "改链接", Slug: "changed-post", BodyMarkdown: "新地址正文",
	})
	if err != nil {
		t.Fatalf("save renamed draft: %v", err)
	}
	if _, err := service.PublicArticle(ctx, "changed-post"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("renamed draft became public early: %v", err)
	}
	if _, err := service.PublicArticle(ctx, "first-post"); err != nil {
		t.Fatalf("old public path changed before publication: %v", err)
	}
	if _, err := service.Publish(ctx, renamed.ID, renamed.LockVersion); err != nil {
		t.Fatalf("publish renamed draft: %v", err)
	}
	if _, err := service.PublicArticle(ctx, "first-post"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old path remained canonical: %v", err)
	}
	if public, err := service.PublicArticle(ctx, "changed-post"); err != nil || public.Title != "改链接" {
		t.Fatalf("new public path: article=%+v err=%v", public, err)
	}
	var redirectTarget string
	if err := db.Reader.QueryRow("SELECT target_path FROM redirects WHERE source_path_key='/posts/first-post'").Scan(&redirectTarget); err != nil || redirectTarget != "/posts/changed-post" {
		t.Fatalf("redirect target=%q err=%v", redirectTarget, err)
	}

	var epoch int64
	if err := db.Reader.QueryRow("SELECT render_epoch FROM system_state WHERE id = 1").Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	if epoch != 3 {
		t.Fatalf("render epoch = %d, want 3", epoch)
	}
	var revisions, checkpoints int
	if err := db.Reader.QueryRow("SELECT count(*), sum(is_publication_checkpoint) FROM content_revisions WHERE content_id = ?", draft.ID).Scan(&revisions, &checkpoints); err != nil {
		t.Fatal(err)
	}
	if revisions != 4 || checkpoints != 2 {
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
