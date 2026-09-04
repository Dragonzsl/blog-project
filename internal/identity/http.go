package identity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"image/png"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/pquerna/otp"
	"github.com/zhushilin/blog-project/internal/platform/clientip"
	"github.com/zhushilin/blog-project/internal/platform/config"
	"github.com/zhushilin/blog-project/internal/publishing"
	adminweb "github.com/zhushilin/blog-project/web/admin"
)

const maxAuthFormBytes = 32 << 10

type sessionContextKey struct{}

type DashboardQueries interface {
	Dashboard(context.Context) (publishing.DashboardSummary, error)
}

type PendingCommentQueries interface {
	PendingCount(context.Context) (int, error)
}

type HTTPHandler struct {
	service          *Service
	security         config.Security
	logger           *slog.Logger
	templates        *template.Template
	dashboardQueries DashboardQueries
	pendingComments  PendingCommentQueries
	css              []byte
	cssETag          string
	adminJS          []byte
	adminJSETag      string
	js               []byte
	jsETag           string
	clientIP         *clientip.Resolver
}

func NewHTTPHandler(service *Service, security config.Security, logger *slog.Logger) (*HTTPHandler, error) {
	templates, err := template.ParseFS(adminweb.Files, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse admin templates: %w", err)
	}
	css, err := adminweb.Files.ReadFile("static/admin.css")
	if err != nil {
		return nil, fmt.Errorf("read admin stylesheet: %w", err)
	}
	hash := sha256.Sum256(css)
	adminJS, err := adminweb.Files.ReadFile("static/admin.js")
	if err != nil {
		return nil, fmt.Errorf("read admin interface script: %w", err)
	}
	adminJSHash := sha256.Sum256(adminJS)
	js, err := adminweb.Files.ReadFile("static/editor.js")
	if err != nil {
		return nil, fmt.Errorf("read admin editor script: %w", err)
	}
	jsHash := sha256.Sum256(js)
	return &HTTPHandler{
		service:     service,
		security:    security,
		logger:      logger,
		templates:   templates,
		css:         css,
		cssETag:     `"` + base64.RawURLEncoding.EncodeToString(hash[:12]) + `"`,
		adminJS:     adminJS,
		adminJSETag: `"` + base64.RawURLEncoding.EncodeToString(adminJSHash[:12]) + `"`,
		js:          js,
		jsETag:      `"` + base64.RawURLEncoding.EncodeToString(jsHash[:12]) + `"`,
		clientIP:    clientip.DirectPeerOnly(),
	}, nil
}

func (h *HTTPHandler) SetClientIPResolver(resolver *clientip.Resolver) {
	if resolver == nil {
		resolver = clientip.DirectPeerOnly()
	}
	h.clientIP = resolver
}

func (h *HTTPHandler) SetDashboardQueries(queries DashboardQueries) {
	h.dashboardQueries = queries
}

func (h *HTTPHandler) SetPendingCommentQueries(queries PendingCommentQueries) {
	h.pendingComments = queries
}

func (h *HTTPHandler) RegisterPublic(router chi.Router) {
	router.Get("/assets/admin.css", h.stylesheet)
	router.Get("/assets/admin.js", h.adminScript)
	router.Get("/assets/editor.js", h.editorScript)
	router.Get("/setup", h.setupPage)
	router.Post("/setup/start", h.setupStart)
	router.Post("/setup/complete", h.setupComplete)
	router.Get("/login", h.loginPage)
	router.Post("/login", h.login)
}

func (h *HTTPHandler) RegisterProtected(router chi.Router) {
	router.Get("/", h.dashboard)
	router.Post("/logout", h.logout)
}

func (h *HTTPHandler) SecurityHeaders(next http.Handler) http.Handler {
	return h.securityHeaders(next)
}

func (h *HTTPHandler) RequireSession(next http.Handler) http.Handler {
	return h.requireSession(next)
}

