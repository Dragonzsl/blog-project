package publishing

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	adminweb "github.com/zhushilin/blog-project/web/admin"
)

const maxArticleFormBytes = (2 << 20) + (64 << 10)

type AdminSecurity interface {
	CSRFToken(*http.Request) string
	VerifyParsedCSRF(http.ResponseWriter, *http.Request) bool
}

type SiteNamer interface {
	SiteName(context.Context) (string, error)
}

type HTTPHandler struct {
	service   *Service
	security  AdminSecurity
	siteNamer SiteNamer
	logger    *slog.Logger
	templates *template.Template
}

func NewHTTPHandler(service *Service, security AdminSecurity, siteNamer SiteNamer, logger *slog.Logger) (*HTTPHandler, error) {
	templates, err := template.ParseFS(adminweb.Files, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse publishing templates: %w", err)
	}
	return &HTTPHandler{service: service, security: security, siteNamer: siteNamer, logger: logger, templates: templates}, nil
}

func (h *HTTPHandler) RegisterAdmin(router chi.Router) {
	router.Get("/articles", h.articleList)
	router.Get("/articles/new", h.articleNew)
	router.Post("/articles", h.articleCreate)
	router.Get("/articles/{articleID}/edit", h.articleEdit)
	router.Post("/articles/{articleID}", h.articleUpdate)
	router.Post("/articles/{articleID}/publish", h.articlePublish)
}

func (h *HTTPHandler) articleList(w http.ResponseWriter, r *http.Request) {
	articles, err := h.service.Articles(r.Context())
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	views := make([]map[string]any, 0, len(articles))
	for _, article := range articles {
		views = append(views, map[string]any{
			"ID":        article.ID,
			"Title":     article.Title,
			"Slug":      article.Slug,
			"Status":    statusLabel(article.Status),
			"Published": article.Status == "published",
			"UpdatedAt": article.UpdatedAt.Format("2006-01-02 15:04 UTC"),
		})
	}
	h.renderAdmin(w, r, "articles.html", map[string]any{"Articles": views})
}

func (h *HTTPHandler) articleNew(w http.ResponseWriter, r *http.Request) {
	h.renderEditor(w, r, Article{LockVersion: 1}, "", http.StatusOK)
}

func (h *HTTPHandler) articleCreate(w http.ResponseWriter, r *http.Request) {
	if !h.parseArticleForm(w, r) {
		return
	}
	article, err := h.service.CreateDraft(r.Context(), articleInput(r))
	if err != nil {
		if !isUserFacingError(err) {
			h.internalError(w, r, err)
			return
		}
		h.renderEditor(w, r, Article{
			Title:        r.FormValue("title"),
			Slug:         r.FormValue("slug"),
			Excerpt:      r.FormValue("excerpt"),
			BodyMarkdown: r.FormValue("body_markdown"),
			LockVersion:  1,
		}, userMessage(err), statusForPublishingError(err))
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/articles/%d/edit", article.ID), http.StatusSeeOther)
}

func (h *HTTPHandler) articleEdit(w http.ResponseWriter, r *http.Request) {
	article, ok := h.loadArticle(w, r)
	if !ok {
		return
	}
	h.renderEditor(w, r, article, "", http.StatusOK)
}

func (h *HTTPHandler) articleUpdate(w http.ResponseWriter, r *http.Request) {
	if !h.parseArticleForm(w, r) {
		return
	}
	id, err := articleID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	version, err := strconv.ParseInt(r.FormValue("lock_version"), 10, 64)
	if err != nil || version < 1 {
		http.Error(w, "Invalid editor version", http.StatusBadRequest)
		return
	}
	article, err := h.service.UpdateDraft(r.Context(), id, version, articleInput(r))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if !isUserFacingError(err) {
			h.internalError(w, r, err)
			return
		}
		current := Article{
			ID:           id,
			Title:        r.FormValue("title"),
			Slug:         r.FormValue("slug"),
			Excerpt:      r.FormValue("excerpt"),
			BodyMarkdown: r.FormValue("body_markdown"),
			LockVersion:  version,
		}
		h.renderEditor(w, r, current, userMessage(err), statusForPublishingError(err))
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/articles/%d/edit", article.ID), http.StatusSeeOther)
}

func (h *HTTPHandler) articlePublish(w http.ResponseWriter, r *http.Request) {
	if !h.parseArticleForm(w, r) {
		return
	}
	id, err := articleID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	version, err := strconv.ParseInt(r.FormValue("lock_version"), 10, 64)
	if err != nil || version < 1 {
		http.Error(w, "Invalid editor version", http.StatusBadRequest)
		return
	}
	article, err := h.service.Publish(r.Context(), id, version)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if !isUserFacingError(err) {
			h.internalError(w, r, err)
			return
		}
		current, loadErr := h.service.Article(r.Context(), id)
		if loadErr != nil {
			h.handleReadError(w, r, loadErr)
			return
		}
		h.renderEditor(w, r, current, userMessage(err), statusForPublishingError(err))
		return
	}
	http.Redirect(w, r, "/posts/"+article.Slug, http.StatusSeeOther)
}

