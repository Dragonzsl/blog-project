package notifications

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/zhushilin/blog-project/internal/operations"
	"github.com/zhushilin/blog-project/internal/platform/database"
	"github.com/zhushilin/blog-project/internal/platform/netguard"
	"github.com/zhushilin/blog-project/internal/platform/publicwrite"
)

var ErrInvalidNewsletterToken = errors.New("newsletter token is invalid or expired")

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

const (
	newsletterEmailHashVersion = 2
	newsletterRekeyBatchSize   = 200
)

func NewLocalNewsletter(db *database.DB, secret []byte) *LocalNewsletter {
	return &LocalNewsletter{db: db, secret: append([]byte(nil), secret...), provider: "local", now: func() time.Time { return time.Now().UTC() }}
}

func (l *LocalNewsletter) Name() string { return l.provider }

func (l *LocalNewsletter) Subscribe(ctx context.Context, email string) error {
	email, err := normalizeEmail(email)
	if err != nil {
		return err
	}
	hash := newsletterEmailHash(l.secret, email)
	ciphertext, err := l.encrypt([]byte(email))
	if err != nil {
		return err
	}
	now := l.now().UnixMilli()
	_, err = l.db.Writer.ExecContext(ctx, `INSERT INTO newsletter_subscribers(email_hash,email_ciphertext,provider,status,created_at,updated_at,email_hash_version) VALUES(?,?,?,'active',?,?,?) ON CONFLICT(email_hash) DO UPDATE SET status='active',updated_at=excluded.updated_at,email_hash_version=excluded.email_hash_version`, hash, ciphertext, l.provider, now, now, newsletterEmailHashVersion)
	return err
}

func (l *LocalNewsletter) Unsubscribe(ctx context.Context, email string) error {
	email, err := normalizeEmail(email)
	if err != nil {
		return err
	}
	hash := newsletterEmailHash(l.secret, email)
	_, err = l.db.Writer.ExecContext(ctx, "UPDATE newsletter_subscribers SET status='unsubscribed',updated_at=? WHERE email_hash=?", l.now().UnixMilli(), hash)
	return err
}

func (l *LocalNewsletter) encrypt(plaintext []byte) ([]byte, error) {
	return encryptWithSecret(l.secret, plaintext, []byte("blog:newsletter:v1"))
}

type HTTPNewsletter struct {
	Endpoint string
	Token    string
	Client   *http.Client
	Provider string
}

func NewHTTPNewsletter(endpoint, token, provider string) (*HTTPNewsletter, error) {
	parsed, err := netguard.ValidateURL(endpoint)
	if err != nil {
		return nil, err
	}
	if provider == "" {
		provider = "external"
	}
	return &HTTPNewsletter{Endpoint: strings.TrimRight(parsed.String(), "/"), Token: token, Provider: provider, Client: newGuardedHTTPClient()}, nil
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
	client := h.Client
	if client == nil {
		client = newGuardedHTTPClient()
	}
	response, err := client.Do(request)
	if err != nil {
		if netguard.IsPermanent(err) {
			return operations.Permanent(err)
		}
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		err := fmt.Errorf("newsletter provider returned %s", response.Status)
		if response.StatusCode >= 400 && response.StatusCode < 500 && response.StatusCode != http.StatusRequestTimeout && response.StatusCode != http.StatusTooManyRequests {
			return operations.Permanent(err)
		}
		return err
	}
	return nil
}

type NewsletterRequest struct {
	IdempotencyKey string
	ClientIdentity string
}

type NewsletterService struct {
	Adapter       NewsletterAdapter
	db            *database.DB
	secret        []byte
	guard         *publicwrite.Guard
	outbox        *Outbox
	baseURL       string
	now           func() time.Time
	enqueueTaskTx func(context.Context, *sql.Tx, string, any, string, time.Time) error
}

func NewNewsletterService(db *database.DB, adapter NewsletterAdapter, secret []byte) *NewsletterService {
	return &NewsletterService{Adapter: adapter, db: db, secret: append([]byte(nil), secret...), guard: publicwrite.NewGuard(db, secret), now: func() time.Time { return time.Now().UTC() }}
}