func (h *HTTPHandler) VerifyCSRF(w http.ResponseWriter, r *http.Request) bool {
	if !h.parseForm(w, r) {
		return false
	}
	return h.VerifyParsedCSRF(w, r)
}

func (h *HTTPHandler) VerifyParsedCSRF(w http.ResponseWriter, r *http.Request) bool {
	if !h.originAllowed(r) {
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return false
	}
	session := sessionFromContext(r.Context())
	if session.Token == "" || !h.service.VerifySessionCSRF(session.Token, r.FormValue("csrf_token")) {
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return false
	}
	return true
}

func (h *HTTPHandler) CSRFToken(r *http.Request) string {
	return h.service.SessionCSRF(sessionFromContext(r.Context()).Token)
}

func SessionFromRequest(r *http.Request) (Session, bool) {
	session := sessionFromContext(r.Context())
	return session, session.Token != ""
}

func (h *HTTPHandler) stylesheet(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("If-None-Match") == h.cssETag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=0, must-revalidate")
	w.Header().Set("ETag", h.cssETag)
	_, _ = w.Write(h.css)
}

func (h *HTTPHandler) editorScript(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("If-None-Match") == h.jsETag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=0, must-revalidate")
	w.Header().Set("ETag", h.jsETag)
	_, _ = w.Write(h.js)
}

func (h *HTTPHandler) adminScript(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("If-None-Match") == h.adminJSETag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=0, must-revalidate")
	w.Header().Set("ETag", h.adminJSETag)
	_, _ = w.Write(h.adminJS)
}

func (h *HTTPHandler) setupPage(w http.ResponseWriter, r *http.Request) {
	initialized, err := h.service.Initialized(r.Context())
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	if initialized {
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		return
	}
	nonce, err := h.formNonce(r)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	h.setCookie(w, h.formCookieName(), nonce, 15*time.Minute)
	h.render(w, r, "setup.html", map[string]any{"CSRF": nonce})
}

