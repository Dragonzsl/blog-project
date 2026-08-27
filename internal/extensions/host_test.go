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
