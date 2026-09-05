package presentation

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	defaulttheme "github.com/zhushilin/blog-project/themes/default"
)

const (
	ThemeAPIVersion       = 1
	DefaultThemeID        = "default"
	defaultThemeMaxBytes  = 32 << 20
	defaultThemeMaxFiles  = 256
	defaultThemeMaxUnpack = 64 << 20
	maxThemeSettingsJSON  = 64 << 10
)

// ThemeManifest is the signed-by-distribution metadata for a theme package.
// Theme packages contain templates and static assets only; they never execute
// server-side code.
type ThemeManifest struct {
	ID              string                       `json:"id"`
	Name            string                       `json:"name"`
	Version         string                       `json:"version"`
	ThemeAPI        int                          `json:"themeApi"`
	Core            string                       `json:"core"`
	Features        []string                     `json:"features"`
	SettingsVersion int                          `json:"settingsVersion,omitempty"`
	SettingsSchema  map[string]SettingDefinition `json:"settingsSchema"`
}

type SettingDefinition struct {
	Type     string   `json:"type"`
	Default  any      `json:"default"`
	Minimum  *float64 `json:"minimum,omitempty"`
	Maximum  *float64 `json:"maximum,omitempty"`
	Options  []string `json:"options,omitempty"`
	Required bool     `json:"required,omitempty"`
	Secret   bool     `json:"secret,omitempty"`
}

type ThemePackage struct {
	Manifest          ThemeManifest
	Path              string
	Checksum          [32]byte
	DirectoryChecksum [32]byte
	Files             int
	Bytes             int64
}

type ThemeInstallOptions struct {
	Root        string
	MaxBytes    int64
	MaxFiles    int
	MaxUnpacked int64
	CoreVersion string
}

func (o ThemeInstallOptions) withDefaults() ThemeInstallOptions {
	if o.MaxBytes <= 0 {
		o.MaxBytes = defaultThemeMaxBytes
	}
	if o.MaxFiles <= 0 {
		o.MaxFiles = defaultThemeMaxFiles
	}
	if o.MaxUnpacked <= 0 {
		o.MaxUnpacked = defaultThemeMaxUnpack
	}
	return o
}

