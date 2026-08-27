// Package webhooks implements the optional, signed, post-commit delivery
// adapter.  It never performs network I/O in an HTTP request or publishing
// transaction; events become durable jobs first and are retried by the shared
// extension worker.
package webhooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/zhushilin/blog-project/internal/extensions"
	"github.com/zhushilin/blog-project/internal/platform/database"
)

const (
	PluginID        = "webhooks.signed"
	MaxTaskAttempts = 5
)

type Config struct {
	Endpoint    string
	Secret      string
	MaxAttempts int
}

type Delivery struct {
	ID           string
	EventName    string
	EventVersion int
	Endpoint     string
	Payload      []byte
	Status       string
	Attempts     int
	LastError    string
}

type Store struct{ db *database.DB }

func NewStore(db *database.DB) *Store { return &Store{db: db} }

func (s *Store) Create(ctx context.Context, delivery Delivery, now time.Time) error {
	if s == nil || s.db == nil {
		return errors.New("webhook store is not configured")
	}
	_, err := s.db.Writer.ExecContext(ctx, `INSERT INTO webhook_deliveries(delivery_id,event_name,event_version,endpoint,payload,status,attempts,created_at,updated_at) VALUES(?,?,?,?,?,'pending',0,?,?)`, delivery.ID, delivery.EventName, delivery.EventVersion, delivery.Endpoint, delivery.Payload, now.UnixMilli(), now.UnixMilli())
	return err
}

func (s *Store) Get(ctx context.Context, id string) (Delivery, error) {
	var result Delivery
	err := s.db.Reader.QueryRowContext(ctx, `SELECT delivery_id,event_name,event_version,endpoint,payload,status,attempts,COALESCE(last_error,'') FROM webhook_deliveries WHERE delivery_id=?`, id).Scan(&result.ID, &result.EventName, &result.EventVersion, &result.Endpoint, &result.Payload, &result.Status, &result.Attempts, &result.LastError)
	if errors.Is(err, sql.ErrNoRows) {
		return Delivery{}, sql.ErrNoRows
	}
	return result, err
}

func (s *Store) BeginAttempt(ctx context.Context, id string, now time.Time) (int, error) {
	result, err := s.db.Writer.ExecContext(ctx, `UPDATE webhook_deliveries SET status='running',attempts=attempts+1,updated_at=? WHERE delivery_id=? AND status IN ('pending','running')`, now.UnixMilli(), id)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return 0, sql.ErrNoRows
	}
	var attempts int
	if err := s.db.Reader.QueryRowContext(ctx, "SELECT attempts FROM webhook_deliveries WHERE delivery_id=?", id).Scan(&attempts); err != nil {
		return 0, err
	}
	return attempts, nil
}

func (s *Store) Finish(ctx context.Context, id, status, lastError string, now time.Time) error {
	if status != "pending" && status != "succeeded" && status != "failed" {
		return errors.New("invalid webhook delivery status")
	}
	var delivered any
	if status == "succeeded" {
		delivered = now.UnixMilli()
	}
	_, err := s.db.Writer.ExecContext(ctx, `UPDATE webhook_deliveries SET status=?,last_error=?,delivered_at=?,updated_at=? WHERE delivery_id=?`, status, strings.TrimSpace(lastError), delivered, now.UnixMilli(), id)
	return err
}

type eventEnvelope struct {
	DeliveryID string         `json:"delivery_id"`
	Event      string         `json:"event"`
	Version    int            `json:"version"`
	ObjectID   string         `json:"object_id,omitempty"`
	Payload    map[string]any `json:"payload,omitempty"`
	OccurredAt time.Time      `json:"occurred_at"`
}

type taskPayload struct {
	DeliveryID string `json:"delivery_id"`
}

type Plugin struct {
	config     Config
	store      *Store
	client     *http.Client
	now        func() time.Time
	enqueueJob func(context.Context, string, time.Time) error
}

func NewPlugin(store *Store, cfg Config) *Plugin {
	if cfg.MaxAttempts < 1 || cfg.MaxAttempts > MaxTaskAttempts {
		cfg.MaxAttempts = MaxTaskAttempts
	}
	return &Plugin{config: cfg, store: store, client: &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("webhook redirects are not allowed") }}, now: func() time.Time { return time.Now().UTC() }}
}

func (p *Plugin) Manifest() extensions.Manifest {
	return extensions.Manifest{ID: PluginID, Name: "签名 Webhook", Version: "1.0.0", APIVersion: extensions.HostAPIVersion, Kind: "webhook", Capabilities: []string{"post_commit_events", "signed_http", "durable_retry"}}
}

