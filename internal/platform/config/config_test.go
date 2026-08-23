package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadUsesFileAndEnvironmentOverrides(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "blog.toml")
	contents := []byte(`[server]
listen_address = "127.0.0.1:9000"
shutdown_timeout = "12s"

[storage]
data_dir = "var"

[database]
path = "database/site.sqlite"
busy_timeout = "3s"
cache_size_kib = 8192
read_connections = 1

[media]
max_upload_bytes = 16777216
max_image_pixels = 12000000
variant_widths = [720, 1440]
jpeg_quality = 94

[publishing]
scheduler_interval = "9s"
scheduler_batch_size = 12
editing_snapshot_interval = "20s"
revision_limit = 80
trash_retention_days = 45
trash_cleanup_interval = "4h"

[discovery]
base_url = "https://example.test/blog"
sync_batch_size = 18
max_results = 40
feed_limit = 30
sitemap_limit = 20000

[logging]
level = "warn"
format = "json"
`)
	if err := os.WriteFile(configPath, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BLOG_LISTEN_ADDRESS", ":7777")
	t.Setenv("BLOG_DATABASE_READ_CONNECTIONS", "3")
	t.Setenv("BLOG_MEDIA_VARIANT_WIDTHS", "800, 1600")
	t.Setenv("BLOG_PUBLISHING_SCHEDULER_BATCH_SIZE", "16")
	t.Setenv("BLOG_SEARCH_MAX_RESULTS", "35")

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Server.ListenAddress != ":7777" {
		t.Fatalf("listen address = %q", cfg.Server.ListenAddress)
	}
	if cfg.Server.ShutdownTimeout.Duration != 12*time.Second {
		t.Fatalf("shutdown timeout = %s", cfg.Server.ShutdownTimeout.Duration)
	}
	if cfg.Database.ReadConnections != 3 {
		t.Fatalf("read connections = %d", cfg.Database.ReadConnections)
	}
	if cfg.Media.MaxUploadBytes != 16<<20 || cfg.Media.MaxImagePixels != 12_000_000 || cfg.Media.JPEGQuality != 94 {
		t.Fatalf("media configuration = %+v", cfg.Media)
	}
	if len(cfg.Media.VariantWidths) != 2 || cfg.Media.VariantWidths[0] != 800 || cfg.Media.VariantWidths[1] != 1600 {
		t.Fatalf("media variant widths = %v", cfg.Media.VariantWidths)
	}
	if cfg.Publishing.SchedulerInterval.Duration != 9*time.Second || cfg.Publishing.SchedulerBatchSize != 16 || cfg.Publishing.EditingSnapshotInterval.Duration != 20*time.Second || cfg.Publishing.RevisionLimit != 80 || cfg.Publishing.TrashRetentionDays != 45 || cfg.Publishing.TrashCleanupInterval.Duration != 4*time.Hour {
		t.Fatalf("publishing configuration = %+v", cfg.Publishing)
	}
	if cfg.Discovery.BaseURL != "https://example.test/blog" || cfg.Discovery.SyncBatchSize != 18 || cfg.Discovery.MaxResults != 35 || cfg.Discovery.FeedLimit != 30 || cfg.Discovery.SitemapLimit != 20000 {
		t.Fatalf("discovery configuration = %+v", cfg.Discovery)
	}
	wantDataDir := filepath.Join(filepath.Dir(configPath), "var")
	if cfg.Storage.DataDir != wantDataDir {
		t.Fatalf("data dir = %q, want %q", cfg.Storage.DataDir, wantDataDir)
	}
	wantDatabase := filepath.Join(wantDataDir, "database/site.sqlite")
	if cfg.Database.Path != wantDatabase {
		t.Fatalf("database path = %q, want %q", cfg.Database.Path, wantDatabase)
	}
	wantAuthSecret := filepath.Join(wantDataDir, "secrets/auth.key")
	if cfg.Security.AuthSecretFile != wantAuthSecret {
		t.Fatalf("auth secret path = %q, want %q", cfg.Security.AuthSecretFile, wantAuthSecret)
	}
}

func TestLoadRejectsInvalidEnvironment(t *testing.T) {
	t.Setenv("BLOG_DATABASE_READ_CONNECTIONS", "unbounded")
	if _, err := Load(""); err == nil {
		t.Fatal("Load() error = nil, want invalid integer error")
	}
}

func TestValidateRejectsUnsafeMediaConfiguration(t *testing.T) {
	cfg := Defaults()
	cfg.Media.VariantWidths = []int{1280, 640}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want invalid media widths")
	}
}
