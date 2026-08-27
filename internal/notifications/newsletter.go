package notifications

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/zhushilin/blog-project/internal/platform/database"
)

type NewsletterAdapter interface {
	Name() string
	Subscribe(context.Context, string) error
	Unsubscribe(context.Context, string) error
}

type LocalNewsletter struct {
	db       *database.DB
	secret   []byte
	provider string
	now      func() time.Time
}

func NewLocalNewsletter(db *database.DB, secret []byte) *LocalNewsletter {
	return &LocalNewsletter{db: db, secret: append([]byte(nil), secret...), provider: "local", now: func() time.Time { return time.Now().UTC() }}
}

func (l *LocalNewsletter) Name() string { return l.provider }

func (l *LocalNewsletter) Subscribe(ctx context.Context, email string) error {
	email, err := normalizeEmail(email)
	if err != nil {
		return err
	}
	hash := sha256.Sum256([]byte(email))
	ciphertext, err := l.encrypt([]byte(email))
	if err != nil {
		return err
	}
	now := l.now().UnixMilli()
	_, err = l.db.Writer.ExecContext(ctx, `INSERT INTO newsletter_subscribers(email_hash,email_ciphertext,provider,status,created_at,updated_at) VALUES(?,?,?,'active',?,?) ON CONFLICT(email_hash) DO UPDATE SET status='active',updated_at=excluded.updated_at`, hash[:], ciphertext, l.provider, now, now)
	return err
}

func (l *LocalNewsletter) Unsubscribe(ctx context.Context, email string) error {
	email, err := normalizeEmail(email)
	if err != nil {
		return err
	}
	hash := sha256.Sum256([]byte(email))
	_, err = l.db.Writer.ExecContext(ctx, "UPDATE newsletter_subscribers SET status='unsubscribed',updated_at=? WHERE email_hash=?", l.now().UnixMilli(), hash[:])
	return err
}

func (l *LocalNewsletter) encrypt(plaintext []byte) ([]byte, error) {
	key := sha256.Sum256(l.secret)
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, []byte("blog:newsletter:v1")), nil
}

type HTTPNewsletter struct {
	Endpoint string
	Token    string
	Client   *http.Client
	Provider string
}

func NewHTTPNewsletter(endpoint, token, provider string) (*HTTPNewsletter, error) {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return nil, errors.New("newsletter endpoint must be an absolute HTTP or HTTPS URL")
	}
	if provider == "" {
		provider = "external"
	}
	return &HTTPNewsletter{Endpoint: strings.TrimRight(parsed.String(), "/"), Token: token, Provider: provider, Client: &http.Client{Timeout: 15 * time.Second}}, nil
}

func (h *HTTPNewsletter) Name() string { return h.Provider }

func (h *HTTPNewsletter) Subscribe(ctx context.Context, email string) error {
	return h.send(ctx, "subscribe", email)
}

func (h *HTTPNewsletter) Unsubscribe(ctx context.Context, email string) error {
	return h.send(ctx, "unsubscribe", email)
}

func (h *HTTPNewsletter) send(ctx context.Context, action, email string) error {
	email, err := normalizeEmail(email)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]string{"action": action, "email": email})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, h.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(h.Token) != "" {
		request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(h.Token))
	}
	response, err := h.Client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("newsletter provider returned %s", response.Status)
	}
	return nil
}

type NewsletterService struct {
	Adapter NewsletterAdapter
}

func (s NewsletterService) Subscribe(ctx context.Context, email string) error {
	if s.Adapter == nil {
		return errors.New("newsletter provider is disabled")
	}
	return s.Adapter.Subscribe(ctx, email)
}
func (s NewsletterService) Unsubscribe(ctx context.Context, email string) error {
	if s.Adapter == nil {
		return errors.New("newsletter provider is disabled")
	}
	return s.Adapter.Unsubscribe(ctx, email)
}

func normalizeEmail(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || len(value) > 320 || strings.ContainsAny(value, "\r\n") || !strings.Contains(value, "@") {
		return "", errors.New("invalid email address")
	}
	return value, nil
}
