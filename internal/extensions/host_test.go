package extensions

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/zhushilin/blog-project/internal/platform/config"
	"github.com/zhushilin/blog-project/internal/platform/database"
)

type testPlugin struct {
	id, kind string
	fail     bool
}

func (p testPlugin) Manifest() Manifest {
	return Manifest{ID: p.id, Name: p.id, Version: "1.0.0", APIVersion: HostAPIVersion, Kind: p.kind}
}
func (p testPlugin) Register(h *Host) error {
	if p.fail {
		return errors.New("init failed")
	}
	if err := h.RegisterSettings(SettingsSchema{"enabled": {Type: "boolean", Default: true}}); err != nil {
		return err
	}
	if err := h.RegisterMenu(MenuItem{Label: p.id, Path: "/" + p.id}); err != nil {
		return err
	}
	if err := h.RegisterTask("refresh", func(context.Context, []byte) error { return nil }); err != nil {
		return err
	}
	return h.Subscribe("test.event", func(context.Context, Event) error { return nil })
}

func TestRegistrySettingsEventsTasksAndFailureIsolation(t *testing.T) {
	db := openExtensionsDatabase(t)
	registry := NewRegistry(db, chi.NewRouter(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := registry.Register(testPlugin{id: "ok"}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(testPlugin{id: "bad", fail: true}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Initialize(context.Background(), []string{"bad"}); err == nil {
		t.Fatal("failed plugin was not reported")
	}
	if err := registry.Enable(context.Background(), "ok"); err != nil {
		t.Fatal(err)
	}
	if err := registry.SaveSettings(context.Background(), "ok", map[string]any{"enabled": false}); err != nil {
		t.Fatal(err)
	}
	settings, err := registry.Settings(context.Background(), "ok")
	if err != nil || settings["enabled"] != false {
		t.Fatalf("settings=%v err=%v", settings, err)
	}
	if len(registry.Dispatch(context.Background(), Event{Name: "test.event", Version: 1})) != 0 {
		t.Fatal("event handler failed")
	}
	if err := (&Host{registry: registry, pluginID: "ok"}).EnqueueTask(context.Background(), "refresh", map[string]string{"x": "y"}, "test-id", time.Now()); err != nil {
		t.Fatal(err)
	}
	processed, err := registry.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
	var status string
	if err := db.Reader.QueryRowContext(context.Background(), "SELECT status FROM jobs WHERE id=1").Scan(&status); err != nil || status != "succeeded" {
		t.Fatalf("job status=%s err=%v", status, err)
	}
}

func TestCommentProviderMutualExclusion(t *testing.T) {
	db := openExtensionsDatabase(t)
	r := NewRegistry(db, chi.NewRouter(), nil)
	if err := r.Register(testPlugin{id: "local", kind: "comment_provider"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(testPlugin{id: "external", kind: "comment_provider"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Enable(context.Background(), "local"); err != nil {
		t.Fatal(err)
	}
	if err := r.Enable(context.Background(), "external"); !errors.Is(err, ErrProviderConflict) {
		t.Fatalf("conflict err=%v", err)
	}
}

func TestPluginRouteIsScoped(t *testing.T) {
	db := openExtensionsDatabase(t)
	router := chi.NewRouter()
	r := NewRegistry(db, router, nil)
	plugin := routePlugin{}
	if err := r.Register(plugin); err != nil {
		t.Fatal(err)
	}
	if err := r.Enable(context.Background(), "route"); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/admin/plugins/route/ping", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status=%d", response.Code)
	}
}

func TestDisabledPluginDoesNotReceiveEventsOrTasks(t *testing.T) {
	db := openExtensionsDatabase(t)
	registry := NewRegistry(db, chi.NewRouter(), nil)
	plugin := &countingPlugin{}
	if err := registry.Register(plugin); err != nil {
		t.Fatal(err)
	}
	if err := registry.Enable(context.Background(), "counting"); err != nil {
		t.Fatal(err)
	}
	if err := registry.Disable(context.Background(), "counting"); err != nil {
		t.Fatal(err)
	}
	registry.Dispatch(context.Background(), Event{Name: "counting.event", Version: 1})
	if plugin.events != 0 || plugin.tasks != 0 {
		t.Fatalf("disabled plugin ran: events=%d tasks=%d", plugin.events, plugin.tasks)
	}
	if _, err := db.Writer.ExecContext(context.Background(), `INSERT INTO jobs(kind,payload_version,payload,idempotency_key,status,available_at,attempts,created_at,updated_at) VALUES('plugin:counting:refresh',1,X'7B7D','disabled-task','pending',0,0,0,0)`); err != nil {
		t.Fatal(err)
	}
	processed, err := registry.ProcessOne(context.Background())
	if err != nil || processed {
		t.Fatalf("disabled task processed=%v err=%v", processed, err)
	}
	var status string
	if err := db.Reader.QueryRowContext(context.Background(), "SELECT status FROM jobs WHERE id=1").Scan(&status); err != nil || status != "pending" {
		t.Fatalf("disabled task status=%s err=%v", status, err)
	}
}

func TestFailedRegistrationLeavesNoHostState(t *testing.T) {
	db := openExtensionsDatabase(t)
	router := chi.NewRouter()
	registry := NewRegistry(db, router, nil)
	plugin := &partialPlugin{}
	if err := registry.Register(plugin); err != nil {
		t.Fatal(err)
	}
	if err := registry.Enable(context.Background(), "partial"); err == nil {
		t.Fatal("partial registration unexpectedly succeeded")
	}
	if len(registry.Menus()) != 0 {
		t.Fatalf("menus leaked after failed registration: %+v", registry.Menus())
	}
	if _, err := registry.Settings(context.Background(), "partial"); !errors.Is(err, ErrPluginNotFound) {
		t.Fatalf("settings leaked: %v", err)
	}
	registry.Dispatch(context.Background(), Event{Name: "partial.event.v1", Version: 1})
	if plugin.events != 0 {
		t.Fatalf("event handler leaked: %d", plugin.events)
	}
	request := httptest.NewRequest(http.MethodGet, "/plugins/partial/ping", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("route leaked with status=%d", response.Code)
	}
	if registry.Enabled("partial") {
		t.Fatal("failed plugin is enabled")
	}
}

func TestPluginPublicNamespaceVersionAndPayloadContracts(t *testing.T) {
	db := openExtensionsDatabase(t)
	router := chi.NewRouter()
	registry := NewRegistry(db, router, nil)
	plugin := &contractPlugin{}
	if err := registry.Register(plugin); err != nil {
		t.Fatal(err)
	}
	if err := registry.Enable(context.Background(), "contract"); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/plugins/contract/ping", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("namespaced route status=%d", response.Code)
	}
	host := &Host{registry: registry, pluginID: "contract"}
	if err := host.Route(http.MethodGet, "/admin/escape", func(http.ResponseWriter, *http.Request) {}); !errors.Is(err, ErrCapabilityDenied) {
		t.Fatalf("unscoped route err=%v", err)
	}
	registry.Dispatch(context.Background(), Event{Name: "contract.event.v2", Version: 2})
	if plugin.events != 0 {
		t.Fatalf("wrong event version dispatched: %d", plugin.events)
	}
	registry.Dispatch(context.Background(), Event{Name: "contract.event.v1", Version: 1})
	if plugin.events != 1 {
		t.Fatalf("matching event version not dispatched: %d", plugin.events)
	}
	if err := host.EnqueueTask(context.Background(), "small", "too-long", "small-1", time.Now()); err == nil {
		t.Fatal("oversized task payload was accepted")
	}
	if err := host.EnqueueTask(context.Background(), "small", "ok", "small-2", time.Now()); err != nil {
		t.Fatal(err)
	}
	if processed, err := registry.ProcessOne(context.Background()); err != nil || !processed || plugin.tasks != 1 {
		t.Fatalf("task processed=%v tasks=%d err=%v", processed, plugin.tasks, err)
	}
}

func TestPluginSecretSettingsAreMaskedAndPlaceholderPreservesValue(t *testing.T) {
	db := openExtensionsDatabase(t)
	registry := NewRegistry(db, chi.NewRouter(), nil)
	if err := registry.Register(secretPlugin{}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Enable(context.Background(), "secret"); err != nil {
		t.Fatal(err)
	}
	if err := registry.SaveSettings(context.Background(), "secret", map[string]any{"token": "private-value"}); err != nil {
		t.Fatal(err)
	}
	settings, err := registry.Settings(context.Background(), "secret")
	if err != nil || settings["token"] != "••••••" {
		t.Fatalf("masked settings=%v err=%v", settings, err)
	}
	if err := registry.SaveSettings(context.Background(), "secret", map[string]any{"token": "••••••"}); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := db.Reader.QueryRowContext(context.Background(), "SELECT json_extract(values_json,'$.token') FROM plugin_settings WHERE plugin_id='secret'").Scan(&stored); err != nil || stored != "private-value" {
		t.Fatalf("stored secret=%q err=%v", stored, err)
	}
}

func TestPluginSettingsMigrationIsVersionedAndFailureIsolated(t *testing.T) {
	t.Run("migrates before enable", func(t *testing.T) {
		db := openExtensionsDatabase(t)
		registry := NewRegistry(db, chi.NewRouter(), nil)
		if err := registry.Register(migrationPlugin{id: "migrate"}); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Writer.ExecContext(context.Background(), `INSERT INTO plugin_settings(plugin_id,schema_version,values_json,updated_at) VALUES('migrate',1,'{"name":"legacy"}',0)`); err != nil {
			t.Fatal(err)
		}
		if err := registry.Enable(context.Background(), "migrate"); err != nil {
			t.Fatal(err)
		}
		settings, err := registry.Settings(context.Background(), "migrate")
		if err != nil || settings["label"] != "legacy" {
			t.Fatalf("settings=%v err=%v", settings, err)
		}
		var version int
		if err := db.Reader.QueryRowContext(context.Background(), "SELECT schema_version FROM plugin_settings WHERE plugin_id='migrate'").Scan(&version); err != nil || version != 2 {
			t.Fatalf("schema version=%d err=%v", version, err)
		}
		states, err := registry.States(context.Background())
		if err != nil || len(states) != 1 || states[0].Manifest.SettingsVersion != 2 {
			t.Fatalf("states=%+v err=%v", states, err)
		}
	})

	t.Run("keeps old value after migration failure", func(t *testing.T) {
		db := openExtensionsDatabase(t)
		registry := NewRegistry(db, chi.NewRouter(), nil)
		if err := registry.Register(migrationPlugin{id: "broken-migrate", fail: true}); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Writer.ExecContext(context.Background(), `INSERT INTO plugin_settings(plugin_id,schema_version,values_json,updated_at) VALUES('broken-migrate',1,'{"name":"legacy"}',0)`); err != nil {
			t.Fatal(err)
		}
		if err := registry.Enable(context.Background(), "broken-migrate"); err == nil {
			t.Fatal("migration failure was not reported")
		}
		var version int
		var values string
		if err := db.Reader.QueryRowContext(context.Background(), "SELECT schema_version,values_json FROM plugin_settings WHERE plugin_id='broken-migrate'").Scan(&version, &values); err != nil || version != 1 || values != `{"name":"legacy"}` {
			t.Fatalf("old settings changed: version=%d values=%q err=%v", version, values, err)
		}
		if _, err := registry.Settings(context.Background(), "broken-migrate"); !errors.Is(err, ErrPluginNotFound) {
			t.Fatalf("failed migration leaked schema: %v", err)
		}
	})
}

type partialPlugin struct{ events int }

func (p *partialPlugin) Manifest() Manifest {
	return Manifest{ID: "partial", Name: "partial", Version: "1.0.0", APIVersion: HostAPIVersion}
}

func (p *partialPlugin) Register(h *Host) error {
	if err := h.RegisterSettings(SettingsSchema{"enabled": {Type: "boolean", Default: true}}); err != nil {
		return err
	}
	if err := h.RegisterMenu(MenuItem{Label: "partial", Path: "/partial"}); err != nil {
		return err
	}
	if err := h.Route(http.MethodGet, "/plugins/partial/ping", func(http.ResponseWriter, *http.Request) {}); err != nil {
		return err
	}
	if err := h.Subscribe("partial.event.v1", func(context.Context, Event) error { p.events++; return nil }); err != nil {
		return err
	}
	return errors.New("registration failed after declarations")
}

type contractPlugin struct {
	events int
	tasks  int
}

func (p *contractPlugin) Manifest() Manifest {
	return Manifest{ID: "contract", Name: "contract", Version: "1.0.0", APIVersion: HostAPIVersion}
}

func (p *contractPlugin) Register(h *Host) error {
	if err := h.Route(http.MethodGet, "/plugins/contract/ping", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }); err != nil {
		return err
	}
	if err := h.Subscribe("contract.event.v1", func(context.Context, Event) error { p.events++; return nil }); err != nil {
		return err
	}
	return h.RegisterTaskVersion("small", 2, 4, func(context.Context, []byte) error { p.tasks++; return nil })
}

type secretPlugin struct{}

func (secretPlugin) Manifest() Manifest {
	return Manifest{ID: "secret", Name: "secret", Version: "1.0.0", APIVersion: HostAPIVersion, SettingsVersion: 1}
}

func (secretPlugin) Register(h *Host) error {
	return h.RegisterSettingsVersion(SettingsSchema{"token": {Type: "text", Secret: true}}, 1, nil)
}

type migrationPlugin struct {
	id   string
	fail bool
}

func (p migrationPlugin) Manifest() Manifest {
	return Manifest{ID: p.id, Name: p.id, Version: "1.0.0", APIVersion: HostAPIVersion, SettingsVersion: 2}
}

func (p migrationPlugin) Register(h *Host) error {
	return h.RegisterSettingsVersion(SettingsSchema{"label": {Type: "text", Required: true}}, 2, func(_ context.Context, values map[string]any, from, to int) (map[string]any, error) {
		if p.fail {
			return nil, errors.New("migration failed")
		}
		if from != 1 || to != 2 {
			return nil, errors.New("unexpected migration versions")
		}
		values["label"] = values["name"]
		delete(values, "name")
		return values, nil
	})
}

type countingPlugin struct {
	events int
	tasks  int
}

func (*countingPlugin) Manifest() Manifest {
	return Manifest{ID: "counting", Name: "counting", Version: "1.0.0", APIVersion: HostAPIVersion}
}

func (p *countingPlugin) Register(host *Host) error {
	if err := host.Subscribe("counting.event", func(context.Context, Event) error { p.events++; return nil }); err != nil {
		return err
	}
	return host.RegisterTask("refresh", func(context.Context, []byte) error { p.tasks++; return nil })
}

type routePlugin struct{}

func (routePlugin) Manifest() Manifest {
	return Manifest{ID: "route", Name: "route", Version: "1", APIVersion: HostAPIVersion}
}
func (routePlugin) Register(h *Host) error {
	return h.AdminRoute(http.MethodGet, "/ping", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
}

func openExtensionsDatabase(t *testing.T) *database.DB {
	t.Helper()
	db, err := database.Open(context.Background(), config.Database{Path: filepath.Join(t.TempDir(), "blog.sqlite"), BusyTimeout: config.Duration{Duration: time.Second}, CacheSizeKiB: 4096, ReadConnections: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
