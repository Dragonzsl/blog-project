package extensions

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/zhushilin/blog-project/internal/operations"
	"github.com/zhushilin/blog-project/internal/platform/database"
	platformid "github.com/zhushilin/blog-project/internal/platform/id"
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
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Version         string   `json:"version"`
	APIVersion      int      `json:"apiVersion"`
	Kind            string   `json:"kind"`
	Capabilities    []string `json:"capabilities,omitempty"`
	SettingsVersion int      `json:"settingsVersion,omitempty"`
}

type SettingField struct {
	Type     string   `json:"type"`
	Required bool     `json:"required,omitempty"`
	Default  any      `json:"default,omitempty"`
	Min      *float64 `json:"minimum,omitempty"`
	Max      *float64 `json:"maximum,omitempty"`
	Options  []string `json:"options,omitempty"`
	Secret   bool     `json:"secret,omitempty"`
}

type SettingsSchema map[string]SettingField

type MenuItem struct {
	PluginID string
	Label    string
	Path     string
	Section  string
	Order    int
}

type PluginState struct {
	Manifest       Manifest
	Enabled        bool
	LastInitResult string
	UpdatedAt      time.Time
	Scope          string
	Surface        string
}

type Event struct {
	ID         []byte
	Name       string
	Version    int
	ObjectID   []byte
	Payload    map[string]any
	OccurredAt time.Time
}

type EventHandler func(context.Context, Event) error
type TaskHandler func(context.Context, []byte) error

const (
	// Plugin work is intentionally bounded. A failed remote adapter must not
	// spin forever or make the SQLite jobs table grow without an operator-visible
	// terminal state.
	maxTaskAttempts = 5
	taskLease       = 30 * time.Second
	maxSettingsJSON = 64 << 10
)

type eventSubscription struct {
	pluginID string
	version  int
	handler  EventHandler
}

type routeDefinition struct {
	method  string
	path    string
	handler http.HandlerFunc
}

type taskDefinition struct {
	version         int
	maxPayloadBytes int
}

type registration struct {
	schema          SettingsSchema
	settingsVersion int
	migrator        SettingsMigrator
	menus           []MenuItem
	events          map[string][]eventSubscription
	tasks           map[string]taskDefinition
	taskHandlers    map[string]TaskHandler
	taskRetryHooks  map[string]operations.TaskRetryHook
	routes          []routeDefinition
	adminRoutes     []routeDefinition
}

type SettingsMigrator func(context.Context, map[string]any, int, int) (map[string]any, error)

type Plugin interface {
	Manifest() Manifest
	Register(*Host) error
}

type Host struct {
	registry     *Registry
	pluginID     string
	registration *registration
}

func (h *Host) RegisterSettings(schema SettingsSchema) error {
	return h.RegisterSettingsVersion(schema, 0, nil)
}

