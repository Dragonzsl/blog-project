//go:build phase3load

package app

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhushilin/blog-project/internal/discovery"
	"github.com/zhushilin/blog-project/internal/organization"
	"github.com/zhushilin/blog-project/internal/platform/config"
	"github.com/zhushilin/blog-project/internal/platform/database"
	"github.com/zhushilin/blog-project/internal/presentation"
)

const phase3LoadFixtureContents = 10_000

type phase3LoadSample struct {
	duration time.Duration
	status   int
	bytes    int64
	cache    string
	err      error
}

type phase3LoadResult struct {
	scenario          string
	repeat            int
	concurrency       int
	requests          int
	duration          time.Duration
	rps               float64
	p50               time.Duration
	p95               time.Duration
	p99               time.Duration
	errors            int
	statusFailures    int
	bytes             int64
	cacheHits         int
	cacheMisses       int
	cacheCoalesced    int
	readerWaitCount   int64
	readerWait        time.Duration
	writerWaitCount   int64
	writerWait        time.Duration
	heapDelta         int64
	goroutines        int
	readerConnections int
	statusBreakdown   string
}

// TestPhase3HTTPConcurrency is intentionally excluded from the normal suite.
// Run it with:
//
//	go test -tags 'fts5 sqlite_omit_load_extension phase3load' ./internal/app -run TestPhase3HTTPConcurrency -count=1 -v
//
// It starts the complete application router with 10,000 published articles in
// a temporary database and measures real HTTP requests. The default database
// read pool remains two connections, matching config.example.toml.
func TestPhase3HTTPConcurrency(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	cfg := config.Defaults()
	cfg.Storage.DataDir = filepath.Join(root, "data")
	cfg.Database.Path = filepath.Join(root, "data", "db", "blog.sqlite")
	cfg.Database.ReadConnections = phase3LoadEnvInt(t, "PHASE3_LOAD_READERS", 2)
	if cfg.Database.ReadConnections > 8 {
		t.Fatalf("PHASE3_LOAD_READERS must be between 1 and 8")
	}
	cfg.Discovery.BaseURL = "http://127.0.0.1"
	cfg.Discovery.SyncBatchSize = 500
	cfg.Security.AuthSecretFile = filepath.Join(root, "data", "secrets", "auth.key")
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))

	application, err := New(ctx, cfg, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := application.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}()

	seedPhase3HTTPFixture(t, application.database.Writer)
	if os.Getenv("PHASE3_LOAD_SKIP_TAXONOMY") != "1" {
		organizationService := organization.NewService(application.database)
		started := time.Now()
		rebuilt := 0
		for {
			count, complete, err := organizationService.RebuildPublicTaxonomy(ctx, 256)
			if err != nil {
				t.Fatal(err)
			}
			rebuilt += count
			if complete {
				break
			}
		}
		state, err := organizationService.PublicTaxonomyRebuildState(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("phase3 load taxonomy projection: status=%s records=%d duration=%s", state.Status, rebuilt, time.Since(started))
	} else {
		t.Log("phase3 load taxonomy projection skipped")
	}
	if os.Getenv("PHASE3_LOAD_SKIP_INDEX") != "1" {
		started := time.Now()
		synced, err := application.discovery.SyncAllDirty(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("phase3 load fixture: contents=%d search_documents_synced=%d duration=%s", phase3LoadFixtureContents, synced, time.Since(started))
	} else {
		t.Log("phase3 load fixture: search index synchronization skipped")
	}

	server := httptest.NewServer(application.server.Handler)
	defer server.Close()
	client := &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        256,
			MaxIdleConnsPerHost: 256,
			MaxConnsPerHost:     256,
			IdleConnTimeout:     60 * time.Second,
		},
	}

	if os.Getenv("PHASE3_LOAD_SKIP_PROBES") != "1" {
		probePhase3Queries(t, application)
		probePhase3Segments(t, application)
		probePhase3Home(t, client, server.URL)
	}
	warmPhase3HTTP(t, client, server.URL)
	levels := phase3LoadLevels(t)
	requestsPerLevel := phase3LoadEnvInt(t, "PHASE3_LOAD_REQUESTS", 256)
	if requestsPerLevel < 128 {
		requestsPerLevel = 128
	}
	coldRequests := phase3LoadEnvInt(t, "PHASE3_LOAD_COLD_REQUESTS", 32)
	repeats := phase3LoadEnvInt(t, "PHASE3_LOAD_REPEATS", 1)
	if repeats > 8 {
		t.Fatalf("PHASE3_LOAD_REPEATS must be between 1 and 8")
	}

	for repeatIndex := 0; repeatIndex < repeats; repeatIndex++ {
		for levelIndex, concurrency := range levels {
			for _, scenario := range phase3LoadScenarios(t) {
				requestCount := requestsPerLevel
				if scenario == "cold-article" {
					requestCount = coldRequests
				}
				paths := phase3LoadPaths(scenario, levelIndex, repeatIndex, requestCount)
				result := runPhase3HTTPLoad(t, application, client, server.URL, scenario, concurrency, paths)
				result.repeat = repeatIndex + 1
				t.Logf("phase3 http load: repeat=%d scenario=%s concurrency=%d requests=%d duration=%s rps=%.1f p50=%s p95=%s p99=%s errors=%d status_failures=%d status_breakdown=%s bytes=%d cache_hit=%d cache_miss=%d cache_coalesced=%d reader_wait=%d/%s writer_wait=%d/%s reader_open=%d heap_delta=%d goroutines=%d", result.repeat, result.scenario, result.concurrency, result.requests, result.duration, result.rps, result.p50, result.p95, result.p99, result.errors, result.statusFailures, result.statusBreakdown, result.bytes, result.cacheHits, result.cacheMisses, result.cacheCoalesced, result.readerWaitCount, result.readerWait, result.writerWaitCount, result.writerWait, result.readerConnections, result.heapDelta, result.goroutines)
			}
		}
	}
}

