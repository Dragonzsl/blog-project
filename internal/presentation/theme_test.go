package presentation

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhushilin/blog-project/internal/platform/config"
	"github.com/zhushilin/blog-project/internal/platform/database"
)

func TestDefaultThemeRendersSanitizedArticle(t *testing.T) {
	theme, err := NewDefaultTheme(NewMarkdown())
	if err != nil {
		t.Fatal(err)
	}
	body, err := theme.RenderArticle("纸上花园", ArticleData{
		Title:        "现代文章",
		Slug:         "modern-article",
		Excerpt:      "一段摘要",
		BodyMarkdown: "## 正文\n\n**安全内容**\n\n<script>alert(1)</script>",
	}, false, "")
	if err != nil {
		t.Fatal(err)
	}
	html := string(body)
	if !strings.Contains(html, "现代文章") || !strings.Contains(html, "<strong>安全内容</strong>") {
		t.Fatalf("article content missing: %s", html)
	}
	if strings.Contains(html, "alert(1)") {
		t.Fatalf("unsafe script content rendered: %s", html)
	}
	if !strings.Contains(html, theme.AssetURL()) {
		t.Fatalf("fingerprinted asset URL missing: %s", html)
	}
}

func TestThemePackageInstallSwitchAndFallback(t *testing.T) {
	root := t.TempDir()
	manifest := ThemeManifest{ID: "paper", Name: "Paper", Version: "1.0.0", ThemeAPI: ThemeAPIVersion}
	archiveBytes := themeArchive(t, manifest, map[string]string{
		"templates/home.html":       "{{define \"page_metadata\"}}{{end}}{{define \"primary_navigation\"}}{{end}}{{define \"footer_navigation\"}}{{end}}<!doctype html><title>{{.SiteName}}</title>",
		"templates/article.html":    "{{define \"page_metadata\"}}{{end}}{{define \"primary_navigation\"}}{{end}}{{define \"footer_navigation\"}}{{end}}<!doctype html><h1>{{.Article.Title}}</h1>",
		"templates/listing.html":    "{{define \"page_metadata\"}}{{end}}{{define \"primary_navigation\"}}{{end}}{{define \"footer_navigation\"}}{{end}}<!doctype html><h1>{{.Title}}</h1>",
		"templates/search.html":     "{{define \"page_metadata\"}}{{end}}{{define \"primary_navigation\"}}{{end}}{{define \"footer_navigation\"}}{{end}}<!doctype html><h1>Search</h1>",
		"templates/navigation.html": "{{define \"primary_navigation\"}}{{end}}{{define \"footer_navigation\"}}{{end}}{{define \"page_metadata\"}}{{end}}",
		"assets/theme.css":          "body{color:#123}",
	})
	pkg, err := InstallTheme(context.Background(), bytes.NewReader(archiveBytes), ThemeInstallOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if pkg.Manifest.ID != "paper" || pkg.Files != 7 || pkg.Path == "" {
		t.Fatalf("package=%+v", pkg)
	}
	if _, err := os.Stat(pkg.Path); err != nil {
		t.Fatal(err)
	}
	fallback, err := NewDefaultTheme(NewMarkdown())
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewThemeManager(fallback, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.ActivatePackage(pkg); err != nil {
		t.Fatal(err)
	}
	if manager.IsFallback() || manager.Current().ThemeID() != "paper" {
		t.Fatalf("active theme=%s fallback=%v", manager.Current().ThemeID(), manager.IsFallback())
	}
	if err := manager.Rollback(); err != nil || !manager.IsFallback() {
		t.Fatalf("rollback err=%v fallback=%v", err, manager.IsFallback())
	}
	unsafe := themeArchive(t, manifest, map[string]string{
		"../escape": "bad",
	})
	if _, err := InstallTheme(context.Background(), bytes.NewReader(unsafe), ThemeInstallOptions{Root: t.TempDir()}); err == nil {
		t.Fatal("unsafe theme path was accepted")
	}
}

func TestThemeCatalogSettingsActivationAndReconcile(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, config.Database{Path: filepath.Join(t.TempDir(), "blog.sqlite"), BusyTimeout: config.Duration{Duration: time.Second}, CacheSizeKiB: 4096, ReadConnections: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	fallback, err := NewDefaultTheme(NewMarkdown())
	if err != nil {
		t.Fatal(err)
	}
	themeRoot := filepath.Join(t.TempDir(), "themes")
	manager, err := NewThemeManager(fallback, themeRoot)
	if err != nil {
		t.Fatal(err)
	}
	catalog := NewThemeCatalog(db, manager)
	catalog.SetMediaResolver(func(_ context.Context, publicID string) (MediaData, error) {
		if publicID != "0102030405060708090a0b0c0d0e0f10" {
			t.Fatalf("unexpected media public id: %s", publicID)
		}
		return MediaData{URL: "/media/0102030405060708090a0b0c0d0e0f10/original", Alt: "fixture", Width: 1200, Height: 800}, nil
	})
	manifest := ThemeManifest{
		ID: "catalog", Name: "Catalog", Version: "1.0.0", ThemeAPI: ThemeAPIVersion, SettingsVersion: 1,
		SettingsSchema: map[string]SettingDefinition{
			"accent": {Type: "color", Default: "#123"},
			"secret": {Type: "text", Default: "keep-me", Secret: true},
			"hero":   {Type: "media", Default: "0102030405060708090a0b0c0d0e0f10"},
		},
	}
	files := map[string]string{
		"templates/home.html":       "{{define \"page_metadata\"}}{{end}}{{define \"primary_navigation\"}}{{end}}{{define \"footer_navigation\"}}{{end}}<!doctype html><main data-accent=\"{{index .Settings \"accent\"}}\">{{.SiteName}}</main>",
		"templates/article.html":    "{{define \"page_metadata\"}}{{end}}{{define \"primary_navigation\"}}{{end}}{{define \"footer_navigation\"}}{{end}}<!doctype html><h1>{{.Article.Title}}</h1>",
		"templates/listing.html":    "{{define \"page_metadata\"}}{{end}}{{define \"primary_navigation\"}}{{end}}{{define \"footer_navigation\"}}{{end}}<!doctype html><h1>{{.Title}}</h1>",
		"templates/search.html":     "{{define \"page_metadata\"}}{{end}}{{define \"primary_navigation\"}}{{end}}{{define \"footer_navigation\"}}{{end}}<!doctype html><h1>Search</h1>",
		"templates/navigation.html": "{{define \"primary_navigation\"}}{{end}}{{define \"footer_navigation\"}}{{end}}{{define \"page_metadata\"}}{{end}}",
		"assets/theme.css":          "body{color:#123}",
	}
	archiveBytes := themeArchive(t, manifest, files)
	record, err := catalog.Install(ctx, bytes.NewReader(archiveBytes), ThemeInstallOptions{Root: themeRoot})
	if err != nil {
		t.Fatal(err)
	}
	if record.DirectoryChecksum == "" || record.Manifest.SettingsVersion != 1 {
		t.Fatalf("record=%+v", record)
	}
	_, settings, err := catalog.Settings(ctx, "catalog", "1.0.0")
	if err != nil || settings["accent"] != "#123" || settings["secret"] != themeSecretPlaceholder {
		t.Fatalf("settings=%v err=%v", settings, err)
	}
	var beforeEpoch, afterEpoch int64
	if err := db.Reader.QueryRowContext(ctx, "SELECT render_epoch FROM system_state WHERE id=1").Scan(&beforeEpoch); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveSettings(ctx, "catalog", "1.0.0", map[string]any{"accent": "not-a-color"}); err == nil {
		t.Fatal("invalid theme setting was accepted")
	}
	if err := db.Reader.QueryRowContext(ctx, "SELECT render_epoch FROM system_state WHERE id=1").Scan(&afterEpoch); err != nil {
		t.Fatal(err)
	}
	if afterEpoch != beforeEpoch {
		t.Fatalf("invalid setting changed render epoch: before=%d after=%d", beforeEpoch, afterEpoch)
	}
	if err := catalog.SaveSettings(ctx, "catalog", "1.0.0", map[string]any{"accent": "#456", "secret": themeSecretPlaceholder}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveSettings(ctx, "catalog", "1.0.0", map[string]any{"secret": ""}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Activate(ctx, "catalog", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if manager.IsFallback() || manager.Current().Settings()["accent"] != "#456" {
		t.Fatalf("active theme=%s settings=%v", manager.Current().ThemeID(), manager.Current().Settings())
	}
	if _, ok := manager.Current().Settings()["secret"]; ok {
		t.Fatal("secret theme setting was exposed to templates")
	}
	if hero, ok := manager.Current().Settings()["hero"].(MediaData); !ok || hero.URL == "" || hero.Width != 1200 {
		t.Fatalf("media theme setting=%#v", manager.Current().Settings()["hero"])
	}
	var storedSecret string
	if err := db.Reader.QueryRowContext(ctx, "SELECT json_extract(values_json,'$.secret') FROM theme_settings WHERE theme_id=?", record.ID).Scan(&storedSecret); err != nil || storedSecret != "keep-me" {
		t.Fatalf("stored secret=%q err=%v", storedSecret, err)
	}
	if err := os.WriteFile(manager.marker, []byte(`{"id":"wrong","version":"9.9.9"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	marker, err := os.ReadFile(manager.marker)
	if err != nil || !strings.Contains(string(marker), `"catalog"`) || !strings.Contains(string(marker), `"1.0.0"`) {
		t.Fatalf("marker=%s err=%v", marker, err)
	}
	if err := catalog.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if !manager.IsFallback() {
		t.Fatalf("rollback did not select fallback: %s", manager.Current().ThemeID())
	}
	var activeCount int
	if err := db.Reader.QueryRowContext(ctx, "SELECT count(*) FROM themes WHERE active=1").Scan(&activeCount); err != nil || activeCount != 0 {
		t.Fatalf("active themes=%d err=%v", activeCount, err)
	}
}

func themeArchive(t *testing.T, manifest ThemeManifest, files map[string]string) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	entries := map[string][]byte{"theme.json": manifestBytes}
	for name, value := range files {
		entries[name] = []byte(value)
	}
	for name, value := range entries {
		file, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(value); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
