package extensions

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/zhushilin/blog-project/internal/platform/database"
)

const HostAPIVersion = 1

var (
	ErrPluginNotFound   = errors.New("plugin not found")
	ErrPluginDisabled   = errors.New("plugin is disabled")
	ErrCapabilityDenied = errors.New("plugin capability denied")
	ErrProviderConflict = errors.New("comment providers are mutually exclusive")
	ErrInvalidSettings  = errors.New("plugin settings are invalid")
)

type Manifest struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	APIVersion   int      `json:"apiVersion"`
	Kind         string   `json:"kind"`
	Capabilities []string `json:"capabilities,omitempty"`
}

type SettingField struct {
	Type     string   `json:"type"`
	Required bool     `json:"required,omitempty"`
	Default  any      `json:"default,omitempty"`
	Min      *float64 `json:"minimum,omitempty"`
	Max      *float64 `json:"maximum,omitempty"`
	Options  []string `json:"options,omitempty"`
}

type SettingsSchema map[string]SettingField

type MenuItem struct {
	PluginID string
	Label    string
	Path     string
	Section  string
	Order    int
}

type Event struct {
	Name       string
	Version    int
	ObjectID   []byte
	Payload    map[string]any
	OccurredAt time.Time
}

type EventHandler func(context.Context, Event) error
type TaskHandler func(context.Context, []byte) error

type Plugin interface {
	Manifest() Manifest
	Register(*Host) error
}

type Host struct {
	registry *Registry
	pluginID string
}

func (h *Host) RegisterSettings(schema SettingsSchema) error {
	return h.registry.registerSettings(h.pluginID, schema)
}
func (h *Host) Settings(ctx context.Context) (map[string]any, error) {
	return h.registry.Settings(ctx, h.pluginID)
}
func (h *Host) SaveSettings(ctx context.Context, values map[string]any) error {
	return h.registry.SaveSettings(ctx, h.pluginID, values)
}
func (h *Host) RegisterMenu(item MenuItem) error {
	if item.PluginID == "" {
		item.PluginID = h.pluginID
	}
	if item.PluginID != h.pluginID {
		return ErrCapabilityDenied
	}
	h.registry.mu.Lock()
	defer h.registry.mu.Unlock()
	h.registry.menus = append(h.registry.menus, item)
	return nil
}
func (h *Host) Subscribe(event string, handler EventHandler) error {
	if strings.TrimSpace(event) == "" || handler == nil {
		return errors.New("event and handler are required")
	}
	h.registry.mu.Lock()
	defer h.registry.mu.Unlock()
	h.registry.events[event] = append(h.registry.events[event], handler)
	return nil
}
func (h *Host) RegisterTask(kind string, handler TaskHandler) error {
	if strings.TrimSpace(kind) == "" || handler == nil {
		return errors.New("task kind and handler are required")
	}
	key := h.pluginID + ":" + kind
	h.registry.mu.Lock()
	defer h.registry.mu.Unlock()
	if _, exists := h.registry.tasks[key]; exists {
		return fmt.Errorf("task %s already registered", key)
	}
	h.registry.tasks[key] = handler
	return nil
}
func (h *Host) EnqueueTask(ctx context.Context, kind string, payload any, idempotency string, availableAt time.Time) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if idempotency == "" {
		return errors.New("task idempotency key is required")
	}
	if availableAt.IsZero() {
		availableAt = time.Now().UTC()
	}
	_, err = h.registry.db.Writer.ExecContext(ctx, `INSERT INTO jobs(kind,payload_version,payload,idempotency_key,status,available_at,attempts,created_at,updated_at) VALUES(?,?,?,?,'pending',?,0,?,?) ON CONFLICT(idempotency_key) DO NOTHING`, "plugin:"+h.pluginID+":"+kind, 1, encoded, idempotency, availableAt.UTC().UnixMilli(), time.Now().UTC().UnixMilli(), time.Now().UTC().UnixMilli())
	return err
}
func (h *Host) Route(method, path string, handler http.HandlerFunc) error {
	if h.registry.router == nil || handler == nil {
		return ErrCapabilityDenied
	}
	if !strings.HasPrefix(path, "/") || strings.Contains(path, "..") {
		return ErrCapabilityDenied
	}
	switch strings.ToUpper(method) {
	case http.MethodGet:
		h.registry.router.Get(path, handler)
	case http.MethodPost:
		h.registry.router.Post(path, handler)
	case http.MethodPut:
		h.registry.router.Put(path, handler)
	case http.MethodDelete:
		h.registry.router.Delete(path, handler)
	default:
		return fmt.Errorf("unsupported plugin route method %q", method)
	}
	return nil
}
func (h *Host) AdminRoute(method, path string, handler http.HandlerFunc) error {
	if !strings.HasPrefix(path, "/") {
		return ErrCapabilityDenied
	}
	return h.Route(method, "/admin/plugins/"+h.pluginID+path, handler)
}
func (h *Host) Logger() *slog.Logger { return h.registry.logger.With("plugin", h.pluginID) }