func probePhase3Home(t *testing.T, client *http.Client, baseURL string) {
	t.Helper()
	started := time.Now()
	response, err := client.Get(baseURL + "/")
	if err != nil {
		t.Logf("phase3 home probe: duration=%s error=%v", time.Since(started), err)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	response.Body.Close()
	t.Logf("phase3 home probe: status=%d duration=%s bytes=%d body_prefix=%q", response.StatusCode, time.Since(started), len(body), string(body))
}

func probePhase3Queries(t *testing.T, application *App) {
	t.Helper()
	organizationService := organization.NewService(application.database)
	probes := []struct {
		name string
		call func(context.Context) error
	}{
		{name: "published-cards", call: func(ctx context.Context) error {
			_, err := application.publishing.PublishedArticles(ctx, 20)
			return err
		}},
		{name: "public-article", call: func(ctx context.Context) error {
			_, err := application.publishing.PublicArticle(ctx, "phase3-load-00001")
			return err
		}},
		{name: "public-categories", call: func(ctx context.Context) error {
			_, err := organizationService.PublicCategories(ctx)
			return err
		}},
		{name: "public-tags", call: func(ctx context.Context) error {
			_, err := organizationService.PublicTags(ctx)
			return err
		}},
		{name: "navigation", call: func(ctx context.Context) error {
			_, err := organizationService.NavigationItems(ctx)
			return err
		}},
		{name: "archive-index", call: func(ctx context.Context) error {
			_, err := application.discovery.ArchiveIndex(ctx)
			return err
		}},
		{name: "search-chinese", call: func(ctx context.Context) error {
			_, err := application.discovery.SearchPage(ctx, discovery.SearchQuery{Text: "阶段三", PerPage: 20})
			return err
		}},
	}
	for _, probe := range probes {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		started := time.Now()
		err := probe.call(ctx)
		cancel()
		t.Logf("phase3 query probe: name=%s duration=%s error=%v", probe.name, time.Since(started), err)
		if err != nil {
			t.Logf("phase3 query probe failed: name=%s error=%v", probe.name, err)
		}
	}
	logPhase3QueryPlan(t, application.database, "taxonomy-category-page", `EXPLAIN QUERY PLAN SELECT content_id FROM public_taxonomy_members WHERE taxonomy_kind='category' AND taxonomy_public_id=? ORDER BY published_at DESC,content_id DESC LIMIT 20`, phase3LoadID(1))
	logPhase3QueryPlan(t, application.database, "taxonomy-tag-page", `EXPLAIN QUERY PLAN SELECT content_id FROM public_taxonomy_members WHERE taxonomy_kind='tag' AND taxonomy_public_id=? ORDER BY published_at DESC,content_id DESC LIMIT 20`, phase3LoadID(1001))
	logPhase3QueryPlan(t, application.database, "search-chinese-candidates", `EXPLAIN QUERY PLAN WITH candidates AS MATERIALIZED (SELECT content_id FROM search_grams WHERE gram=? INTERSECT SELECT content_id FROM search_grams WHERE gram=?) SELECT COUNT(*) FROM candidates`, "阶段", "段三")
	probePhase3ArticleSegments(t, application)
}

func probePhase3ArticleSegments(t *testing.T, application *App) {
	t.Helper()
	ctx := context.Background()
	started := time.Now()
	article, err := application.publishing.PublicArticle(ctx, "phase3-load-00001")
	t.Logf("phase3 article segment: name=public-article duration=%s error=%v", time.Since(started), err)
	if err != nil {
		return
	}
	started = time.Now()
	_, err = application.publishing.PublicArticleNavigationForArticle(ctx, article, 3)
	t.Logf("phase3 article segment: name=article-navigation duration=%s error=%v", time.Since(started), err)
	if article.Category != nil {
		tagIDs := make([][]byte, 0, len(article.Tags))
		for _, tag := range article.Tags {
			tagIDs = append(tagIDs, tag.PublicID)
		}
		started = time.Now()
		ids, ready, relatedErr := application.organization.PublicRelatedArticleIDs(ctx, article.ID, article.Category.PublicID, tagIDs, 3)
		t.Logf("phase3 article segment: name=related-ids duration=%s ready=%t rows=%d error=%v", time.Since(started), ready, len(ids), relatedErr)
		if relatedErr == nil && ready {
			started = time.Now()
			_, relatedErr = application.publishing.PublicArticleCardsByIDs(ctx, ids)
			t.Logf("phase3 article segment: name=related-cards duration=%s error=%v", time.Since(started), relatedErr)
		}
	}
	started = time.Now()
	if _, err := presentation.NewMarkdown().Render(phase3LoadBody()); err != nil {
		t.Logf("phase3 article segment: name=markdown duration=%s error=%v", time.Since(started), err)
	} else {
		t.Logf("phase3 article segment: name=markdown duration=%s error=<nil>", time.Since(started))
	}
}

func probePhase3Segments(t *testing.T, application *App) {
	t.Helper()
	ctx := context.Background()
	segments := []struct {
		name string
		call func(context.Context) error
	}{
		{name: "home-published-cards", call: func(ctx context.Context) error {
			_, err := application.publishing.PublishedArticles(ctx, 20)
			return err
		}},
		{name: "home-taxonomy-categories", call: func(ctx context.Context) error {
			_, err := application.organization.PublicCategories(ctx)
			return err
		}},
		{name: "home-archive", call: func(ctx context.Context) error {
			_, err := application.discovery.ArchiveIndex(ctx)
			return err
		}},
		{name: "home-about", call: func(ctx context.Context) error {
			_, err := application.publishing.PublicPageCard(ctx, "about")
			return err
		}},
		{name: "home-navigation", call: func(ctx context.Context) error {
			_, err := application.organization.PublicNavigation(ctx, "primary")
			return err
		}},
	}
	for _, segment := range segments {
		started := time.Now()
		err := segment.call(ctx)
		t.Logf("phase3 home segment: name=%s duration=%s error=%v", segment.name, time.Since(started), err)
	}
}

func logPhase3QueryPlan(t *testing.T, db *database.DB, name, statement string, args ...any) {
	t.Helper()
	rows, err := db.Reader.QueryContext(context.Background(), statement, args...)
	if err != nil {
		t.Logf("phase3 query plan: name=%s error=%v", name, err)
		return
	}
	defer rows.Close()
	parts := make([]string, 0, 8)
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Logf("phase3 query plan: name=%s scan_error=%v", name, err)
			return
		}
		parts = append(parts, detail)
	}
	if err := rows.Err(); err != nil {
		t.Logf("phase3 query plan: name=%s error=%v", name, err)
		return
	}
	t.Logf("phase3 query plan: name=%s detail=%s", name, strings.Join(parts, " | "))
}

