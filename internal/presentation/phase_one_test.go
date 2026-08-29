package presentation

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/zhushilin/blog-project/internal/discovery"
	"github.com/zhushilin/blog-project/internal/organization"
	"github.com/zhushilin/blog-project/internal/platform/config"
	"github.com/zhushilin/blog-project/internal/platform/database"
	"github.com/zhushilin/blog-project/internal/publishing"
)

func TestPhaseOnePublicCollectionsAndArticleComposition(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, config.Database{
		Path: filepath.Join(t.TempDir(), "blog.sqlite"), BusyTimeout: config.Duration{Duration: time.Second},
		CacheSizeKiB: 4096, ReadConnections: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	organizations := organization.NewService(db)
	category, err := organizations.CreateCategory(ctx, organization.TermInput{Name: "设计", Slug: "design", Description: "设计实践"})
	if err != nil {
		t.Fatal(err)
	}
	tag, err := organizations.CreateTag(ctx, organization.TermInput{Name: "博客", Slug: "blog"})
	if err != nil {
		t.Fatal(err)
	}
	publisher := publishing.NewService(publishing.NewRepository(db))
	articles := make([]publishing.Article, 0, 21)
	for number := 1; number <= 21; number++ {
		draft, err := publisher.CreateDraft(ctx, publishing.DraftInput{
			Title: fmt.Sprintf("主流博客重构 %02d", number), Slug: fmt.Sprintf("refactor-%02d", number),
			Excerpt:      "让阅读、发现与继续阅读形成连贯路径。",
			BodyMarkdown: fmt.Sprintf("## 章节 %02d\n\n### 细节\n\n重构后的博客内容，强调清晰的信息架构。", number),
			CategoryID:   category.ID, TagIDs: []int64{tag.ID},
		})
		if err != nil {
			t.Fatalf("create article %d: %v", number, err)
		}
		published, err := publisher.Publish(ctx, draft.ID, draft.LockVersion)
		if err != nil {
			t.Fatalf("publish article %d: %v", number, err)
		}
		articles = append(articles, published)
	}
	if _, err := publisher.CreateDraft(ctx, publishing.DraftInput{Title: "不应公开的草稿", Slug: "hidden-draft", BodyMarkdown: "重构草稿"}); err != nil {
		t.Fatal(err)
	}

	discoveryService, err := discovery.NewService(discovery.NewRepository(db), discovery.Options{
		BaseURL: "https://blog.example", SyncBatchSize: 20, MaxResults: 50, FeedLimit: 50, SitemapLimit: 50000,
	})
	if err != nil {
		t.Fatal(err)
	}
	theme, err := NewDefaultTheme(NewMarkdown())
	if err != nil {
		t.Fatal(err)
	}
	cache, err := NewPageCache(filepath.Join(t.TempDir(), "cache"), 32, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHTTPHandler(
		publisher, presentationSiteNamer{}, NewStateRepository(db), theme, cache,
		slog.New(slog.NewTextHandler(io.Discard, nil)), organizations,
	)
	handler.SetDiscovery(discoveryService)
	router := chi.NewRouter()
	handler.RegisterPublic(router)

	assertPublic := func(target string, expected ...string) string {
		t.Helper()
		response := requestPublic(t, router, target)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", target, response.Code, response.Body.String())
		}
		body := response.Body.String()
		for _, value := range expected {
			if !strings.Contains(body, value) {
				t.Fatalf("%s missing %q: %s", target, value, body)
			}
		}
		if strings.Contains(body, "不应公开的草稿") || strings.Contains(body, "hidden-draft") {
			t.Fatalf("%s leaked draft content: %s", target, body)
		}
		return body
	}

	articlesPage := assertPublic("/articles?page=2", "第 2 / 2 页", "共 21 项", "主流博客重构 01", "canonical")
	if !strings.Contains(articlesPage, `href="https://blog.example/articles"`) || strings.Contains(articlesPage, "%3Fpage%3D2") {
		t.Fatalf("articles page canonical URL is polluted: %s", articlesPage)
	}
	assertPublic("/categories/design?page=2", "设计", "第 2 / 2 页", "主流博客重构 01")
	categoryPage := requestPublic(t, router, "/categories/design?page=2")
	if !strings.Contains(categoryPage.Body.String(), `rel="canonical" href="https://blog.example/categories/design"`) {
		t.Fatalf("category canonical URL is wrong: %s", categoryPage.Body.String())
	}
	assertPublic("/tags/blog?page=2", "# 博客", "第 2 / 2 页", "主流博客重构 01")
	assertPublic("/search?q=重构&page=2", "找到 21 项结果", "第 2 / 2 页", "主流博客<mark>重构</mark>")

	articlePath := "/posts/" + articles[10].PublishedSlug
	articlePage := assertPublic(articlePath, "article-toc", "章节 11", "上一篇", "下一篇", "继续阅读")
	if !strings.Contains(articlePage, `href="#`) || !strings.Contains(articlePage, `aria-label="文章导航"`) {
		t.Fatalf("article composition missing deep links: %s", articlePage)
	}
}
