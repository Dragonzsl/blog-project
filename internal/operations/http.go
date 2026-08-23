package operations

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

type ReadinessChecker interface {
	Ready(context.Context) error
	MigrationVersion(context.Context) (int64, error)
}

type HealthHandler struct {
	database  ReadinessChecker
	startedAt time.Time
	version   string
}

func NewHealthHandler(database ReadinessChecker, startedAt time.Time, version string) *HealthHandler {
	return &HealthHandler{database: database, startedAt: startedAt, version: version}
}

func (h *HealthHandler) Live(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":         "ok",
		"version":        h.version,
		"uptime_seconds": int64(time.Since(h.startedAt).Seconds()),
	})
}

func (h *HealthHandler) Ready(w http.ResponseWriter, r *http.Request) {
	if err := h.database.Ready(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status": "unavailable",
			"reason": "database not ready",
		})
		return
	}
	version, err := h.database.MigrationVersion(r.Context())
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status": "unavailable",
			"reason": "migration state unavailable",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":            "ok",
		"migration_version": version,
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
