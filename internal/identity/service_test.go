package identity

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
	"github.com/zhushilin/blog-project/internal/platform/config"
	"github.com/zhushilin/blog-project/internal/platform/database"
)

func TestOwnerSetupLoginRecoveryAndLogout(t *testing.T) {
	ctx := context.Background()
	service, closeDatabase := newTestService(t)
	defer closeDatabase()
	fixedTime := time.Date(2026, time.August, 23, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return fixedTime }

	started, err := service.StartSetup(ctx, "纸上花园", "owner", "correct horse battery staple")
	if err != nil {
		t.Fatalf("StartSetup() error = %v", err)
	}
	code, err := totp.GenerateCode(started.TOTPSecret, fixedTime)
	if err != nil {
		t.Fatalf("GenerateCode() error = %v", err)
	}
	completed, err := service.CompleteSetup(ctx, started.Token, code)
	if err != nil {
		t.Fatalf("CompleteSetup() error = %v", err)
	}
	if len(completed.RecoveryCodes) != recoveryCodeCount {
		t.Fatalf("recovery codes = %d", len(completed.RecoveryCodes))
	}
	if _, err := service.Authenticate(ctx, completed.Session.Token); err != nil {
		t.Fatalf("Authenticate(setup session) error = %v", err)
	}
	if _, err := service.StartSetup(ctx, "second", "other", "another secure password"); !errors.Is(err, ErrAlreadyInitialized) {
		t.Fatalf("second StartSetup() error = %v", err)
	}

	if err := service.Logout(ctx, completed.Session.Token); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	if _, err := service.Authenticate(ctx, completed.Session.Token); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Authenticate(logged out) error = %v", err)
	}

	loggedIn, err := service.Login(ctx, "test", "owner", "correct horse battery staple", code)
	if err != nil {
		t.Fatalf("Login(TOTP) error = %v", err)
	}
	if err := service.Logout(ctx, loggedIn.Token); err != nil {
		t.Fatalf("Logout(TOTP session) error = %v", err)
	}

	recoverySession, err := service.Login(ctx, "test", "owner", "correct horse battery staple", completed.RecoveryCodes[0])
	if err != nil {
		t.Fatalf("Login(recovery) error = %v", err)
	}
	if err := service.Logout(ctx, recoverySession.Token); err != nil {
		t.Fatalf("Logout(recovery session) error = %v", err)
	}
	if _, err := service.Login(ctx, "another-key", "owner", "correct horse battery staple", completed.RecoveryCodes[0]); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("reused recovery code error = %v", err)
	}
}

func TestCLIRecoveryInvalidatesSessions(t *testing.T) {
	ctx := context.Background()
	service, closeDatabase := newTestService(t)
	defer closeDatabase()
	fixedTime := time.Date(2026, time.August, 23, 13, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return fixedTime }

	started, err := service.StartSetup(ctx, "纸上花园", "owner", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	code, err := totp.GenerateCode(started.TOTPSecret, fixedTime)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := service.CompleteSetup(ctx, started.Token, code)
	if err != nil {
		t.Fatal(err)
	}

	recovered, err := service.Recover(ctx, "owner", "a brand new secure password")
	if err != nil {
		t.Fatalf("Recover() error = %v", err)
	}
	if len(recovered.RecoveryCodes) != recoveryCodeCount || recovered.TOTPURI == "" {
		t.Fatal("Recover() did not return replacement credentials")
	}
	if _, err := service.Authenticate(ctx, completed.Session.Token); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("old session remains valid: %v", err)
	}
	newCode, err := totp.GenerateCode(recovered.TOTPSecret, fixedTime)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Login(ctx, "test", "owner", "a brand new secure password", newCode); err != nil {
		t.Fatalf("Login(after recovery) error = %v", err)
	}
}

func TestSiteSettingsRoundTripPreservesDefaultSocialImage(t *testing.T) {
	ctx := context.Background()
	service, closeDatabase := newTestService(t)
	defer closeDatabase()
	fixedTime := time.Date(2026, time.August, 23, 14, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return fixedTime }
	started, err := service.StartSetup(ctx, "测试站点", "owner", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	code, err := totp.GenerateCode(started.TOTPSecret, fixedTime)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CompleteSetup(ctx, started.Token, code); err != nil {
		t.Fatal(err)
	}
	settings, err := service.SiteSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.DefaultSocialImageID = []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	if err := service.UpdateSiteSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	updated, err := service.SiteSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(updated.DefaultSocialImageID) != string(settings.DefaultSocialImageID) {
		t.Fatalf("default social image = %x, want %x", updated.DefaultSocialImageID, settings.DefaultSocialImageID)
	}
}

func newTestService(t *testing.T) (*Service, func()) {
	t.Helper()
	db, err := database.Open(context.Background(), config.Database{
		Path:            filepath.Join(t.TempDir(), "blog.sqlite"),
		BusyTimeout:     config.Duration{Duration: time.Second},
		CacheSizeKiB:    4096,
		ReadConnections: 2,
	})
	if err != nil {
		t.Fatalf("database.Open() error = %v", err)
	}
	service, err := NewService(NewRepository(db), []byte("0123456789abcdef0123456789abcdef"), time.Hour)
	if err != nil {
		db.Close()
		t.Fatalf("NewService() error = %v", err)
	}
	return service, func() {
		if err := db.Close(); err != nil {
			t.Errorf("database.Close() error = %v", err)
		}
	}
}
