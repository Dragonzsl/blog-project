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

[operations]
backup_interval = "36h"
backup_daily_retention = 9
backup_weekly_retention = 6

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
	if cfg.Operations.BackupInterval.Duration != 36*time.Hour || cfg.Operations.BackupDailyRetention != 9 || cfg.Operations.BackupWeeklyRetention != 6 {
		t.Fatalf("operations configuration = %+v", cfg.Operations)
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

func TestLoadInfersCookieSecurityFromBaseURL(t *testing.T) {
	t.Setenv("BLOG_COOKIE_SECURE", "")
	t.Setenv("BLOG_BASE_URL", "http://localhost")
	httpConfig, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if httpConfig.Security.CookieSecure {
		t.Fatal("HTTP base URL enabled Secure cookies")
	}

	t.Setenv("BLOG_BASE_URL", "https://localhost")
	httpsConfig, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if !httpsConfig.Security.CookieSecure {
		t.Fatal("HTTPS base URL did not enable Secure cookies")
	}

	t.Setenv("BLOG_BASE_URL", "http://localhost")
	t.Setenv("BLOG_COOKIE_SECURE", "true")
	explicitConfig, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if !explicitConfig.Security.CookieSecure {
		t.Fatal("explicit BLOG_COOKIE_SECURE=true was ignored")
	}
}

func TestValidateRejectsUnsafeMediaConfiguration(t *testing.T) {
	cfg := Defaults()
	cfg.Media.VariantWidths = []int{1280, 640}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want invalid media widths")
	}
}

func TestStage3ExtensionEnvironmentOverrides(t *testing.T) {
	t.Setenv("BLOG_CONTENT_API_ENABLED", "true")
	t.Setenv("BLOG_CONTENT_API_TOKEN", "read-token")
	t.Setenv("BLOG_WEBHOOKS_ENABLED", "true")
	t.Setenv("BLOG_WEBHOOK_ENDPOINT", "https://hooks.example.test/blog")
	t.Setenv("BLOG_WEBHOOK_SECRET", "01234567890123456789012345678901")
	t.Setenv("BLOG_WEBHOOK_MAX_ATTEMPTS", "3")
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ContentAPI.Enabled || cfg.ContentAPI.Token != "read-token" {
		t.Fatalf("content api=%+v", cfg.ContentAPI)
	}
	if !cfg.Webhooks.Enabled || cfg.Webhooks.Endpoint == "" || cfg.Webhooks.Secret == "" || cfg.Webhooks.MaxAttempts != 3 {
		t.Fatalf("webhooks=%+v", cfg.Webhooks)
	}
}

func TestTrustedProxyCIDRsCanBeConfiguredFromEnvironment(t *testing.T) {
	t.Setenv("BLOG_TRUSTED_PROXY_CIDRS", "10.20.0.0/24, 2001:db8::/64")
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Server.TrustedProxyCIDRs) != 2 || cfg.Server.TrustedProxyCIDRs[0] != "10.20.0.0/24" || cfg.Server.TrustedProxyCIDRs[1] != "2001:db8::/64" {
		t.Fatalf("trusted proxy CIDRs=%v", cfg.Server.TrustedProxyCIDRs)
	}
}

func TestValidateRejectsOpenTrustedProxyCIDR(t *testing.T) {
	cfg := Defaults()
	cfg.Server.TrustedProxyCIDRs = []string{"0.0.0.0/0"}
	if err := cfg.Validate(); err == nil {
		t.Fatal("open trusted proxy CIDR was accepted")
	}
}

func TestValidateNormalizesMappedTrustedProxyCIDR(t *testing.T) {
	cfg := Defaults()
	cfg.Server.TrustedProxyCIDRs = []string{"::ffff:192.0.2.0/120"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("mapped trusted proxy CIDR rejected: %v", err)
	}
	cfg.Server.TrustedProxyCIDRs = []string{"::ffff:0:0/96"}
	if err := cfg.Validate(); err == nil {
		t.Fatal("mapped default trusted proxy CIDR was accepted")
	}
}
