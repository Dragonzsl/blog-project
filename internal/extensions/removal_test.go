package extensions

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

func TestPluginRemovalPreservesDataAndSurvivesRegistration(t *testing.T) {
	ctx := context.Background()
	db := openExtensionsDatabase(t)
	router := chi.NewRouter()
	r := NewRegistry(db, router, nil)
	p := &contractPlugin{}
	for _, plugin := range []Plugin{p, testPlugin{id: "settings"}} {
		if err := r.Register(plugin); err != nil {
			t.Fatal(err)
		}
		if err := r.Enable(ctx, plugin.Manifest().ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.SaveSettings(ctx, "settings", map[string]any{"enabled": false}); err != nil {
		t.Fatal(err)
	}
	if err := (&Host{registry: r, pluginID: "contract"}).EnqueueTask(ctx, "small", "ok", "retained", time.Now()); err != nil {
		t.Fatal(err)
	}
	before := db.RenderEpoch()
	for _, id := range []string{"contract", "settings"} {
		if err := r.Remove(ctx, id); err != nil {
			t.Fatal(err)
		}
		if err := r.Remove(ctx, id); err != nil {
			t.Fatal(err)
		}
		if r.Enabled(id) {
			t.Fatal("removed plugin remains enabled")
		}
		if err := r.Enable(ctx, id); !errors.Is(err, ErrPluginRemoved) {
			t.Fatalf("enable removed: %v", err)
		}
	}
	if db.RenderEpoch() != before+2 {
		t.Fatal("removal replay invalidated cache twice")
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/plugins/contract/ping", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("removed route: %d", response.Code)
	}
	r.Dispatch(ctx, Event{Name: "contract.event.v1", Version: 1})
	if processed, err := r.ProcessOne(ctx); err != nil || processed || p.events != 0 || p.tasks != 0 {
		t.Fatal("removed plugin consumed work")
	}
	if err := (&Host{registry: r, pluginID: "contract"}).EnqueueTask(ctx, "small", "ok", "blocked", time.Now()); !errors.Is(err, ErrPluginDisabled) {
		t.Fatalf("enqueue removed: %v", err)
	}
	// Startup re-registers compiled plugins; explicit config must not undo removal.
	restarted := NewRegistry(db, chi.NewRouter(), nil)
	for _, plugin := range []Plugin{&contractPlugin{}, testPlugin{id: "settings"}} {
		if err := restarted.Register(plugin); err != nil {
			t.Fatal(err)
		}
	}
	if err := restarted.Initialize(ctx, []string{"contract", "settings"}); err != nil {
		t.Fatal(err)
	}
	states, err := restarted.States(ctx)
	if err != nil || len(states) != 2 || !states[0].Removed || !states[1].Removed || restarted.Enabled("contract") {
		t.Fatalf("restart state: %+v, %v", states, err)
	}
	for _, id := range []string{"contract", "settings"} {
		if err := restarted.Restore(ctx, id); err != nil {
			t.Fatal(err)
		}
		if restarted.Enabled(id) {
			t.Fatal("restore enabled plugin")
		}
		if err := restarted.Enable(ctx, id); err != nil {
			t.Fatal(err)
		}
		if err := restarted.Restore(ctx, id); err != nil {
			t.Fatal(err)
		}
		if !restarted.Enabled(id) {
			t.Fatal("restore replay disabled plugin")
		}
	}
	settings, err := restarted.Settings(ctx, "settings")
	if err != nil || settings["enabled"] != false {
		t.Fatalf("settings lost: %v", err)
	}
	if processed, err := restarted.ProcessOne(ctx); err != nil || !processed {
		t.Fatalf("retained task not resumed: %v", err)
	}
	if err := restarted.Remove(ctx, "unknown"); !errors.Is(err, ErrPluginNotFound) {
		t.Fatalf("unknown remove: %v", err)
	}
}

func TestPluginRemovalRollbackKeepsPluginEnabled(t *testing.T) {
	ctx := context.Background()
	db := openExtensionsDatabase(t)
	r := NewRegistry(db, chi.NewRouter(), nil)
	if err := r.Register(testPlugin{id: "keep"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Enable(ctx, "keep"); err != nil {
		t.Fatal(err)
	}
	before := db.RenderEpoch()
	if _, err := db.Writer.Exec(`CREATE TRIGGER reject_epoch BEFORE UPDATE OF render_epoch ON system_state BEGIN SELECT RAISE(ABORT, 'test failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := r.Remove(ctx, "keep"); err == nil {
		t.Fatal("expected transaction failure")
	}
	states, err := r.States(ctx)
	if err != nil || !r.Enabled("keep") || states[0].Removed || !states[0].Enabled || db.RenderEpoch() != before {
		t.Fatal("failed removal changed state")
	}
	if _, err := db.Writer.Exec("DROP TRIGGER reject_epoch"); err != nil {
		t.Fatal(err)
	}
	if err := r.Remove(ctx, "keep"); err != nil {
		t.Fatal(err)
	}
}

type removalSecurity struct{}

func (removalSecurity) CSRFToken(*http.Request) string { return "test-csrf" }
func (removalSecurity) VerifyParsedCSRF(w http.ResponseWriter, r *http.Request) bool {
	if r.PostFormValue("csrf_token") != "test-csrf" {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return false
	}
	return true
}

func TestPluginRemovalHTTPConfirmationAndRecovery(t *testing.T) {
	db := openExtensionsDatabase(t)
	router := chi.NewRouter()
	r := NewRegistry(db, router, nil)
	if err := r.Register(testPlugin{id: "sample"}); err != nil {
		t.Fatal(err)
	}
	h, err := NewHTTPHandler(r, removalSecurity{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	router.Route("/admin", h.RegisterAdmin)
	post := func(path string, form url.Values) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	for _, action := range []string{"delete", "restore"} {
		if w := post("/admin/plugins/sample/"+action, url.Values{"confirm_action": {"1"}}); w.Code != http.StatusForbidden {
			t.Fatalf("missing CSRF: %d", w.Code)
		}
	}
	if w := post("/admin/plugins/sample/delete", url.Values{"csrf_token": {"test-csrf"}}); w.Header().Get("Location") != "/admin/plugins?error=confirm" {
		t.Fatal("missing confirmation accepted")
	}
	if w := post("/admin/plugins/sample/delete", url.Values{"csrf_token": {"test-csrf"}, "confirm_action": {"1"}}); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/admin/plugins?notice=removed" {
		t.Fatal("delete failed")
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin/plugins", nil))
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "/sample/enable") || !strings.Contains(w.Body.String(), "/sample/restore") {
		t.Fatal("removed plugin list did not offer restore")
	}
	if w := post("/admin/plugins/sample/enable", url.Values{"csrf_token": {"test-csrf"}}); w.Header().Get("Location") != "/admin/plugins?error=missing" {
		t.Fatal("removed plugin enabled")
	}
	if w := post("/admin/plugins/sample/restore", url.Values{"csrf_token": {"test-csrf"}}); w.Header().Get("Location") != "/admin/plugins?notice=restored" {
		t.Fatal("restore failed")
	}
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin/plugins", nil))
	if !strings.Contains(w.Body.String(), "/sample/delete") || !strings.Contains(w.Body.String(), "name=\"confirm_action\"") || strings.Contains(w.Body.String(), "/sample/restore") {
		t.Fatal("restored plugin not in installed list")
	}
	if w := post("/admin/plugins/missing/delete", url.Values{"csrf_token": {"test-csrf"}, "confirm_action": {"1"}}); w.Header().Get("Location") != "/admin/plugins?error=missing" {
		t.Fatal("unknown plugin not rejected")
	}
}
