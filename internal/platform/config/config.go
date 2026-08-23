package config

import (
	"errors"
	"fmt"
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
	Server   Server   `toml:"server"`
	Storage  Storage  `toml:"storage"`
	Database Database `toml:"database"`
	Logging  Logging  `toml:"logging"`
	Security Security `toml:"security"`
}

type Server struct {
	ListenAddress   string   `toml:"listen_address"`
	ShutdownTimeout Duration `toml:"shutdown_timeout"`
}

type Storage struct {
	DataDir string `toml:"data_dir"`
}

type Database struct {
	Path            string   `toml:"path"`
	BusyTimeout     Duration `toml:"busy_timeout"`
	CacheSizeKiB    int      `toml:"cache_size_kib"`
	ReadConnections int      `toml:"read_connections"`
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

func Defaults() Config {
	return Config{
		Server: Server{
			ListenAddress:   ":8080",
			ShutdownTimeout: Duration{Duration: 10 * time.Second},
		},
		Storage: Storage{DataDir: "./data"},
		Database: Database{
			Path:            "db/blog.sqlite",
			BusyTimeout:     Duration{Duration: 5 * time.Second},
			CacheSizeKiB:    16 * 1024,
			ReadConnections: 2,
		},
		Logging: Logging{Level: "info", Format: "text"},
		Security: Security{
			AuthSecretFile:  "secrets/auth.key",
			CookieSecure:    true,
			SessionLifetime: Duration{Duration: 12 * time.Hour},
		},
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
	if value, ok := os.LookupEnv("BLOG_COOKIE_SECURE"); ok && strings.TrimSpace(value) != "" {
		parsed, err := strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("parse BLOG_COOKIE_SECURE: %w", err)
		}
		cfg.Security.CookieSecure = parsed
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
	if strings.TrimSpace(cfg.Storage.DataDir) == "" {
		problems = append(problems, errors.New("storage.data_dir is required"))
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
