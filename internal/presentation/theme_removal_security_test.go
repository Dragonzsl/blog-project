package presentation_test

import (
	"context"
	"github.com/go-chi/chi/v5"
	"github.com/pquerna/otp/totp"
	"github.com/zhushilin/blog-project/internal/identity"
	"github.com/zhushilin/blog-project/internal/platform/config"
	"github.com/zhushilin/blog-project/internal/platform/database"
	"github.com/zhushilin/blog-project/internal/presentation"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestThemeRemovalRequiresOwnerSessionAndSameOriginCSRF(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, config.Database{Path: filepath.Join(t.TempDir(), "auth.sqlite"), BusyTimeout: config.Duration{Duration: time.Second}, CacheSizeKiB: 4096, ReadConnections: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service, err := identity.NewService(identity.NewRepository(db), []byte(strings.Repeat("x", 32)), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	started, err := service.StartSetup(ctx, "Test", "owner", "test-only-long-password")
	if err != nil {
		t.Fatal("setup failed")
	}
	code, err := totp.GenerateCode(started.TOTPSecret, time.Now())
	if err != nil {
		t.Fatal("TOTP setup failed")
	}
	completed, err := service.CompleteSetup(ctx, started.Token, code)
	if err != nil {
		t.Fatal("setup completion failed")
	}
	security, err := identity.NewHTTPHandler(service, config.Security{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	if _, err := db.Writer.Exec(`INSERT INTO themes(theme_id,name,version,api_version,path,checksum,validation_status,active,installed_at,updated_at) VALUES('protected','Protected','1.0.0',1,'/missing',zeroblob(32),'valid',0,0,0)`); err != nil {
		t.Fatal(err)
	}
	fallback, err := presentation.NewDefaultTheme(presentation.NewMarkdown())
	if err != nil {
		t.Fatal(err)
	}
	manager, err := presentation.NewThemeManager(fallback, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	catalog := presentation.NewThemeCatalog(db, manager)
	h, err := presentation.NewThemeHTTPHandler(catalog, manager, security, themeRemovalSite{}, slog.Default(), presentation.ThemeInstallOptions{MaxBytes: 32 << 20})
	if err != nil {
		t.Fatal(err)
	}
	router.Route("/admin", func(admin chi.Router) { admin.Use(security.RequireSession); h.RegisterAdmin(admin) })
	for _, action := range []string{"delete", "restore"} {
		for _, scenario := range []struct {
			name, origin, csrf string
			session            bool
			want               int
		}{
			{"anonymous", "http://example.com", service.SessionCSRF(completed.Session.Token), false, http.StatusSeeOther},
			{"missing-csrf", "http://example.com", "", true, http.StatusForbidden},
			{"cross-origin", "http://attacker.example", service.SessionCSRF(completed.Session.Token), true, http.StatusForbidden},
			{"authorized", "http://example.com", service.SessionCSRF(completed.Session.Token), true, http.StatusSeeOther},
		} {
			t.Run(action+"/"+scenario.name, func(t *testing.T) {
				form := url.Values{"csrf_token": {scenario.csrf}, "confirm_action": {"1"}}
				req := httptest.NewRequest(http.MethodPost, "http://example.com/admin/themes/protected/1.0.0/"+action, strings.NewReader(form.Encode()))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				req.Header.Set("Origin", scenario.origin)
				if scenario.session {
					req.AddCookie(&http.Cookie{Name: "blog_session", Value: completed.Session.Token})
				}
				w := httptest.NewRecorder()
				router.ServeHTTP(w, req)
				want := scenario.want
				if action == "restore" && scenario.name == "authorized" {
					want = http.StatusUnprocessableEntity
				}
				if w.Code != want {
					t.Fatalf("status=%d want=%d", w.Code, want)
				}
				if !scenario.session && w.Header().Get("Location") != "/admin/login" {
					t.Fatal("anonymous mutation was not redirected to login")
				}
			})
		}
	}
}

type themeRemovalSite struct{}

func (themeRemovalSite) SiteName(context.Context) (string, error) { return "Test", nil }
