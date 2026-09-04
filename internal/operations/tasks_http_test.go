package operations

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

type taskHTTPManager struct {
	counts  TaskCounts
	items   []TaskSummary
	retried int64
}

func (m *taskHTTPManager) TaskList(context.Context, int) ([]TaskSummary, error) {
	return m.items, nil
}

func (m *taskHTTPManager) TaskCounts(context.Context) (TaskCounts, error) {
	return m.counts, nil
}

func (m *taskHTTPManager) RetryTask(_ context.Context, id int64) error {
	m.retried = id
	return nil
}

type taskHTTPSecurity struct{}

func (taskHTTPSecurity) CSRFToken(*http.Request) string { return "csrf" }

func (taskHTTPSecurity) VerifyParsedCSRF(_ http.ResponseWriter, request *http.Request) bool {
	return request.FormValue("csrf_token") == "csrf"
}

func TestTaskHTTPHandlerHidesPayloadAndListsSafeStatus(t *testing.T) {
	manager := &taskHTTPManager{
		counts: TaskCounts{Pending: 2, Running: 1, Failed: 1},
		items: []TaskSummary{{
			ID: 7, Kind: "plugin:webhooks.signed:deliver", Status: "failed", Attempts: 5,
			AvailableAt: time.UnixMilli(1000).UTC(), UpdatedAt: time.UnixMilli(2000).UTC(),
			LastError: "external endpoint failed [redacted]",
		}},
	}
	handler, err := NewTaskHTTPHandler(manager, taskHTTPSecurity{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	handler.RegisterAdmin(router)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/operations/tasks", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, "plugin:webhooks.signed:deliver") || !strings.Contains(body, "external endpoint failed [redacted]") {
		t.Fatalf("safe task data missing: %s", body)
	}
	if strings.Contains(body, "payload") {
		t.Fatalf("task payload leaked into page: %s", body)
	}
}

func TestTaskHTTPHandlerRetryRequiresCSRFAndReusesTaskID(t *testing.T) {
	manager := &taskHTTPManager{}
	handler, err := NewTaskHTTPHandler(manager, taskHTTPSecurity{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	handler.RegisterAdmin(router)

	bad := httptest.NewRecorder()
	badRequest := httptest.NewRequest(http.MethodPost, "/operations/tasks/9/retry", strings.NewReader(url.Values{"csrf_token": {"wrong"}}.Encode()))
	badRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	router.ServeHTTP(bad, badRequest)
	if bad.Code != http.StatusOK && bad.Code != http.StatusForbidden {
		t.Fatalf("bad CSRF status=%d", bad.Code)
	}
	if manager.retried != 0 {
		t.Fatalf("bad CSRF retried task %d", manager.retried)
	}

	good := httptest.NewRecorder()
	goodRequest := httptest.NewRequest(http.MethodPost, "/operations/tasks/9/retry", strings.NewReader(url.Values{"csrf_token": {"csrf"}}.Encode()))
	goodRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	router.ServeHTTP(good, goodRequest)
	if good.Code != http.StatusSeeOther || manager.retried != 9 {
		t.Fatalf("status=%d retried=%d location=%q", good.Code, manager.retried, good.Header().Get("Location"))
	}
}
