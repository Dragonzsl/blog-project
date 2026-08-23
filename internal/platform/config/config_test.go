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

[logging]
level = "warn"
format = "json"
`)
	if err := os.WriteFile(configPath, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BLOG_LISTEN_ADDRESS", ":7777")
	t.Setenv("BLOG_DATABASE_READ_CONNECTIONS", "3")

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
