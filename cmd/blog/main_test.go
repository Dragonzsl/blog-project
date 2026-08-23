package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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

func TestBackupVerifyDrillUpgradeAndRestoreCommands(t *testing.T) {
	for _, name := range []string{
		"BLOG_CONFIG_FILE", "BLOG_DATA_DIR", "BLOG_DATABASE_PATH", "BLOG_AUTH_SECRET", "BLOG_AUTH_SECRET_FILE",
	} {
		t.Setenv(name, "")
	}
	dataDir := t.TempDir()
	configPath := filepath.Join(dataDir, "blog.toml")
	configContents := []byte(fmt.Sprintf("[storage]\ndata_dir = %q\n\n[security]\ncookie_secure = false\n", filepath.Join(dataDir, "data")))
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
	if _, err := db.Writer.Exec("UPDATE system_state SET render_epoch=77 WHERE id=1"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(dataDir, "manual.tar.gz")
	var output bytes.Buffer
	if err := backupWithOutput([]string{"create", "--config", configPath, "--output", archivePath}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Backup created and verified") {
		t.Fatalf("backup output=%q", output.String())
	}
	output.Reset()
	if err := backupWithOutput([]string{"verify", "--archive", archivePath}, &output); err != nil {
		t.Fatal(err)
	}
	if err := backupWithOutput([]string{"drill", "--config", configPath, "--archive", archivePath}, &output); err != nil {
		t.Fatal(err)
	}
	if err := backupWithOutput([]string{"list", "--config", configPath}, &output); err != nil {
		t.Fatal(err)
	}
	if err := auditWithOutput([]string{"list", "--config", configPath}, &output); err != nil {
		t.Fatal(err)
	}
	if err := statusWithOutput([]string{"--config", configPath}, &output); err != nil {
		t.Fatal(err)
	}
	upgradeArchive := filepath.Join(dataDir, "upgrade.tar.gz")
	if err := upgradeWithOutput([]string{"prepare", "--config", configPath, "--output", upgradeArchive}, &output); err != nil {
		t.Fatal(err)
	}
	targetDir := filepath.Join(dataDir, "restored")
	if err := restoreWithOutput([]string{"--archive", archivePath, "--target-data-dir", targetDir}, &output); err != nil {
		t.Fatal(err)
	}
	restored, err := database.Open(context.Background(), config.Database{Path: filepath.Join(targetDir, "db", "blog.sqlite"), BusyTimeout: config.Duration{Duration: time.Second}, CacheSizeKiB: 4096, ReadConnections: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var epoch int64
	if err := restored.Reader.QueryRow("SELECT render_epoch FROM system_state WHERE id=1").Scan(&epoch); err != nil || epoch != 77 {
		t.Fatalf("restored epoch=%d err=%v", epoch, err)
	}
}

const recoveryCodeCountForTest = 10
