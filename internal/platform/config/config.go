package config

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalText(value []byte) error {
	parsed, err := time.ParseDuration(string(value))
	if err != nil {
		return err
	}
	d.Duration = parsed
	return nil
}

type Config struct {
	Server     Server     `toml:"server"`
	Storage    Storage    `toml:"storage"`
	Database   Database   `toml:"database"`
	Media      Media      `toml:"media"`
	Publishing Publishing `toml:"publishing"`
	Discovery  Discovery  `toml:"discovery"`
	Operations Operations `toml:"operations"`
	Logging    Logging    `toml:"logging"`
	Security   Security   `toml:"security"`
	Extensions Extensions `toml:"extensions"`
	Analytics  Analytics  `toml:"analytics"`
	Comments   Comments   `toml:"comments"`
	Mail       Mail       `toml:"mail"`
	Newsletter Newsletter `toml:"newsletter"`
	ContentAPI ContentAPI `toml:"content_api"`
	Webhooks   Webhooks   `toml:"webhooks"`
}

type Server struct {
	ListenAddress     string   `toml:"listen_address"`
	ShutdownTimeout   Duration `toml:"shutdown_timeout"`
	TrustedProxyCIDRs []string `toml:"trusted_proxy_cidrs"`
}

type Storage struct {
	DataDir string `toml:"data_dir"`
	Adapter string `toml:"adapter"`
	S3      S3     `toml:"s3"`
}

type S3 struct {
	Endpoint       string `toml:"endpoint"`
	Bucket         string `toml:"bucket"`
	Region         string `toml:"region"`
	AccessKey      string `toml:"access_key"`
	SecretKey      string `toml:"secret_key"`
	Prefix         string `toml:"prefix"`
	ForcePathStyle bool   `toml:"force_path_style"`
	UseTLS         bool   `toml:"use_tls"`
}

type Database struct {
	Path            string   `toml:"path"`
	BusyTimeout     Duration `toml:"busy_timeout"`
	CacheSizeKiB    int      `toml:"cache_size_kib"`
	ReadConnections int      `toml:"read_connections"`
}

type Media struct {
	MaxUploadBytes int   `toml:"max_upload_bytes"`
	MaxImagePixels int   `toml:"max_image_pixels"`
	VariantWidths  []int `toml:"variant_widths"`
	JPEGQuality    int   `toml:"jpeg_quality"`
}

type Publishing struct {
	SchedulerInterval       Duration `toml:"scheduler_interval"`
	SchedulerBatchSize      int      `toml:"scheduler_batch_size"`
	EditingSnapshotInterval Duration `toml:"editing_snapshot_interval"`
	RevisionLimit           int      `toml:"revision_limit"`
	TrashRetentionDays      int      `toml:"trash_retention_days"`
	TrashCleanupInterval    Duration `toml:"trash_cleanup_interval"`
}

type Discovery struct {
	BaseURL       string `toml:"base_url"`
	SyncBatchSize int    `toml:"sync_batch_size"`
	MaxResults    int    `toml:"max_results"`
	FeedLimit     int    `toml:"feed_limit"`
	SitemapLimit  int    `toml:"sitemap_limit"`
}

type Operations struct {
	BackupInterval        Duration `toml:"backup_interval"`
	BackupDailyRetention  int      `toml:"backup_daily_retention"`
	BackupWeeklyRetention int      `toml:"backup_weekly_retention"`
}

type Logging struct {
	Level  string `toml:"level"`
	Format string `toml:"format"`
}

type Security struct {
	AuthSecret      string   `toml:"-"`
	AuthSecretFile  string   `toml:"auth_secret_file"`
	CookieSecure    bool     `toml:"cookie_secure"`
	SessionLifetime Duration `toml:"session_lifetime"`
}

type Extensions struct {
	ThemePackageMaxBytes int      `toml:"theme_package_max_bytes"`
	ThemeMaxFiles        int      `toml:"theme_max_files"`
	ThemeMaxUnpacked     int64    `toml:"theme_max_unpacked_bytes"`
	EnabledPlugins       []string `toml:"enabled_plugins"`
}