func (s *NewsletterService) SetWriteGuard(guard *publicwrite.Guard) { s.guard = guard }

func (s *NewsletterService) SetOutbox(outbox *Outbox) { s.outbox = outbox }

func (s *NewsletterService) SetBaseURL(baseURL string) {
	s.baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
}

func (s *NewsletterService) Cleanup(ctx context.Context) (int64, error) {
	if s == nil || s.guard == nil {
		return 0, nil
	}
	return s.guard.Cleanup(ctx)
}

func (s *NewsletterService) SetTaskEnqueuer(enqueue func(context.Context, *sql.Tx, string, any, string, time.Time) error) {
	s.enqueueTaskTx = enqueue
}

// Subscribe and Unsubscribe retain the narrow adapter API for trusted callers;
// public HTTP writes use RequestSubscribe and token-based UnsubscribeToken.
func (s *NewsletterService) Subscribe(ctx context.Context, email string) error {
	if s.Adapter == nil {
		return errors.New("newsletter provider is disabled")
	}
	return s.Adapter.Subscribe(ctx, email)
}

func (s *NewsletterService) Unsubscribe(ctx context.Context, email string) error {
	if s.Adapter == nil {
		return errors.New("newsletter provider is disabled")
	}
	return s.Adapter.Unsubscribe(ctx, email)
}