// InstallTheme streams a ZIP package to a private staging directory, checks
// every path and template, and atomically publishes the package under Root.
func InstallTheme(ctx context.Context, source io.Reader, options ThemeInstallOptions) (ThemePackage, error) {
	options = options.withDefaults()
	if strings.TrimSpace(options.Root) == "" {
		return ThemePackage{}, errors.New("theme root is required")
	}
	if options.MaxBytes < 1 || options.MaxFiles < 1 || options.MaxUnpacked < 1 {
		return ThemePackage{}, errors.New("theme package limits must be positive")
	}
	if err := os.MkdirAll(options.Root, 0o700); err != nil {
		return ThemePackage{}, fmt.Errorf("create theme root: %w", err)
	}
	limited := io.LimitReader(source, options.MaxBytes+1)
	archiveBytes, err := io.ReadAll(limited)
	if err != nil {
		return ThemePackage{}, fmt.Errorf("read theme package: %w", err)
	}
	if int64(len(archiveBytes)) > options.MaxBytes {
		return ThemePackage{}, fmt.Errorf("theme package exceeds %d bytes", options.MaxBytes)
	}
	hash := sha256.Sum256(archiveBytes)
	archive, err := zip.NewReader(bytes.NewReader(archiveBytes), int64(len(archiveBytes)))
	if err != nil {
		return ThemePackage{}, fmt.Errorf("open theme package: %w", err)
	}
	if len(archive.File) > options.MaxFiles {
		return ThemePackage{}, fmt.Errorf("theme package contains too many files")
	}
	stage, err := os.MkdirTemp(options.Root, ".theme-install-")
	if err != nil {
		return ThemePackage{}, fmt.Errorf("create theme staging directory: %w", err)
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(stage)
		}
	}()
	var total int64
	for _, entry := range archive.File {
		if err := ctx.Err(); err != nil {
			return ThemePackage{}, err
		}
		name, err := validThemePath(entry.Name)
		if err != nil {
			return ThemePackage{}, err
		}
		if name == "" {
			continue
		}
		if entry.UncompressedSize64 > uint64(options.MaxUnpacked) || total > options.MaxUnpacked-int64(entry.UncompressedSize64) {
			return ThemePackage{}, errors.New("theme package exceeds unpacked size limit")
		}
		total += int64(entry.UncompressedSize64)
		destination := filepath.Join(stage, filepath.FromSlash(name))
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(destination, 0o700); err != nil {
				return ThemePackage{}, err
			}
			continue
		}
		if entry.Mode()&os.ModeSymlink != 0 || !entry.Mode().IsRegular() {
			return ThemePackage{}, fmt.Errorf("theme package contains unsafe file %q", name)
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return ThemePackage{}, err
		}
		reader, err := entry.Open()
		if err != nil {
			return ThemePackage{}, err
		}
		file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			reader.Close()
			return ThemePackage{}, err
		}
		_, copyErr := io.Copy(file, io.LimitReader(reader, options.MaxUnpacked+1))
		closeErr := file.Close()
		reader.Close()
		if copyErr != nil {
			return ThemePackage{}, fmt.Errorf("extract theme file %q: %w", name, copyErr)
		}
		if closeErr != nil {
			return ThemePackage{}, closeErr
		}
	}
	manifestBytes, err := os.ReadFile(filepath.Join(stage, "theme.json"))
	if err != nil {
		return ThemePackage{}, errors.New("theme package must contain theme.json")
	}
	var manifest ThemeManifest
	decoder := json.NewDecoder(bytes.NewReader(manifestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return ThemePackage{}, fmt.Errorf("parse theme manifest: %w", err)
	}
	if err := validateThemeManifest(manifest, options.CoreVersion); err != nil {
		return ThemePackage{}, err
	}
	if err := validateThemeTemplates(stage); err != nil {
		return ThemePackage{}, err
	}
	if _, err := NewThemeFromDirectory(stage, manifest); err != nil {
		return ThemePackage{}, fmt.Errorf("validate theme templates: %w", err)
	}
	if err := validateThemeFixtures(ctx, stage, manifest); err != nil {
		return ThemePackage{}, fmt.Errorf("validate theme fixtures: %w", err)
	}
	directoryChecksum, err := ThemeDirectoryChecksum(stage)
	if err != nil {
		return ThemePackage{}, fmt.Errorf("checksum theme directory: %w", err)
	}
	target := filepath.Join(options.Root, manifest.ID, manifest.Version)
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return ThemePackage{}, err
	}
	if _, err := os.Lstat(target); err == nil {
		return ThemePackage{}, fmt.Errorf("theme %s@%s is already installed", manifest.ID, manifest.Version)
	} else if !os.IsNotExist(err) {
		return ThemePackage{}, err
	}
	if err := os.Rename(stage, target); err != nil {
		return ThemePackage{}, fmt.Errorf("publish theme package: %w", err)
	}
	complete = true
	return ThemePackage{Manifest: manifest, Path: target, Checksum: hash, DirectoryChecksum: directoryChecksum, Files: len(archive.File), Bytes: total}, nil
}

// ThemeDirectoryChecksum is independent of ZIP entry order, timestamps, and
// compression details. It is therefore suitable for detecting changes to the
// extracted package at activation/startup time.
func ThemeDirectoryChecksum(root string) ([32]byte, error) {
	var zero [32]byte
	entries := make([]string, 0, 64)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("theme directory contains symlink %q", relative)
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("theme directory contains non-regular file %q", relative)
		}
		entries = append(entries, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		return zero, err
	}
	sort.Strings(entries)
	hasher := sha256.New()
	for _, relative := range entries {
		contents, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			return zero, err
		}
		_, _ = hasher.Write([]byte(relative + "\x00"))
		_, _ = hasher.Write(contents)
		_, _ = hasher.Write([]byte("\x00"))
	}
	var result [32]byte
	copy(result[:], hasher.Sum(nil))
	return result, nil
}