func (p *Plugin) Register(host *extensions.Host) error {
	if p == nil || p.store == nil {
		return errors.New("webhook store is required")
	}
	_, err := validateEndpoint(p.config.Endpoint)
	if err != nil {
		return err
	}
	if strings.TrimSpace(p.config.Secret) == "" {
		return errors.New("webhook secret is required")
	}
	if err := host.RegisterMenu(extensions.MenuItem{Label: "Webhook 投递", Path: "/admin/plugins/" + PluginID + "/status", Section: "settings", Order: 90}); err != nil {
		return err
	}
	p.enqueueJob = func(ctx context.Context, id string, availableAt time.Time) error {
		return host.EnqueueTask(ctx, "deliver", taskPayload{DeliveryID: id}, "webhook:"+id, availableAt)
	}
	if err := host.Subscribe("ContentPublished.v1", p.enqueue); err != nil {
		return err
	}
	if err := host.Subscribe("CommentApproved.v1", p.enqueue); err != nil {
		return err
	}
	if err := host.RegisterTask("deliver", p.deliver); err != nil {
		return err
	}
	return host.AdminRoute(http.MethodGet, "/status", func(w http.ResponseWriter, r *http.Request) {
		var pending, failed, succeeded int
		_ = p.store.db.Reader.QueryRowContext(r.Context(), "SELECT count(*) FROM webhook_deliveries WHERE status='pending'").Scan(&pending)
		_ = p.store.db.Reader.QueryRowContext(r.Context(), "SELECT count(*) FROM webhook_deliveries WHERE status='failed'").Scan(&failed)
		_ = p.store.db.Reader.QueryRowContext(r.Context(), "SELECT count(*) FROM webhook_deliveries WHERE status='succeeded'").Scan(&succeeded)
		writeJSON(w, http.StatusOK, map[string]any{"id": PluginID, "pending": pending, "failed": failed, "succeeded": succeeded})
	})
}

func (p *Plugin) enqueue(ctx context.Context, event extensions.Event) error {
	if event.Name == "" || event.Version < 1 {
		return errors.New("webhook event metadata is invalid")
	}
	now := event.OccurredAt
	if now.IsZero() {
		now = p.now().UTC()
	}
	deliveryID, err := newDeliveryID()
	if err != nil {
		return err
	}
	envelope := eventEnvelope{DeliveryID: deliveryID, Event: event.Name, Version: event.Version, ObjectID: hex.EncodeToString(event.ObjectID), Payload: event.Payload, OccurredAt: now.UTC()}
	body, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	endpoint, err := validateEndpoint(p.config.Endpoint)
	if err != nil {
		return err
	}
	if err := p.store.Create(ctx, Delivery{ID: deliveryID, EventName: event.Name, EventVersion: event.Version, Endpoint: endpoint, Payload: body}, p.now().UTC()); err != nil {
		return err
	}
	return p.enqueueTask(ctx, deliveryID, now)
}

func (p *Plugin) enqueueTask(ctx context.Context, id string, availableAt time.Time) error {
	// The task is registered against the current Host during Register. The
	// closure is installed there so a plugin cannot enqueue work while disabled.
	if p.enqueueJob == nil {
		return errors.New("webhook host is not bound")
	}
	return p.enqueueJob(ctx, id, availableAt)
}

// taskEnqueuer is populated at registration time. Keeping it on the plugin
// avoids exposing the host's database or router to the delivery implementation.
func (p *Plugin) deliver(ctx context.Context, payload []byte) error {
	var task taskPayload
	if err := json.Unmarshal(payload, &task); err != nil || task.DeliveryID == "" {
		return errors.New("invalid webhook task payload")
	}
	delivery, err := p.store.Get(ctx, task.DeliveryID)
	if err != nil {
		return err
	}
	if delivery.Status == "succeeded" {
		return nil
	}
	attempt, err := p.store.BeginAttempt(ctx, delivery.ID, p.now().UTC())
	if err != nil {
		return err
	}
	requestBody := bytes.NewReader(delivery.Payload)
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, delivery.Endpoint, requestBody)
	if err != nil {
		_ = p.store.Finish(ctx, delivery.ID, terminalStatus(attempt, p.config.MaxAttempts), err.Error(), p.now().UTC())
		return err
	}
	signature := hmac.New(sha256.New, []byte(p.config.Secret))
	_, _ = signature.Write(delivery.Payload)
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("User-Agent", "personal-blog-webhooks/1")
	httpRequest.Header.Set("X-Blog-Event", delivery.EventName)
	httpRequest.Header.Set("X-Blog-Event-Version", fmt.Sprintf("%d", delivery.EventVersion))
	httpRequest.Header.Set("X-Blog-Delivery", delivery.ID)
	httpRequest.Header.Set("X-Blog-Signature-256", "sha256="+hex.EncodeToString(signature.Sum(nil)))
	httpRequest.Header.Set("X-Blog-Timestamp", p.now().UTC().Format(time.RFC3339))
	response, err := p.client.Do(httpRequest)
	if err != nil {
		status := terminalStatus(attempt, p.config.MaxAttempts)
		_ = p.store.Finish(ctx, delivery.ID, status, err.Error(), p.now().UTC())
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		err = fmt.Errorf("webhook endpoint returned %s", response.Status)
		status := terminalStatus(attempt, p.config.MaxAttempts)
		_ = p.store.Finish(ctx, delivery.ID, status, err.Error(), p.now().UTC())
		return err
	}
	return p.store.Finish(ctx, delivery.ID, "succeeded", "", p.now().UTC())
}

func terminalStatus(attempt, max int) string {
	if attempt >= max {
		return "failed"
	}
	return "pending"
}

func newDeliveryID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func validateEndpoint(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || len(parsed.String()) > 2048 {
		return "", errors.New("webhook endpoint must be an absolute HTTP or HTTPS URL without credentials")
	}
	return parsed.String(), nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