func (s *NewsletterService) RequestSubscribe(ctx context.Context, email string, request NewsletterRequest) error {
	email, err := normalizeEmail(email)
	if err != nil {
		return err
	}
	if s.db == nil || s.guard == nil {
		return errors.New("newsletter service is not configured")
	}
	fingerprint := strings.Join([]string{"subscribe", request.ClientIdentity, email}, "\x00")
	now := s.now().UTC()
	tx, err := s.db.Writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if strings.TrimSpace(request.IdempotencyKey) != "" {
		claim, err := s.guard.ClaimTx(ctx, tx, "newsletter.subscribe", request.IdempotencyKey, fingerprint)
		if err != nil {
			return err
		}
		if claim.Completed {
			return tx.Commit()
		}
	} else if _, err := s.guard.ClaimFingerprintTx(ctx, tx, "newsletter.subscribe", fingerprint, nil); err != nil {
		return err
	}
	emailHash := newsletterEmailHash(s.secret, email)
	emailCiphertext, err := encryptWithSecret(s.secret, []byte(email), []byte("blog:newsletter:v1"))
	if err != nil {
		return err
	}
	provider := "external"
	if s.Adapter != nil && s.Adapter.Name() != "" {
		provider = s.Adapter.Name()
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO newsletter_subscribers(email_hash,email_ciphertext,provider,status,created_at,updated_at,email_hash_version) VALUES(?,?,?,'pending',?,?,?) ON CONFLICT(email_hash) DO UPDATE SET email_ciphertext=excluded.email_ciphertext,provider=excluded.provider,status=CASE WHEN newsletter_subscribers.status='active' THEN 'active' ELSE 'pending' END,updated_at=excluded.updated_at,email_hash_version=excluded.email_hash_version`, emailHash, emailCiphertext, provider, now.UnixMilli(), now.UnixMilli(), newsletterEmailHashVersion); err != nil {
		return err
	}
	var subscriberID int64
	var subscriberStatus string
	if err := tx.QueryRowContext(ctx, "SELECT id,status FROM newsletter_subscribers WHERE email_hash=?", emailHash).Scan(&subscriberID, &subscriberStatus); err != nil {
		return err
	}
	if subscriberStatus != "active" {
		token, tokenHash, err := s.guard.NewToken()
		if err != nil {
			return err
		}
		tokenCiphertext, err := encryptWithSecret(s.secret, []byte(token), []byte("blog:newsletter:token:v1"))
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO newsletter_tokens(subscriber_id,purpose,token_hash,token_ciphertext,expires_at,created_at) VALUES(?,'confirm',?,?,?,?)`, subscriberID, tokenHash, tokenCiphertext, now.Add(24*time.Hour).UnixMilli(), now.UnixMilli())
		if err != nil {
			return err
		}
		tokenID, err := result.LastInsertId()
		if err != nil {
			return err
		}
		if s.enqueueTaskTx != nil {
			if err := s.enqueueTaskTx(ctx, tx, "send_confirmation", newsletterTask{TokenID: tokenID}, fmt.Sprintf("newsletter:confirmation:%d", tokenID), now); err != nil {
				return err
			}
		}
	}
	if strings.TrimSpace(request.IdempotencyKey) != "" {
		body, _ := json.Marshal(map[string]string{"status": "accepted"})
		if err := s.guard.CompleteTx(ctx, tx, "newsletter.subscribe", request.IdempotencyKey, http.StatusAccepted, nil, body); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type newsletterTask struct {
	TokenID      int64  `json:"token_id,omitempty"`
	SubscriberID int64  `json:"subscriber_id,omitempty"`
	Operation    string `json:"operation,omitempty"`
	Version      int64  `json:"version,omitempty"`
}

func (s *NewsletterService) ProcessConfirmationTask(ctx context.Context, payload []byte) error {
	var task newsletterTask
	if err := json.Unmarshal(payload, &task); err != nil || task.TokenID < 1 {
		return errors.New("invalid newsletter confirmation task")
	}
	if s.outbox == nil || s.db == nil {
		return errors.New("newsletter confirmation delivery is not configured")
	}
	var tokenCiphertext, emailCiphertext []byte
	var expiresAt, consumedAt sql.NullInt64
	var subscriberID int64
	err := s.db.Reader.QueryRowContext(ctx, `SELECT token.token_ciphertext,token.expires_at,token.consumed_at,subscriber.id,subscriber.email_ciphertext FROM newsletter_tokens token JOIN newsletter_subscribers subscriber ON subscriber.id=token.subscriber_id WHERE token.id=? AND token.purpose='confirm'`, task.TokenID).Scan(&tokenCiphertext, &expiresAt, &consumedAt, &subscriberID, &emailCiphertext)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if expiresAt.Int64 <= s.now().UnixMilli() || consumedAt.Valid {
		return nil
	}
	token, err := decryptWithSecret(s.secret, tokenCiphertext, []byte("blog:newsletter:token:v1"))
	if err != nil {
		return err
	}
	email, err := decryptWithSecret(s.secret, emailCiphertext, []byte("blog:newsletter:v1"))
	if err != nil {
		return err
	}
	link := strings.TrimRight(s.baseURL, "/") + "/newsletter/confirm?token=" + url.QueryEscape(string(token))
	body := "请打开以下链接确认订阅：\n" + link
	return s.outbox.EnqueueIdempotent(ctx, "newsletter.confirmation", string(email), "请确认 Newsletter 订阅", body, fmt.Sprintf("newsletter:confirmation:%d", task.TokenID), s.now())
}

type ConfirmResult struct {
	UnsubscribeURL string
}

func (s *NewsletterService) Confirm(ctx context.Context, token string) (ConfirmResult, error) {
	if s.db == nil || s.guard == nil || len(token) < 20 || len(token) > 256 {
		return ConfirmResult{}, ErrInvalidNewsletterToken
	}
	tokenHash := s.guard.Digest("newsletter-token", token)
	now := s.now().UTC()
	tx, err := s.db.Writer.BeginTx(ctx, nil)
	if err != nil {
		return ConfirmResult{}, err
	}
	defer tx.Rollback()
	var tokenID, subscriberID int64
	var purpose, status string
	var expiresAt sql.NullInt64
	var consumedAt sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT token.id,token.subscriber_id,token.purpose,token.expires_at,token.consumed_at,subscriber.status FROM newsletter_tokens token JOIN newsletter_subscribers subscriber ON subscriber.id=token.subscriber_id WHERE token.token_hash=?`, tokenHash).Scan(&tokenID, &subscriberID, &purpose, &expiresAt, &consumedAt, &status); err != nil {
		return ConfirmResult{}, ErrInvalidNewsletterToken
	}
	if purpose != "confirm" || expiresAt.Int64 <= now.UnixMilli() {
		return ConfirmResult{}, ErrInvalidNewsletterToken
	}
	if consumedAt.Valid {
		if status != "active" {
			return ConfirmResult{}, ErrInvalidNewsletterToken
		}
		result, err := s.unsubscribeURLTx(ctx, tx, subscriberID)
		if err != nil {
			return ConfirmResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return ConfirmResult{}, err
		}
		return result, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE newsletter_subscribers SET status='active',updated_at=? WHERE id=?`, now.UnixMilli(), subscriberID); err != nil {
		return ConfirmResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE newsletter_tokens SET consumed_at=? WHERE id=? AND consumed_at IS NULL`, now.UnixMilli(), tokenID); err != nil {
		return ConfirmResult{}, err
	}
	unsubscribeToken, unsubscribeHash, err := s.guard.NewToken()
	if err != nil {
		return ConfirmResult{}, err
	}
	unsubCiphertext, err := encryptWithSecret(s.secret, []byte(unsubscribeToken), []byte("blog:newsletter:token:v1"))
	if err != nil {
		return ConfirmResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO newsletter_tokens(subscriber_id,purpose,token_hash,token_ciphertext,expires_at,created_at) VALUES(?,'unsubscribe',?,?,?,?)`, subscriberID, unsubscribeHash, unsubCiphertext, now.Add(365*24*time.Hour).UnixMilli(), now.UnixMilli()); err != nil {
		return ConfirmResult{}, err
	}
	if s.enqueueTaskTx != nil && s.Adapter != nil && s.Adapter.Name() != "local" {
		if err := s.enqueueTaskTx(ctx, tx, "sync", newsletterTask{SubscriberID: subscriberID, Operation: "subscribe", Version: tokenID}, fmt.Sprintf("newsletter:%d:subscribe:%d", subscriberID, tokenID), now); err != nil {
			return ConfirmResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return ConfirmResult{}, err
	}
	return ConfirmResult{UnsubscribeURL: strings.TrimRight(s.baseURL, "/") + "/newsletter/unsubscribe?token=" + url.QueryEscape(unsubscribeToken)}, nil
}

func (s *NewsletterService) unsubscribeURLTx(ctx context.Context, tx *sql.Tx, subscriberID int64) (ConfirmResult, error) {
	var token string
	err := tx.QueryRowContext(ctx, `SELECT token_ciphertext FROM newsletter_tokens token WHERE token.subscriber_id=? AND token.purpose='unsubscribe' AND token.consumed_at IS NULL AND token.expires_at>? ORDER BY token.id DESC LIMIT 1`, subscriberID, s.now().UnixMilli()).Scan(&token)
	if err != nil {
		return ConfirmResult{}, ErrInvalidNewsletterToken
	}
	decrypted, err := decryptWithSecret(s.secret, []byte(token), []byte("blog:newsletter:token:v1"))
	if err != nil {
		return ConfirmResult{}, ErrInvalidNewsletterToken
	}
	return ConfirmResult{UnsubscribeURL: strings.TrimRight(s.baseURL, "/") + "/newsletter/unsubscribe?token=" + url.QueryEscape(string(decrypted))}, nil
}

func (s *NewsletterService) UnsubscribeToken(ctx context.Context, token string) error {
	if s.db == nil || s.guard == nil || len(token) < 20 || len(token) > 256 {
		return ErrInvalidNewsletterToken
	}
	now := s.now().UTC()
	tx, err := s.db.Writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var tokenID, subscriberID int64
	var purpose string
	var expiresAt, consumedAt sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT id,subscriber_id,purpose,expires_at,consumed_at FROM newsletter_tokens WHERE token_hash=?`, s.guard.Digest("newsletter-token", token)).Scan(&tokenID, &subscriberID, &purpose, &expiresAt, &consumedAt); err != nil {
		return ErrInvalidNewsletterToken
	}
	if purpose != "unsubscribe" || expiresAt.Int64 <= now.UnixMilli() {
		return ErrInvalidNewsletterToken
	}
	if consumedAt.Valid {
		return tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `UPDATE newsletter_subscribers SET status='unsubscribed',updated_at=? WHERE id=?`, now.UnixMilli(), subscriberID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE newsletter_tokens SET consumed_at=? WHERE id=?`, now.UnixMilli(), tokenID); err != nil {
		return err
	}
	if s.enqueueTaskTx != nil && s.Adapter != nil && s.Adapter.Name() != "local" {
		if err := s.enqueueTaskTx(ctx, tx, "sync", newsletterTask{SubscriberID: subscriberID, Operation: "unsubscribe", Version: tokenID}, fmt.Sprintf("newsletter:%d:unsubscribe:%d", subscriberID, tokenID), now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *NewsletterService) ProcessSyncTask(ctx context.Context, payload []byte) error {
	var task newsletterTask
	if err := json.Unmarshal(payload, &task); err != nil || task.SubscriberID < 1 || (task.Operation != "subscribe" && task.Operation != "unsubscribe") {
		return errors.New("invalid newsletter sync task")
	}
	if s.Adapter == nil || s.db == nil {
		return errors.New("newsletter provider is disabled")
	}
	var encrypted []byte
	if err := s.db.Reader.QueryRowContext(ctx, "SELECT email_ciphertext FROM newsletter_subscribers WHERE id=?", task.SubscriberID).Scan(&encrypted); err != nil {
		return err
	}
	email, err := decryptWithSecret(s.secret, encrypted, []byte("blog:newsletter:v1"))
	if err != nil {
		return err
	}
	if task.Operation == "subscribe" {
		return s.Adapter.Subscribe(ctx, string(email))
	}
	return s.Adapter.Unsubscribe(ctx, string(email))
}

func newsletterEmailHash(secret []byte, email string) []byte {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte("blog:newsletter:email:v2"))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(email))
	return mac.Sum(nil)
}

