package organization_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/zhushilin/blog-project/internal/organization"
	"github.com/zhushilin/blog-project/internal/platform/config"
	"github.com/zhushilin/blog-project/internal/platform/database"
)

type organizationSecurity struct{}

func (organizationSecurity) CSRFToken(*http.Request) string                           { return "csrf" }
func (organizationSecurity) VerifyParsedCSRF(http.ResponseWriter, *http.Request) bool { return true }

type organizationSite struct{}

func (organizationSite) SiteName(context.Context) (string, error) { return "纸上花园", nil }

type noContentOptions struct{}

func (noContentOptions) NavigationContentOptions(context.Context) ([]organization.ContentOption, error) {
	return nil, nil
}

func TestOrganizationAdminCreatesTermsAndNavigation(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, config.Database{Path: filepath.Join(t.TempDir(), "blog.sqlite"), BusyTimeout: config.Duration{Duration: time.Second}, CacheSizeKiB: 4096, ReadConnections: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := organization.NewService(db)
	handler, err := organization.NewHTTPHandler(service, noContentOptions{}, organizationSecurity{}, organizationSite{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Route("/admin", handler.RegisterAdmin)
	for target, values := range map[string]url.Values{"/admin/organization/categories": {"csrf_token": {"csrf"}, "name": {"随笔"}, "slug": {"essays"}, "sort_order": {"1"}}, "/admin/organization/tags": {"csrf_token": {"csrf"}, "name": {"Go"}, "slug": {"go"}}, "/admin/organization/navigation": {"csrf_token": {"csrf"}, "label": {"项目"}, "target_kind": {"external"}, "external_url": {"https://example.com"}, "location": {"primary"}, "sort_order": {"1"}}} {
		request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(values.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusSeeOther {
			t.Fatalf("%s status=%d body=%s", target, response.Code, response.Body.String())
		}
	}
	page := httptest.NewRecorder()
	router.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/admin/organization", nil))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "随笔") || !strings.Contains(page.Body.String(), "https://example.com") {
		t.Fatalf("organization page status=%d body=%s", page.Code, page.Body.String())
	}
}