type Analytics struct {
	Enabled       bool `toml:"enabled"`
	RetentionDays int  `toml:"retention_days"`
}

type Comments struct {
	Enabled           bool   `toml:"enabled"`
	RequireModeration bool   `toml:"require_moderation"`
	Provider          string `toml:"provider"`
	ExternalEndpoint  string `toml:"external_endpoint"`
}

type Mail struct {
	Enabled  bool   `toml:"enabled"`
	Host     string `toml:"host"`
	Port     int    `toml:"port"`
	Username string `toml:"username"`
	Password string `toml:"password"`
	From     string `toml:"from"`
	StartTLS bool   `toml:"starttls"`
}

type Newsletter struct {
	Enabled  bool   `toml:"enabled"`
	Provider string `toml:"provider"`
	Endpoint string `toml:"endpoint"`
	Token    string `toml:"-"`
}

type ContentAPI struct {
	Enabled bool   `toml:"enabled"`
	Token   string `toml:"-"`
}

type Webhooks struct {
	Enabled     bool   `toml:"enabled"`
	Endpoint    string `toml:"endpoint"`
	Secret      string `toml:"-"`
	MaxAttempts int    `toml:"max_attempts"`
}

func Defaults() Config {
	return Config{
		Server: Server{
			ListenAddress:   ":8080",
			ShutdownTimeout: Duration{Duration: 10 * time.Second},
		},
		Storage: Storage{DataDir: "./data", Adapter: "local", S3: S3{Region: "us-east-1", UseTLS: true}},
		Database: Database{
			Path:            "db/blog.sqlite",
			BusyTimeout:     Duration{Duration: 5 * time.Second},
			CacheSizeKiB:    16 * 1024,
			ReadConnections: 2,
		},
		Media: Media{
			MaxUploadBytes: 12 << 20,
			MaxImagePixels: 16_000_000,
			VariantWidths:  []int{640, 1280},
			JPEGQuality:    92,
		},
		Publishing: Publishing{
			SchedulerInterval:       Duration{Duration: 15 * time.Second},
			SchedulerBatchSize:      20,
			EditingSnapshotInterval: Duration{Duration: 15 * time.Second},
			RevisionLimit:           50,
			TrashRetentionDays:      30,
			TrashCleanupInterval:    Duration{Duration: 6 * time.Hour},
		},
		Discovery: Discovery{
			BaseURL:       "http://localhost:8080",
			SyncBatchSize: 20,
			MaxResults:    50,
			FeedLimit:     50,
			SitemapLimit:  50_000,
		},
		Operations: Operations{
			BackupInterval:        Duration{Duration: 24 * time.Hour},
			BackupDailyRetention:  7,
			BackupWeeklyRetention: 4,
		},
		Logging: Logging{Level: "info", Format: "text"},
		Security: Security{
			AuthSecretFile:  "secrets/auth.key",
			CookieSecure:    true,
			SessionLifetime: Duration{Duration: 12 * time.Hour},
		},
		Extensions: Extensions{ThemePackageMaxBytes: 32 << 20, ThemeMaxFiles: 256, ThemeMaxUnpacked: 64 << 20},
		Analytics:  Analytics{Enabled: false, RetentionDays: 365},
		Comments:   Comments{Enabled: false, RequireModeration: true, Provider: "local"},
		Mail:       Mail{Port: 587, StartTLS: true},
		Newsletter: Newsletter{Provider: "disabled"},
		ContentAPI: ContentAPI{},
		Webhooks:   Webhooks{MaxAttempts: 5},
	}
}