func validateThemeFixtures(ctx context.Context, root string, manifest ThemeManifest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	theme, err := NewThemeFromDirectory(root, manifest)
	if err != nil {
		return err
	}
	navigation := Navigation{CurrentPath: "/"}
	article := ArticleData{Kind: "article", Title: "Theme fixture", Slug: "theme-fixture", Excerpt: "fixture", BodyMarkdown: "## fixture\n\ncontent"}
	checks := []func() ([]byte, error){
		func() ([]byte, error) {
			return theme.RenderHomePageWithView("Fixture", HomePageData{}, navigation, PageMetadata{}, nil)
		},
		func() ([]byte, error) {
			return theme.RenderArticlePage("Fixture", article, false, "", navigation, PageMetadata{})
		},
		func() ([]byte, error) {
			return theme.RenderCollectionPage("Fixture", CollectionView{Title: "Collection"}, navigation, PageMetadata{})
		},
		func() ([]byte, error) {
			return theme.RenderDirectoryPage("Fixture", DirectoryView{Title: "Directory"}, navigation, PageMetadata{})
		},
		func() ([]byte, error) {
			return theme.RenderSearch("Fixture", SearchPageData{}, navigation, PageMetadata{})
		},
		func() ([]byte, error) {
			return theme.RenderStatusPage("Fixture", StatusView{Code: 404, Title: "Not found", Message: "fixture"}, navigation, PageMetadata{})
		},
	}
	for index, check := range checks {
		output, err := check()
		if err != nil {
			return fmt.Errorf("fixture %d: %w", index+1, err)
		}
		if len(output) == 0 || len(output) > 2<<20 {
			return fmt.Errorf("fixture %d output is outside bounds", index+1)
		}
	}
	return nil
}

func validThemePath(raw string) (string, error) {
	if !utf8.ValidString(raw) || strings.ContainsRune(raw, '\x00') {
		return "", fmt.Errorf("theme package path is invalid")
	}
	raw = strings.ReplaceAll(raw, "\\", "/")
	if raw == "" || strings.HasPrefix(raw, "/") || strings.Contains(raw, ":") {
		return "", fmt.Errorf("theme package path %q is not relative", raw)
	}
	clean := filepath.ToSlash(filepath.Clean(raw))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return "", fmt.Errorf("theme package path %q escapes package root", raw)
	}
	return clean, nil
}

func validateThemeManifest(manifest ThemeManifest, coreVersion string) error {
	if !validThemeID(manifest.ID) {
		return errors.New("theme manifest id is invalid")
	}
	if strings.TrimSpace(manifest.Name) == "" || !utf8.ValidString(manifest.Name) {
		return errors.New("theme manifest name is required")
	}
	if strings.TrimSpace(manifest.Version) == "" || strings.ContainsAny(manifest.Version, "/\\\x00") {
		return errors.New("theme manifest version is invalid")
	}
	if manifest.ThemeAPI != ThemeAPIVersion {
		return fmt.Errorf("unsupported theme API version %d", manifest.ThemeAPI)
	}
	if manifest.SettingsVersion < 0 {
		return errors.New("theme settings version is invalid")
	}
	if coreVersion != "" && !coreRangeAllows(manifest.Core, coreVersion) {
		return fmt.Errorf("theme requires core %s", manifest.Core)
	}
	for key, definition := range manifest.SettingsSchema {
		if !validSettingKey(key) {
			return fmt.Errorf("theme setting key %q is invalid", key)
		}
		if err := validateSettingDefinition(definition); err != nil {
			return fmt.Errorf("theme setting %q: %w", key, err)
		}
	}
	if _, err := validateThemeSettings(manifest.SettingsSchema, defaultThemeSettings(manifest.SettingsSchema)); err != nil {
		return err
	}
	return nil
}

