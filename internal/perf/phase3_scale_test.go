package perf

import (
	"context"
	"encoding/binary"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/zhushilin/blog-project/internal/platform/config"
	"github.com/zhushilin/blog-project/internal/platform/database"
	"github.com/zhushilin/blog-project/internal/platform/pagination"
	"github.com/zhushilin/blog-project/internal/publishing"
)

const phase3FixtureContents = 10_000

func TestPhase3ScaleProjections(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, config.Database{
		Path:            filepath.Join(t.TempDir(), "blog.sqlite"),
		BusyTimeout:     config.Duration{Duration: time.Second},
		CacheSizeKiB:    4096,
		ReadConnections: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	contentIDs := seedPhase3Fixture(t, db)
	service := publishing.NewService(publishing.NewRepository(db))

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	started := time.Now()
	articles, err := service.PublishedArticles(ctx, 50)
	listDuration := time.Since(started)
	if err != nil {
		t.Fatal(err)
	}
	if len(articles) != 50 {
		t.Fatalf("published list length=%d, want 50", len(articles))
	}
	for _, article := range articles {
		if article.BodyMarkdown != "" {
			t.Fatal("published list projection loaded body_markdown")
		}
	}

	started = time.Now()
	adminPage, err := service.AdminContentsPage(ctx, "article", publishing.AdminContentFilter{}, pagination.Request{Page: 1, PerPage: 50})
	adminDuration := time.Since(started)
	if err != nil {
		t.Fatal(err)
	}
	if adminPage.Pagination.Total != phase3FixtureContents || len(adminPage.Contents) != 50 {
		t.Fatalf("admin page total=%d items=%d", adminPage.Pagination.Total, len(adminPage.Contents))
	}
	if adminPage.Contents[0].BodyMarkdown != "" || adminPage.Contents[0].Category == nil || len(adminPage.Contents[0].Tags) != 1 {
		t.Fatalf("admin projection=%+v", adminPage.Contents[0])
	}

	started = time.Now()
	cards, err := service.PublicArticleCardsByIDs(ctx, contentIDs[:50])
	cardsDuration := time.Since(started)
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 50 || cards[0].BodyMarkdown != "" {
		t.Fatalf("public cards length=%d first body bytes=%d", len(cards), len(cards[0].BodyMarkdown))
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	heapDelta := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	if heapDelta > 32<<20 {
		t.Fatalf("list projection heap grew by %d bytes", heapDelta)
	}
	queryBudget := phase3ScaleQueryBudget()
	if listDuration > queryBudget || adminDuration > queryBudget || cardsDuration > queryBudget {
		t.Fatalf("scale query too slow: published=%s admin=%s cards=%s", listDuration, adminDuration, cardsDuration)
	}
	t.Logf("phase3 scale: contents=%d body_bytes=%d published_page_size=50 published=%s admin=%s cards=%s heap_delta=%d bytes", phase3FixtureContents, len(phase3FixtureBody()), listDuration, adminDuration, cardsDuration, heapDelta)
}

func seedPhase3Fixture(t *testing.T, db *database.DB) []int64 {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, time.September, 5, 0, 0, 0, 0, time.UTC).UnixMilli()
	categoryPublicID := phase3ID(1)
	tagPublicID := phase3ID(2)
	result, err := db.Writer.ExecContext(ctx, `INSERT INTO categories(public_id,slug,slug_key,name,description,sort_order,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, categoryPublicID, "scale-category", "scale-category", "规模测试", "阶段三固定数据", 1, now, now)
	if err != nil {
		t.Fatal(err)
	}
	categoryID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Writer.ExecContext(ctx, `INSERT INTO tags(public_id,slug,slug_key,name,description,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, tagPublicID, "scale-tag", "scale-tag", "规模标签", "阶段三固定数据", now, now); err != nil {
		t.Fatal(err)
	}
	var tagID int64
	if err := db.Writer.QueryRowContext(ctx, "SELECT id FROM tags WHERE public_id=?", tagPublicID).Scan(&tagID); err != nil {
		t.Fatal(err)
	}

	tx, err := db.Writer.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	contentInsert, err := tx.PrepareContext(ctx, `INSERT INTO contents(public_id,kind,status,slug,slug_key,title,excerpt,seo_title,seo_description,body_markdown,published_at,lock_version,created_at,updated_at,published_slug,published_slug_key,category_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	defer contentInsert.Close()
	revisionInsert, err := tx.PrepareContext(ctx, `INSERT INTO content_revisions(public_id,content_id,revision_number,title,slug,excerpt,seo_title,seo_description,body_markdown,reason,is_publication_checkpoint,created_at,category_public_id,tag_public_ids_json) VALUES(?,?,?,?,?,?,?,?,?,'import',1,?,?,?)`)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	defer revisionInsert.Close()
	contentUpdate, err := tx.PrepareContext(ctx, "UPDATE contents SET current_revision_id=?,status='published',published_revision_id=?,published_at=?,published_slug=?,published_slug_key=? WHERE id=?")
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	defer contentUpdate.Close()
	tagInsert, err := tx.PrepareContext(ctx, "INSERT INTO content_tags(content_id,tag_id) VALUES(?,?)")
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	defer tagInsert.Close()
	body := phase3FixtureBody()
	tagJSON := fmt.Sprintf(`["%x"]`, tagPublicID)
	contentIDs := make([]int64, 0, phase3FixtureContents)
	for index := 0; index < phase3FixtureContents; index++ {
		number := index + 1
		slug := fmt.Sprintf("phase3-scale-%05d", number)
		title := fmt.Sprintf("阶段三规模文章 %05d", number)
		publishedAt := now + int64(index)
		result, err := contentInsert.ExecContext(ctx, phase3ID(int64(1000+number)), "article", "draft", slug, slug, title, "阶段三规模列表摘要", "", "", body, nil, 1, publishedAt, publishedAt, nil, nil, categoryID)
		if err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		contentID, err := result.LastInsertId()
		if err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		revision, err := revisionInsert.ExecContext(ctx, phase3ID(int64(20_000+number)), contentID, 1, title, slug, "阶段三规模列表摘要", "", "", body, publishedAt, categoryPublicID, tagJSON)
		if err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		revisionID, err := revision.LastInsertId()
		if err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if _, err := contentUpdate.ExecContext(ctx, revisionID, revisionID, publishedAt, slug, slug, contentID); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if _, err := tagInsert.ExecContext(ctx, contentID, tagID); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		contentIDs = append(contentIDs, contentID)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Writer.ExecContext(ctx, "DELETE FROM search_dirty"); err != nil {
		t.Fatal(err)
	}
	return contentIDs
}

func phase3FixtureBody() string {
	return strings.Repeat("阶段三规模测试正文，用于确认列表投影不会把大正文带回应用层。\n", 64)
}

func phase3ID(value int64) []byte {
	result := make([]byte, 16)
	binary.BigEndian.PutUint64(result[:8], 0x7068617365330000)
	binary.BigEndian.PutUint64(result[8:], uint64(value))
	return result
}