func (h *Host) RegisterSettingsVersion(schema SettingsSchema, version int, migrator SettingsMigrator) error {
	if h.registration != nil {
		if version < 1 {
			version = h.registration.settingsVersion
		}
		if err := validateSettingsSchema(schema); err != nil {
			return err
		}
		h.registration.schema = schema
		h.registration.settingsVersion = version
		h.registration.migrator = migrator
		return nil
	}
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
	if strings.TrimSpace(item.Label) == "" || !strings.HasPrefix(item.Path, "/") || strings.Contains(item.Path, "..") {
		return ErrCapabilityDenied
	}
	if h.registration != nil {
		h.registration.menus = append(h.registration.menus, item)
		return nil
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
	version := eventVersion(event)
	subscription := eventSubscription{pluginID: h.pluginID, version: version, handler: handler}
	if h.registration != nil {
		if h.registration.events == nil {
			h.registration.events = make(map[string][]eventSubscription)
		}
		h.registration.events[event] = append(h.registration.events[event], subscription)
		return nil
	}
	h.registry.mu.Lock()
	defer h.registry.mu.Unlock()
	h.registry.events[event] = append(h.registry.events[event], subscription)
	return nil
}

func eventVersion(name string) int {
	marker := strings.LastIndex(name, ".v")
	if marker < 0 || marker+2 >= len(name) {
		return 0
	}
	version, err := strconv.Atoi(name[marker+2:])
	if err != nil || version < 1 {
		return 0
	}
	return version
}
func (h *Host) RegisterTask(kind string, handler TaskHandler) error {
	return h.registerTask(kind, handler, nil)
}

// RegisterTaskWithRetry adds a transaction-safe hook used by the operations
// page when an operator retries a terminal plugin delivery.
func (h *Host) RegisterTaskWithRetry(kind string, handler TaskHandler, retry operations.TaskRetryHook) error {
	return h.registerTask(kind, handler, retry)
}

func (h *Host) registerTask(kind string, handler TaskHandler, retry operations.TaskRetryHook) error {
	return h.registerTaskVersion(kind, 1, 64<<10, handler, retry)
}

func (h *Host) RegisterTaskVersion(kind string, version, maxPayloadBytes int, handler TaskHandler) error {
	return h.registerTaskVersion(kind, version, maxPayloadBytes, handler, nil)
}

func (h *Host) registerTaskVersion(kind string, version, maxPayloadBytes int, handler TaskHandler, retry operations.TaskRetryHook) error {
	if strings.TrimSpace(kind) == "" || handler == nil {
		return errors.New("task kind and handler are required")
	}
	if version < 1 || maxPayloadBytes < 1 || maxPayloadBytes > 64<<10 {
		return errors.New("task version or payload limit is invalid")
	}
	key := h.pluginID + ":" + kind
	definition := taskDefinition{version: version, maxPayloadBytes: maxPayloadBytes}
	if h.registration != nil {
		if _, exists := h.registration.tasks[key]; exists {
			return fmt.Errorf("task %s already registered", key)
		}
		h.registration.tasks[key] = definition
		h.registration.taskHandlers[key] = handler
		if retry != nil {
			h.registration.taskRetryHooks[key] = retry
		}
		return nil
	}
	h.registry.mu.Lock()
	defer h.registry.mu.Unlock()
	if _, exists := h.registry.tasks[key]; exists {
		return fmt.Errorf("task %s already registered", key)
	}
	h.registry.tasks[key] = handler
	h.registry.taskDefinitions[key] = definition
	if retry != nil {
		h.registry.taskRetryHooks[key] = retry
	}
	return nil
}
func (h *Host) EnqueueTask(ctx context.Context, kind string, payload any, idempotency string, availableAt time.Time) error {
	if !h.registry.isEnabled(h.pluginID) {
		return ErrPluginDisabled
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if idempotency == "" || len(idempotency) > 180 {
		return errors.New("task idempotency key is required")
	}
	definition, ok := h.registry.taskDefinition(h.pluginID, kind)
	if !ok || len(encoded) > definition.maxPayloadBytes {
		return errors.New("task payload exceeds registered contract")
	}
	if availableAt.IsZero() {
		availableAt = time.Now().UTC()
	}
	return h.registry.queue.Enqueue(ctx, operations.Task{Kind: "plugin:" + h.pluginID + ":" + kind, PayloadVersion: definition.version, Payload: encoded, IdempotencyKey: "plugin:" + h.pluginID + ":" + idempotency, AvailableAt: availableAt})
}

// EnqueueTaskTx atomically records a plugin-owned durable record and its job.
func (h *Host) EnqueueTaskTx(ctx context.Context, tx *sql.Tx, kind string, payload any, idempotency string, availableAt time.Time) error {
	if !h.registry.isEnabled(h.pluginID) {
		return ErrPluginDisabled
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if idempotency == "" || len(idempotency) > 180 {
		return errors.New("task idempotency key is required")
	}
	definition, ok := h.registry.taskDefinition(h.pluginID, kind)
	if !ok || len(encoded) > definition.maxPayloadBytes {
		return errors.New("task payload exceeds registered contract")
	}
	return h.registry.queue.EnqueueTx(ctx, tx, operations.Task{Kind: "plugin:" + h.pluginID + ":" + kind, PayloadVersion: definition.version, Payload: encoded, IdempotencyKey: "plugin:" + h.pluginID + ":" + idempotency, AvailableAt: availableAt})
}
func (h *Host) Route(method, path string, handler http.HandlerFunc) error {
	if h.registry.router == nil || handler == nil {
		return ErrCapabilityDenied
	}
	if !validRoute(method, path) || !strings.HasPrefix(path, "/plugins/"+h.pluginID+"/") {
		return ErrCapabilityDenied
	}
	return h.addRoute(false, method, path, handler)
}

// RouteSlot reserves a documented host-owned public path while keeping the
// route registration transactional. Built-in extensions use slots for their
// backwards-compatible public URLs; third-party extensions use Route with a
// /plugins/{id}/ namespace.
func (h *Host) RouteSlot(slot, method, path string, handler http.HandlerFunc) error {
	if !h.registry.routeSlotAllowed(h.pluginID, slot, path) || !validRoute(method, path) || handler == nil {
		return ErrCapabilityDenied
	}
	return h.addRoute(false, method, path, handler)
}
func (h *Host) AdminRoute(method, path string, handler http.HandlerFunc) error {
	if !strings.HasPrefix(path, "/") {
		return ErrCapabilityDenied
	}
	if h.registry.adminRouter == nil {
		return ErrCapabilityDenied
	}
	if handler == nil || strings.Contains(path, "..") {
		return ErrCapabilityDenied
	}
	prefix := h.registry.adminPrefix
	scoped := prefix + "/plugins/" + h.pluginID + path
	return h.addRoute(true, method, scoped, handler)
}

func (h *Host) addRoute(admin bool, method, path string, handler http.HandlerFunc) error {
	if !validRoute(method, path) || handler == nil {
		return ErrCapabilityDenied
	}
	definition := routeDefinition{method: strings.ToUpper(method), path: path, handler: h.guard(handler)}
	if h.registration != nil {
		if admin {
			h.registration.adminRoutes = append(h.registration.adminRoutes, definition)
		} else {
			h.registration.routes = append(h.registration.routes, definition)
		}
		return nil
	}
	return h.registry.registerRoute(definition, admin)
}

func validRoute(method, path string) bool {
	if !strings.HasPrefix(path, "/") || strings.Contains(path, "..") || strings.ContainsAny(path, "\r\n") {
		return false
	}
	switch strings.ToUpper(method) {
	case http.MethodGet:
		return true
	case http.MethodPost:
		return true
	case http.MethodPut:
		return true
	case http.MethodDelete:
		return true
	default:
		return false
	}
}
func (h *Host) Logger() *slog.Logger { return h.registry.logger.With("plugin", h.pluginID) }

func (h *Host) guard(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.registry.isEnabled(h.pluginID) {
			http.NotFound(w, r)
			return
		}
		handler(w, r)
	}
}

type Registry struct {
	db              *database.DB
	router          chi.Router
	adminRouter     chi.Router
	adminPrefix     string
	logger          *slog.Logger
	mu              sync.RWMutex
	plugins         map[string]Plugin
	schemas         map[string]SettingsSchema
	events          map[string][]eventSubscription
	tasks           map[string]TaskHandler
	taskDefinitions map[string]taskDefinition
	taskRetryHooks  map[string]operations.TaskRetryHook
	schemaVersions  map[string]int
	routeClaims     map[string]struct{}
	menus           []MenuItem
	enabled         map[string]bool
	initialized     map[string]bool
	coreTasks       map[string]TaskHandler
	coreRetryHooks  map[string]operations.TaskRetryHook
	queue           *operations.TaskQueue
	enableMu        sync.Mutex
}

func NewRegistry(db *database.DB, router chi.Router, logger *slog.Logger) *Registry {
	if logger == nil {
		logger = slog.Default()
	}
	return &Registry{db: db, router: router, adminRouter: router, adminPrefix: "/admin", logger: logger, plugins: make(map[string]Plugin), schemas: make(map[string]SettingsSchema), events: make(map[string][]eventSubscription), tasks: make(map[string]TaskHandler), taskDefinitions: make(map[string]taskDefinition), taskRetryHooks: make(map[string]operations.TaskRetryHook), schemaVersions: make(map[string]int), routeClaims: make(map[string]struct{}), coreTasks: make(map[string]TaskHandler), coreRetryHooks: make(map[string]operations.TaskRetryHook), enabled: make(map[string]bool), initialized: make(map[string]bool), queue: operations.NewTaskQueue(db)}
}

// TaskQueue returns the shared, bounded task scheduler used by core adapters.
func (r *Registry) TaskQueue() *operations.TaskQueue { return r.queue }

func (r *Registry) RegisterCoreTask(kind string, handler TaskHandler) error {
	return r.RegisterCoreTaskWithRetry(kind, handler, nil)
}

func (r *Registry) RegisterCoreTaskWithRetry(kind string, handler TaskHandler, retry operations.TaskRetryHook) error {
	if !strings.HasPrefix(kind, "core:") || handler == nil {
		return errors.New("core task kind and handler are required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.coreTasks[kind]; exists {
		return fmt.Errorf("core task %s already registered", kind)
	}
	r.coreTasks[kind] = handler
	if retry != nil {
		r.coreRetryHooks[kind] = retry
	}
	return nil
}

func (r *Registry) RetryTask(ctx context.Context, id int64) error {
	task, err := r.queue.Get(ctx, id)
	if err != nil {
		return err
	}
	r.mu.RLock()
	var hook operations.TaskRetryHook
	if strings.HasPrefix(task.Kind, "core:") {
		hook = r.coreRetryHooks[task.Kind]
	} else if strings.HasPrefix(task.Kind, "plugin:") {
		hook = r.taskRetryHooks[strings.TrimPrefix(task.Kind, "plugin:")]
	}
	r.mu.RUnlock()
	return r.queue.RetryWith(ctx, id, hook)
}

func (r *Registry) TaskList(ctx context.Context, limit int) ([]operations.TaskSummary, error) {
	return r.queue.List(ctx, limit)
}

func (r *Registry) TaskCounts(ctx context.Context) (operations.TaskCounts, error) {
	return r.queue.Counts(ctx)
}

func (r *Registry) TaskSnapshot(ctx context.Context) (operations.TaskSnapshot, error) {
	return r.queue.Snapshot(ctx)
}

// SetAdminRouter binds the protected /admin subtree. It must be called before
// enabling plugins; keeping it separate prevents a plugin from accidentally
// registering an unauthenticated administrative endpoint.
func (r *Registry) SetAdminRouter(router chi.Router) {
	r.adminRouter = router
	r.adminPrefix = ""
}

var publicRouteSlots = map[string]map[string]string{
	"content_api": {"contentapi.readonly": "/api/v1/"},
	"comments":    {"comments.local": "/posts/", "comments.external": "/posts/"},
	"newsletter":  {"newsletter.local": "/newsletter/", "newsletter.external": "/newsletter/"},
}

func (r *Registry) routeSlotAllowed(pluginID, slot, path string) bool {
	owners, ok := publicRouteSlots[slot]
	if !ok {
		return false
	}
	prefix, ok := owners[pluginID]
	return ok && strings.HasPrefix(path, prefix)
}

func routeClaimKey(admin bool, method, path string) string {
	surface := "public"
	if admin {
		surface = "admin"
	}
	return surface + ":" + strings.ToUpper(method) + ":" + path
}

func (r *Registry) registerRoute(definition routeDefinition, admin bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := routeClaimKey(admin, definition.method, definition.path)
	if _, exists := r.routeClaims[key]; exists {
		return fmt.Errorf("route %s already registered", definition.path)
	}
	r.routeClaims[key] = struct{}{}
	if admin {
		registerRouterRoute(r.adminRouter, definition)
	} else {
		registerRouterRoute(r.router, definition)
	}
	return nil
}

func registerRouterRoute(router chi.Router, definition routeDefinition) {
	switch definition.method {
	case http.MethodGet:
		router.Get(definition.path, definition.handler)
	case http.MethodPost:
		router.Post(definition.path, definition.handler)
	case http.MethodPut:
		router.Put(definition.path, definition.handler)
	case http.MethodDelete:
		router.Delete(definition.path, definition.handler)
	}
}

func (r *Registry) commitRegistration(id string, value *registration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	pending := make(map[string]struct{}, len(value.routes)+len(value.adminRoutes))
	for _, route := range value.routes {
		key := routeClaimKey(false, route.method, route.path)
		if _, exists := r.routeClaims[key]; exists {
			return fmt.Errorf("route %s already registered", route.path)
		}
		if _, exists := pending[key]; exists {
			return fmt.Errorf("route %s already registered", route.path)
		}
		pending[key] = struct{}{}
	}
	for _, route := range value.adminRoutes {
		key := routeClaimKey(true, route.method, route.path)
		if _, exists := r.routeClaims[key]; exists {
			return fmt.Errorf("admin route %s already registered", route.path)
		}
		if _, exists := pending[key]; exists {
			return fmt.Errorf("route %s already registered", route.path)
		}
		pending[key] = struct{}{}
	}
	for _, route := range value.routes {
		r.routeClaims[routeClaimKey(false, route.method, route.path)] = struct{}{}
		registerRouterRoute(r.router, route)
	}
	for _, route := range value.adminRoutes {
		r.routeClaims[routeClaimKey(true, route.method, route.path)] = struct{}{}
		registerRouterRoute(r.adminRouter, route)
	}
	if value.schema != nil {
		r.schemas[id] = value.schema
	}
	r.schemaVersions[id] = value.settingsVersion
	for event, subscriptions := range value.events {
		r.events[event] = append(r.events[event], subscriptions...)
	}
	for key, handler := range value.taskHandlers {
		r.tasks[key] = handler
		r.taskDefinitions[key] = value.tasks[key]
	}
	for key, retry := range value.taskRetryHooks {
		r.taskRetryHooks[key] = retry
	}
	r.menus = append(r.menus, value.menus...)
	return nil
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
	_, err := r.db.Writer.Exec(`INSERT INTO plugin_states(plugin_id,name,version,api_version,kind,enabled,config_schema_version,last_init_result,updated_at) VALUES(?,?,?,?,?,0,?,'disabled',?) ON CONFLICT(plugin_id) DO UPDATE SET name=excluded.name,version=excluded.version,api_version=excluded.api_version,kind=excluded.kind,updated_at=excluded.updated_at`, manifest.ID, manifest.Name, manifest.Version, manifest.APIVersion, manifest.Kind, normalizedPluginSettingsVersion(manifest.SettingsVersion), now)
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
		persisted, err := r.persistedEnabled(ctx, id)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		// Explicit configuration wins, while a state previously changed in the
		// admin UI survives a restart when no configuration entry overrides it.
		if _, ok := wanted[id]; !ok && !persisted {
			if err := r.Disable(ctx, id); err != nil {
				failures = append(failures, err)
			}
			continue
		}
		if err := r.Enable(ctx, id); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (r *Registry) persistedEnabled(ctx context.Context, id string) (bool, error) {
	var enabled int
	err := r.db.Reader.QueryRowContext(ctx, "SELECT enabled FROM plugin_states WHERE plugin_id=?", id).Scan(&enabled)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return enabled == 1, err
}

func normalizedPluginSettingsVersion(version int) int {
	if version < 1 {
		return 1
	}
	return version
}

func (r *Registry) taskDefinition(pluginID, kind string) (taskDefinition, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	definition, ok := r.taskDefinitions[pluginID+":"+kind]
	return definition, ok
}

func (r *Registry) migratePluginSettings(ctx context.Context, id string, value *registration) error {
	if value == nil || value.schema == nil {
		return nil
	}
	var storedVersion int
	var raw string
	err := r.db.Reader.QueryRowContext(ctx, "SELECT schema_version,values_json FROM plugin_settings WHERE plugin_id=?", id).Scan(&storedVersion, &raw)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if storedVersion == value.settingsVersion {
		return nil
	}
	if value.migrator == nil {
		return fmt.Errorf("%w: schema %d to %d has no migration", ErrInvalidSettings, storedVersion, value.settingsVersion)
	}
	values := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return err
	}
	migrated, err := value.migrator(ctx, values, storedVersion, value.settingsVersion)
	if err != nil {
		return err
	}
	if err := ValidateSettings(value.schema, migrated); err != nil {
		return err
	}
	encoded, err := json.Marshal(migrated)
	if err != nil {
		return err
	}
	now := time.Now().UTC().UnixMilli()
	_, err = r.db.Writer.ExecContext(ctx, `UPDATE plugin_settings SET schema_version=?,values_json=?,updated_at=? WHERE plugin_id=?`, value.settingsVersion, string(encoded), now, id)
	return err
}

func (r *Registry) Enable(ctx context.Context, id string) error {
	r.enableMu.Lock()
	defer r.enableMu.Unlock()
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
	r.mu.RLock()
	alreadyInitialized := r.initialized[id]
	r.mu.RUnlock()
	if alreadyInitialized {
		r.mu.Lock()
		r.enabled[id] = true
		r.mu.Unlock()
		_, err := r.db.Writer.ExecContext(ctx, "UPDATE plugin_states SET enabled=1,last_init_result='ready',updated_at=? WHERE plugin_id=?", time.Now().UTC().UnixMilli(), id)
		return err
	}
	host := &Host{registry: r, pluginID: id, registration: &registration{
		settingsVersion: normalizedPluginSettingsVersion(manifest.SettingsVersion),
		events:          make(map[string][]eventSubscription),
		tasks:           make(map[string]taskDefinition),
		taskHandlers:    make(map[string]TaskHandler),
		taskRetryHooks:  make(map[string]operations.TaskRetryHook),
	}}
	if err := plugin.Register(host); err != nil {
		r.mu.Lock()
		r.enabled[id] = false
		r.mu.Unlock()
		_, _ = r.db.Writer.ExecContext(ctx, "UPDATE plugin_states SET last_init_result=?,enabled=0,updated_at=? WHERE plugin_id=?", err.Error(), time.Now().UTC().UnixMilli(), id)
		return fmt.Errorf("initialize plugin %s: %w", id, err)
	}
	if err := r.migratePluginSettings(ctx, id, host.registration); err != nil {
		r.mu.Lock()
		r.enabled[id] = false
		r.mu.Unlock()
		_, _ = r.db.Writer.ExecContext(ctx, "UPDATE plugin_states SET last_init_result=?,enabled=0,updated_at=? WHERE plugin_id=?", err.Error(), time.Now().UTC().UnixMilli(), id)
		return fmt.Errorf("initialize plugin %s settings: %w", id, err)
	}
	if err := r.commitRegistration(id, host.registration); err != nil {
		r.mu.Lock()
		r.enabled[id] = false
		r.mu.Unlock()
		_, _ = r.db.Writer.ExecContext(ctx, "UPDATE plugin_states SET last_init_result=?,enabled=0,updated_at=? WHERE plugin_id=?", err.Error(), time.Now().UTC().UnixMilli(), id)
		return fmt.Errorf("initialize plugin %s registration: %w", id, err)
	}
	r.mu.Lock()
	r.initialized[id] = true
	r.enabled[id] = true
	r.mu.Unlock()
	if _, err := r.db.Writer.ExecContext(ctx, "UPDATE plugin_states SET enabled=1,last_init_result='ready',updated_at=? WHERE plugin_id=?", time.Now().UTC().UnixMilli(), id); err != nil {
		return err
	}
	return nil
}

func (r *Registry) Disable(ctx context.Context, id string) error {
	r.mu.Lock()
	_, exists := r.plugins[id]
	if exists {
		r.enabled[id] = false
	}
	r.mu.Unlock()
	if !exists {
		return ErrPluginNotFound
	}
	if _, err := r.db.Writer.ExecContext(ctx, "UPDATE plugin_states SET enabled=0,last_init_result='disabled',updated_at=? WHERE plugin_id=?", time.Now().UTC().UnixMilli(), id); err != nil {
		return err
	}
	return nil
}

func (r *Registry) isEnabled(id string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.enabled[id]
}

// Enabled reports the in-process state used by route, event, and task guards.
// It is intentionally read-only; changes go through Enable/Disable so the
// persisted plugin state remains the source of truth across restarts.
func (r *Registry) Enabled(id string) bool { return r.isEnabled(id) }

func (r *Registry) Registered() []Manifest {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Manifest, 0, len(r.plugins))
	for _, plugin := range r.plugins {
		result = append(result, plugin.Manifest())
	}
	return result
}

func (r *Registry) States(ctx context.Context) ([]PluginState, error) {
	rows, err := r.db.Reader.QueryContext(ctx, `SELECT plugin_id,name,version,api_version,kind,enabled,config_schema_version,last_init_result,updated_at FROM plugin_states ORDER BY plugin_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []PluginState
	for rows.Next() {
		var id, name, version, kind, initResult string
		var api, enabled, schemaVersion, updated int64
		if err := rows.Scan(&id, &name, &version, &api, &kind, &enabled, &schemaVersion, &initResult, &updated); err != nil {
			return nil, err
		}
		state := PluginState{Manifest: Manifest{ID: id, Name: name, Version: version, APIVersion: int(api), Kind: kind, SettingsVersion: int(schemaVersion)}, Enabled: enabled == 1, LastInitResult: initResult, UpdatedAt: time.UnixMilli(updated).UTC()}
		r.mu.RLock()
		registered := r.plugins[id]
		r.mu.RUnlock()
		if registered != nil {
			manifest := registered.Manifest()
			state.Manifest.Capabilities = append([]string(nil), manifest.Capabilities...)
			state.Manifest.SettingsVersion = normalizedPluginSettingsVersion(manifest.SettingsVersion)
		}
		state.Scope, state.Surface = pluginPresentation(state.Manifest)
		result = append(result, state)
	}
	return result, rows.Err()
}

func pluginPresentation(manifest Manifest) (string, string) {
	capabilities := make(map[string]struct{}, len(manifest.Capabilities))
	for _, capability := range manifest.Capabilities {
		capabilities[capability] = struct{}{}
	}
	var scope []string
	if _, ok := capabilities["public_route"]; ok {
		scope = append(scope, "公开端")
	}
	if _, ok := capabilities["admin_menu"]; ok {
		scope = append(scope, "管理端")
	}
	if _, ok := capabilities["persistent_task"]; ok {
		scope = append(scope, "后台任务")
	}
	if len(scope) == 0 {
		scope = append(scope, "基础能力")
	}
	surface := map[string]string{"analytics": "访问统计", "comment_provider": "评论", "newsletter": "Newsletter", "content_api": "内容 API", "webhook": "Webhook"}[manifest.Kind]
	if surface == "" {
		surface = manifest.Kind
	}
	return strings.Join(scope, " · "), surface
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
	handlers := append([]eventSubscription(nil), r.events[event.Name]...)
	r.mu.RUnlock()
	var failures []error
	for _, subscription := range handlers {
		if !r.isEnabled(subscription.pluginID) {
			continue
		}
		if subscription.version > 0 && event.Version != subscription.version {
			continue
		}
		if err := subscription.handler(ctx, event); err != nil {
			failures = append(failures, err)
			r.logger.ErrorContext(ctx, "plugin event failed", "event", event.Name, "error", operations.SafeError(err))
		}
	}
	return failures
}

type durableEventTask struct {
	EventID string `json:"event_id"`
}

// RecordEventTx persists an event and its dispatch job in the caller's
// business transaction. A publication or moderation write therefore cannot
// commit without its durable after-commit work.
func (r *Registry) RecordEventTx(ctx context.Context, tx *sql.Tx, event Event) error {
	if r == nil || r.queue == nil || tx == nil {
		return errors.New("event recorder is not configured")
	}
	if strings.TrimSpace(event.Name) == "" || len(event.Name) > 120 || strings.ContainsAny(event.Name, "\r\n") || event.Version < 1 || len(event.ObjectID) > 64 {
		return errors.New("event metadata is invalid")
	}
	now := event.OccurredAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	eventID, err := platformid.NewPublicID(now)
	if err != nil {
		return err
	}
	payload := event.Payload
	if payload == nil {
		payload = map[string]any{}
	}
	encoded, err := json.Marshal(payload)
	if err != nil || len(encoded) > 16<<10 {
		return errors.New("event payload is invalid or too large")
	}
	objectID := append([]byte(nil), event.ObjectID...)
	if objectID == nil {
		objectID = []byte{}
	}
	eventIDHex := hex.EncodeToString(eventID)
	if _, err := tx.ExecContext(ctx, `INSERT INTO event_outbox(event_id,event_name,event_version,object_public_id,payload,status,occurred_at,created_at,updated_at) VALUES(?,?,?,?,?,'pending',?,?,?)`, eventID, event.Name, event.Version, objectID, encoded, now.UnixMilli(), now.UnixMilli(), now.UnixMilli()); err != nil {
		return err
	}
	taskPayload, err := json.Marshal(durableEventTask{EventID: eventIDHex})
	if err != nil {
		return err
	}
	return r.queue.EnqueueTx(ctx, tx, operations.Task{Kind: "core:event_dispatch", PayloadVersion: 1, Payload: taskPayload, IdempotencyKey: "event:" + eventIDHex, AvailableAt: now})
}

func (r *Registry) processDurableEvent(ctx context.Context, payload []byte) error {
	var task durableEventTask
	if err := json.Unmarshal(payload, &task); err != nil || len(task.EventID) != 32 {
		return errors.New("invalid event task payload")
	}
	eventID, err := hex.DecodeString(task.EventID)
	if err != nil || len(eventID) != 16 {
		return errors.New("invalid event ID")
	}
	var event Event
	var eventPayload []byte
	var status string
	var occurredAt int64
	err = r.db.Reader.QueryRowContext(ctx, `SELECT event_name,event_version,object_public_id,payload,status,occurred_at FROM event_outbox WHERE event_id=?`, eventID).Scan(&event.Name, &event.Version, &event.ObjectID, &eventPayload, &status, &occurredAt)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("event record not found")
	}
	if err != nil {
		return err
	}
	if status == "succeeded" {
		return nil
	}
	if err := json.Unmarshal(eventPayload, &event.Payload); err != nil {
		return err
	}
	event.ID = append([]byte(nil), eventID...)
	event.OccurredAt = time.UnixMilli(occurredAt).UTC()
	if failures := r.Dispatch(ctx, event); len(failures) > 0 {
		message := operations.SafeError(errors.Join(failures...))
		_, _ = r.db.Writer.ExecContext(ctx, `UPDATE event_outbox SET last_error=?,updated_at=? WHERE event_id=?`, message, time.Now().UTC().UnixMilli(), eventID)
		return errors.Join(failures...)
	}
	_, err = r.db.Writer.ExecContext(ctx, `UPDATE event_outbox SET status='succeeded',last_error='',updated_at=? WHERE event_id=?`, time.Now().UTC().UnixMilli(), eventID)
	return err
}

func (r *Registry) ProcessOne(ctx context.Context) (bool, error) {
	r.mu.RLock()
	handlers := make(map[string]TaskHandler, len(r.tasks))
	for key, handler := range r.tasks {
		handlers[key] = handler
	}
	coreHandlers := make(map[string]TaskHandler, len(r.coreTasks))
	for key, handler := range r.coreTasks {
		coreHandlers[key] = handler
	}
	definitions := make(map[string]taskDefinition, len(r.taskDefinitions))
	for key, definition := range r.taskDefinitions {
		definitions[key] = definition
	}
	r.mu.RUnlock()
	return r.queue.ProcessOne(ctx, func(task operations.Task) (operations.TaskHandler, bool) {
		if task.Kind == "core:event_dispatch" {
			return func(ctx context.Context, task operations.Task) error { return r.processDurableEvent(ctx, task.Payload) }, true
		}
		if strings.HasPrefix(task.Kind, "core:") {
			handler, ok := coreHandlers[task.Kind]
			if !ok {
				return nil, false
			}
			return func(ctx context.Context, task operations.Task) error { return handler(ctx, task.Payload) }, true
		}
		if !strings.HasPrefix(task.Kind, "plugin:") {
			return nil, false
		}
		key := strings.TrimPrefix(task.Kind, "plugin:")
		pluginID := key
		if separator := strings.IndexByte(key, ':'); separator >= 0 {
			pluginID = key[:separator]
		}
		if !r.isEnabled(pluginID) {
			return nil, false
		}
		handler, ok := handlers[key]
		if !ok {
			return nil, false
		}
		definition, ok := definitions[key]
		if !ok || task.PayloadVersion != definition.version {
			return func(context.Context, operations.Task) error {
				return operations.Permanent(fmt.Errorf("unsupported payload version %d for task %s", task.PayloadVersion, key))
			}, true
		}
		return func(ctx context.Context, task operations.Task) error { return handler(ctx, task.Payload) }, true
	})
}

func (r *Registry) registerSettings(id string, schema SettingsSchema) error {
	if err := validateSettingsSchema(schema); err != nil {
		return err
	}
	r.mu.Lock()
	r.schemas[id] = schema
	r.mu.Unlock()
	return nil
}

func (r *Registry) Settings(ctx context.Context, id string) (map[string]any, error) {
	r.mu.RLock()
	schema, registered := r.schemas[id]
	r.mu.RUnlock()
	if !registered {
		return nil, ErrPluginNotFound
	}
	result, err := r.rawSettings(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := ValidateSettings(schema, result); err != nil {
		return nil, err
	}
	for key, field := range schema {
		if field.Secret {
			if _, ok := result[key]; ok {
				result[key] = "••••••"
			}
		}
	}
	return result, nil
}

func (r *Registry) rawSettings(ctx context.Context, id string) (map[string]any, error) {
	var raw string
	err := r.db.Reader.QueryRowContext(ctx, "SELECT values_json FROM plugin_settings WHERE plugin_id=?", id).Scan(&raw)
	if err == sql.ErrNoRows {
		return r.defaults(id), nil
	}
	if err != nil {
		return nil, err
	}
	if len(raw) > maxSettingsJSON {
		return nil, fmt.Errorf("%w: settings JSON is too large", ErrInvalidSettings)
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
	version := r.schemaVersions[id]
	r.mu.RUnlock()
	if !ok {
		return ErrPluginNotFound
	}
	existing, err := r.rawSettings(ctx, id)
	if err != nil {
		return err
	}
	for key, value := range values {
		if field, known := schema[key]; known && field.Secret {
			// Empty values are what the server-rendered admin form submits for
			// masked secrets. Preserve the stored value just like the mask.
			if value == "••••••" || value == "" {
				continue
			}
		}
		existing[key] = value
	}
	if err := ValidateSettings(schema, existing); err != nil {
		return err
	}
	raw, err := json.Marshal(existing)
	if err != nil {
		return err
	}
	now := time.Now().UTC().UnixMilli()
	_, err = r.db.Writer.ExecContext(ctx, `INSERT INTO plugin_settings(plugin_id,schema_version,values_json,updated_at) SELECT ?,?,?,? ON CONFLICT(plugin_id) DO UPDATE SET schema_version=excluded.schema_version,values_json=excluded.values_json,updated_at=excluded.updated_at`, id, normalizedPluginSettingsVersion(version), string(raw), now)
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
	if err := validateSettingsSchema(schema); err != nil {
		return err
	}
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
			text, ok := value.(string)
			if !ok || len(text) > 4096 {
				return fmt.Errorf("%w: %s must be string", ErrInvalidSettings, key)
			}
			if field.Type == "url" && text != "" {
				parsed, err := url.ParseRequestURI(text)
				if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
					return fmt.Errorf("%w: %s must be an http(s) URL", ErrInvalidSettings, key)
				}
			}
			if field.Type == "color" && text != "" && !validPluginColor(text) {
				return fmt.Errorf("%w: %s must be a hex color", ErrInvalidSettings, key)
			}
			if field.Type == "media" && text != "" && !validPluginMediaID(text) {
				return fmt.Errorf("%w: %s must be a media public id", ErrInvalidSettings, key)
			}
		case "integer":
			number, ok := pluginNumber(value)
			if !ok || number != float64(int64(number)) || field.Min != nil && number < *field.Min || field.Max != nil && number > *field.Max {
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
	encoded, err := json.Marshal(values)
	if err != nil || len(encoded) > maxSettingsJSON {
		return fmt.Errorf("%w: settings JSON is too large", ErrInvalidSettings)
	}
	return nil
}

func validateSettingsSchema(schema SettingsSchema) error {
	if len(schema) > 64 {
		return fmt.Errorf("%w: too many settings", ErrInvalidSettings)
	}
	for key, field := range schema {
		if strings.TrimSpace(key) == "" || len(key) > 64 {
			return fmt.Errorf("%w: invalid setting key", ErrInvalidSettings)
		}
		if err := validateField(field); err != nil {
			return fmt.Errorf("%w: %s: %v", ErrInvalidSettings, key, err)
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
	if field.Secret && field.Type != "text" && field.Type != "url" {
		return errors.New("secret settings must be text or url")
	}
	if field.Min != nil && field.Max != nil && *field.Min > *field.Max {
		return errors.New("minimum exceeds maximum")
	}
	seen := make(map[string]struct{}, len(field.Options))
	for _, option := range field.Options {
		if option == "" || len(option) > 256 {
			return errors.New("select option is invalid")
		}
		if _, exists := seen[option]; exists {
			return errors.New("select options must be unique")
		}
		seen[option] = struct{}{}
	}
	if field.Default != nil {
		if err := validatePluginValue(field, field.Default); err != nil {
			return err
		}
	}
	return nil
}

func validatePluginValue(field SettingField, value any) error {
	switch field.Type {
	case "boolean":
		if _, ok := value.(bool); !ok {
			return errors.New("default must be boolean")
		}
	case "text", "url", "color", "media", "select":
		text, ok := value.(string)
		if !ok || len(text) > 4096 {
			return errors.New("default must be a bounded string")
		}
		if field.Type == "url" && text != "" {
			parsed, err := url.ParseRequestURI(text)
			if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
				return errors.New("default must be an http(s) URL")
			}
		}
		if field.Type == "select" && !contains(field.Options, text) {
			return errors.New("default is not an allowed option")
		}
		if field.Type == "color" && text != "" && !validPluginColor(text) {
			return errors.New("default must be a hex color")
		}
		if field.Type == "media" && text != "" && !validPluginMediaID(text) {
			return errors.New("default must be a media public id")
		}
	case "integer":
		number, ok := pluginNumber(value)
		if !ok || number != float64(int64(number)) || field.Min != nil && number < *field.Min || field.Max != nil && number > *field.Max {
			return errors.New("default integer is out of range")
		}
	default:
		return errors.New("default has unsupported type")
	}
	return nil
}

func pluginNumber(value any) (float64, bool) {
	switch number := value.(type) {
	case float64:
		return number, true
	case float32:
		return float64(number), true
	case int:
		return float64(number), true
	case int64:
		return float64(number), true
	case json.Number:
		parsed, err := number.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func validPluginColor(value string) bool {
	if len(value) != 4 && len(value) != 7 || value[0] != '#' {
		return false
	}
	for _, r := range value[1:] {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}

func validPluginMediaID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
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
	if m.ID == "" || m.Name == "" || m.Version == "" || m.APIVersion <= 0 || len(m.ID) > 80 {
		return false
	}
	for index, r := range m.ID {
		if !(r == '-' || r == '_' || r == '.' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9') || index == 0 && (r == '.' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