func (h *HTTPHandler) setupStart(w http.ResponseWriter, r *http.Request) {
	if !h.parseProtectedForm(w, r, h.formCookieName(), h.service.VerifyFormNonce) {
		return
	}
	siteName := strings.TrimSpace(r.FormValue("site_name"))
	username := strings.TrimSpace(r.FormValue("username"))
	if !constantStringEqual(r.FormValue("password"), r.FormValue("password_confirm")) {
		h.renderAuthError(w, r, "setup.html", "两次输入的密码不一致", map[string]any{"CSRF": r.FormValue("csrf_token"), "SiteName": siteName, "Username": username})
		return
	}
	started, err := h.service.StartSetup(r.Context(), siteName, username, r.FormValue("password"))
	if err != nil {
		if errors.Is(err, ErrAlreadyInitialized) {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		h.renderAuthError(w, r, "setup.html", err.Error(), map[string]any{"CSRF": r.FormValue("csrf_token"), "SiteName": siteName, "Username": username})
		return
	}
	h.setCookie(w, h.setupCookieName(), started.Token, 15*time.Minute)
	h.renderSetupTOTP(w, r, started, siteName, "")
}

func (h *HTTPHandler) setupComplete(w http.ResponseWriter, r *http.Request) {
	if !h.parseForm(w, r) || !h.originAllowed(r) {
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return
	}
	cookie, err := r.Cookie(h.setupCookieName())
	if err != nil || !h.service.VerifySetupCSRF(cookie.Value, r.FormValue("csrf_token")) {
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return
	}
	completed, err := h.service.CompleteSetup(r.Context(), cookie.Value, r.FormValue("totp_code"))
	if err != nil {
		if !errors.Is(err, ErrInvalidCredentials) && !errors.Is(err, ErrInvalidSetup) {
			h.internalError(w, r, err)
			return
		}
		binding, bindingErr := h.service.SetupBinding(r.Context(), cookie.Value)
		if bindingErr != nil {
			http.Redirect(w, r, "/admin/setup", http.StatusSeeOther)
			return
		}
		h.renderSetupTOTP(w, r, binding, binding.SiteName, "验证代码无效，请检查设备时间后重试")
		return
	}
	h.clearCookie(w, h.setupCookieName())
	h.setSessionCookie(w, completed.Session)
	siteName, err := h.service.SiteName(r.Context())
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	h.render(w, r, "recovery.html", map[string]any{"SiteName": siteName, "Codes": completed.RecoveryCodes})
}

func (h *HTTPHandler) loginPage(w http.ResponseWriter, r *http.Request) {
	initialized, err := h.service.Initialized(r.Context())
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	if !initialized {
		http.Redirect(w, r, "/admin/setup", http.StatusSeeOther)
		return
	}
	if cookie, err := r.Cookie(h.sessionCookieName()); err == nil {
		if _, err := h.service.Authenticate(r.Context(), cookie.Value); err == nil {
			http.Redirect(w, r, "/admin/", http.StatusSeeOther)
			return
		}
	}
	nonce, err := h.formNonce(r)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	h.setCookie(w, h.formCookieName(), nonce, 15*time.Minute)
	h.renderLogin(w, r, nonce, "", "")
}

func (h *HTTPHandler) login(w http.ResponseWriter, r *http.Request) {
	if !h.parseProtectedForm(w, r, h.formCookieName(), h.service.VerifyFormNonce) {
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	session, err := h.service.Login(r.Context(), h.resolveClientIP(r), username, r.FormValue("password"), r.FormValue("second_factor"))
	if err != nil {
		if !errors.Is(err, ErrInvalidCredentials) && !errors.Is(err, ErrRateLimited) {
			h.internalError(w, r, err)
			return
		}
		message := "用户名、密码或二次验证无效"
		if errors.Is(err, ErrRateLimited) {
			message = "尝试过于频繁，请稍后再试"
		}
		h.renderLogin(w, r, r.FormValue("csrf_token"), username, message)
		return
	}
	h.clearCookie(w, h.formCookieName())
	h.setSessionCookie(w, session)
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}

func (h *HTTPHandler) dashboard(w http.ResponseWriter, r *http.Request) {
	session := sessionFromContext(r.Context())
	siteName, err := h.service.SiteName(r.Context())
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	dashboard := dashboardView{}
	if h.dashboardQueries != nil {
		summary, err := h.dashboardQueries.Dashboard(r.Context())
		if err != nil {
			h.internalError(w, r, err)
			return
		}
		dashboard = newDashboardView(summary)
	}
	if h.pendingComments != nil {
		pending, err := h.pendingComments.PendingCount(r.Context())
		if err != nil {
			h.internalError(w, r, err)
			return
		}
		dashboard.PendingCommentCount = pending
	}
	h.render(w, r, "dashboard.html", map[string]any{
		"SiteName":     siteName,
		"Username":     session.Username,
		"CSRF":         h.service.SessionCSRF(session.Token),
		"AdminSection": "overview",
		"Dashboard":    dashboard,
	})
}

type dashboardView struct {
	DraftCount          int
	ScheduledCount      int
	PublishedCount      int
	TrashedCount        int
	PublishedThisWeek   int
	PendingCommentCount int
	WithoutExcerptCount int
	RecentEdits         []dashboardContentView
	RecentPublished     []dashboardContentView
}

type dashboardContentView struct {
	Title        string
	Kind         string
	Status       string
	UpdatedAt    string
	PublishedAt  string
	EditURL      string
	PublicURL    string
	HasPublicURL bool
}

func newDashboardView(summary publishing.DashboardSummary) dashboardView {
	view := dashboardView{
		DraftCount:          summary.DraftCount,
		ScheduledCount:      summary.ScheduledCount,
		PublishedCount:      summary.PublishedCount,
		TrashedCount:        summary.TrashedCount,
		PublishedThisWeek:   summary.PublishedThisWeek,
		WithoutExcerptCount: summary.WithoutExcerptCount,
	}
	for _, content := range summary.RecentEdits {
		view.RecentEdits = append(view.RecentEdits, dashboardContent(content))
	}
	for _, content := range summary.RecentPublished {
		view.RecentPublished = append(view.RecentPublished, dashboardContent(content))
	}
	return view
}

func dashboardContent(content publishing.AdminContentSummary) dashboardContentView {
	descriptor := "articles"
	kind := "文章"
	publicURL := "/posts/" + content.Slug
	if content.Kind == "page" {
		descriptor = "pages"
		kind = "页面"
		publicURL = "/" + content.Slug
	}
	view := dashboardContentView{
		Title:        content.Title,
		Kind:         kind,
		Status:       dashboardStatusLabel(content.Status),
		UpdatedAt:    content.UpdatedAt.Format("2006-01-02 15:04"),
		EditURL:      fmt.Sprintf("/admin/%s/%d/edit", descriptor, content.ID),
		PublicURL:    publicURL,
		HasPublicURL: content.Status == "published" && content.Slug != "",
	}
	if content.PublishedAt != nil {
		view.PublishedAt = content.PublishedAt.Format("2006-01-02 15:04")
	}
	return view
}

func dashboardStatusLabel(status string) string {
	switch status {
	case "published":
		return "已发布"
	case "scheduled":
		return "定时发布"
	default:
		return "草稿"
	}
}

func (h *HTTPHandler) logout(w http.ResponseWriter, r *http.Request) {
	if !h.parseForm(w, r) || !h.originAllowed(r) {
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return
	}
	session := sessionFromContext(r.Context())
	if !h.service.VerifySessionCSRF(session.Token, r.FormValue("csrf_token")) {
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return
	}
	if err := h.service.Logout(r.Context(), session.Token); err != nil {
		h.internalError(w, r, err)
		return
	}
	h.clearCookie(w, h.sessionCookieName())
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

func (h *HTTPHandler) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(h.sessionCookieName())
		if err != nil {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		session, err := h.service.Authenticate(r.Context(), cookie.Value)
		if err != nil {
			if !errors.Is(err, ErrInvalidCredentials) {
				h.internalError(w, r, err)
				return
			}
			h.clearCookie(w, h.sessionCookieName())
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionContextKey{}, session)))
	})
}

