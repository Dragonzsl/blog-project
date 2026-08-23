package publishing

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestRevisionSnapshotScheduleUnpublishAndTrashLifecycle(t *testing.T) {
	ctx := context.Background()
	baseService, _ := newPublishingTestService(t)
	service := NewService(baseService.repository, Options{SchedulerBatchSize: 10, SnapshotInterval: 15 * time.Second, RevisionLimit: 10, TrashRetention: 30 * 24 * time.Hour})
	now := time.Date(2026, time.August, 23, 8, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }

	content, err := service.CreateDraft(ctx, DraftInput{Title: "发布检查点", Slug: "lifecycle", BodyMarkdown: "第一版"})
	if err != nil {
		t.Fatal(err)
	}
	content, err = service.Publish(ctx, content.ID, content.LockVersion)
	if err != nil {
		t.Fatal(err)
	}
	checkpointID := content.PublishedRevisionID

	for index := 1; index <= 12; index++ {
		now = now.Add(time.Minute)
		content, err = service.UpdateDraft(ctx, content.ID, content.LockVersion, DraftInput{Title: fmt.Sprintf("编辑 %02d", index), Slug: "lifecycle", BodyMarkdown: fmt.Sprintf("正文 %02d", index)})
		if err != nil {
			t.Fatal(err)
		}
	}
	revisions, err := service.Revisions(ctx, "article", content.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(revisions) != 11 {
		t.Fatalf("revision count=%d, want ten ordinary revisions plus checkpoint", len(revisions))
	}
	checkpointFound := false
	for _, revision := range revisions {
		if revision.BodyMarkdown != "" {
			t.Fatal("revision index loaded full Markdown bodies")
		}
		if revision.ID == checkpointID && revision.IsPublicationCheckpoint {
			checkpointFound = true
		}
	}
	if !checkpointFound {
		t.Fatal("published checkpoint was pruned")
	}

	snapshot := EditingSnapshot{ContentID: content.ID, BaseLockVersion: content.LockVersion, BrowserVersion: 100, Input: DraftInput{Title: "未完成标题", Slug: "lifecycle", BodyMarkdown: "突然断线前的正文"}}
	if err := service.SaveEditingSnapshot(ctx, "article", snapshot); err != nil {
		t.Fatal(err)
	}
	loadedSnapshot, err := service.EditingSnapshot(ctx, "article", content.ID)
	if err != nil || loadedSnapshot.Input.BodyMarkdown != "突然断线前的正文" {
		t.Fatalf("snapshot=%+v err=%v", loadedSnapshot, err)
	}
	afterSnapshotRevisions, err := service.Revisions(ctx, "article", content.ID)
	if err != nil || len(afterSnapshotRevisions) != len(revisions) {
		t.Fatalf("editing snapshot created a formal revision: before=%d after=%d err=%v", len(revisions), len(afterSnapshotRevisions), err)
	}
	if err := service.SaveEditingSnapshot(ctx, "article", snapshot); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale browser snapshot error=%v", err)
	}
	content, err = service.UpdateDraft(ctx, content.ID, content.LockVersion, DraftInput{Title: "正式保存", Slug: "lifecycle", BodyMarkdown: "正式正文"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.EditingSnapshot(ctx, "article", content.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("snapshot survived formal save: %v", err)
	}

	content, err = service.RestoreRevision(ctx, "article", content.ID, checkpointID, content.LockVersion)
	if err != nil {
		t.Fatal(err)
	}
	if content.Title != "发布检查点" || content.Slug != "lifecycle" {
		t.Fatalf("restored content=%+v", content)
	}
	revisions, err = service.Revisions(ctx, "article", content.ID)
	if err != nil || revisions[0].Reason != "restore" {
		t.Fatalf("restored revision=%+v err=%v", revisions, err)
	}
	if err := service.SaveEditingSnapshot(ctx, "article", EditingSnapshot{ContentID: content.ID, BaseLockVersion: content.LockVersion, BrowserVersion: 200, Input: DraftInput{Title: "生命周期前快照", Slug: "lifecycle", BodyMarkdown: "仍需恢复"}}); err != nil {
		t.Fatal(err)
	}

	content, err = service.Unpublish(ctx, "article", content.ID, content.LockVersion)
	if err != nil || content.Status != "draft" || content.WithdrawnAt == nil {
		t.Fatalf("unpublished content=%+v err=%v", content, err)
	}
	if _, err := service.PublicArticle(ctx, "lifecycle"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unpublished article remains public: %v", err)
	}
	rebasedSnapshot, err := service.EditingSnapshot(ctx, "article", content.ID)
	if err != nil || rebasedSnapshot.BaseLockVersion != content.LockVersion || rebasedSnapshot.Input.BodyMarkdown != "仍需恢复" {
		t.Fatalf("lifecycle did not rebase editing snapshot: snapshot=%+v err=%v", rebasedSnapshot, err)
	}

	publishAt := now.Add(2 * time.Hour)
	content, err = service.Schedule(ctx, "article", content.ID, content.LockVersion, publishAt)
	if err != nil || content.Status != "scheduled" || content.ScheduledAt == nil {
		t.Fatalf("scheduled content=%+v err=%v", content, err)
	}
	content, err = service.CancelSchedule(ctx, "article", content.ID, content.LockVersion)
	if err != nil || content.Status != "draft" || content.ScheduledAt != nil {
		t.Fatalf("cancelled schedule=%+v err=%v", content, err)
	}
	content, err = service.Schedule(ctx, "article", content.ID, content.LockVersion, publishAt)
	if err != nil {
		t.Fatal(err)
	}
	if published, _, err := service.ProcessLifecycle(ctx); err != nil || published != 0 {
		t.Fatalf("early lifecycle published=%d err=%v", published, err)
	}
	if _, err := service.PublicArticle(ctx, "lifecycle"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("scheduled article became public early: %v", err)
	}
	now = publishAt.Add(time.Second)
	if published, _, err := service.ProcessLifecycle(ctx); err != nil || published != 1 {
		t.Fatalf("due lifecycle published=%d err=%v", published, err)
	}
	content, err = service.Article(ctx, content.ID)
	if err != nil || content.Status != "published" || content.ScheduledAt != nil {
		t.Fatalf("due content=%+v err=%v", content, err)
	}
	dueRevision, err := service.Revision(ctx, "article", content.ID, content.PublishedRevisionID)
	if err != nil || !dueRevision.IsPublicationCheckpoint {
		t.Fatalf("scheduled publication checkpoint=%+v err=%v", dueRevision, err)
	}
	if published, _, err := service.ProcessLifecycle(ctx); err != nil || published != 0 {
		t.Fatalf("scheduled publish was not idempotent: published=%d err=%v", published, err)
	}

	if err := service.Trash(ctx, "article", content.ID, content.LockVersion); err != nil {
		t.Fatal(err)
	}
	trashed, err := service.TrashedContents(ctx)
	if err != nil || len(trashed) != 1 || trashed[0].Status != "draft" {
		t.Fatalf("trash=%+v err=%v", trashed, err)
	}
	content, err = service.RestoreFromTrash(ctx, content.ID)
	if err != nil || content.Status != "draft" || content.TrashedAt != nil {
		t.Fatalf("restored trash=%+v err=%v", content, err)
	}
	if err := service.Trash(ctx, "article", content.ID, content.LockVersion); err != nil {
		t.Fatal(err)
	}
	now = now.Add(31 * 24 * time.Hour)
	if _, purged, err := service.ProcessLifecycle(ctx); err != nil || purged != 1 {
		t.Fatalf("purged=%d err=%v", purged, err)
	}
	if _, err := service.Article(ctx, content.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("purged content error=%v", err)
	}
	if _, err := service.CreateDraft(ctx, DraftInput{Title: "占用历史路径", Slug: "lifecycle"}); !errors.Is(err, ErrSlugUnavailable) {
		t.Fatalf("historical path was reused: %v", err)
	}
}
