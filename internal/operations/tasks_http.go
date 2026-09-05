package operations

import (
	"context"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"

	"github.com/go-chi/chi/v5"
	adminweb "github.com/zhushilin/blog-project/web/admin"
)

type TaskManager interface {
	TaskList(context.Context, int) ([]TaskSummary, error)
	TaskCounts(context.Context) (TaskCounts, error)
	RetryTask(context.Context, int64) error
}

type TaskSnapshotManager interface {
	TaskSnapshot(context.Context) (TaskSnapshot, error)
}

type OperationsSummaryProvider func(context.Context) (OperationsSummary, error)

type TaskHTTPHandler struct {
	manager   TaskManager
	security  AdminSecurity
	logger    *slog.Logger
	templates *template.Template
	summary   OperationsSummaryProvider
}

func (h *TaskHTTPHandler) SetOperationsSummaryProvider(provider OperationsSummaryProvider) {
	h.summary = provider
}

type AdminSecurity interface {
	CSRFToken(*http.Request) string
	VerifyParsedCSRF(http.ResponseWriter, *http.Request) bool
}

func NewTaskHTTPHandler(manager TaskManager, security AdminSecurity, logger *slog.Logger) (*TaskHTTPHandler, error) {
	templates, err := template.ParseFS(adminweb.Files, "templates/*.html")
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &TaskHTTPHandler{manager: manager, security: security, logger: logger, templates: templates}, nil
}

func (h *TaskHTTPHandler) RegisterAdmin(router chi.Router) {
	router.Get("/operations/tasks", h.index)
	router.Post("/operations/tasks/{taskID}/retry", h.retry)
}

func (h *TaskHTTPHandler) index(w http.ResponseWriter, r *http.Request) {
	if h.manager == nil || h.security == nil {
		h.internalError(w, r, errors.New("task manager is not configured"))
		return
	}
	counts, err := h.manager.TaskCounts(r.Context())
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	tasks, err := h.manager.TaskList(r.Context(), 100)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	var snapshot TaskSnapshot
	if provider, ok := h.manager.(TaskSnapshotManager); ok {
		snapshot, err = provider.TaskSnapshot(r.Context())
		if err != nil {
			h.internalError(w, r, err)
			return
		}
	}
	var operationsSummary OperationsSummary
	if h.summary != nil {
		operationsSummary, err = h.summary(r.Context())
		if err != nil {
			h.internalError(w, r, err)
			return
		}
	} else {
		operationsSummary.Tasks = snapshot
	}
	notice, message := taskFeedback(r)
	data := map[string]any{
		"Tasks": tasks, "Counts": counts, "Snapshot": snapshot, "Operations": operationsSummary, "CSRF": h.security.CSRFToken(r),
		"AdminSection": "operations", "Notice": notice, "Error": message,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := h.templates.ExecuteTemplate(w, "operations_tasks.html", data); err != nil {
		h.internalError(w, r, err)
	}
}

func (h *TaskHTTPHandler) retry(w http.ResponseWriter, r *http.Request) {
	if h.manager == nil || h.security == nil {
		h.internalError(w, r, errors.New("task manager is not configured"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil || !h.security.VerifyParsedCSRF(w, r) {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "taskID"), 10, 64)
	if err != nil || id < 1 {
		h.redirect(w, r, "invalid")
		return
	}
	if err := h.manager.RetryTask(r.Context(), id); err != nil {
		if !errors.Is(err, ErrTaskNotFound) {
			h.logger.ErrorContext(r.Context(), "retry task", "error", SafeError(err))
		}
		h.redirect(w, r, "retry")
		return
	}
	query := url.Values{"notice": []string{"retried"}}
	http.Redirect(w, r, "/admin/operations/tasks?"+query.Encode(), http.StatusSeeOther)
}

func (h *TaskHTTPHandler) redirect(w http.ResponseWriter, r *http.Request, reason string) {
	http.Redirect(w, r, "/admin/operations/tasks?error="+url.QueryEscape(reason), http.StatusSeeOther)
}

func taskFeedback(r *http.Request) (string, string) {
	switch r.URL.Query().Get("notice") {
	case "retried":
		return "任务已重新排队。", ""
	}
	switch r.URL.Query().Get("error") {
	case "invalid":
		return "", "任务编号无效。"
	case "retry":
		return "", "任务当前不可重试，请刷新后再试。"
	}
	return "", ""
}

func (h *TaskHTTPHandler) internalError(w http.ResponseWriter, r *http.Request, err error) {
	h.logger.ErrorContext(r.Context(), "operations task request failed", "error", SafeError(err))
	http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
}
