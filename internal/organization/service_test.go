package organization_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhushilin/blog-project/internal/organization"
	"github.com/zhushilin/blog-project/internal/platform/config"
	"github.com/zhushilin/blog-project/internal/platform/database"
	"github.com/zhushilin/blog-project/internal/platform/pagination"
	"github.com/zhushilin/blog-project/internal/publishing"
)

func TestTaxonomyNavigationAndRenderInvalidation(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, config.Database{Path: filepath.Join(t.TempDir(), "blog.sqlite"), BusyTimeout: config.Duration{Duration: time.Second}, CacheSizeKiB: 4096, ReadConnections: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := organization.NewService(db)
	category, err := service.CreateCategory(ctx, organization.TermInput{Name: "随笔", Slug: "Café-随笔", Description: "生活记录", SortOrder: 1})
	if err != nil {
		t.Fatal(err)
	}
	tag, err := service.CreateTag(ctx, organization.TermInput{Name: "花园", Slug: "花园"})
	if err != nil {
		t.Fatal(err)
	}
	publisher := publishing.NewService(publishing.NewRepository(db))
	article, err := publisher.CreateDraft(ctx, publishing.DraftInput{Title: "春日", Slug: "春日-notes", BodyMarkdown: "正文", CategoryID: category.ID, TagIDs: []int64{tag.ID}})
	if err != nil {
		t.Fatal(err)
	}
	published, err := publisher.Publish(ctx, article.ID, article.LockVersion)
	if err != nil {
		t.Fatal(err)
	}
	contentLink, err := service.CreateNavigationItem(ctx, organization.NavigationInput{Location: "primary", Label: "春日", TargetKind: "content", TargetID: published.ID, SortOrder: 1})
	if err != nil {
		t.Fatal(err)
	}
	child, err := service.CreateNavigationItem(ctx, organization.NavigationInput{Location: "primary", ParentID: contentLink.ID, Label: "外部", TargetKind: "external", ExternalURL: "https://example.com", SortOrder: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateNavigationItem(ctx, organization.NavigationInput{Location: "primary", ParentID: child.ID, Label: "过深", TargetKind: "external", ExternalURL: "https://example.com/deep"}); !errors.Is(err, organization.ErrInvalidTarget) {
		t.Fatalf("grandchild error=%v", err)
	}
	navigation, err := service.PublicNavigation(ctx, "primary")
	if err != nil {
		t.Fatal(err)
	}
	if len(navigation) != 2 || navigation[0].URL != "/posts/%E6%98%A5%E6%97%A5-notes" || navigation[1].URL != "https://example.com" {
		t.Fatalf("navigation=%+v", navigation)
	}
	publicCategory, categoryIDs, err := service.PublicCategory(ctx, "café-随笔", 20)
	if err != nil {
		t.Fatal(err)
	}
	if publicCategory.ID != category.ID || len(categoryIDs) != 1 || categoryIDs[0] != article.ID {
		t.Fatalf("category=%+v ids=%v", publicCategory, categoryIDs)
	}
	categoryPage, err := service.PublicCategoryPage(ctx, category.Slug, pagination.Request{Page: 1, PerPage: 1})
	if err != nil || categoryPage.Pagination.Total != 1 || len(categoryPage.ArticleIDs) != 1 {
		t.Fatalf("category page=%+v err=%v", categoryPage, err)
	}
	_, tagIDs, err := service.PublicTag(ctx, "花园", 20)
	if err != nil || len(tagIDs) != 1 {
		t.Fatalf("tag ids=%v err=%v", tagIDs, err)
	}
	loaded, err := publisher.Article(ctx, article.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Category == nil || loaded.Category.ID != category.ID || len(loaded.Tags) != 1 || loaded.Tags[0].ID != tag.ID {
		t.Fatalf("taxonomy=%+v tags=%+v", loaded.Category, loaded.Tags)
	}
	secondCategory, err := service.CreateCategory(ctx, organization.TermInput{Name: "生活", Slug: "life"})
	if err != nil {
		t.Fatal(err)
	}
	secondTag, err := service.CreateTag(ctx, organization.TermInput{Name: "日常", Slug: "daily"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := publisher.UpdateDraft(ctx, published.ID, published.LockVersion, publishing.DraftInput{Title: "未发布组织", Slug: "春日-notes", BodyMarkdown: "草稿", CategoryID: secondCategory.ID, TagIDs: []int64{secondTag.ID}}); err != nil {
		t.Fatal(err)
	}
	publicArticle, err := publisher.PublicArticle(ctx, "春日-notes")
	if err != nil {
		t.Fatal(err)
	}
	if publicArticle.Category == nil || publicArticle.Category.ID != category.ID || len(publicArticle.Tags) != 1 || publicArticle.Tags[0].ID != tag.ID {
		t.Fatalf("published taxonomy leaked current draft: category=%+v tags=%+v", publicArticle.Category, publicArticle.Tags)
	}
	_, oldCategoryIDs, err := service.PublicCategory(ctx, category.Slug, 20)
	if err != nil || len(oldCategoryIDs) != 1 || oldCategoryIDs[0] != article.ID {
		t.Fatalf("published article left old category archive: ids=%v err=%v", oldCategoryIDs, err)
	}
	_, draftCategoryIDs, err := service.PublicCategory(ctx, secondCategory.Slug, 20)
	if err != nil || len(draftCategoryIDs) != 0 {
		t.Fatalf("draft category leaked into public archive: ids=%v err=%v", draftCategoryIDs, err)
	}
	_, oldTagIDs, err := service.PublicTag(ctx, tag.Slug, 20)
	if err != nil || len(oldTagIDs) != 1 || oldTagIDs[0] != article.ID {
		t.Fatalf("published article left old tag archive: ids=%v err=%v", oldTagIDs, err)
	}
	_, draftTagIDs, err := service.PublicTag(ctx, secondTag.Slug, 20)
	if err != nil || len(draftTagIDs) != 0 {
		t.Fatalf("draft tag leaked into public archive: ids=%v err=%v", draftTagIDs, err)
	}
	var epoch int64
	if err := db.Reader.QueryRowContext(ctx, "SELECT render_epoch FROM system_state WHERE id=1").Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	if epoch < 5 {
		t.Fatalf("render epoch=%d", epoch)
	}
}

func TestTaxonomyRedirectChainsAreFlattenedAndHistoricalPathsStayReserved(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, config.Database{Path: filepath.Join(t.TempDir(), "blog.sqlite"), BusyTimeout: config.Duration{Duration: time.Second}, CacheSizeKiB: 4096, ReadConnections: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := organization.NewService(db)
	category, err := service.CreateCategory(ctx, organization.TermInput{Name: "技术", Slug: "tech"})
	if err != nil {
		t.Fatal(err)
	}
	category, err = service.UpdateCategory(ctx, category.ID, organization.TermInput{Name: "技术", Slug: "engineering"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateCategory(ctx, category.ID, organization.TermInput{Name: "技术", Slug: "architecture"}); err != nil {
		t.Fatal(err)
	}
	for source, want := range map[string]string{"/categories/tech": "/categories/architecture", "/categories/engineering": "/categories/architecture"} {
		var target string
		if err := db.Reader.QueryRowContext(ctx, "SELECT target_path FROM redirects WHERE source_path_key=?", source).Scan(&target); err != nil || target != want {
			t.Fatalf("redirect %s target=%q err=%v", source, target, err)
		}
	}
	if _, err := service.CreateCategory(ctx, organization.TermInput{Name: "旧地址", Slug: "tech"}); !errors.Is(err, organization.ErrSlugUnavailable) {
		t.Fatalf("reuse historical taxonomy path error=%v", err)
	}
	if _, err := service.UpdateCategory(ctx, category.ID, organization.TermInput{Name: "技术", Slug: "tech"}); !errors.Is(err, organization.ErrSlugUnavailable) {
		t.Fatalf("redirect loop error=%v", err)
	}
}