func phase3LoadEnvInt(t *testing.T, name string, fallback int) int {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 {
		t.Fatalf("%s must be a positive integer, got %q", name, value)
	}
	return parsed
}

func phase3LoadLevels(t *testing.T) []int {
	t.Helper()
	value := strings.TrimSpace(os.Getenv("PHASE3_LOAD_LEVELS"))
	if value == "" {
		return []int{1, 2, 4, 8, 16, 32, 64, 128}
	}
	parts := strings.Split(value, ",")
	levels := make([]int, 0, len(parts))
	for _, part := range parts {
		level, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || level < 1 {
			t.Fatalf("PHASE3_LOAD_LEVELS must contain positive integers, got %q", value)
		}
		levels = append(levels, level)
	}
	return levels
}

func phase3LoadScenarios(t *testing.T) []string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv("PHASE3_LOAD_SCENARIOS"))
	if value == "" {
		return []string{"cache-articles", "cache-article", "cold-article"}
	}
	allowed := map[string]bool{
		"cache-articles": true,
		"cache-article":  true,
		"cold-article":   true,
		"search":         true,
	}
	parts := strings.Split(value, ",")
	scenarios := make([]string, 0, len(parts))
	for _, part := range parts {
		scenario := strings.TrimSpace(part)
		if !allowed[scenario] {
			t.Fatalf("PHASE3_LOAD_SCENARIOS contains unsupported scenario %q", scenario)
		}
		scenarios = append(scenarios, scenario)
	}
	return scenarios
}