func validThemeID(value string) bool {
	if len(value) < 1 || len(value) > 80 || !utf8.ValidString(value) {
		return false
	}
	for index, r := range value {
		if !(r == '-' || r == '_' || r == '.' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9') || index == 0 && r == '.' {
			return false
		}
	}
	return true
}

func validSettingKey(value string) bool {
	if len(value) < 1 || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if !(r == '-' || r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func validateSettingDefinition(definition SettingDefinition) error {
	switch definition.Type {
	case "boolean", "text", "color", "integer", "select", "media", "url":
	default:
		return fmt.Errorf("unsupported type %q", definition.Type)
	}
	if definition.Type == "select" && len(definition.Options) == 0 {
		return errors.New("select requires options")
	}
	if definition.Type == "integer" && definition.Minimum != nil && definition.Maximum != nil && *definition.Minimum > *definition.Maximum {
		return errors.New("minimum exceeds maximum")
	}
	if definition.Secret && definition.Type != "text" && definition.Type != "url" {
		return errors.New("secret settings must be text or url")
	}
	seenOptions := make(map[string]struct{}, len(definition.Options))
	for _, option := range definition.Options {
		if strings.TrimSpace(option) == "" || !utf8.ValidString(option) || len(option) > 256 {
			return errors.New("select options must be non-empty and bounded")
		}
		if _, exists := seenOptions[option]; exists {
			return errors.New("select options must be unique")
		}
		seenOptions[option] = struct{}{}
	}
	if definition.Default != nil {
		if err := validateSettingValue(definition, definition.Default); err != nil {
			return fmt.Errorf("default is invalid: %w", err)
		}
	}
	return nil
}

func normalizedSettingsVersion(version int) int {
	if version < 1 {
		return 1
	}
	return version
}

func validateSettingValue(definition SettingDefinition, value any) error {
	switch definition.Type {
	case "boolean":
		if _, ok := value.(bool); !ok {
			return errors.New("must be boolean")
		}
	case "text", "url", "color", "media", "select":
		text, ok := value.(string)
		if !ok {
			return errors.New("must be text")
		}
		if utf8.RuneCountInString(text) > 4096 {
			return errors.New("text is too long")
		}
		if definition.Type == "select" {
			valid := false
			for _, option := range definition.Options {
				if option == text {
					valid = true
					break
				}
			}
			if !valid {
				return errors.New("is not one of the allowed options")
			}
		}
		if definition.Type == "url" && text != "" {
			parsed, err := url.ParseRequestURI(text)
			if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
				return errors.New("must be an http(s) URL")
			}
		}
		if definition.Type == "color" && text != "" && !validThemeColor(text) {
			return errors.New("must be a hex color")
		}
		if definition.Type == "media" && text != "" && !validMediaPublicID(text) {
			return errors.New("must be a media public id")
		}
	case "integer":
		number, ok := settingNumber(value)
		if !ok || number != float64(int64(number)) {
			return errors.New("must be an integer")
		}
		if definition.Minimum != nil && number < *definition.Minimum {
			return errors.New("is below minimum")
		}
		if definition.Maximum != nil && number > *definition.Maximum {
			return errors.New("is above maximum")
		}
	default:
		return fmt.Errorf("unsupported type %q", definition.Type)
	}
	return nil
}

func settingNumber(value any) (float64, bool) {
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
		value, err := number.Float64()
		return value, err == nil
	default:
		return 0, false
	}
}

