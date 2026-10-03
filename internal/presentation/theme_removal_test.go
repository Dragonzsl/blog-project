package presentation

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/zhushilin/blog-project/internal/platform/config"
	"github.com/zhushilin/blog-project/internal/platform/database"
)

func removalThemeCatalog(t *testing.T) (*ThemeCatalog, ThemeRecord) {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, config.Database{Path: filepath.Join(t.TempDir(), "site.sqlite"), BusyTimeout: config.Duration{Duration: time.Second}, CacheSizeKiB: 4096, ReadConnections: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	fallback, err := NewDefaultTheme(NewMarkdown())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "themes")
	manager, err := NewThemeManager(fallback, root)
	if err != nil {
		t.Fatal(err)
	}
	catalog := NewThemeCatalog(db, manager)
	files := map[string]string{"assets/theme.css": "body{color:#123}", "templates/navigation.html": `{{define "primary_navigation"}}{{end}}{{define "footer_navigation"}}{{end}}{{define "page_metadata"}}{{end}}{{define "pagination"}}{{end}}`}
	for _, name := range []string{"home", "article", "listing", "search", "status", "directory"} {
		files["templates/"+name+".html"] = "<!doctype html><main>Test theme</main>"
	}
	record, err := catalog.Install(ctx, bytes.NewReader(themeArchive(t, ThemeManifest{ID: "removable", Name: "Removable", Version: "1.0.0", ThemeAPI: ThemeAPIVersion, SettingsVersion: 1, SettingsSchema: map[string]SettingDefinition{"accent": {Type: "color", Default: "#123"}}}, files)), ThemeInstallOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	return catalog, record
}

func TestThemeRemovalRetainsSettingsAndProtectsActive(t *testing.T) {
	c, record := removalThemeCatalog(t)
	ctx := context.Background()
	if err := c.SaveSettings(ctx, "removable", "1.0.0", map[string]any{"accent": "#456"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Activate(ctx, "removable", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := c.Remove(ctx, "removable", "1.0.0"); err == nil {
		t.Fatal("active theme removed")
	}
	if err := c.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := c.Remove(ctx, "removable", "1.0.0"); err != nil {
			t.Fatal(err)
		}
	}
	if list, err := c.List(ctx); err != nil || len(list) != 0 {
		t.Fatalf("installed list: %v", err)
	}
	if list, err := c.ListRemoved(ctx); err != nil || len(list) != 1 {
		t.Fatalf("removed list: %v", err)
	}
	if _, err := os.Stat(record.Path); err != nil {
		t.Fatal("package lost", err)
	}
	if err := c.Activate(ctx, "removable", "1.0.0"); err == nil {
		t.Fatal("removed theme activated")
	}
	if _, err := c.Theme(ctx, "removable", "1.0.0"); err == nil {
		t.Fatal("removed theme preview allowed")
	}
	restarted := NewThemeCatalog(c.db, c.manager)
	if err := restarted.Reconcile(ctx); err != nil || !c.manager.IsFallback() {
		t.Fatalf("restart: %v", err)
	}
	if list, err := restarted.ListRemoved(ctx); err != nil || len(list) != 1 {
		t.Fatal("removal lost on restart")
	}
	for i := 0; i < 2; i++ {
		if err := restarted.Restore(ctx, "removable", "1.0.0"); err != nil {
			t.Fatal(err)
		}
	}
	if !c.manager.IsFallback() {
		t.Fatal("restore activated theme")
	}
	_, settings, err := restarted.Settings(ctx, "removable", "1.0.0")
	if err != nil || settings["accent"] != "#456" {
		t.Fatal("settings lost", err)
	}
	if err := c.Remove(ctx, "default", "1.0.0"); err == nil {
		t.Fatal("embedded fallback removed")
	}
	var audits int
	if err := c.db.Reader.QueryRow("SELECT count(*) FROM audit_entries WHERE action IN ('theme.removed','theme.restored')").Scan(&audits); err != nil || audits != 2 {
		t.Fatal("replay generated extra writes", audits, err)
	}
}

func TestThemeRestoreRejectsChangedPackage(t *testing.T) {
	c, record := removalThemeCatalog(t)
	ctx := context.Background()
	if err := c.Remove(ctx, "removable", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(record.Path, "assets/theme.css"), []byte("modified"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := c.Restore(ctx, "removable", "1.0.0"); err == nil {
		t.Fatal("changed package restored")
	}
	if list, err := c.ListRemoved(ctx); err != nil || len(list) != 1 {
		t.Fatal("failed restore changed state")
	}
}

type removalThemeSecurity struct{}

func (removalThemeSecurity) CSRFToken(*http.Request) string { return "test-csrf" }
func (removalThemeSecurity) VerifyParsedCSRF(w http.ResponseWriter, r *http.Request) bool {
	if r.PostForm.Get("csrf_token") != "test-csrf" {
		http.Error(w, "Forbidden", 403)
		return false
	}
	return true
}

func TestThemeRemovalHTTPConfirmationAndCSRF(t *testing.T) {
	c, _ := removalThemeCatalog(t)
	h, err := NewThemeHTTPHandler(c, c.manager, removalThemeSecurity{}, presentationSiteNamer{}, slog.Default(), ThemeInstallOptions{Root: c.Root(), MaxBytes: 32 << 20})
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Route("/admin", h.RegisterAdmin)
	for _, tc := range []struct {
		path, csrf, confirm string
		status              int
	}{
		{"delete", "", "1", 403}, {"restore", "", "", 403}, {"delete", "test-csrf", "", 422}, {"delete", "test-csrf", "1", 303}, {"delete", "test-csrf", "1", 303}, {"restore", "test-csrf", "", 303},
	} {
		form := url.Values{"csrf_token": {tc.csrf}, "confirm_action": {tc.confirm}}
		req := httptest.NewRequest("POST", "/admin/themes/removable/1.0.0/"+tc.path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if response.Code != tc.status {
			t.Fatalf("%s: got %d want %d", tc.path, response.Code, tc.status)
		}
	}
	if err := c.Activate(context.Background(), "removable", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest("GET", "/admin/themes", nil))
	if response.Code != 200 || strings.Contains(response.Body.String(), `action="/admin/themes/removable/1.0.0/delete"`) {
		t.Fatal("active theme exposes deletion")
	}
}

func TestThemeRemovalTransactionFailureKeepsInstalled(t *testing.T) {
	c, _ := removalThemeCatalog(t)
	ctx := context.Background()
	if _, err := c.db.Writer.Exec(`CREATE TRIGGER reject_theme_removal BEFORE INSERT ON audit_entries WHEN NEW.action='theme.removed' BEGIN SELECT RAISE(ABORT,'test failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := c.Remove(ctx, "removable", "1.0.0"); err == nil {
		t.Fatal("expected transaction failure")
	}
	if list, err := c.List(ctx); err != nil || len(list) != 1 {
		t.Fatal("partial removal persisted")
	}
}

func TestThemeRollbackSkipsRemovedPreviousTheme(t *testing.T) {
	c, record := removalThemeCatalog(t)
	ctx := context.Background()
	// A successful switch from the retained package to a second installed version.
	if err := c.Activate(ctx, "removable", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	result, err := c.db.Writer.Exec(`INSERT INTO themes(theme_id,name,version,api_version,path,checksum,directory_checksum,validation_status,active,installed_at,updated_at) SELECT 'second',name,version,api_version,path,checksum,directory_checksum,'valid',0,installed_at,updated_at FROM themes WHERE id=?`, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.db.Writer.Exec("UPDATE themes SET active=0 WHERE id=?", record.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.db.Writer.Exec("UPDATE themes SET active=1 WHERE id=?", second); err != nil {
		t.Fatal(err)
	}
	if _, err := c.db.Writer.Exec(`INSERT INTO theme_activation_history(previous_theme_id,active_theme_id,operation,status,started_at) VALUES(?,?,'activate','succeeded',0)`, record.ID, second); err != nil {
		t.Fatal(err)
	}
	if err := c.Remove(ctx, "removable", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := c.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if !c.manager.IsFallback() {
		t.Fatal("rollback resurrected removed theme")
	}
}