type Registry struct {
	db      *database.DB
	router  chi.Router
	logger  *slog.Logger
	mu      sync.RWMutex
	plugins map[string]Plugin
	schemas map[string]SettingsSchema
	events  map[string][]EventHandler
	tasks   map[string]TaskHandler
	menus   []MenuItem
}

func NewRegistry(db *database.DB, router chi.Router, logger *slog.Logger) *Registry {
	if logger == nil {
		logger = slog.Default()
	}
	return &Registry{db: db, router: router, logger: logger, plugins: make(map[string]Plugin), schemas: make(map[string]SettingsSchema), events: make(map[string][]EventHandler), tasks: make(map[string]TaskHandler)}
}

func (r *Registry) Register(plugin Plugin) error {
	if plugin == nil {
		return errors.New("plugin is required")
	}
	manifest := plugin.Manifest()
	if !validManifest(manifest) {
		return fmt.Errorf("invalid plugin manifest %q", manifest.ID)
	}
	if manifest.APIVersion != HostAPIVersion {
		return fmt.Errorf("plugin %s requires host API %d", manifest.ID, manifest.APIVersion)
	}
	r.mu.Lock()
	if _, exists := r.plugins[manifest.ID]; exists {
		r.mu.Unlock()
		return fmt.Errorf("plugin %s already registered", manifest.ID)
	}
	r.plugins[manifest.ID] = plugin
	r.mu.Unlock()
	now := time.Now().UTC().UnixMilli()
	_, err := r.db.Writer.Exec(`INSERT INTO plugin_states(plugin_id,name,version,api_version,kind,enabled,config_schema_version,last_init_result,updated_at) VALUES(?,?,?,?,?,0,1,'disabled',?) ON CONFLICT(plugin_id) DO UPDATE SET name=excluded.name,version=excluded.version,api_version=excluded.api_version,kind=excluded.kind,updated_at=excluded.updated_at`, manifest.ID, manifest.Name, manifest.Version, manifest.APIVersion, manifest.Kind, now)
	return err
}