func (h *HTTPHandler) parseProtectedForm(w http.ResponseWriter, r *http.Request, cookieName string, verify func(string, string) bool) bool {
	if !h.parseForm(w, r) || !h.originAllowed(r) {
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return false
	}
	cookie, err := r.Cookie(cookieName)
	if err != nil || !verify(cookie.Value, r.FormValue("csrf_token")) {
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return false
	}
	return true
}

func (h *HTTPHandler) parseForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxAuthFormBytes)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return false
	}
	return true
}

func (h *HTTPHandler) formNonce(r *http.Request) (string, error) {
	if cookie, err := r.Cookie(h.formCookieName()); err == nil && h.service.VerifyFormNonce(cookie.Value, cookie.Value) {
		return cookie.Value, nil
	}
	return h.service.NewFormNonce()
}

func (h *HTTPHandler) originAllowed(r *http.Request) bool {
	value := r.Header.Get("Origin")
	if value == "" {
		value = r.Header.Get("Referer")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || !strings.EqualFold(parsed.Host, r.Host) {
		return false
	}
	expectedScheme := "http"
	if h.security.CookieSecure {
		expectedScheme = "https"
	}
	return strings.EqualFold(parsed.Scheme, expectedScheme)
}

func (h *HTTPHandler) renderSetupTOTP(w http.ResponseWriter, r *http.Request, started SetupStartResult, siteName, errorMessage string) {
	key, err := otp.NewKeyFromURL(started.TOTPURI)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	image, err := key.Image(220, 220)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image); err != nil {
		h.internalError(w, r, err)
		return
	}
	h.renderWithStatus(w, r, "setup_totp.html", map[string]any{
		"SiteName": siteName,
		"Secret":   started.TOTPSecret,
		"QRCode":   template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(encoded.Bytes())),
		"CSRF":     h.service.SetupCSRF(started.Token),
		"Error":    errorMessage,
	}, statusForError(errorMessage))
}

