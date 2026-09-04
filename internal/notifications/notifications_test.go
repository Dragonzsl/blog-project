package notifications

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/zhushilin/blog-project/internal/extensions"
	"github.com/zhushilin/blog-project/internal/platform/config"
	"github.com/zhushilin/blog-project/internal/platform/database"
	"github.com/zhushilin/blog-project/internal/platform/netguard"
)

func TestMailMessageRejectsHeaderInjection(t *testing.T) {
	sender, err := NewSMTPSender(SMTPConfig{Enabled: true, Host: "localhost", Port: 2525, From: "owner@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if err := sender.Send(context.Background(), Message{To: "a@example.com", Subject: "ok\r\nBcc:bad", Text: "body"}); err == nil {
		t.Fatal("header injection accepted")
	}
	if !strings.Contains(buildMessage("owner@example.com", Message{To: "a@example.com", Subject: "Hi", Text: "text", HTML: "<p>html</p>"}), "multipart/alternative") {
		t.Fatal("multipart message missing")
	}
}

type captureMailer struct {
	mu       sync.Mutex
	messages []Message
}

func (m *captureMailer) Send(_ context.Context, message Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = append(m.messages, message)
	return nil
}

func (m *captureMailer) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.messages)
}

func TestNewsletterRequiresConfirmationAndTokenUnsubscribe(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, config.Database{Path: filepath.Join(t.TempDir(), "blog.sqlite"), BusyTimeout: config.Duration{Duration: time.Second}, CacheSizeKiB: 4096, ReadConnections: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mailer := &captureMailer{}
	outbox := NewOutbox(db, mailer)
	registry := extensions.NewRegistry(db, chi.NewRouter(), nil)
	outbox.SetTaskQueue(registry.TaskQueue())
	if err := registry.RegisterCoreTask("core:notification_send", outbox.ProcessTask); err != nil {
		t.Fatal(err)
	}
	adapter := NewLocalNewsletter(db, []byte("newsletter-secret"))
	service := NewNewsletterService(db, adapter, []byte("newsletter-secret"))
	service.SetOutbox(outbox)
	service.SetBaseURL("https://blog.example")
	handler := NewNewsletterHTTPHandlerFromService(service)
	plugin := &NewsletterPlugin{ID: "newsletter.local", Name: "Local Newsletter", Handler: handler, Service: service}
	if err := registry.Register(plugin); err != nil {
		t.Fatal(err)
	}
	if err := registry.Enable(ctx, plugin.ID); err != nil {
		t.Fatal(err)
	}
	request := NewsletterRequest{IdempotencyKey: "newsletter-request", ClientIdentity: "198.51.100.20"}
	if err := service.RequestSubscribe(ctx, "User@Example.com", request); err != nil {
		t.Fatal(err)
	}
	if err := service.RequestSubscribe(ctx, "User@Example.com", request); err != nil {
		t.Fatalf("idempotent subscribe error=%v", err)
	}
	var tokenCiphertext []byte
	if err := db.Reader.QueryRowContext(ctx, "SELECT token_ciphertext FROM newsletter_tokens WHERE purpose='confirm' LIMIT 1").Scan(&tokenCiphertext); err != nil {
		t.Fatal(err)
	}
	confirmation, err := decryptWithSecret([]byte("newsletter-secret"), tokenCiphertext, []byte("blog:newsletter:token:v1"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		processed, err := registry.ProcessOne(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !processed {
			break
		}
	}
	if mailer.Count() != 1 {
		t.Fatalf("confirmation messages=%d, want 1", mailer.Count())
	}
	result, err := service.Confirm(ctx, string(confirmation))
	if err != nil || result.UnsubscribeURL == "" {
		t.Fatalf("confirm result=%+v err=%v", result, err)
	}
	var status string
	if err := db.Reader.QueryRowContext(ctx, "SELECT status FROM newsletter_subscribers LIMIT 1").Scan(&status); err != nil || status != "active" {
		t.Fatalf("subscriber status=%q err=%v", status, err)
	}
	var unsubscribeCiphertext []byte
	if err := db.Reader.QueryRowContext(ctx, "SELECT token_ciphertext FROM newsletter_tokens WHERE purpose='unsubscribe' LIMIT 1").Scan(&unsubscribeCiphertext); err != nil {
		t.Fatal(err)
	}
	unsubscribe, err := decryptWithSecret([]byte("newsletter-secret"), unsubscribeCiphertext, []byte("blog:newsletter:token:v1"))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.UnsubscribeToken(ctx, string(unsubscribe)); err != nil {
		t.Fatal(err)
	}
	if err := service.UnsubscribeToken(ctx, string(unsubscribe)); err != nil {
		t.Fatalf("repeated unsubscribe error=%v", err)
	}
	if err := db.Reader.QueryRowContext(ctx, "SELECT status FROM newsletter_subscribers LIMIT 1").Scan(&status); err != nil || status != "unsubscribed" {
		t.Fatalf("unsubscribed status=%q err=%v", status, err)
	}
	if _, err := service.Confirm(ctx, "invalid-newsletter-token"); err == nil {
		t.Fatal("invalid token accepted")
	}
}

func TestHTTPNewsletterAndLocalEncryption(t *testing.T) {
	var received map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		_ = json.NewDecoder(r.Body).Decode(&received)
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Errorf("authorization missing")
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	adapter, err := NewHTTPNewsletter(server.URL, "token", "mailchimp")
	if err != nil {
		t.Fatal(err)
	}
	// The production adapter rejects loopback destinations; a test transport
	// is explicitly injected for this in-process fixture.
	adapter.Client = server.Client()
	if err := adapter.Subscribe(context.Background(), "User@Example.com"); err != nil {
		t.Fatal(err)
	}
	if received["email"] != "user@example.com" || received["action"] != "subscribe" {
		t.Fatalf("received=%v", received)
	}
	db, err := database.Open(context.Background(), config.Database{Path: filepath.Join(t.TempDir(), "blog.sqlite"), BusyTimeout: config.Duration{Duration: time.Second}, CacheSizeKiB: 4096, ReadConnections: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	local := NewLocalNewsletter(db, []byte("secret"))
	if err := local.Subscribe(context.Background(), "User@Example.com"); err != nil {
		t.Fatal(err)
	}
	var ciphertext []byte
	if err := db.Reader.QueryRowContext(context.Background(), "SELECT email_ciphertext FROM newsletter_subscribers LIMIT 1").Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ciphertext), "user@example.com") {
		t.Fatal("email stored as plaintext")
	}
}

func TestHTTPNewsletterBlocksLoopbackWithProductionClient(t *testing.T) {
	adapter, err := NewHTTPNewsletter("http://127.0.0.1:65535/subscribers", "token", "external")
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Subscribe(context.Background(), "user@example.com"); !errors.Is(err, netguard.ErrBlockedAddress) {
		t.Fatalf("loopback error=%v, want %v", err, netguard.ErrBlockedAddress)
	}
}

func TestRekeyNewsletterHashesUsesSecretBoundDigest(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, config.Database{Path: filepath.Join(t.TempDir(), "blog.sqlite"), BusyTimeout: config.Duration{Duration: time.Second}, CacheSizeKiB: 4096, ReadConnections: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	secret := []byte("rekey-secret")
	ciphertext, err := encryptWithSecret(secret, []byte("user@example.com"), []byte("blog:newsletter:v1"))
	if err != nil {
		t.Fatal(err)
	}
	legacy := sha256.Sum256([]byte("user@example.com"))
	if _, err := db.Writer.ExecContext(ctx, `INSERT INTO newsletter_subscribers(email_hash,email_ciphertext,provider,status,created_at,updated_at,email_hash_version) VALUES(?,?, 'local','pending',1,1,1)`, legacy[:], ciphertext); err != nil {
		t.Fatal(err)
	}
	if err := RekeyNewsletterHashes(ctx, db, secret); err != nil {
		t.Fatal(err)
	}
	var hash []byte
	var version int
	if err := db.Reader.QueryRowContext(ctx, "SELECT email_hash,email_hash_version FROM newsletter_subscribers LIMIT 1").Scan(&hash, &version); err != nil {
		t.Fatal(err)
	}
	want := newsletterEmailHash(secret, "user@example.com")
	if version != newsletterEmailHashVersion || string(hash) != string(want) || string(hash) == string(legacy[:]) {
		t.Fatalf("hash=%x version=%d", hash, version)
	}
}
