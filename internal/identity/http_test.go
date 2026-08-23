package identity

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/pquerna/otp/totp"
	"github.com/zhushilin/blog-project/internal/platform/config"
)

var (
	csrfPattern   = regexp.MustCompile(`name="csrf_token" value="([^"]+)"`)
	secretPattern = regexp.MustCompile(`<code id="totp-secret">([^<]+)</code>`)
)

func TestHTTPSetupAndLogoutFlow(t *testing.T) {
	service, closeDatabase := newTestService(t)
	defer closeDatabase()
	fixedTime := time.Date(2026, time.August, 23, 14, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return fixedTime }
	handler, err := NewHTTPHandler(service, config.Security{CookieSecure: false}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewHTTPHandler() error = %v", err)
	}
	router := chi.NewRouter()
	registerTestRoutes(router, handler)
	server := httptest.NewServer(router)
	defer server.Close()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}

	setupBody := getBody(t, client, server.URL+"/admin/setup")
	setupCSRF := extract(t, csrfPattern, setupBody)
	startBody, status := postForm(t, client, server.URL, "/admin/setup/start", url.Values{
		"csrf_token":       {setupCSRF},
		"site_name":        {"纸上花园"},
		"username":         {"owner"},
		"password":         {"correct horse battery staple"},
		"password_confirm": {"correct horse battery staple"},
	})
	if status != http.StatusOK {
		t.Fatalf("setup start status = %d, body = %s", status, startBody)
	}
	secret := extract(t, secretPattern, startBody)
	setupTOTPcsrf := extract(t, csrfPattern, startBody)
	code, err := totp.GenerateCode(secret, fixedTime)
	if err != nil {
		t.Fatal(err)
	}
	recoveryBody, status := postForm(t, client, server.URL, "/admin/setup/complete", url.Values{
		"csrf_token": {setupTOTPcsrf},
		"totp_code":  {code},
	})
	if status != http.StatusOK || !strings.Contains(recoveryBody, "一次性恢复码") {
		t.Fatalf("setup complete status = %d, body = %s", status, recoveryBody)
	}

	dashboard := getBody(t, client, server.URL+"/admin/")
	if !strings.Contains(dashboard, "安全初始化已经完成") {
		t.Fatalf("dashboard body = %s", dashboard)
	}
	logoutCSRF := extract(t, csrfPattern, dashboard)
	_, status = postForm(t, client, server.URL, "/admin/logout", url.Values{"csrf_token": {logoutCSRF}})
	if status != http.StatusOK {
		t.Fatalf("logout final status = %d", status)
	}
	loginPage := getBody(t, client, server.URL+"/admin/")
	if !strings.Contains(loginPage, "欢迎回来") {
		t.Fatalf("protected page did not redirect to login: %s", loginPage)
	}
}

func TestHTTPRejectsCrossSiteSetup(t *testing.T) {
	service, closeDatabase := newTestService(t)
	defer closeDatabase()
	handler, err := NewHTTPHandler(service, config.Security{CookieSecure: false}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	registerTestRoutes(router, handler)
	server := httptest.NewServer(router)
	defer server.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	body := getBody(t, client, server.URL+"/admin/setup")
	csrf := extract(t, csrfPattern, body)
	request, err := http.NewRequest(http.MethodPost, server.URL+"/admin/setup/start", strings.NewReader(url.Values{
		"csrf_token":       {csrf},
		"site_name":        {"纸上花园"},
		"username":         {"owner"},
		"password":         {"correct horse battery staple"},
		"password_confirm": {"correct horse battery staple"},
	}.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", "https://attacker.example")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site setup status = %d", response.StatusCode)
	}
}

func getBody(t *testing.T, client *http.Client, endpoint string) string {
	t.Helper()
	response, err := client.Get(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d, body = %s", endpoint, response.StatusCode, body)
	}
	return string(body)
}

func postForm(t *testing.T, client *http.Client, origin, path string, values url.Values) (string, int) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, origin+path, strings.NewReader(values.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", origin)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body), response.StatusCode
}

func extract(t *testing.T, pattern *regexp.Regexp, body string) string {
	t.Helper()
	matches := pattern.FindStringSubmatch(body)
	if len(matches) != 2 {
		t.Fatalf("pattern %s not found in body", pattern)
	}
	return matches[1]
}

func registerTestRoutes(router chi.Router, handler *HTTPHandler) {
	router.Route("/admin", func(admin chi.Router) {
		admin.Use(handler.SecurityHeaders)
		handler.RegisterPublic(admin)
		admin.Group(func(protected chi.Router) {
			protected.Use(handler.RequireSession)
			handler.RegisterProtected(protected)
		})
	})
}