func (h *HTTPHandler) renderLogin(w http.ResponseWriter, r *http.Request, csrf, username, errorMessage string) {
	siteName, err := h.service.SiteName(r.Context())
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	h.renderWithStatus(w, r, "login.html", map[string]any{
		"SiteName": siteName,
		"CSRF":     csrf,
		"Username": username,
		"Error":    errorMessage,
	}, statusForError(errorMessage))
}

func (h *HTTPHandler) renderAuthError(w http.ResponseWriter, r *http.Request, name, message string, data map[string]any) {
	data["Error"] = message
	h.renderWithStatus(w, r, name, data, http.StatusUnprocessableEntity)
}

func (h *HTTPHandler) render(w http.ResponseWriter, r *http.Request, name string, data any) {
	h.renderWithStatus(w, r, name, data, http.StatusOK)
}

func (h *HTTPHandler) renderWithStatus(w http.ResponseWriter, r *http.Request, name string, data any, status int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := h.templates.ExecuteTemplate(w, name, data); err != nil {
		h.logger.ErrorContext(r.Context(), "render admin template", "template", name, "error", err)
	}
}

func (h *HTTPHandler) internalError(w http.ResponseWriter, r *http.Request, err error) {
	h.logger.ErrorContext(r.Context(), "identity request failed", "error", err)
	http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
}

func (h *HTTPHandler) setSessionCookie(w http.ResponseWriter, session Session) {
	// Derive the cookie lifetime from the same clock used to create and
	// validate the session.  Besides keeping the two expiry mechanisms in
	// lockstep, this preserves deterministic tests that inject Service.now
	// without accidentally emitting an already-expired browser cookie.
	h.setCookie(w, h.sessionCookieName(), session.Token, session.ExpiresAt.Sub(h.service.now()))
}

func (h *HTTPHandler) setCookie(w http.ResponseWriter, name, value string, lifetime time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		Secure:   h.security.CookieSecure,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(lifetime.Seconds()),
		Expires:  time.Now().UTC().Add(lifetime),
	})
}

func (h *HTTPHandler) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		Secure:   h.security.CookieSecure,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
		Expires:  time.Unix(1, 0),
	})
}

func (h *HTTPHandler) sessionCookieName() string { return h.cookieName("blog_session") }
func (h *HTTPHandler) setupCookieName() string   { return h.cookieName("blog_setup") }
func (h *HTTPHandler) formCookieName() string    { return h.cookieName("blog_form") }

func (h *HTTPHandler) cookieName(base string) string {
	if h.security.CookieSecure {
		return "__Host-" + base
	}
	return base
}

func (h *HTTPHandler) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; script-src 'self'; img-src 'self' data:; connect-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		next.ServeHTTP(w, r)
	})
}

func sessionFromContext(ctx context.Context) Session {
	session, _ := ctx.Value(sessionContextKey{}).(Session)
	return session
}

func (h *HTTPHandler) resolveClientIP(r *http.Request) string {
	if h.clientIP == nil {
		return clientip.DirectPeerOnly().Resolve(r)
	}
	return h.clientIP.Resolve(r)
}

// remoteAddress is kept for package-level callers from the original handler;
// it intentionally uses the safe direct-peer-only policy.
func remoteAddress(r *http.Request) string { return clientip.DirectPeerOnly().Resolve(r) }

func statusForError(message string) int {
	if message != "" {
		return http.StatusUnprocessableEntity
	}
	return http.StatusOK
}
