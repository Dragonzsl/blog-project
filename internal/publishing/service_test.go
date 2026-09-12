package publishing

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhushilin/blog-project/internal/organization"
	"github.com/zhushilin/blog-project/internal/platform/config"
	"github.com/zhushilin/blog-project/internal/platform/database"
	"github.com/zhushilin/blog-project/internal/platform/pagination"
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

func TestSlugIsGeneratedFromTitleWhenRequested(t *testing.T) {
	ctx := context.Background()
	service, _ := newPublishingTestService(t)

	first, err := service.CreateDraft(ctx, DraftInput{Title: "测试文章 01：新入口", BodyMarkdown: "正文"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Slug != "测试文章-01-新入口" {
		t.Fatalf("generated slug=%q", first.Slug)
	}

	second, err := service.CreateDraft(ctx, DraftInput{Title: "测试文章 01：新入口", BodyMarkdown: "正文"})
	if err != nil {
		t.Fatal(err)
	}
	if second.Slug != "测试文章-01-新入口-2" {
		t.Fatalf("collision slug=%q", second.Slug)
	}

	page, err := service.CreatePageDraft(ctx, DraftInput{Title: "关于这个地方", BodyMarkdown: "正文"})
	if err != nil {
		t.Fatal(err)
	}
	if page.Slug != "关于这个地方" {
		t.Fatalf("generated page slug=%q", page.Slug)
	}
}

func TestArchiveRevisionImportRollsBackAsOneUnit(t *testing.T) {
	ctx := context.Background()
	service, _ := newPublishingTestService(t)

	_, err := service.ImportDraftRevisions(ctx, "article", []DraftInput{
		{Title: "可回滚归档", Slug: "atomic-archive", BodyMarkdown: "初始正文"},
		{Title: "可回滚归档", Slug: "atomic-archive", BodyMarkdown: "后续正文", CoverMediaPublicID: make([]byte, 16)},
	})
	if err == nil {
		t.Fatal("ImportDraftRevisions() unexpectedly succeeded")
	}
	items, err := service.Articles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("partially imported contents = %+v", items)
	}
}

func TestScheduledContentsHasMoreOnlyWhenAnExtraRowExists(t *testing.T) {
	ctx := context.Background()
	service, _ := newPublishingTestService(t)
	now := time.Date(2026, time.August, 23, 15, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	for _, slug := range []string{"calendar-one", "calendar-two"} {
		content, err := service.CreateDraft(ctx, DraftInput{Title: slug, Slug: slug, BodyMarkdown: "正文"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.Schedule(ctx, "article", content.ID, content.LockVersion, now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	items, hasMore, err := service.ScheduledContents(ctx, "article", now, now.Add(2*time.Hour), 1)
	if err != nil || len(items) != 1 || !hasMore {
		t.Fatalf("limited calendar page = %d/%v err=%v", len(items), hasMore, err)
	}
	items, hasMore, err = service.ScheduledContents(ctx, "article", now, now.Add(2*time.Hour), 2)
	if err != nil || len(items) != 2 || hasMore {
		t.Fatalf("exact calendar page = %d/%v err=%v", len(items), hasMore, err)
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

func TestPublicArticlePagesAndNavigation(t *testing.T) {
	ctx := context.Background()
	service, db := newPublishingTestService(t)
	service.now = func() time.Time { return time.Date(2026, time.August, 28, 8, 0, 0, 0, time.UTC) }
	organizations := organization.NewService(db)
	category, err := organizations.CreateCategory(ctx, organization.TermInput{Name: "设计", Slug: "design"})
	if err != nil {
		t.Fatal(err)
	}
	tag, err := organizations.CreateTag(ctx, organization.TermInput{Name: "博客", Slug: "blog"})
	if err != nil {
		t.Fatal(err)
	}

	articles := make([]Article, 0, 21)
	for number := 1; number <= 21; number++ {
		draft, err := service.CreateDraft(ctx, DraftInput{
			Title: "文章 " + fmt.Sprintf("%02d", number), Slug: fmt.Sprintf("article-%02d", number),
			Excerpt: "列表摘要", BodyMarkdown: "## 目录标题\n\n正文内容。", CategoryID: category.ID, TagIDs: []int64{tag.ID},
		})
		if err != nil {
			t.Fatalf("create article %d: %v", number, err)
		}
		published, err := service.Publish(ctx, draft.ID, draft.LockVersion)
		if err != nil {
			t.Fatalf("publish article %d: %v", number, err)
		}
		articles = append(articles, published)
	}
	if _, err := service.CreateDraft(ctx, DraftInput{Title: "只存在于草稿箱", Slug: "draft-only"}); err != nil {
		t.Fatal(err)
	}

	page, err := service.PublicArticlesPage(ctx, pagination.Request{Page: 2, PerPage: 20})
	if err != nil {
		t.Fatal(err)
	}
	if page.Pagination.Total != 21 || page.Pagination.PageCount != 2 || page.Pagination.Page != 2 || len(page.Articles) != 1 || page.Articles[0].Title != "文章 01" {
		t.Fatalf("public page = %+v articles=%+v", page.Pagination, page.Articles)
	}
	if page.Articles[0].BodyMarkdown != "" {
		t.Fatal("public article list loaded the canonical body")
	}
	detail, err := service.PublicArticle(ctx, "article-01")
	if err != nil || detail.BodyMarkdown != "## 目录标题\n\n正文内容。" {
		t.Fatalf("public detail body=%q err=%v", detail.BodyMarkdown, err)
	}
	cards, err := service.PublicArticleCardsByIDs(ctx, []int64{articles[2].ID, articles[0].ID})
	if err != nil || len(cards) != 2 || cards[0].ID != articles[2].ID || cards[1].ID != articles[0].ID {
		t.Fatalf("public card projection=%+v err=%v", cards, err)
	}
	for _, card := range cards {
		if card.BodyMarkdown != "" {
			t.Fatal("public card projection loaded the canonical body")
		}
	}
	for _, article := range page.Articles {
		if article.Title == "只存在于草稿箱" {
			t.Fatal("draft leaked into public article page")
		}
	}

	navigation, err := service.PublicArticleNavigation(ctx, articles[10].ID, 3)
	if err != nil {
		t.Fatal(err)
	}
	if navigation.Previous == nil || navigation.Previous.Title != "文章 10" || navigation.Next == nil || navigation.Next.Title != "文章 12" {
		t.Fatalf("article navigation = %+v", navigation)
	}
	if len(navigation.Related) == 0 || len(navigation.Related) > 3 {
		t.Fatalf("related articles = %+v", navigation.Related)
	}
	for _, related := range navigation.Related {
		if related.ID == articles[10].ID {
			t.Fatal("current article appeared in related articles")
		}
	}

	firstNavigation, err := service.PublicArticleNavigation(ctx, articles[0].ID, 3)
	if err != nil {
		t.Fatal(err)
	}
	if firstNavigation.Previous != nil || firstNavigation.Next == nil {
		t.Fatalf("boundary navigation = %+v", firstNavigation)
	}
}

func TestAdminContentsPageFiltersTaxonomyStatusAndPagination(t *testing.T) {
	ctx := context.Background()
	service, db := newPublishingTestService(t)
	service.now = func() time.Time { return time.Date(2026, time.August, 29, 8, 0, 0, 0, time.UTC) }
	organizations := organization.NewService(db)
	category, err := organizations.CreateCategory(ctx, organization.TermInput{Name: "设计系统", Slug: "design-system"})
	if err != nil {
		t.Fatal(err)
	}
	tag, err := organizations.CreateTag(ctx, organization.TermInput{Name: "工作流", Slug: "workflow"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := service.CreateDraft(ctx, DraftInput{Title: "草稿 Alpha", Slug: "draft-alpha", Excerpt: "有摘要", BodyMarkdown: "草稿正文", CategoryID: category.ID, TagIDs: []int64{tag.ID}}); err != nil {
		t.Fatal(err)
	}
	publishedDraft, err := service.CreateDraft(ctx, DraftInput{Title: "已发布 Beta", Slug: "published-beta", BodyMarkdown: "已发布正文", CategoryID: category.ID, TagIDs: []int64{tag.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Publish(ctx, publishedDraft.ID, publishedDraft.LockVersion); err != nil {
		t.Fatal(err)
	}
	scheduled, err := service.CreateDraft(ctx, DraftInput{Title: "定时 Gamma", Slug: "scheduled-gamma", BodyMarkdown: "定时正文", CategoryID: category.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Schedule(ctx, "article", scheduled.ID, scheduled.LockVersion, service.now().Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	drafts, err := service.AdminContentsPage(ctx, "article", AdminContentFilter{Status: "draft"}, pagination.Request{Page: 1, PerPage: 20})
	if err != nil {
		t.Fatal(err)
	}
	if drafts.Pagination.Total != 1 || len(drafts.Contents) != 1 || drafts.Contents[0].Title != "草稿 Alpha" {
		t.Fatalf("draft filter = %+v contents=%+v", drafts.Pagination, drafts.Contents)
	}

	byCategory, err := service.AdminContentsPage(ctx, "article", AdminContentFilter{Category: category.Slug}, pagination.Request{Page: 1, PerPage: 20})
	if err != nil {
		t.Fatal(err)
	}
	if byCategory.Pagination.Total != 3 {
		t.Fatalf("category filter total = %d, want 3", byCategory.Pagination.Total)
	}
	byTag, err := service.AdminContentsPage(ctx, "article", AdminContentFilter{Tag: tag.Slug, Query: "已发布"}, pagination.Request{Page: 1, PerPage: 20})
	if err != nil {
		t.Fatal(err)
	}
	if byTag.Pagination.Total != 1 || byTag.Contents[0].Title != "已发布 Beta" {
		t.Fatalf("tag/query filter = %+v contents=%+v", byTag.Pagination, byTag.Contents)
	}

	firstPage, err := service.AdminContentsPage(ctx, "article", AdminContentFilter{Sort: "title"}, pagination.Request{Page: 1, PerPage: 1})
	if err != nil {
		t.Fatal(err)
	}
	if firstPage.Pagination.Total != 3 || firstPage.Pagination.PageCount != 3 || !firstPage.Pagination.HasNext || firstPage.Contents[0].Title != "定时 Gamma" {
		t.Fatalf("title pagination = %+v contents=%+v", firstPage.Pagination, firstPage.Contents)
	}
}

func TestDashboardSummaryUsesEditorialState(t *testing.T) {
	ctx := context.Background()
	service, _ := newPublishingTestService(t)
	fixedTime := time.Date(2026, time.August, 29, 8, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return fixedTime }
	if _, err := service.CreateDraft(ctx, DraftInput{Title: "需要继续", Slug: "continue", Excerpt: "已写摘要", BodyMarkdown: "正文"}); err != nil {
		t.Fatal(err)
	}
	published, err := service.CreateDraft(ctx, DraftInput{Title: "刚刚发布", Slug: "fresh", BodyMarkdown: "正文"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Publish(ctx, published.ID, published.LockVersion); err != nil {
		t.Fatal(err)
	}
	scheduled, err := service.CreateDraft(ctx, DraftInput{Title: "稍后公开", Slug: "later", BodyMarkdown: "正文"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Schedule(ctx, "article", scheduled.ID, scheduled.LockVersion, fixedTime.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	summary, err := service.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if summary.DraftCount != 1 || summary.ScheduledCount != 1 || summary.PublishedCount != 1 || summary.TrashedCount != 0 {
		t.Fatalf("summary counts = %+v", summary)
	}
	if summary.PublishedThisWeek != 1 || summary.WithoutExcerptCount != 2 {
		t.Fatalf("summary health = %+v", summary)
	}
	if len(summary.RecentEdits) != 3 || summary.RecentEdits[0].Title != "稍后公开" {
		t.Fatalf("recent edits = %+v", summary.RecentEdits)
	}
	if len(summary.RecentPublished) != 1 || summary.RecentPublished[0].Title != "刚刚发布" {
		t.Fatalf("recent published = %+v", summary.RecentPublished)
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
