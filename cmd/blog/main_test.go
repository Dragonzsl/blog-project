package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
	"github.com/zhushilin/blog-project/internal/identity"
	"github.com/zhushilin/blog-project/internal/platform/config"
	"github.com/zhushilin/blog-project/internal/platform/database"
	"github.com/zhushilin/blog-project/internal/platform/secrets"
)

func TestAuthRecoverCommandInvalidatesExistingSessions(t *testing.T) {
	for _, name := range []string{
		"BLOG_CONFIG_FILE", "BLOG_DATA_DIR", "BLOG_DATABASE_PATH", "BLOG_AUTH_SECRET", "BLOG_AUTH_SECRET_FILE",
	} {
		t.Setenv(name, "")
	}
	dataDir := t.TempDir()
	configPath := filepath.Join(dataDir, "blog.toml")
	configContents := []byte(fmt.Sprintf("[storage]\ndata_dir = %q\n\n[security]\ncookie_secure = false\n", dataDir))
	if err := os.WriteFile(configPath, configContents, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(context.Background(), cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	authSecret, err := secrets.LoadOrCreateAuthSecret(cfg.Security)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	service, err := identity.NewService(identity.NewRepository(db), authSecret, time.Hour)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	started, err := service.StartSetup(context.Background(), "CLI Test", "owner", "correct horse battery staple")
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	code, err := totp.GenerateCode(started.TOTPSecret, time.Now().UTC())
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := service.CompleteSetup(context.Background(), started.Token, code); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	passwordPath := filepath.Join(dataDir, "new-password")
	if err := os.WriteFile(passwordPath, []byte("a brand new secure password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := authWithOutput([]string{"recover", "--config", configPath, "--username", "owner", "--password-file", passwordPath}, io.Discard); err != nil {
		t.Fatalf("auth recover error = %v", err)
	}

	db, err = database.Open(context.Background(), cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var authVersion, sessions, recoveryCodes int
	if err := db.Reader.QueryRow("SELECT auth_version FROM owners WHERE id = 1").Scan(&authVersion); err != nil {
		t.Fatal(err)
	}
	if err := db.Reader.QueryRow("SELECT count(*) FROM sessions").Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if err := db.Reader.QueryRow("SELECT count(*) FROM owner_recovery_codes WHERE used_at IS NULL").Scan(&recoveryCodes); err != nil {
		t.Fatal(err)
	}
	if authVersion != 2 || sessions != 0 || recoveryCodes != recoveryCodeCountForTest {
		t.Fatalf("after recovery auth_version=%d sessions=%d recovery_codes=%d", authVersion, sessions, recoveryCodes)
	}
}

const recoveryCodeCountForTest = 10