func (r *Registry) Initialize(ctx context.Context, enabled []string) error {
	wanted := make(map[string]struct{}, len(enabled))
	for _, id := range enabled {
		wanted[strings.TrimSpace(id)] = struct{}{}
	}
	r.mu.RLock()
	ids := make([]string, 0, len(r.plugins))
	for id := range r.plugins {
		ids = append(ids, id)
	}
	r.mu.RUnlock()
	var failures []error
	for _, id := range ids {
		if _, ok := wanted[id]; !ok {
			continue
		}
		if err := r.Enable(ctx, id); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (r *Registry) Enable(ctx context.Context, id string) error {
	r.mu.RLock()
	plugin, ok := r.plugins[id]
	r.mu.RUnlock()
	if !ok {
		return ErrPluginNotFound
	}
	manifest := plugin.Manifest()
	if manifest.Kind == "comment_provider" {
		var other string
		if err := r.db.Reader.QueryRowContext(ctx, "SELECT plugin_id FROM plugin_states WHERE kind='comment_provider' AND enabled=1 AND plugin_id<>? LIMIT 1", id).Scan(&other); err == nil {
			return fmt.Errorf("%w: %s already enabled", ErrProviderConflict, other)
		} else if err != sql.ErrNoRows {
			return err
		}
	}
	host := &Host{registry: r, pluginID: id}
	if err := plugin.Register(host); err != nil {
		_, _ = r.db.Writer.ExecContext(ctx, "UPDATE plugin_states SET last_init_result=?,enabled=0,updated_at=? WHERE plugin_id=?", err.Error(), time.Now().UTC().UnixMilli(), id)
		return fmt.Errorf("initialize plugin %s: %w", id, err)
	}
	if _, err := r.db.Writer.ExecContext(ctx, "UPDATE plugin_states SET enabled=1,last_init_result='ready',updated_at=? WHERE plugin_id=?", time.Now().UTC().UnixMilli(), id); err != nil {
		return err
	}
	return nil
}

func (r *Registry) Disable(ctx context.Context, id string) error {
	if _, err := r.db.Writer.ExecContext(ctx, "UPDATE plugin_states SET enabled=0,last_init_result='disabled',updated_at=? WHERE plugin_id=?", time.Now().UTC().UnixMilli(), id); err != nil {
		return err
	}
	return nil
}

func (r *Registry) Registered() []Manifest {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Manifest, 0, len(r.plugins))
	for _, plugin := range r.plugins {
		result = append(result, plugin.Manifest())
	}
	return result
}
func (r *Registry) Menus() []MenuItem {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := append([]MenuItem(nil), r.menus...)
	return result
}

func (r *Registry) Dispatch(ctx context.Context, event Event) []error {
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now().UTC()
	}
	r.mu.RLock()
	handlers := append([]EventHandler(nil), r.events[event.Name]...)
	r.mu.RUnlock()
	var failures []error
	for _, handler := range handlers {
		if err := handler(ctx, event); err != nil {
			failures = append(failures, err)
			r.logger.ErrorContext(ctx, "plugin event failed", "event", event.Name, "error", err)
		}
	}
	return failures
}

func (r *Registry) ProcessOne(ctx context.Context) (bool, error) {
	r.mu.RLock()
	handlers := make(map[string]TaskHandler, len(r.tasks))
	for key, handler := range r.tasks {
		handlers[key] = handler
	}
	r.mu.RUnlock()
	if len(handlers) == 0 {
		return false, nil
	}
	row := r.db.Writer.QueryRowContext(ctx, `SELECT id,kind,payload FROM jobs WHERE status='pending' AND available_at<=? AND kind LIKE 'plugin:%' ORDER BY available_at,id LIMIT 1`, time.Now().UTC().UnixMilli())
	var id int64
	var kind string
	var payload []byte
	if err := row.Scan(&id, &kind, &payload); err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	handler, ok := handlers[strings.TrimPrefix(kind, "plugin:")]
	if !ok {
		_, _ = r.db.Writer.ExecContext(ctx, "UPDATE jobs SET status='failed',last_error=?,updated_at=? WHERE id=?", "no handler", time.Now().UTC().UnixMilli(), id)
		return false, nil
	}
	if _, err := r.db.Writer.ExecContext(ctx, "UPDATE jobs SET status='running',attempts=attempts+1,updated_at=? WHERE id=? AND status='pending'", time.Now().UTC().UnixMilli(), id); err != nil {
		return false, err
	}
	if err := handler(ctx, payload); err != nil {
		_, _ = r.db.Writer.ExecContext(ctx, "UPDATE jobs SET status='failed',last_error=?,updated_at=? WHERE id=?", err.Error(), time.Now().UTC().UnixMilli(), id)
		return true, err
	}
	_, err := r.db.Writer.ExecContext(ctx, "UPDATE jobs SET status='succeeded',updated_at=? WHERE id=?", time.Now().UTC().UnixMilli(), id)
	return true, err
}