func warmPhase3HTTP(t *testing.T, client *http.Client, baseURL string) {
	t.Helper()
	paths := []string{
		"/articles",
		"/posts/phase3-load-00001",
	}
	if os.Getenv("PHASE3_LOAD_WARM_SEARCH") == "1" {
		paths = append(paths, "/search?q="+url.QueryEscape("阶段三"))
	}
	for _, path := range paths {
		started := time.Now()
		response, err := client.Get(baseURL + path)
		if err != nil {
			t.Fatalf("warm %s: %v", path, err)
		}
		body, _ := io.ReadAll(io.LimitReader(response.Body, 16<<10))
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("warm %s: status=%d body=%q", path, response.StatusCode, string(body))
		}
		cache := response.Header.Get("X-Page-Cache")
		if cache == "" {
			cache = response.Header.Get("X-Search-Cache")
		}
		t.Logf("phase3 warm: path=%s status=%d duration=%s bytes=%d cache=%s", path, response.StatusCode, time.Since(started), len(body), cache)
	}
}

func phase3LoadPaths(scenario string, levelIndex, repeatIndex, count int) []string {
	paths := make([]string, count)
	for index := range paths {
		switch scenario {
		case "cache-articles":
			paths[index] = "/articles"
		case "cache-article":
			paths[index] = "/posts/phase3-load-00001"
		case "search":
			paths[index] = "/search?q=" + url.QueryEscape("阶段三")
		case "cold-article":
			articleNumber := 2 + repeatIndex*2048 + levelIndex*count + index
			if articleNumber > phase3LoadFixtureContents {
				articleNumber = 2 + (articleNumber % (phase3LoadFixtureContents - 1))
			}
			paths[index] = fmt.Sprintf("/posts/phase3-load-%05d", articleNumber)
		}
	}
	return paths
}

