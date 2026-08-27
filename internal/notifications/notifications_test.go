package notifications

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhushilin/blog-project/internal/platform/config"
	"github.com/zhushilin/blog-project/internal/platform/database"
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
