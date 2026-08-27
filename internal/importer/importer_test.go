package importer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhushilin/blog-project/internal/platform/config"
	"github.com/zhushilin/blog-project/internal/platform/database"
	"github.com/zhushilin/blog-project/internal/publishing"
)

func TestParsersReadWordPressGhostAndMarkdown(t *testing.T) {
	wordpress := []byte(`<?xml version="1.0"?><rss><channel><item><title>旧文章</title><post_id>42</post_id><post_name>old-post</post_name><post_status>publish</post_status><post_type>post</post_type><excerpt>摘要</excerpt><content:encoded xmlns:content="http://purl.org/rss/1.0/modules/content/"><![CDATA[<h2>标题</h2><p>正文。</p>]]></content:encoded><category domain="category">技术</category><category domain="post_tag">Go</category><pubDate>Wed, 26 Aug 2026 10:00:00 +0000</pubDate></item></channel></rss>`)
	items, warnings, err := Parse(FormatWordPress, wordpress, "sample.xml")
	if err != nil || len(items) != 1 {
		t.Fatalf("WordPress items=%+v warnings=%v err=%v", items, warnings, err)
	}
	if items[0].Kind != "article" || items[0].Slug != "old-post" || items[0].Category != "技术" || len(items[0].Tags) != 1 || items[0].BodyMarkdown == "" {
		t.Fatalf("WordPress item=%+v", items[0])
	}
	ghost := []byte(`{"db":[{"data":{"posts":[{"id":"g1","title":"Ghost 文章","slug":"ghost-post","html":"<p>Hello <strong>world</strong></p>","status":"published","tags":[{"name":"写作"}],"published_at":"2026-08-26T10:00:00Z"}],"pages":[{"id":"g2","title":"关于","slug":"about","markdown":"关于我"}]}}]}`)
	items, _, err = Parse(FormatGhost, ghost, "ghost.json")
	if err != nil || len(items) != 2 {
		t.Fatalf("Ghost items=%+v err=%v", items, err)
	}
	if items[0].BodyMarkdown == "" || items[1].Kind != "page" {
		t.Fatalf("Ghost items=%+v", items)
	}
	markdown := []byte("---\ntitle: Markdown 文章\nslug: markdown-post\ntags: Go, SQLite\n---\n\n正文。\n")
	items, _, err = Parse(FormatMarkdown, markdown, "one.md")
	if err != nil || len(items) != 1 || items[0].Title != "Markdown 文章" || len(items[0].Tags) != 2 {
		t.Fatalf("Markdown items=%+v err=%v", items, err)
	}
}

func TestImportDryRunCreatesDraftsAndDeduplicatesSource(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := database.Open(ctx, config.Database{Path: filepath.Join(root, "blog.sqlite"), BusyTimeout: config.Duration{Duration: time.Second}, CacheSizeKiB: 4096, ReadConnections: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	path := filepath.Join(root, "entry.md")
	if err := os.WriteFile(path, []byte("---\ntitle: 导入文章\nslug: imported-post\n---\n\n正文。"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewService(db, publishing.NewService(publishing.NewRepository(db)), nil)
	report, err := service.ImportPath(ctx, FormatMarkdown, path, true)
	if err != nil || !report.DryRun || report.Planned != 1 || report.Imported != 0 {
		t.Fatalf("dry report=%+v err=%v", report, err)
	}
	report, err = service.ImportPath(ctx, FormatMarkdown, path, false)
	if err != nil || report.Imported != 1 {
		t.Fatalf("import report=%+v err=%v", report, err)
	}
	articles, err := service.content.Articles(ctx)
	if err != nil || len(articles) != 1 || articles[0].Status != "draft" {
		t.Fatalf("articles=%+v err=%v", articles, err)
	}
	report, err = service.ImportPath(ctx, FormatMarkdown, path, false)
	if err != nil || !report.Duplicate || report.Skipped != 1 {
		t.Fatalf("duplicate report=%+v err=%v", report, err)
	}
}