func runPhase3HTTPLoad(t *testing.T, application *App, client *http.Client, baseURL, scenario string, concurrency int, paths []string) phase3LoadResult {
	t.Helper()
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > len(paths) {
		concurrency = len(paths)
	}
	readBefore := application.database.Reader.Stats()
	writeBefore := application.database.Writer.Stats()
	runtime.GC()
	var heapBefore runtime.MemStats
	runtime.ReadMemStats(&heapBefore)

	samples := make([]phase3LoadSample, len(paths))
	startGate := make(chan struct{})
	targetRPS := 0
	if value := strings.TrimSpace(os.Getenv("PHASE3_LOAD_TARGET_RPS")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 10_000 {
			t.Fatalf("PHASE3_LOAD_TARGET_RPS must be between 1 and 10000, got %q", value)
		}
		targetRPS = parsed
	}
	var scheduledRequest atomic.Int64
	loadStarted := time.Now()
	var wait sync.WaitGroup
	wait.Add(concurrency)
	for worker := 0; worker < concurrency; worker++ {
		go func(worker int) {
			defer wait.Done()
			<-startGate
			for index := worker; index < len(paths); index += concurrency {
				if targetRPS > 0 {
					slot := scheduledRequest.Add(1) - 1
					scheduled := loadStarted.Add(time.Duration(slot) * time.Second / time.Duration(targetRPS))
					if delay := time.Until(scheduled); delay > 0 {
						timer := time.NewTimer(delay)
						<-timer.C
					}
				}
				requestStarted := time.Now()
				response, err := client.Get(baseURL + paths[index])
				sample := phase3LoadSample{duration: time.Since(requestStarted), err: err}
				if response != nil {
					sample.status = response.StatusCode
					sample.cache = response.Header.Get("X-Page-Cache")
					if sample.cache == "" {
						sample.cache = response.Header.Get("X-Search-Cache")
					}
					sample.bytes, _ = io.Copy(io.Discard, response.Body)
					response.Body.Close()
				}
				samples[index] = sample
			}
		}(worker)
	}
	close(startGate)
	wait.Wait()
	duration := time.Since(loadStarted)
	runtime.GC()
	var heapAfter runtime.MemStats
	runtime.ReadMemStats(&heapAfter)
	readAfter := application.database.Reader.Stats()
	writeAfter := application.database.Writer.Stats()

	durations := make([]time.Duration, 0, len(samples))
	result := phase3LoadResult{
		scenario:          scenario,
		concurrency:       concurrency,
		requests:          len(samples),
		duration:          duration,
		rps:               float64(len(samples)) / duration.Seconds(),
		heapDelta:         int64(heapAfter.HeapAlloc) - int64(heapBefore.HeapAlloc),
		goroutines:        runtime.NumGoroutine(),
		readerConnections: readAfter.MaxOpenConnections,
		readerWaitCount:   readAfter.WaitCount - readBefore.WaitCount,
		readerWait:        readAfter.WaitDuration - readBefore.WaitDuration,
		writerWaitCount:   writeAfter.WaitCount - writeBefore.WaitCount,
		writerWait:        writeAfter.WaitDuration - writeBefore.WaitDuration,
	}
	statusCounts := make(map[int]int)
	for _, sample := range samples {
		durations = append(durations, sample.duration)
		result.bytes += sample.bytes
		if sample.err != nil {
			result.errors++
		}
		if sample.status != http.StatusOK {
			result.statusFailures++
		}
		if sample.status != 0 {
			statusCounts[sample.status]++
		}
		switch sample.cache {
		case "HIT":
			result.cacheHits++
		case "MISS":
			result.cacheMisses++
		case "COALESCED":
			result.cacheCoalesced++
		}
	}
	sort.Slice(durations, func(left, right int) bool { return durations[left] < durations[right] })
	result.p50 = phase3LoadPercentile(durations, 0.50)
	result.p95 = phase3LoadPercentile(durations, 0.95)
	result.p99 = phase3LoadPercentile(durations, 0.99)
	result.statusBreakdown = phase3LoadStatusBreakdown(statusCounts)
	return result
}

func phase3LoadStatusBreakdown(counts map[int]int) string {
	if len(counts) == 0 {
		return "none"
	}
	statuses := make([]int, 0, len(counts))
	for status := range counts {
		statuses = append(statuses, status)
	}
	sort.Ints(statuses)
	parts := make([]string, 0, len(statuses))
	for _, status := range statuses {
		parts = append(parts, fmt.Sprintf("%d:%d", status, counts[status]))
	}
	return strings.Join(parts, ",")
}

func phase3LoadPercentile(values []time.Duration, fraction float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	index := int(float64(len(values)-1) * fraction)
	return values[index]
}

