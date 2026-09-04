package operations

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhushilin/blog-project/internal/platform/config"
	"github.com/zhushilin/blog-project/internal/platform/database"
)

func TestTaskQueueClaimsOnceAndCompletes(t *testing.T) {
	ctx := context.Background()
	db := openTaskDatabase(t)
	queue := NewTaskQueue(db, TaskQueueOptions{Now: func() time.Time { return time.UnixMilli(1000).UTC() }})
	if err := queue.Enqueue(ctx, Task{Kind: "core:test", PayloadVersion: 1, Payload: []byte(`{"ok":true}`), IdempotencyKey: "task-once", AvailableAt: time.UnixMilli(1000)}); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	selector := func(Task) (TaskHandler, bool) {
		return func(context.Context, Task) error { calls.Add(1); return nil }, true
	}
	var wait sync.WaitGroup
	results := make(chan bool, 2)
	for i := 0; i < 2; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			processed, err := queue.ProcessOne(ctx, selector)
			if err != nil {
				t.Errorf("ProcessOne() error = %v", err)
			}
			results <- processed
		}()
	}
	wait.Wait()
	close(results)
	var processed int
	for result := range results {
		if result {
			processed++
		}
	}
	if processed != 1 || calls.Load() != 1 {
		t.Fatalf("processed=%d calls=%d, want one", processed, calls.Load())
	}
	task, err := queue.Get(ctx, 1)
	if err != nil || task.Status != "succeeded" || task.Attempts != 1 {
		t.Fatalf("task=%+v err=%v", task, err)
	}
}

func TestTaskQueueRetriesWithBoundAndAuditsManualRetry(t *testing.T) {
	ctx := context.Background()
	db := openTaskDatabase(t)
	now := time.UnixMilli(2000).UTC()
	queue := NewTaskQueue(db, TaskQueueOptions{Now: func() time.Time { return now }, RetryBase: time.Nanosecond, RetryMax: time.Nanosecond})
	if err := queue.Enqueue(ctx, Task{Kind: "core:failing", PayloadVersion: 1, Payload: []byte(`{}`), IdempotencyKey: "task-fails", AvailableAt: now}); err != nil {
		t.Fatal(err)
	}
	selector := func(Task) (TaskHandler, bool) {
		return func(context.Context, Task) error { return errors.New("password=secret@example.com") }, true
	}
	for attempt := 0; attempt < DefaultTaskMaxAttempts; attempt++ {
		if _, err := db.Writer.ExecContext(ctx, "UPDATE jobs SET available_at=0"); err != nil {
			t.Fatal(err)
		}
		if _, err := queue.ProcessOne(ctx, selector); err == nil {
			t.Fatal("failing task unexpectedly succeeded")
		}
	}
	task, err := queue.Get(ctx, 1)
	if err != nil || task.Status != "failed" || task.Attempts != DefaultTaskMaxAttempts {
		t.Fatalf("task=%+v err=%v", task, err)
	}
	if task.LastError == "" || task.LastError == "password=secret@example.com" || len(task.LastError) > 512 {
		t.Fatalf("unsafe last error=%q", task.LastError)
	}
	if err := queue.Retry(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	reopened, err := queue.Get(ctx, task.ID)
	if err != nil || reopened.Status != "pending" || reopened.Attempts != 0 {
		t.Fatalf("reopened=%+v err=%v", reopened, err)
	}
	var auditCount int
	if err := db.Reader.QueryRowContext(ctx, "SELECT count(*) FROM audit_entries WHERE action='operations.task.retried'").Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("audit count=%d err=%v", auditCount, err)
	}
}

func TestTaskQueueRecoversExpiredLease(t *testing.T) {
	ctx := context.Background()
	db := openTaskDatabase(t)
	queue := NewTaskQueue(db, TaskQueueOptions{Now: func() time.Time { return time.UnixMilli(5000).UTC() }})
	if err := queue.Enqueue(ctx, Task{Kind: "core:lease", PayloadVersion: 1, Payload: []byte(`{}`), IdempotencyKey: "task-lease", AvailableAt: time.UnixMilli(1)}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Writer.ExecContext(ctx, "UPDATE jobs SET status='running',attempts=1,lease_expires_at=1 WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	if err := queue.RecoverExpired(ctx); err != nil {
		t.Fatal(err)
	}
	task, err := queue.Get(ctx, 1)
	if err != nil || task.Status != "pending" || task.LeaseExpiresAt != nil {
		t.Fatalf("task=%+v err=%v", task, err)
	}
}

func TestTaskQueueStopsRetryingPermanentError(t *testing.T) {
	ctx := context.Background()
	db := openTaskDatabase(t)
	queue := NewTaskQueue(db, TaskQueueOptions{Now: func() time.Time { return time.UnixMilli(7000).UTC() }})
	if err := queue.Enqueue(ctx, Task{Kind: "core:permanent", PayloadVersion: 1, Payload: []byte(`{}`), IdempotencyKey: "permanent-task", AvailableAt: time.UnixMilli(1)}); err != nil {
		t.Fatal(err)
	}
	processed, err := queue.ProcessOne(ctx, func(Task) (TaskHandler, bool) {
		return func(context.Context, Task) error { return Permanent(errors.New("external endpoint is blocked")) }, true
	})
	if !processed || err == nil {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
	task, err := queue.Get(ctx, 1)
	if err != nil || task.Status != "failed" || task.Attempts != 1 {
		t.Fatalf("task=%+v err=%v", task, err)
	}
}

func TestSafeErrorRedactsIPv6Address(t *testing.T) {
	message := SafeError(errors.New("dial tcp [2001:db8::1]:443: connection refused"))
	if strings.Contains(message, "2001:db8") || strings.Contains(message, "::1") {
		t.Fatalf("IPv6 address leaked from safe error: %q", message)
	}
}

func openTaskDatabase(t *testing.T) *database.DB {
	t.Helper()
	db, err := database.Open(context.Background(), config.Database{Path: filepath.Join(t.TempDir(), "blog.sqlite"), BusyTimeout: config.Duration{Duration: time.Second}, CacheSizeKiB: 4096, ReadConnections: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