func Load(path string) (Config, error) {
	cfg := Defaults()
	configPath := strings.TrimSpace(path)
	if configPath == "" {
		configPath = strings.TrimSpace(os.Getenv("BLOG_CONFIG_FILE"))
	}

	baseDir, err := os.Getwd()
	if err != nil {
		return Config{}, fmt.Errorf("determine working directory: %w", err)
	}
	if configPath != "" {
		absolutePath, err := filepath.Abs(configPath)
		if err != nil {
			return Config{}, fmt.Errorf("resolve config path: %w", err)
		}
		contents, err := os.ReadFile(absolutePath)
		if err != nil {
			return Config{}, fmt.Errorf("read config: %w", err)
		}
		if err := toml.Unmarshal(contents, &cfg); err != nil {
			return Config{}, fmt.Errorf("parse config: %w", err)
		}
		baseDir = filepath.Dir(absolutePath)
	}

	if err := applyEnvironment(&cfg); err != nil {
		return Config{}, err
	}
	if strings.TrimSpace(cfg.Storage.DataDir) != "" && !filepath.IsAbs(cfg.Storage.DataDir) {
		cfg.Storage.DataDir = filepath.Join(baseDir, cfg.Storage.DataDir)
	}
	if strings.TrimSpace(cfg.Storage.DataDir) != "" {
		cfg.Storage.DataDir = filepath.Clean(cfg.Storage.DataDir)
	}
	if strings.TrimSpace(cfg.Database.Path) != "" && !filepath.IsAbs(cfg.Database.Path) {
		cfg.Database.Path = filepath.Join(cfg.Storage.DataDir, cfg.Database.Path)
	}
	if strings.TrimSpace(cfg.Database.Path) != "" {
		cfg.Database.Path = filepath.Clean(cfg.Database.Path)
	}
	if strings.TrimSpace(cfg.Security.AuthSecretFile) != "" && !filepath.IsAbs(cfg.Security.AuthSecretFile) {
		cfg.Security.AuthSecretFile = filepath.Join(cfg.Storage.DataDir, cfg.Security.AuthSecretFile)
	}
	if strings.TrimSpace(cfg.Security.AuthSecretFile) != "" {
		cfg.Security.AuthSecretFile = filepath.Clean(cfg.Security.AuthSecretFile)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func applyEnvironment(cfg *Config) error {
	baseURL, baseURLFromEnvironment := os.LookupEnv("BLOG_BASE_URL")
	stringOverrides := []struct {
		name   string
		target *string
	}{
		{"BLOG_LISTEN_ADDRESS", &cfg.Server.ListenAddress},
		{"BLOG_DATA_DIR", &cfg.Storage.DataDir},
		{"BLOG_DATABASE_PATH", &cfg.Database.Path},
		{"BLOG_LOG_LEVEL", &cfg.Logging.Level},
		{"BLOG_LOG_FORMAT", &cfg.Logging.Format},
		{"BLOG_AUTH_SECRET", &cfg.Security.AuthSecret},
		{"BLOG_AUTH_SECRET_FILE", &cfg.Security.AuthSecretFile},
		{"BLOG_BASE_URL", &cfg.Discovery.BaseURL},
		{"BLOG_STORAGE_ADAPTER", &cfg.Storage.Adapter},
		{"BLOG_S3_ENDPOINT", &cfg.Storage.S3.Endpoint},
		{"BLOG_S3_BUCKET", &cfg.Storage.S3.Bucket},
		{"BLOG_S3_REGION", &cfg.Storage.S3.Region},
		{"BLOG_S3_ACCESS_KEY", &cfg.Storage.S3.AccessKey},
		{"BLOG_S3_SECRET_KEY", &cfg.Storage.S3.SecretKey},
		{"BLOG_S3_PREFIX", &cfg.Storage.S3.Prefix},
		{"BLOG_MAIL_HOST", &cfg.Mail.Host},
		{"BLOG_MAIL_USERNAME", &cfg.Mail.Username},
		{"BLOG_MAIL_PASSWORD", &cfg.Mail.Password},
		{"BLOG_MAIL_FROM", &cfg.Mail.From},
		{"BLOG_NEWSLETTER_PROVIDER", &cfg.Newsletter.Provider},
		{"BLOG_NEWSLETTER_ENDPOINT", &cfg.Newsletter.Endpoint},
		{"BLOG_NEWSLETTER_TOKEN", &cfg.Newsletter.Token},
		{"BLOG_CONTENT_API_TOKEN", &cfg.ContentAPI.Token},
		{"BLOG_WEBHOOK_ENDPOINT", &cfg.Webhooks.Endpoint},
		{"BLOG_WEBHOOK_SECRET", &cfg.Webhooks.Secret},
	}
	for _, override := range stringOverrides {
		if value, ok := os.LookupEnv(override.name); ok && strings.TrimSpace(value) != "" {
			*override.target = strings.TrimSpace(value)
		}
	}

	durationOverrides := []struct {
		name   string
		target *Duration
	}{
		{"BLOG_SHUTDOWN_TIMEOUT", &cfg.Server.ShutdownTimeout},
		{"BLOG_DATABASE_BUSY_TIMEOUT", &cfg.Database.BusyTimeout},
		{"BLOG_SESSION_LIFETIME", &cfg.Security.SessionLifetime},
		{"BLOG_PUBLISHING_SCHEDULER_INTERVAL", &cfg.Publishing.SchedulerInterval},
		{"BLOG_PUBLISHING_SNAPSHOT_INTERVAL", &cfg.Publishing.EditingSnapshotInterval},
		{"BLOG_PUBLISHING_TRASH_CLEANUP_INTERVAL", &cfg.Publishing.TrashCleanupInterval},
		{"BLOG_BACKUP_INTERVAL", &cfg.Operations.BackupInterval},
	}
	for _, override := range durationOverrides {
		if value, ok := os.LookupEnv(override.name); ok && strings.TrimSpace(value) != "" {
			if err := override.target.UnmarshalText([]byte(strings.TrimSpace(value))); err != nil {
				return fmt.Errorf("parse %s: %w", override.name, err)
			}
		}
	}

	integerOverrides := []struct {
		name   string
		target *int
	}{
		{"BLOG_DATABASE_CACHE_SIZE_KIB", &cfg.Database.CacheSizeKiB},
		{"BLOG_DATABASE_READ_CONNECTIONS", &cfg.Database.ReadConnections},
		{"BLOG_MEDIA_MAX_UPLOAD_BYTES", &cfg.Media.MaxUploadBytes},
		{"BLOG_MEDIA_MAX_IMAGE_PIXELS", &cfg.Media.MaxImagePixels},
		{"BLOG_MEDIA_JPEG_QUALITY", &cfg.Media.JPEGQuality},
		{"BLOG_PUBLISHING_SCHEDULER_BATCH_SIZE", &cfg.Publishing.SchedulerBatchSize},
		{"BLOG_PUBLISHING_REVISION_LIMIT", &cfg.Publishing.RevisionLimit},
		{"BLOG_PUBLISHING_TRASH_RETENTION_DAYS", &cfg.Publishing.TrashRetentionDays},
		{"BLOG_DISCOVERY_SYNC_BATCH_SIZE", &cfg.Discovery.SyncBatchSize},
		{"BLOG_SEARCH_MAX_RESULTS", &cfg.Discovery.MaxResults},
		{"BLOG_RSS_LIMIT", &cfg.Discovery.FeedLimit},
		{"BLOG_SITEMAP_LIMIT", &cfg.Discovery.SitemapLimit},
		{"BLOG_BACKUP_DAILY_RETENTION", &cfg.Operations.BackupDailyRetention},
		{"BLOG_BACKUP_WEEKLY_RETENTION", &cfg.Operations.BackupWeeklyRetention},
		{"BLOG_THEME_PACKAGE_MAX_BYTES", &cfg.Extensions.ThemePackageMaxBytes},
		{"BLOG_THEME_MAX_FILES", &cfg.Extensions.ThemeMaxFiles},
		{"BLOG_ANALYTICS_RETENTION_DAYS", &cfg.Analytics.RetentionDays},
		{"BLOG_MAIL_PORT", &cfg.Mail.Port},
	}
	if value, ok := os.LookupEnv("BLOG_MEDIA_VARIANT_WIDTHS"); ok && strings.TrimSpace(value) != "" {
		var widths []int
		for _, part := range strings.Split(value, ",") {
			parsed, err := strconv.Atoi(strings.TrimSpace(part))
			if err != nil {
				return fmt.Errorf("parse BLOG_MEDIA_VARIANT_WIDTHS: %w", err)
			}
			widths = append(widths, parsed)
		}
		cfg.Media.VariantWidths = widths
	}
	for _, override := range integerOverrides {
		if value, ok := os.LookupEnv(override.name); ok && strings.TrimSpace(value) != "" {
			parsed, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return fmt.Errorf("parse %s: %w", override.name, err)
			}
			*override.target = parsed
		}
	}
	if value, ok := os.LookupEnv("BLOG_THEME_MAX_UNPACKED_BYTES"); ok && strings.TrimSpace(value) != "" {
		parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil {
			return fmt.Errorf("parse BLOG_THEME_MAX_UNPACKED_BYTES: %w", err)
		}
		cfg.Extensions.ThemeMaxUnpacked = parsed
	}
	if value, ok := os.LookupEnv("BLOG_TRUSTED_PROXY_CIDRS"); ok {
		value = strings.TrimSpace(value)
		if value == "" {
			cfg.Server.TrustedProxyCIDRs = nil
		} else {
			cfg.Server.TrustedProxyCIDRs = make([]string, 0, len(strings.Split(value, ",")))
			for _, part := range strings.Split(value, ",") {
				cfg.Server.TrustedProxyCIDRs = append(cfg.Server.TrustedProxyCIDRs, strings.TrimSpace(part))
			}
		}
	}
	if value, ok := os.LookupEnv("BLOG_COOKIE_SECURE"); ok && strings.TrimSpace(value) != "" {
		parsed, err := strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("parse BLOG_COOKIE_SECURE: %w", err)
		}
		cfg.Security.CookieSecure = parsed
	} else if baseURLFromEnvironment && strings.TrimSpace(baseURL) != "" {
		// The public base URL is the source of truth for Compose/local runs.
		// In particular, an HTTP development URL must not receive Secure
		// cookies or the browser will omit the setup/session cookie entirely.
		if parsed, err := url.Parse(strings.TrimSpace(baseURL)); err == nil {
			cfg.Security.CookieSecure = strings.EqualFold(parsed.Scheme, "https")
		}
	}
	if value, ok := os.LookupEnv("BLOG_S3_FORCE_PATH_STYLE"); ok && strings.TrimSpace(value) != "" {
		parsed, err := strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("parse BLOG_S3_FORCE_PATH_STYLE: %w", err)
		}
		cfg.Storage.S3.ForcePathStyle = parsed
	}
	if value, ok := os.LookupEnv("BLOG_MAIL_ENABLED"); ok && strings.TrimSpace(value) != "" {
		parsed, err := strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("parse BLOG_MAIL_ENABLED: %w", err)
		}
		cfg.Mail.Enabled = parsed
	}
	if value, ok := os.LookupEnv("BLOG_ANALYTICS_ENABLED"); ok && strings.TrimSpace(value) != "" {
		parsed, err := strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("parse BLOG_ANALYTICS_ENABLED: %w", err)
		}
		cfg.Analytics.Enabled = parsed
	}
	if value, ok := os.LookupEnv("BLOG_COMMENTS_ENABLED"); ok && strings.TrimSpace(value) != "" {
		parsed, err := strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("parse BLOG_COMMENTS_ENABLED: %w", err)
		}
		cfg.Comments.Enabled = parsed
	}
	if value, ok := os.LookupEnv("BLOG_CONTENT_API_ENABLED"); ok && strings.TrimSpace(value) != "" {
		parsed, err := strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("parse BLOG_CONTENT_API_ENABLED: %w", err)
		}
		cfg.ContentAPI.Enabled = parsed
	}
	if value, ok := os.LookupEnv("BLOG_WEBHOOKS_ENABLED"); ok && strings.TrimSpace(value) != "" {
		parsed, err := strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("parse BLOG_WEBHOOKS_ENABLED: %w", err)
		}
		cfg.Webhooks.Enabled = parsed
	}
	if value, ok := os.LookupEnv("BLOG_WEBHOOK_MAX_ATTEMPTS"); ok && strings.TrimSpace(value) != "" {
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("parse BLOG_WEBHOOK_MAX_ATTEMPTS: %w", err)
		}
		cfg.Webhooks.MaxAttempts = parsed
	}
	if value, ok := os.LookupEnv("BLOG_COMMENTS_EXTERNAL_ENDPOINT"); ok && strings.TrimSpace(value) != "" {
		cfg.Comments.ExternalEndpoint = strings.TrimSpace(value)
	}
	return nil
}