// RekeyNewsletterHashes upgrades legacy unkeyed email hashes without losing
// subscriber rows. The encrypted email is the only place from which the
// normalized address can be recovered, and each short transaction handles a
// bounded batch so startup does not hold the SQLite writer indefinitely.
func RekeyNewsletterHashes(ctx context.Context, db *database.DB, secret []byte) error {
	if db == nil || db.Writer == nil {
		return errors.New("newsletter database is not configured")
	}
	for {
		tx, err := db.Writer.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT id,email_ciphertext FROM newsletter_subscribers WHERE email_hash_version<? ORDER BY id LIMIT ?`, newsletterEmailHashVersion, newsletterRekeyBatchSize)
		if err != nil {
			tx.Rollback()
			return err
		}
		count := 0
		for rows.Next() {
			var id int64
			var ciphertext []byte
			if err := rows.Scan(&id, &ciphertext); err != nil {
				rows.Close()
				tx.Rollback()
				return err
			}
			email, err := decryptWithSecret(secret, ciphertext, []byte("blog:newsletter:v1"))
			if err != nil {
				rows.Close()
				tx.Rollback()
				return fmt.Errorf("decrypt newsletter subscriber %d: %w", id, err)
			}
			normalized, err := normalizeEmail(string(email))
			if err != nil {
				rows.Close()
				tx.Rollback()
				return fmt.Errorf("normalize newsletter subscriber %d: %w", id, err)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE newsletter_subscribers SET email_hash=?,email_hash_version=? WHERE id=? AND email_hash_version<?`, newsletterEmailHash(secret, normalized), newsletterEmailHashVersion, id, newsletterEmailHashVersion); err != nil {
				rows.Close()
				tx.Rollback()
				return fmt.Errorf("rekey newsletter subscriber %d: %w", id, err)
			}
			count++
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			tx.Rollback()
			return err
		}
		if err := rows.Close(); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		if count < newsletterRekeyBatchSize {
			return nil
		}
	}
}

func encryptWithSecret(secret, plaintext, purpose []byte) ([]byte, error) {
	key := sha256.Sum256(append(append([]byte(nil), secret...), purpose...))
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
	return gcm.Seal(nonce, nonce, plaintext, purpose), nil
}

func decryptWithSecret(secret, ciphertext, purpose []byte) ([]byte, error) {
	key := sha256.Sum256(append(append([]byte(nil), secret...), purpose...))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return nil, errors.New("newsletter ciphertext is invalid")
	}
	nonce, payload := ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():]
	return gcm.Open(nil, nonce, payload, purpose)
}

func normalizeEmail(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || len(value) > 320 || strings.ContainsAny(value, "\r\n") || !strings.Contains(value, "@") {
		return "", errors.New("invalid email address")
	}
	return value, nil
}
