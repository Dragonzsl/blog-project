package publicwrite

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhushilin/blog-project/internal/platform/config"
	"github.com/zhushilin/blog-project/internal/platform/database"
)

func TestGuardBindsIdempotencyToRequestAndStoresOnlyBoundedResult(t *testing.T) {
	ctx := context.Background()
	db := openGuardDatabase(t)
	now := time.UnixMilli(1000).UTC()
	guard := NewGuard(db, []byte("test-secret"), Options{Now: func() time.Time { return now }})
	claimAndComplete := func(key, fingerprint string) Claim {
		tx, err := db.Writer.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		claim, err := guard.ClaimTx(ctx, tx, "comments.create", key, fingerprint)
		if err != nil {
			t.Fatal(err)
		}
		if err := guard.CompleteTx(ctx, tx, "comments.create", key, 201, []byte("public-id"), []byte(`{"status":"created"}`)); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		return claim
	}
	first := claimAndComplete("key-1", "body-a")
	if !first.New || first.Completed {
		t.Fatalf("first claim=%+v", first)
	}
	tx, err := db.Writer.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	replayed, err := guard.ClaimTx(ctx, tx, "comments.create", "key-1", "body-a")
	if err != nil || !replayed.Completed || string(replayed.ResponseBody) != `{"status":"created"}` {
		t.Fatalf("replayed=%+v err=%v", replayed, err)
	}
	if _, err := guard.ClaimTx(ctx, tx, "comments.create", "key-1", "body-b"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflict error=%v", err)
	}
	var storedKey, storedRequest []byte
	if err := db.Reader.QueryRowContext(ctx, "SELECT key_hash,request_hash FROM request_idempotencies LIMIT 1").Scan(&storedKey, &storedRequest); err != nil {
		t.Fatal(err)
	}
	if string(storedKey) == "key-1" || string(storedRequest) == "body-a" || len(storedKey) != 32 || len(storedRequest) != 32 {
		t.Fatalf("raw digest stored: key=%q request=%q", storedKey, storedRequest)
	}
}

func TestGuardRejectsDuplicateFingerprintAndGeneratesOpaqueToken(t *testing.T) {
	ctx := context.Background()
	db := openGuardDatabase(t)
	guard := NewGuard(db, []byte("test-secret"))
	tx, err := db.Writer.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guard.ClaimFingerprintTx(ctx, tx, "comments.create", "same-comment", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := guard.ClaimFingerprintTx(ctx, tx, "comments.create", "same-comment", nil); !errors.Is(err, ErrDuplicateRequest) {
		t.Fatalf("duplicate error=%v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	token, digest, err := guard.NewToken()
	if err != nil || len(token) < 40 || len(digest) != 32 || string(digest) == token {
		t.Fatalf("token=%q digest=%x err=%v", token, digest, err)
	}
}

func TestGuardConcurrentIdempotencyProducesOneClaim(t *testing.T) {
	ctx := context.Background()
	db := openGuardDatabase(t)
	guard := NewGuard(db, []byte("concurrent-secret"))
	var wait sync.WaitGroup
	var newClaims atomic.Int32
	errorsCh := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			tx, err := db.Writer.BeginTx(ctx, nil)
			if err != nil {
				errorsCh <- err
				return
			}
			defer tx.Rollback()
			claim, err := guard.ClaimTx(ctx, tx, "comments.create", "same-key", "same-request")
			if err != nil {
				errorsCh <- err
				return
			}
			if claim.New {
				newClaims.Add(1)
				if err := guard.CompleteTx(ctx, tx, "comments.create", "same-key", 201, []byte("public-id"), []byte(`{"status":"created"}`)); err != nil {
					errorsCh <- err
					return
				}
			}
			if err := tx.Commit(); err != nil {
				errorsCh <- err
			}
		}()
	}
	wait.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	if newClaims.Load() != 1 {
		t.Fatalf("new claims=%d, want 1", newClaims.Load())
	}
	var count int
	if err := db.Reader.QueryRowContext(ctx, "SELECT count(*) FROM request_idempotencies WHERE scope='comments.create'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("idempotency rows=%d err=%v", count, err)
	}
}

func openGuardDatabase(t *testing.T) *database.DB {
	t.Helper()
	db, err := database.Open(context.Background(), config.Database{Path: filepath.Join(t.TempDir(), "blog.sqlite"), BusyTimeout: config.Duration{Duration: time.Second}, CacheSizeKiB: 4096, ReadConnections: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
