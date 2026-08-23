package publishing

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

type fakeAdminSecurity struct{}

func (fakeAdminSecurity) CSRFToken(*http.Request) string { return "test-csrf" }
func (fakeAdminSecurity) VerifyParsedCSRF(http.ResponseWriter, *http.Request) bool {
	return true
}

type fakeSiteNamer struct{}

func (fakeSiteNamer) SiteName(context.Context) (string, error) { return "纸上花园", nil }

func TestHTTPArticleCreatePreviewAndPublish(t *testing.T) {
	service, _ := newPublishingTestService(t)
	handler, err := NewHTTPHandler(
		service,
		fakeAdminSecurity{},
		fakeSiteNamer{},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Route("/admin", handler.RegisterAdmin)

	create := formRequest(http.MethodPost, "/admin/articles", url.Values{
		"csrf_token":    {"test-csrf"},
		"title":         {"安全发布"},
		"slug":          {"safe-publish"},
		"excerpt":       {"摘要"},
		"body_markdown": {"# Hello\n\n<script>alert(1)</script>"},
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, create)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/admin/articles/1/edit" {
		t.Fatalf("create status=%d location=%q body=%s", response.Code, response.Header().Get("Location"), response.Body.String())
	}

	publish := httptest.NewRecorder()
	router.ServeHTTP(publish, formRequest(http.MethodPost, "/admin/articles/1/publish", url.Values{
		"csrf_token":   {"test-csrf"},
		"lock_version": {"1"},
	}))
	if publish.Code != http.StatusSeeOther || publish.Header().Get("Location") != "/posts/safe-publish" {
		t.Fatalf("publish status=%d location=%q body=%s", publish.Code, publish.Header().Get("Location"), publish.Body.String())
	}

}

func formRequest(method, target string, values url.Values) *http.Request {
	request := httptest.NewRequest(method, target, strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return request
}
