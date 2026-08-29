package presentation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPhaseFourPublicPluginSlotsAreOptInAndAccessible(t *testing.T) {
	theme, err := NewDefaultTheme(NewMarkdown())
	if err != nil {
		t.Fatal(err)
	}
	article := ArticleData{Kind: "article", Title: "插槽测试", Slug: "slot-test", BodyMarkdown: "正文"}
	withoutPlugins, err := theme.RenderArticlePage("示例博客", article, false, "", Navigation{}, PageMetadata{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(withoutPlugins), "data-comments-slot") || strings.Contains(string(withoutPlugins), "data-newsletter-form") {
		t.Fatalf("disabled plugin slots leaked into public page: %s", withoutPlugins)
	}
	withPlugins, err := theme.RenderArticlePage("示例博客", article, false, "", Navigation{Features: FeatureFlags{Comments: true, CommentsMode: "local", Newsletter: true}}, PageMetadata{})
	if err != nil {
		t.Fatal(err)
	}
	page := string(withPlugins)
	for _, expected := range []string{"data-comments-slot", "data-comments-form", "data-newsletter-form", "aria-live=\"polite\"", "autocomplete=\"email\""} {
		if !strings.Contains(page, expected) {
			t.Fatalf("enabled plugin page missing %q: %s", expected, page)
		}
	}
}

func TestThemePreviewImageAcceptsOnlyKnownFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "preview.webp"), []byte("preview"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, name, err := openThemePreview(root)
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	if name != "preview.webp" {
		t.Fatalf("preview name=%q", name)
	}
	if err := os.WriteFile(filepath.Join(root, "preview.svg"), []byte("unsafe"), 0o600); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	if _, _, err := openThemePreview(other); !os.IsNotExist(err) {
		t.Fatalf("missing preview error=%v", err)
	}
}