func seedPhase3HTTPFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, time.September, 5, 0, 0, 0, 0, time.UTC).UnixMilli()
	const categoryCount = 100
	const tagCount = 500
	categoryPublicIDs := make([][]byte, categoryCount)
	categoryIDs := make([]int64, categoryCount)
	for index := 0; index < categoryCount; index++ {
		publicID := phase3LoadID(int64(1 + index))
		result, err := db.ExecContext(ctx, "INSERT INTO categories(public_id,slug,slug_key,name,description,sort_order,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)", publicID, fmt.Sprintf("scale-category-%03d", index+1), fmt.Sprintf("scale-category-%03d", index+1), fmt.Sprintf("规模分类 %03d", index+1), "阶段三 HTTP 并发测试", index, now, now)
		if err != nil {
			t.Fatal(err)
		}
		categoryID, err := result.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		categoryPublicIDs[index] = publicID
		categoryIDs[index] = categoryID
	}
	tagPublicIDs := make([][]byte, tagCount)
	tagIDs := make([]int64, tagCount)
	for index := 0; index < tagCount; index++ {
		publicID := phase3LoadID(int64(1001 + index))
		result, err := db.ExecContext(ctx, "INSERT INTO tags(public_id,slug,slug_key,name,description,created_at,updated_at) VALUES(?,?,?,?,?,?,?)", publicID, fmt.Sprintf("scale-tag-%03d", index+1), fmt.Sprintf("scale-tag-%03d", index+1), fmt.Sprintf("规模标签 %03d", index+1), "阶段三 HTTP 并发测试", now, now)
		if err != nil {
			t.Fatal(err)
		}
		tagID, err := result.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		tagPublicIDs[index] = publicID
		tagIDs[index] = tagID
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	rollback := true
	defer func() {
		if rollback {
			_ = tx.Rollback()
		}
	}()
	contentInsert, err := tx.PrepareContext(ctx, "INSERT INTO contents(public_id,kind,status,slug,slug_key,title,excerpt,seo_title,seo_description,body_markdown,published_at,lock_version,created_at,updated_at,published_slug,published_slug_key,category_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)")
	if err != nil {
		t.Fatal(err)
	}
	defer contentInsert.Close()
	revisionInsert, err := tx.PrepareContext(ctx, "INSERT INTO content_revisions(public_id,content_id,revision_number,title,slug,excerpt,seo_title,seo_description,body_markdown,reason,is_publication_checkpoint,created_at,category_public_id,tag_public_ids_json) VALUES(?,?,?,?,?,?,?,?,?,'import',1,?,?,?)")
	if err != nil {
		t.Fatal(err)
	}
	defer revisionInsert.Close()
	contentUpdate, err := tx.PrepareContext(ctx, "UPDATE contents SET current_revision_id=?,status='published',published_revision_id=?,published_at=?,published_slug=?,published_slug_key=? WHERE id=?")
	if err != nil {
		t.Fatal(err)
	}
	defer contentUpdate.Close()
	tagInsert, err := tx.PrepareContext(ctx, "INSERT INTO content_tags(content_id,tag_id) VALUES(?,?)")
	if err != nil {
		t.Fatal(err)
	}
	defer tagInsert.Close()
	body := phase3LoadBody()
	for index := 0; index < phase3LoadFixtureContents; index++ {
		number := index + 1
		slug := fmt.Sprintf("phase3-load-%05d", number)
		title := fmt.Sprintf("阶段三并发测试文章 %05d", number)
		publishedAt := now + int64(index)
		categoryIndex := index % categoryCount
		tagTotal := 1 + index%4
		encodedTags := make([]string, 0, tagTotal)
		for tagOffset := 0; tagOffset < tagTotal; tagOffset++ {
			tagIndex := (index*7 + tagOffset*31) % tagCount
			encodedTags = append(encodedTags, fmt.Sprintf("%x", tagPublicIDs[tagIndex]))
		}
		tagJSON := "[\"" + strings.Join(encodedTags, "\",\"") + "\"]"
		result, err := contentInsert.ExecContext(ctx, phase3LoadID(int64(1000+number)), "article", "draft", slug, slug, title, "阶段三并发列表摘要", "", "", body, nil, 1, publishedAt, publishedAt, nil, nil, categoryIDs[categoryIndex])
		if err != nil {
			t.Fatal(err)
		}
		contentID, err := result.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		revision, err := revisionInsert.ExecContext(ctx, phase3LoadID(int64(20_000+number)), contentID, 1, title, slug, "阶段三并发列表摘要", "", "", body, publishedAt, categoryPublicIDs[categoryIndex], tagJSON)
		if err != nil {
			t.Fatal(err)
		}
		revisionID, err := revision.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := contentUpdate.ExecContext(ctx, revisionID, revisionID, publishedAt, slug, slug, contentID); err != nil {
			t.Fatal(err)
		}
		for tagOffset := 0; tagOffset < tagTotal; tagOffset++ {
			tagIndex := (index*7 + tagOffset*31) % tagCount
			if _, err := tagInsert.ExecContext(ctx, contentID, tagIDs[tagIndex]); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	rollback = false
}

func phase3LoadBody() string {
	return strings.Repeat("阶段三规模测试正文，用于测量 10000 篇文章下的公开页面并发、Markdown 渲染和数据库读取瓶颈。\n", 48)
}

func phase3LoadID(value int64) []byte {
	result := make([]byte, 16)
	binary.BigEndian.PutUint64(result[:8], 0x7068617365336c64)
	binary.BigEndian.PutUint64(result[8:], uint64(value))
	return result
}
