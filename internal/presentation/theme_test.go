package presentation

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
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
