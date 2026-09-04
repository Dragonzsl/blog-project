package comments

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhushilin/blog-project/internal/platform/config"
	"github.com/zhushilin/blog-project/internal/platform/database"
	"github.com/zhushilin/blog-project/internal/platform/publicwrite"
	"github.com/zhushilin/blog-project/internal/presentation"
	"github.com/zhushilin/blog-project/internal/publishing"
)

func TestServiceCreatesModeratesAndSanitizesComment(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, config.Database{Path: filepath.Join(t.TempDir(), "blog.sqlite"), BusyTimeout: config.Duration{Duration: time.Second}, CacheSizeKiB: 4096, ReadConnections: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	publishingService := publishing.NewService(publishing.NewRepository(db))
	article, err := publishingService.CreateDraft(ctx, publishing.DraftInput{Title: "测试文章", Slug: "hello", BodyMarkdown: "正文"})
	if err != nil {
		t.Fatal(err)
	}
	article, err = publishingService.Publish(ctx, article.ID, article.LockVersion)
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(db, publishingService, presentation.NewMarkdown(), true)
	comment, err := service.Create(ctx, article.Slug, Input{DisplayName: "访客", Email: " A@Example.COM ", Body: "你好 <script>alert(1)</script>"})
	if err != nil {
		t.Fatal(err)
	}
	if comment.Status != "pending" || strings.Contains(comment.BodyHTML, "<script") {
		t.Fatalf("comment=%+v", comment)
	}
	if len(comment.PublicID) != 16 {
		t.Fatalf("public id length=%d", len(comment.PublicID))
	}
	if got, err := service.Approved(ctx, article.ID); err != nil || len(got) != 0 {
		t.Fatalf("approved=%v err=%v", got, err)
	}
	if err := service.Moderate(ctx, comment.ID, "approved"); err != nil {
		t.Fatal(err)
	}
	got, err := service.Approved(ctx, article.ID)
	if err != nil || len(got) != 1 || got[0].DisplayName != "访客" {
		t.Fatalf("approved=%v err=%v", got, err)
	}
	if _, err := service.Create(ctx, article.Slug, Input{DisplayName: "访客", Website: "javascript:alert(1)", Body: "x"}); err == nil {
		t.Fatal("unsafe website accepted")
	}
	if _, err := service.Create(ctx, "missing", Input{DisplayName: "访客", Body: "x"}); err != ErrNotFound {
		t.Fatalf("missing err=%v", err)
	}
}

func TestServicePublicWriteIdempotencyAndFingerprint(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, config.Database{Path: filepath.Join(t.TempDir(), "blog.sqlite"), BusyTimeout: config.Duration{Duration: time.Second}, CacheSizeKiB: 4096, ReadConnections: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	publishingService := publishing.NewService(publishing.NewRepository(db))
	article, err := publishingService.CreateDraft(ctx, publishing.DraftInput{Title: "幂等文章", Slug: "idempotent", BodyMarkdown: "正文"})
	if err != nil {
		t.Fatal(err)
	}
	if article, err = publishingService.Publish(ctx, article.ID, article.LockVersion); err != nil {
		t.Fatal(err)
	}
	service := NewService(db, publishingService, presentation.NewMarkdown(), true, []byte("comment-secret"))
	input := Input{DisplayName: "访客", Email: "visitor@example.com", Body: "同一条评论"}
	request := WriteRequest{IdempotencyKey: "comment-key", ClientIdentity: "198.51.100.10"}
	first, err := service.CreateWithRequest(ctx, article.Slug, input, request)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := service.CreateWithRequest(ctx, article.Slug, input, request)
	if err != nil || replayed.ID != first.ID {
		t.Fatalf("replayed=%+v err=%v", replayed, err)
	}
	if _, err := service.CreateWithRequest(ctx, article.Slug, Input{DisplayName: "访客", Body: "另一条评论"}, request); !errors.Is(err, publicwrite.ErrIdempotencyConflict) {
		t.Fatalf("conflict error=%v", err)
	}
	noKey := WriteRequest{ClientIdentity: "198.51.100.11"}
	if _, err := service.CreateWithRequest(ctx, article.Slug, input, noKey); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateWithRequest(ctx, article.Slug, input, noKey); !errors.Is(err, publicwrite.ErrDuplicateRequest) {
		t.Fatalf("fingerprint error=%v", err)
	}
	var count int
	if err := db.Reader.QueryRowContext(ctx, "SELECT count(*) FROM comments").Scan(&count); err != nil || count != 2 {
		t.Fatalf("comment count=%d err=%v", count, err)
	}
}