func (r *Registry) registerSettings(id string, schema SettingsSchema) error {
	for key, field := range schema {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("%w: empty setting key", ErrInvalidSettings)
		}
		if err := validateField(field); err != nil {
			return fmt.Errorf("%w: %s: %v", ErrInvalidSettings, key, err)
		}
	}
	r.mu.Lock()
	r.schemas[id] = schema
	r.mu.Unlock()
	return nil
}

func (r *Registry) Settings(ctx context.Context, id string) (map[string]any, error) {
	var raw string
	err := r.db.Reader.QueryRowContext(ctx, "SELECT values_json FROM plugin_settings WHERE plugin_id=?", id).Scan(&raw)
	if err == sql.ErrNoRows {
		return r.defaults(id), nil
	}
	if err != nil {
		return nil, err
	}
	result := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (r *Registry) SaveSettings(ctx context.Context, id string, values map[string]any) error {
	r.mu.RLock()
	schema, ok := r.schemas[id]
	r.mu.RUnlock()
	if !ok {
		return ErrPluginNotFound
	}
	if err := ValidateSettings(schema, values); err != nil {
		return err
	}
	raw, _ := json.Marshal(values)
	now := time.Now().UTC().UnixMilli()
	_, err := r.db.Writer.ExecContext(ctx, `INSERT INTO plugin_settings(plugin_id,schema_version,values_json,updated_at) SELECT ?,1,?,? ON CONFLICT(plugin_id) DO UPDATE SET values_json=excluded.values_json,updated_at=excluded.updated_at`, id, string(raw), now)
	return err
}

func (r *Registry) defaults(id string) map[string]any {
	r.mu.RLock()
	schema := r.schemas[id]
	r.mu.RUnlock()
	result := map[string]any{}
	for key, field := range schema {
		if field.Default != nil {
			result[key] = field.Default
		}
	}
	return result
}

func ValidateSettings(schema SettingsSchema, values map[string]any) error {
	for key, field := range schema {
		value, exists := values[key]
		if !exists || value == nil {
			if field.Required {
				return fmt.Errorf("%w: %s is required", ErrInvalidSettings, key)
			}
			continue
		}
		switch field.Type {
		case "boolean":
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("%w: %s must be boolean", ErrInvalidSettings, key)
			}
		case "text", "url", "color", "media":
			if _, ok := value.(string); !ok {
				return fmt.Errorf("%w: %s must be string", ErrInvalidSettings, key)
			}
		case "integer":
			number, ok := value.(float64)
			if !ok {
				if integer, okInt := value.(int); okInt {
					number = float64(integer)
					ok = true
				}
			}
			if !ok || field.Min != nil && number < *field.Min || field.Max != nil && number > *field.Max {
				return fmt.Errorf("%w: %s out of range", ErrInvalidSettings, key)
			}
		case "select":
			text, ok := value.(string)
			if !ok || !contains(field.Options, text) {
				return fmt.Errorf("%w: %s is not an allowed option", ErrInvalidSettings, key)
			}
		default:
			return fmt.Errorf("%w: unsupported type %q", ErrInvalidSettings, field.Type)
		}
	}
	for key := range values {
		if _, known := schema[key]; !known {
			return fmt.Errorf("%w: unknown field %s", ErrInvalidSettings, key)
		}
	}
	return nil
}

func validateField(field SettingField) error {
	valid := map[string]bool{"boolean": true, "text": true, "url": true, "color": true, "media": true, "integer": true, "select": true}
	if !valid[field.Type] {
		return fmt.Errorf("unsupported type %q", field.Type)
	}
	if field.Type == "select" && len(field.Options) == 0 {
		return errors.New("select requires options")
	}
	return nil
}
func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
func validManifest(m Manifest) bool {
	return m.ID != "" && m.Name != "" && m.Version != "" && m.APIVersion > 0 && len(m.ID) <= 80
}
