package operations

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeReadiness struct {
	err     error
	version int64
}

func (f fakeReadiness) Ready(context.Context) error { return f.err }
func (f fakeReadiness) MigrationVersion(context.Context) (int64, error) {
	return f.version, f.err
}

func TestReady(t *testing.T) {
	handler := NewHealthHandler(fakeReadiness{version: 3}, time.Now(), "test")
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	response := httptest.NewRecorder()

	handler.Ready(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	if !strings.Contains(response.Body.String(), `"migration_version":3`) {
		t.Fatalf("body = %s", response.Body.String())
	}
}

func TestReadyHidesDependencyDetails(t *testing.T) {
	handler := NewHealthHandler(fakeReadiness{err: errors.New("secret path")}, time.Now(), "test")
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	response := httptest.NewRecorder()

	handler.Ready(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", response.Code)
	}
	if strings.Contains(response.Body.String(), "secret path") {
		t.Fatalf("response leaks dependency error: %s", response.Body.String())
	}
}