func validThemeColor(value string) bool {
	if len(value) != 4 && len(value) != 7 {
		return false
	}
	if value[0] != '#' {
		return false
	}
	for _, r := range value[1:] {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}

func validMediaPublicID(value string) bool {
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

func validateThemeSettings(schema map[string]SettingDefinition, values map[string]any) (map[string]any, error) {
	if len(schema) > 64 || len(values) > 64 {
		return nil, errors.New("theme settings are too numerous")
	}
	result := make(map[string]any, len(schema))
	for key := range values {
		if _, ok := schema[key]; !ok {
			return nil, fmt.Errorf("unknown theme setting %q", key)
		}
	}
	for key, definition := range schema {
		value, ok := values[key]
		if !ok {
			value = definition.Default
		}
		if value == nil {
			if definition.Required {
				return nil, fmt.Errorf("required theme setting %q is missing", key)
			}
			continue
		}
		if err := validateSettingValue(definition, value); err != nil {
			return nil, fmt.Errorf("theme setting %q: %w", key, err)
		}
		result[key] = value
	}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > maxThemeSettingsJSON {
		return nil, errors.New("theme settings JSON is too large")
	}
	return result, nil
}

func runtimeThemeSettings(schema map[string]SettingDefinition, values map[string]any) map[string]any {
	result := make(map[string]any, len(values))
	for key, value := range values {
		if definition, ok := schema[key]; ok && definition.Secret {
			continue
		}
		result[key] = value
	}
	return result
}

func validateThemeTemplates(root string) error {
	required := []string{"templates/home.html", "templates/article.html", "templates/listing.html", "templates/search.html", "templates/navigation.html", "assets/theme.css"}
	for _, path := range required {
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("theme package missing required file %q", path)
		}
	}
	return nil
}

func coreRangeAllows(required, current string) bool {
	if strings.TrimSpace(required) == "" {
		return true
	}
	currentVersion, currentOK := parseSemver(current)
	if !currentOK {
		return false
	}
	required = strings.ReplaceAll(required, ",", " ")
	for _, part := range strings.Fields(required) {
		operator := ""
		for _, candidate := range []string{">=", "<=", ">", "<", "="} {
			if strings.HasPrefix(part, candidate) {
				operator = candidate
				part = strings.TrimPrefix(part, candidate)
				break
			}
		}
		version, ok := parseSemver(part)
		if !ok {
			return false
		}
		comparison := compareSemver(currentVersion, version)
		switch operator {
		case ">=":
			if comparison < 0 {
				return false
			}
		case ">":
			if comparison <= 0 {
				return false
			}
		case "<=":
			if comparison > 0 {
				return false
			}
		case "<":
			if comparison >= 0 {
				return false
			}
		default:
			if comparison != 0 {
				return false
			}
		}
	}
	return true
}

type semver struct{ major, minor, patch int }

func parseSemver(value string) (semver, bool) {
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	if dash := strings.IndexByte(value, '-'); dash >= 0 {
		value = value[:dash]
	}
	if plus := strings.IndexByte(value, '+'); plus >= 0 {
		value = value[:plus]
	}
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	values := [3]int{}
	for index, part := range parts {
		if part == "" {
			return semver{}, false
		}
		number := 0
		for _, r := range part {
			if r < '0' || r > '9' {
				return semver{}, false
			}
			number = number*10 + int(r-'0')
			if number > 1_000_000 {
				return semver{}, false
			}
		}
		values[index] = number
	}
	return semver{major: values[0], minor: values[1], patch: values[2]}, true
}

func compareSemver(left, right semver) int {
	if left.major != right.major {
		if left.major < right.major {
			return -1
		}
		return 1
	}
	if left.minor != right.minor {
		if left.minor < right.minor {
			return -1
		}
		return 1
	}
	if left.patch < right.patch {
		return -1
	}
	if left.patch > right.patch {
		return 1
	}
	return 0
}

// NewThemeFromDirectory parses all templates before a package can become
// active. The default theme remains embedded and is never replaced in memory.
func NewThemeFromDirectory(root string, manifest ThemeManifest) (*Theme, error) {
	if err := validateThemeManifest(manifest, ""); err != nil {
		return nil, err
	}
	if err := validateThemeTemplates(root); err != nil {
		return nil, err
	}
	templates, err := parseThemeTemplates(root)
	if err != nil {
		return nil, fmt.Errorf("parse theme templates: %w", err)
	}
	// New public page templates are additive to the theme API. Older themes
	// keep working with their existing templates while the core supplies the
	// default directory and status presentation only when they do not override
	// it. Parsing the fallback unconditionally would replace a custom template
	// with the embedded default because both files share the same template name.
	for _, name := range []string{"theme_shell.html", "directory.html", "status.html"} {
		if templates.Lookup(name) != nil {
			continue
		}
		if templates, err = templates.ParseFS(defaulttheme.Files, "templates/"+name); err != nil {
			return nil, fmt.Errorf("add default public template %q: %w", name, err)
		}
	}
	css, err := os.ReadFile(filepath.Join(root, "assets", "theme.css"))
	if err != nil {
		return nil, fmt.Errorf("read theme stylesheet: %w", err)
	}
	js, err := os.ReadFile(filepath.Join(root, "assets", "theme.js"))
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read theme script: %w", err)
	}
	hash := sha256.Sum256(css)
	assetHash := hex.EncodeToString(hash[:8])
	scriptHash := ""
	scriptURL := ""
	if len(js) > 0 {
		digest := sha256.Sum256(js)
		scriptHash = hex.EncodeToString(digest[:8])
		scriptURL = "/assets/theme/" + manifest.ID + "/" + scriptHash + "/theme.js"
	}
	settings, err := validateThemeSettings(manifest.SettingsSchema, defaultThemeSettings(manifest.SettingsSchema))
	if err != nil {
		return nil, err
	}
	settings = runtimeThemeSettings(manifest.SettingsSchema, settings)
	return &Theme{templates: templates, markdown: NewMarkdown(), css: css, js: js, assetHash: assetHash, scriptHash: scriptHash, assetURL: "/assets/theme/" + manifest.ID + "/" + assetHash + "/theme.css", scriptURL: scriptURL, id: manifest.ID, version: manifest.Version, assetRoot: filepath.Join(root, "assets"), settings: settings}, nil
}

