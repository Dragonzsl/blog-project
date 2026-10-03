package extensions_test

import (
	"context"
	"github.com/go-chi/chi/v5"
	"github.com/pquerna/otp/totp"
	"github.com/zhushilin/blog-project/internal/extensions"
	"github.com/zhushilin/blog-project/internal/identity"
	"github.com/zhushilin/blog-project/internal/platform/config"
	"github.com/zhushilin/blog-project/internal/platform/database"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type protectedPlugin struct{}

func (protectedPlugin) Manifest() extensions.Manifest {
	return extensions.Manifest{ID: "protected", Name: "Protected", Version: "1", APIVersion: extensions.HostAPIVersion}
}
func (protectedPlugin) Register(*extensions.Host) error { return nil }
func TestPluginRemovalRequiresOwnerSessionAndSameOriginCSRF(t *testing.T) {
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
	r := extensions.NewRegistry(db, router, nil)
	if err := r.Register(protectedPlugin{}); err != nil {
		t.Fatal(err)
	}
	h, err := extensions.NewHTTPHandler(r, security, nil)
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
				req := httptest.NewRequest(http.MethodPost, "http://example.com/admin/plugins/protected/"+action, strings.NewReader(form.Encode()))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				req.Header.Set("Origin", scenario.origin)
				if scenario.session {
					req.AddCookie(&http.Cookie{Name: "blog_session", Value: completed.Session.Token})
				}
				w := httptest.NewRecorder()
				router.ServeHTTP(w, req)
				if w.Code != scenario.want {
					t.Fatalf("status=%d want=%d", w.Code, scenario.want)
				}
				if !scenario.session && w.Header().Get("Location") != "/admin/login" {
					t.Fatal("anonymous mutation was not redirected to login")
				}
			})
		}
	}
}
