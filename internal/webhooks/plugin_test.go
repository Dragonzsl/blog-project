package webhooks

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/zhushilin/blog-project/internal/extensions"
	"github.com/zhushilin/blog-project/internal/platform/config"
	"github.com/zhushilin/blog-project/internal/platform/database"
)

func TestWebhookDeliveryIsSignedAndDurable(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, config.Database{Path: filepath.Join(t.TempDir(), "blog.sqlite"), BusyTimeout: config.Duration{Duration: time.Second}, CacheSizeKiB: 4096, ReadConnections: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const secret = "stage-three-test-secret"
	var received struct {
		body      []byte
		signature string
		event     string
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.body, _ = io.ReadAll(r.Body)
		received.signature = r.Header.Get("X-Blog-Signature-256")
		received.event = r.Header.Get("X-Blog-Event")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	router := chi.NewRouter()
	registry := extensions.NewRegistry(db, router, nil)
	plugin := NewPlugin(NewStore(db), Config{Endpoint: server.URL, Secret: secret, MaxAttempts: 3})
	if err := registry.Register(plugin); err != nil {
		t.Fatal(err)
	}
	if err := registry.Enable(ctx, PluginID); err != nil {
		t.Fatal(err)
	}
	if failures := registry.Dispatch(ctx, extensions.Event{Name: "ContentPublished.v1", Version: 1, ObjectID: []byte("object")}); len(failures) != 0 {
		t.Fatalf("dispatch failures=%v", failures)
	}
	processed, err := registry.ProcessOne(ctx)
	if err != nil || !processed {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
	if received.event != "ContentPublished.v1" || len(received.body) == 0 {
		t.Fatalf("received=%+v", received)
	}
	hash := hmac.New(sha256.New, []byte(secret))
	_, _ = hash.Write(received.body)
	if received.signature != "sha256="+hex.EncodeToString(hash.Sum(nil)) {
		t.Fatalf("signature=%q", received.signature)
	}
	var status string
	if err := db.Reader.QueryRow("SELECT status FROM webhook_deliveries LIMIT 1").Scan(&status); err != nil || status != "succeeded" {
		t.Fatalf("delivery status=%q err=%v", status, err)
	}
	var jobs int
	if err := db.Reader.QueryRow("SELECT count(*) FROM jobs WHERE kind='plugin:webhooks.signed:deliver' AND status='succeeded'").Scan(&jobs); err != nil || jobs != 1 {
		t.Fatalf("jobs=%d err=%v", jobs, err)
	}
}

func TestWebhookFailureIsRetriedAndEventuallyFails(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, config.Database{Path: filepath.Join(t.TempDir(), "blog.sqlite"), BusyTimeout: config.Duration{Duration: time.Second}, CacheSizeKiB: 4096, ReadConnections: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls++; http.Error(w, "no", http.StatusBadGateway) }))
	defer server.Close()
	registry := extensions.NewRegistry(db, chi.NewRouter(), nil)
	if err := registry.Register(NewPlugin(NewStore(db), Config{Endpoint: server.URL, Secret: "secret", MaxAttempts: 2})); err != nil {
		t.Fatal(err)
	}
	if err := registry.Enable(ctx, PluginID); err != nil {
		t.Fatal(err)
	}
	registry.Dispatch(ctx, extensions.Event{Name: "ContentPublished.v1", Version: 1})
	if _, err := registry.ProcessOne(ctx); err == nil {
		t.Fatal("first delivery unexpectedly succeeded")
	}
	var jobStatus string
	if err := db.Reader.QueryRow("SELECT status FROM jobs LIMIT 1").Scan(&jobStatus); err != nil || jobStatus != "pending" {
		t.Fatalf("first job status=%q err=%v", jobStatus, err)
	}
	// Make the backoff immediately runnable for the deterministic unit test.
	if _, err := db.Writer.Exec("UPDATE jobs SET available_at=0"); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.ProcessOne(ctx); err == nil {
		t.Fatal("second delivery unexpectedly succeeded")
	}
	// The shared host allows five attempts; the delivery's own max-attempt
	// policy marks the endpoint terminal after two while the job finishes its
	// bounded retry lifecycle.
	for attempt := 0; attempt < 3; attempt++ {
		if _, err := db.Writer.Exec("UPDATE jobs SET available_at=0"); err != nil {
			t.Fatal(err)
		}
		_, _ = registry.ProcessOne(ctx)
	}
	if err := db.Reader.QueryRow("SELECT status FROM jobs LIMIT 1").Scan(&jobStatus); err != nil || jobStatus != "failed" {
		t.Fatalf("final job status=%q err=%v", jobStatus, err)
	}
	var deliveryStatus string
	if err := db.Reader.QueryRow("SELECT status FROM webhook_deliveries LIMIT 1").Scan(&deliveryStatus); err != nil || deliveryStatus != "failed" {
		t.Fatalf("delivery status=%q err=%v", deliveryStatus, err)
	}
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
	if !strings.Contains(receivedError(t, db), "webhook endpoint") {
		t.Fatalf("last_error is not operator-readable: %q", receivedError(t, db))
	}
}

func receivedError(t *testing.T, db *database.DB) string {
	t.Helper()
	var value string
	_ = db.Reader.QueryRow("SELECT COALESCE(last_error,'') FROM webhook_deliveries LIMIT 1").Scan(&value)
	return value
}