func parseThemeTemplates(root string) (*template.Template, error) {
	filesystem := os.DirFS(root)
	var nested []string
	if err := fs.WalkDir(filesystem, "templates", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() || !strings.HasSuffix(name, ".html") {
			return nil
		}
		if strings.Count(name, "/") > 1 {
			nested = append(nested, name)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	templates, err := template.ParseFS(filesystem, "templates/*.html")
	if err != nil {
		return nil, err
	}
	sort.Strings(nested)
	for _, name := range nested {
		contents, err := fs.ReadFile(filesystem, name)
		if err != nil {
			return nil, err
		}
		templateName := strings.TrimPrefix(name, "templates/")
		if _, err := templates.New(templateName).Parse(string(contents)); err != nil {
			return nil, err
		}
	}
	return templates, nil
}

type ThemeManager struct {
	mu       sync.RWMutex
	fallback *Theme
	active   *Theme
	root     string
	marker   string
}

func NewThemeManager(fallback *Theme, root string) (*ThemeManager, error) {
	if fallback == nil {
		return nil, errors.New("theme fallback is required")
	}
	manager := &ThemeManager{fallback: fallback, active: fallback, root: root, marker: filepath.Join(root, "active.json")}
	if strings.TrimSpace(root) == "" {
		return manager, nil
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	var marker struct{ ID, Version string }
	if contents, err := os.ReadFile(manager.marker); err == nil && json.Unmarshal(contents, &marker) == nil && marker.ID != "" && marker.Version != "" {
		loaded, loadErr := NewThemeFromDirectory(filepath.Join(root, marker.ID, marker.Version), ThemeManifest{ID: marker.ID, Name: marker.ID, Version: marker.Version, ThemeAPI: ThemeAPIVersion})
		if loadErr == nil {
			manager.active = loaded
		}
	}
	return manager, nil
}

func (m *ThemeManager) Current() *Theme  { m.mu.RLock(); defer m.mu.RUnlock(); return m.active }
func (m *ThemeManager) Fallback() *Theme { return m.fallback }
func (m *ThemeManager) IsFallback() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.active == m.fallback
}
func (m *ThemeManager) Activate(theme *Theme) error {
	if theme == nil {
		return errors.New("theme is required")
	}
	if theme == m.fallback {
		return m.Rollback()
	}
	previous := m.Current()
	if err := m.ActivateRuntime(theme); err != nil {
		return err
	}
	if err := m.PersistMarker(theme); err != nil {
		m.setRuntime(previous)
		return err
	}
	return nil
}

// ActivateRuntime changes only the in-memory renderer. ThemeCatalog uses it
// after the catalog transaction commits, so a failed database write cannot
// leave the process serving a theme that the database did not activate.
func (m *ThemeManager) ActivateRuntime(theme *Theme) error {
	if theme == nil {
		return errors.New("theme is required")
	}
	if theme != m.fallback {
		if _, err := os.Stat(theme.assetRoot); err != nil {
			return fmt.Errorf("theme assets unavailable: %w", err)
		}
	}
	m.setRuntime(theme)
	return nil
}

func (m *ThemeManager) setRuntime(theme *Theme) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if theme == nil {
		theme = m.fallback
	}
	m.active = theme
}

func (m *ThemeManager) PersistMarker(theme *Theme) error {
	if theme == nil || theme == m.fallback {
		return m.ClearMarker()
	}
	return m.persistMarker(theme.id, theme.version)
}

func (m *ThemeManager) ClearMarker() error {
	if m.root == "" {
		return nil
	}
	if err := os.Remove(m.marker); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (m *ThemeManager) Rollback() error {
	if err := m.ClearMarker(); err != nil {
		return err
	}
	m.setRuntime(m.fallback)
	return nil
}

func (m *ThemeManager) persistMarker(id, version string) error {
	if m.root == "" {
		return nil
	}
	contents, _ := json.Marshal(struct{ ID, Version string }{id, version})
	temporary := m.marker + ".tmp-" + fmt.Sprint(time.Now().UnixNano())
	if err := os.WriteFile(temporary, contents, 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporary, m.marker); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func (m *ThemeManager) LoadPackage(pkg ThemePackage) (*Theme, error) {
	return NewThemeFromDirectory(pkg.Path, pkg.Manifest)
}

func (m *ThemeManager) Installed() ([]ThemePackage, error) {
	if m.root == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(m.root)
	if err != nil {
		return nil, err
	}
	var result []ThemePackage
	for _, idEntry := range entries {
		if !idEntry.IsDir() || !validThemeID(idEntry.Name()) {
			continue
		}
		versions, err := os.ReadDir(filepath.Join(m.root, idEntry.Name()))
		if err != nil {
			return nil, err
		}
		for _, versionEntry := range versions {
			if !versionEntry.IsDir() {
				continue
			}
			root := filepath.Join(m.root, idEntry.Name(), versionEntry.Name())
			contents, err := os.ReadFile(filepath.Join(root, "theme.json"))
			if err != nil {
				continue
			}
			var manifest ThemeManifest
			if json.Unmarshal(contents, &manifest) != nil {
				continue
			}
			result = append(result, ThemePackage{Manifest: manifest, Path: root})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result, nil
}

func (m *ThemeManager) ActivatePackage(pkg ThemePackage) error {
	theme, err := m.LoadPackage(pkg)
	if err != nil {
		return err
	}
	return m.Activate(theme)
}

func (m *ThemeManager) Context() context.Context { return context.Background() }

// ThemeID and ThemeVersion are useful for cache keys and administration.
func (t *Theme) ThemeID() string {
	if t.id == "" {
		return DefaultThemeID
	}
	return t.id
}
func (t *Theme) ThemeVersion() string {
	if t.version == "" {
		return defaulttheme.Version
	}
	return t.version
}
