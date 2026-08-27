// Package perf contains release-gate tests for the budgets in ADR-0031. The
// checks are intentionally in-process and deterministic; the accompanying
// scripts add real HTTP/load evidence when a release environment is available.
package perf

import (
	"context"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/zhushilin/blog-project/internal/discovery"
	"github.com/zhushilin/blog-project/internal/platform/config"
	"github.com/zhushilin/blog-project/internal/platform/database"
	"github.com/zhushilin/blog-project/internal/presentation"
	"github.com/zhushilin/blog-project/internal/publishing"
	defaulttheme "github.com/zhushilin/blog-project/themes/default"
)

func TestADR31Budgets(t *testing.T) {
	css, err := defaulttheme.Files.ReadFile("assets/theme.css")
	if err != nil {
		t.Fatal(err)
	}
	if len(css) > 40<<10 {
		t.Fatalf("default theme CSS is %d bytes, budget is 40 KiB", len(css))
	}

	markdown := presentation.NewMarkdown()
	renderDurations := make([]time.Duration, 0, 50)
	for index := 0; index < cap(renderDurations); index++ {
		started := time.Now()
		if _, err := markdown.Render("# 阶段三\n\n这是一段中文和 English 混排的正文。\n\n- one\n- two"); err != nil {
			t.Fatal(err)
		}
		renderDurations = append(renderDurations, time.Since(started))
	}
	if value := percentile(renderDurations, .95); value > 150*time.Millisecond {
		t.Fatalf("first render p95=%s, budget=150ms", value)
	}

	cache, err := presentation.NewPageCache(t.TempDir(), 32, 4<<20)
	if err != nil {
		t.Fatal(err)
	}
	entry := presentation.CacheEntry{Key: "home", Epoch: 1, Status: 200, ContentType: "text/html; charset=utf-8", ETag: `"stage3"`, Body: []byte("cached body")}
	if err := cache.Put(entry); err != nil {
		t.Fatal(err)
	}
	hitDurations := make([]time.Duration, 0, 1000)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for index := 0; index < cap(hitDurations); index++ {
		started := time.Now()
		if _, ok := cache.Get("home", 1); !ok {
			t.Fatal("cache miss")
		}
		hitDurations = append(hitDurations, time.Since(started))
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	if value := percentile(hitDurations, .95); value > 25*time.Millisecond {
		t.Fatalf("cache hit p95=%s, budget=25ms", value)
	}
	if after.HeapAlloc > before.HeapAlloc+64<<20 {
		t.Fatalf("cache heap grew from %d to %d bytes", before.HeapAlloc, after.HeapAlloc)
	}

	db, err := database.Open(context.Background(), config.Database{Path: filepath.Join(t.TempDir(), "blog.sqlite"), BusyTimeout: config.Duration{Duration: time.Second}, CacheSizeKiB: 4096, ReadConnections: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := publishing.NewService(publishing.NewRepository(db))
	for index := 0; index < 30; index++ {
		article, err := service.CreateDraft(context.Background(), publishing.DraftInput{Title: "Search article " + string(rune('a'+index%26)), Slug: "search-article-" + string(rune('a'+index%26)) + "-" + string(rune('a'+index/26)), BodyMarkdown: "SQLite benchmark body"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.Publish(context.Background(), article.ID, article.LockVersion); err != nil {
			t.Fatal(err)
		}
	}
	search, err := discovery.NewService(discovery.NewRepository(db), discovery.Options{BaseURL: "https://example.test", SyncBatchSize: 50, MaxResults: 50, FeedLimit: 50, SitemapLimit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := search.SyncAllDirty(context.Background()); err != nil {
		t.Fatal(err)
	}
	searchDurations := make([]time.Duration, 0, 20)
	for index := 0; index < cap(searchDurations); index++ {
		started := time.Now()
		if _, err := search.Search(context.Background(), discovery.SearchQuery{Text: "SQLite", Limit: 20}); err != nil {
			t.Fatal(err)
		}
		searchDurations = append(searchDurations, time.Since(started))
	}
	if value := percentile(searchDurations, .95); value > 250*time.Millisecond {
		t.Fatalf("search p95=%s, budget=250ms", value)
	}
}

func percentile(values []time.Duration, fraction float64) time.Duration {
	copyOf := append([]time.Duration(nil), values...)
	sort.Slice(copyOf, func(i, j int) bool { return copyOf[i] < copyOf[j] })
	if len(copyOf) == 0 {
		return 0
	}
	index := int(float64(len(copyOf)-1) * fraction)
	return copyOf[index]
}