func (cfg Config) Validate() error {
	var problems []error
	if strings.TrimSpace(cfg.Server.ListenAddress) == "" {
		problems = append(problems, errors.New("server.listen_address is required"))
	}
	if cfg.Server.ShutdownTimeout.Duration <= 0 || cfg.Server.ShutdownTimeout.Duration > time.Minute {
		problems = append(problems, errors.New("server.shutdown_timeout must be between 1ns and 1m"))
	}
	if len(cfg.Server.TrustedProxyCIDRs) > 32 {
		problems = append(problems, errors.New("server.trusted_proxy_cidrs must contain at most 32 entries"))
	}
	seenProxyCIDRs := make(map[string]struct{}, len(cfg.Server.TrustedProxyCIDRs))
	for _, raw := range cfg.Server.TrustedProxyCIDRs {
		prefix, err := normalizeProxyCIDR(raw)
		if err != nil {
			problems = append(problems, fmt.Errorf("server.trusted_proxy_cidrs contains invalid CIDR %q", raw))
			continue
		}
		key := prefix.String()
		if _, exists := seenProxyCIDRs[key]; exists {
			problems = append(problems, fmt.Errorf("server.trusted_proxy_cidrs contains duplicate CIDR %q", raw))
			continue
		}
		seenProxyCIDRs[key] = struct{}{}
	}
	if strings.TrimSpace(cfg.Storage.DataDir) == "" {
		problems = append(problems, errors.New("storage.data_dir is required"))
	}
	if cfg.Storage.Adapter != "local" && cfg.Storage.Adapter != "s3" {
		problems = append(problems, errors.New("storage.adapter must be local or s3"))
	}
	if cfg.Storage.Adapter == "s3" {
		if strings.TrimSpace(cfg.Storage.S3.Endpoint) == "" || strings.TrimSpace(cfg.Storage.S3.Bucket) == "" || strings.TrimSpace(cfg.Storage.S3.Region) == "" {
			problems = append(problems, errors.New("storage.s3 endpoint, bucket, and region are required when storage.adapter is s3"))
		}
	}
	if strings.TrimSpace(cfg.Database.Path) == "" {
		problems = append(problems, errors.New("database.path is required"))
	}
	if cfg.Database.BusyTimeout.Duration <= 0 || cfg.Database.BusyTimeout.Duration > time.Minute {
		problems = append(problems, errors.New("database.busy_timeout must be between 1ns and 1m"))
	}
	if cfg.Database.CacheSizeKiB < 1024 || cfg.Database.CacheSizeKiB > 128*1024 {
		problems = append(problems, errors.New("database.cache_size_kib must be between 1024 and 131072"))
	}
	if cfg.Database.ReadConnections < 1 || cfg.Database.ReadConnections > 8 {
		problems = append(problems, errors.New("database.read_connections must be between 1 and 8"))
	}
	if cfg.Media.MaxUploadBytes < 1<<20 || cfg.Media.MaxUploadBytes > 100<<20 {
		problems = append(problems, errors.New("media.max_upload_bytes must be between 1048576 and 104857600"))
	}
	if cfg.Media.MaxImagePixels < 1_000_000 || cfg.Media.MaxImagePixels > 40_000_000 {
		problems = append(problems, errors.New("media.max_image_pixels must be between 1000000 and 40000000"))
	}
	if len(cfg.Media.VariantWidths) < 1 || len(cfg.Media.VariantWidths) > 4 {
		problems = append(problems, errors.New("media.variant_widths must contain between 1 and 4 widths"))
	} else {
		previous := 0
		for _, width := range cfg.Media.VariantWidths {
			if width < 160 || width > 3840 || width <= previous {
				problems = append(problems, errors.New("media.variant_widths must be strictly increasing values between 160 and 3840"))
				break
			}
			previous = width
		}
	}
	if cfg.Media.JPEGQuality < 85 || cfg.Media.JPEGQuality > 100 {
		problems = append(problems, errors.New("media.jpeg_quality must be between 85 and 100"))
	}
	if cfg.Publishing.SchedulerInterval.Duration < time.Second || cfg.Publishing.SchedulerInterval.Duration > 5*time.Minute {
		problems = append(problems, errors.New("publishing.scheduler_interval must be between 1s and 5m"))
	}
	if cfg.Publishing.SchedulerBatchSize < 1 || cfg.Publishing.SchedulerBatchSize > 100 {
		problems = append(problems, errors.New("publishing.scheduler_batch_size must be between 1 and 100"))
	}
	if cfg.Publishing.EditingSnapshotInterval.Duration < 5*time.Second || cfg.Publishing.EditingSnapshotInterval.Duration > 5*time.Minute {
		problems = append(problems, errors.New("publishing.editing_snapshot_interval must be between 5s and 5m"))
	}
	if cfg.Publishing.RevisionLimit < 10 || cfg.Publishing.RevisionLimit > 500 {
		problems = append(problems, errors.New("publishing.revision_limit must be between 10 and 500"))
	}
	if cfg.Publishing.TrashRetentionDays < 1 || cfg.Publishing.TrashRetentionDays > 3650 {
		problems = append(problems, errors.New("publishing.trash_retention_days must be between 1 and 3650"))
	}
	if cfg.Publishing.TrashCleanupInterval.Duration < time.Minute || cfg.Publishing.TrashCleanupInterval.Duration > 24*time.Hour {
		problems = append(problems, errors.New("publishing.trash_cleanup_interval must be between 1m and 24h"))
	}
	baseURL, err := url.Parse(strings.TrimSpace(cfg.Discovery.BaseURL))
	if err != nil || (baseURL.Scheme != "http" && baseURL.Scheme != "https") || baseURL.Host == "" || baseURL.User != nil || baseURL.RawQuery != "" || baseURL.Fragment != "" {
		problems = append(problems, errors.New("discovery.base_url must be an absolute HTTP or HTTPS URL without credentials, query, or fragment"))
	}
	if cfg.Discovery.SyncBatchSize < 1 || cfg.Discovery.SyncBatchSize > 100 {
		problems = append(problems, errors.New("discovery.sync_batch_size must be between 1 and 100"))
	}
	if cfg.Discovery.MaxResults < 10 || cfg.Discovery.MaxResults > 100 {
		problems = append(problems, errors.New("discovery.max_results must be between 10 and 100"))
	}
	if cfg.Discovery.FeedLimit < 1 || cfg.Discovery.FeedLimit > 200 {
		problems = append(problems, errors.New("discovery.feed_limit must be between 1 and 200"))
	}
	if cfg.Discovery.SitemapLimit < 100 || cfg.Discovery.SitemapLimit > 50_000 {
		problems = append(problems, errors.New("discovery.sitemap_limit must be between 100 and 50000"))
	}
	if cfg.Operations.BackupInterval.Duration < time.Hour || cfg.Operations.BackupInterval.Duration > 7*24*time.Hour {
		problems = append(problems, errors.New("operations.backup_interval must be between 1h and 168h"))
	}
	if cfg.Operations.BackupDailyRetention < 1 || cfg.Operations.BackupDailyRetention > 31 {
		problems = append(problems, errors.New("operations.backup_daily_retention must be between 1 and 31"))
	}
	if cfg.Operations.BackupWeeklyRetention < 0 || cfg.Operations.BackupWeeklyRetention > 52 {
		problems = append(problems, errors.New("operations.backup_weekly_retention must be between 0 and 52"))
	}
	if cfg.Extensions.ThemePackageMaxBytes < 1<<20 || cfg.Extensions.ThemePackageMaxBytes > 256<<20 {
		problems = append(problems, errors.New("extensions.theme_package_max_bytes must be between 1 MiB and 256 MiB"))
	}
	if cfg.Extensions.ThemeMaxFiles < 16 || cfg.Extensions.ThemeMaxFiles > 4096 {
		problems = append(problems, errors.New("extensions.theme_max_files must be between 16 and 4096"))
	}
	if cfg.Extensions.ThemeMaxUnpacked < 1<<20 || cfg.Extensions.ThemeMaxUnpacked > 512<<20 {
		problems = append(problems, errors.New("extensions.theme_max_unpacked_bytes must be between 1 MiB and 512 MiB"))
	}
	if cfg.Analytics.RetentionDays < 30 || cfg.Analytics.RetentionDays > 3650 {
		problems = append(problems, errors.New("analytics.retention_days must be between 30 and 3650"))
	}
	if cfg.Comments.Provider != "local" && cfg.Comments.Provider != "external" && cfg.Comments.Provider != "disabled" {
		problems = append(problems, errors.New("comments.provider must be local, external, or disabled"))
	}
	if cfg.Comments.Provider == "external" {
		external, err := url.Parse(strings.TrimSpace(cfg.Comments.ExternalEndpoint))
		if err != nil || (external.Scheme != "http" && external.Scheme != "https") || external.Host == "" || external.User != nil || external.RawQuery != "" || external.Fragment != "" {
			problems = append(problems, errors.New("comments.external_endpoint must be an absolute HTTP or HTTPS URL without credentials, query, or fragment when provider is external"))
		}
	}
	if cfg.Mail.Port < 1 || cfg.Mail.Port > 65535 {
		problems = append(problems, errors.New("mail.port must be between 1 and 65535"))
	}
	if cfg.Newsletter.Provider == "" {
		problems = append(problems, errors.New("newsletter.provider must not be empty"))
	}
	if cfg.Webhooks.MaxAttempts < 1 || cfg.Webhooks.MaxAttempts > 5 {
		problems = append(problems, errors.New("webhooks.max_attempts must be between 1 and 5"))
	}
	if cfg.Webhooks.Enabled || strings.TrimSpace(cfg.Webhooks.Endpoint) != "" {
		parsed, err := url.Parse(strings.TrimSpace(cfg.Webhooks.Endpoint))
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
			problems = append(problems, errors.New("webhooks.endpoint must be an absolute HTTP or HTTPS URL without credentials or fragment"))
		}
		if strings.TrimSpace(cfg.Webhooks.Secret) == "" {
			problems = append(problems, errors.New("webhooks.secret is required when webhooks are enabled"))
		}
	}
	if cfg.Logging.Format != "text" && cfg.Logging.Format != "json" {
		problems = append(problems, errors.New("logging.format must be text or json"))
	}
	switch cfg.Logging.Level {
	case "debug", "info", "warn", "error":
	default:
		problems = append(problems, errors.New("logging.level must be debug, info, warn, or error"))
	}
	if strings.TrimSpace(cfg.Security.AuthSecret) == "" && strings.TrimSpace(cfg.Security.AuthSecretFile) == "" {
		problems = append(problems, errors.New("security.auth_secret_file is required when BLOG_AUTH_SECRET is unset"))
	}
	if cfg.Security.SessionLifetime.Duration < 15*time.Minute || cfg.Security.SessionLifetime.Duration > 7*24*time.Hour {
		problems = append(problems, errors.New("security.session_lifetime must be between 15m and 168h"))
	}
	return errors.Join(problems...)
}

func normalizeProxyCIDR(raw string) (netip.Prefix, error) {
	prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
	if err != nil || !prefix.IsValid() {
		return netip.Prefix{}, errors.New("invalid proxy CIDR")
	}
	address := prefix.Addr()
	bits := prefix.Bits()
	if address.Is4In6() {
		address = address.Unmap()
		bits -= 96
	}
	if bits <= 0 {
		return netip.Prefix{}, errors.New("proxy CIDR must not be a default route")
	}
	normalized := netip.PrefixFrom(address, bits).Masked()
	if !normalized.IsValid() {
		return netip.Prefix{}, errors.New("invalid normalized proxy CIDR")
	}
	return normalized, nil
}