func (h *HTTPHandler) loadArticle(w http.ResponseWriter, r *http.Request) (Article, bool) {
	id, err := articleID(r)
	if err != nil {
		http.NotFound(w, r)
		return Article{}, false
	}
	article, err := h.service.Article(r.Context(), id)
	if err != nil {
		h.handleReadError(w, r, err)
		return Article{}, false
	}
	return article, true
}

func (h *HTTPHandler) parseArticleForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxArticleFormBytes)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid article form", http.StatusBadRequest)
		return false
	}
	return h.security.VerifyParsedCSRF(w, r)
}

func (h *HTTPHandler) renderEditor(w http.ResponseWriter, r *http.Request, article Article, errorMessage string, status int) {
	action := "/admin/articles"
	if article.ID > 0 {
		action = fmt.Sprintf("/admin/articles/%d", article.ID)
	}
	h.renderAdminWithStatus(w, r, "article_edit.html", map[string]any{
		"Article":   article,
		"Action":    action,
		"IsNew":     article.ID == 0,
		"Published": article.Status == "published",
		"Status":    statusLabel(article.Status),
		"Error":     errorMessage,
	}, status)
}

func (h *HTTPHandler) renderAdmin(w http.ResponseWriter, r *http.Request, name string, data map[string]any) {
	h.renderAdminWithStatus(w, r, name, data, http.StatusOK)
}

func (h *HTTPHandler) renderAdminWithStatus(w http.ResponseWriter, r *http.Request, name string, data map[string]any, status int) {
	siteName, err := h.siteNamer.SiteName(r.Context())
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	data["SiteName"] = siteName
	data["CSRF"] = h.security.CSRFToken(r)
	h.renderPage(w, r, name, data, status, true)
}

func (h *HTTPHandler) renderPage(w http.ResponseWriter, r *http.Request, name string, data any, status int, private bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if private {
		w.Header().Set("Cache-Control", "no-store")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	w.WriteHeader(status)
	if err := h.templates.ExecuteTemplate(w, name, data); err != nil {
		h.logger.ErrorContext(r.Context(), "render publishing template", "template", name, "error", err)
	}
}

func (h *HTTPHandler) handleReadError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	h.internalError(w, r, err)
}

func (h *HTTPHandler) internalError(w http.ResponseWriter, r *http.Request, err error) {
	h.logger.ErrorContext(r.Context(), "publishing request failed", "error", err)
	http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
}

func articleID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(chi.URLParam(r, "articleID"), 10, 64)
	if err != nil || id < 1 {
		return 0, ErrNotFound
	}
	return id, nil
}

func articleInput(r *http.Request) DraftInput {
	return DraftInput{
		Title:        r.FormValue("title"),
		Slug:         r.FormValue("slug"),
		Excerpt:      r.FormValue("excerpt"),
		BodyMarkdown: r.FormValue("body_markdown"),
	}
}

func userMessage(err error) string {
	var validation ValidationError
	if errors.As(err, &validation) {
		return validation.Message
	}
	switch {
	case errors.Is(err, ErrConflict):
		return "文章已在其他页面被修改，请刷新后合并更改。"
	case errors.Is(err, ErrSlugUnavailable):
		return "这个固定链接已经被使用或保留。"
	case errors.Is(err, ErrPublishedSlugImmutable):
		return "已发布文章的固定链接不可直接修改。"
	default:
		return err.Error()
	}
}

func isUserFacingError(err error) bool {
	var validation ValidationError
	return errors.As(err, &validation) ||
		errors.Is(err, ErrConflict) ||
		errors.Is(err, ErrSlugUnavailable) ||
		errors.Is(err, ErrPublishedSlugImmutable)
}

func statusForPublishingError(err error) int {
	if errors.Is(err, ErrConflict) {
		return http.StatusConflict
	}
	return http.StatusUnprocessableEntity
}

func statusLabel(status string) string {
	switch status {
	case "published":
		return "已发布"
	case "scheduled":
		return "待定时发布"
	default:
		return "草稿"
	}
}
