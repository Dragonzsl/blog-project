package analytics

import (
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	adminweb "github.com/zhushilin/blog-project/web/admin"
)

type Security interface {
	CSRFToken(*http.Request) string
}

type HTTPHandler struct {
	service   *Service
	security  Security
	logger    *slog.Logger
	templates *template.Template
}

type DashboardMetrics struct {
	Views          int64
	PathVisitors   int64
	TrackedPaths   int
	LatestActivity string
}

func NewHTTPHandler(service *Service, security Security, logger *slog.Logger) (*HTTPHandler, error) {
	templates, err := template.ParseFS(adminweb.Files, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse analytics templates: %w", err)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &HTTPHandler{service: service, security: security, logger: logger, templates: templates}, nil
}

func (h *HTTPHandler) RegisterAdmin(router chi.Router) {
	router.Get("/analytics", h.dashboard)
}

func (h *HTTPHandler) dashboard(w http.ResponseWriter, r *http.Request) {
	days := 30
	if value, err := strconv.Atoi(r.URL.Query().Get("days")); err == nil && value > 0 {
		days = value
	}
	rows, err := h.service.Summary(r.Context(), days)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "read analytics summary", "error", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	metrics := DashboardMetrics{}
	paths := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		metrics.Views += row.Views
		metrics.PathVisitors += row.UniqueVisitors
		paths[row.Path] = struct{}{}
		if row.Day > metrics.LatestActivity {
			metrics.LatestActivity = row.Day
		}
	}
	metrics.TrackedPaths = len(paths)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := h.templates.ExecuteTemplate(w, "analytics.html", map[string]any{"Rows": rows, "Days": days, "Ranges": []int{7, 30, 90}, "Metrics": metrics, "CSRF": h.security.CSRFToken(r), "AdminSection": "plugins"}); err != nil {
		h.logger.ErrorContext(r.Context(), "render analytics dashboard", "error", err)
	}
}
