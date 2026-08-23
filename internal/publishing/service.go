package publishing

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zhushilin/blog-project/internal/organization"
	platformid "github.com/zhushilin/blog-project/internal/platform/id"
	platformslug "github.com/zhushilin/blog-project/internal/platform/slug"
)

type Service struct {
	repository *Repository
	now        func() time.Time
}

func NewService(repository *Repository) *Service {
	return &Service{repository: repository, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) CreateDraft(ctx context.Context, input DraftInput) (Article, error) {
	return s.createDraft(ctx, "article", input)
}

func (s *Service) CreatePageDraft(ctx context.Context, input DraftInput) (Article, error) {
	return s.createDraft(ctx, "page", input)
}

func (s *Service) createDraft(ctx context.Context, kind string, input DraftInput) (Article, error) {
	input, err := validateInput(kind, input)
	if err != nil {
		return Article{}, err
	}
	now := s.now()
	contentPublicID, err := platformid.NewPublicID(now)
	if err != nil {
		return Article{}, err
	}
	revisionPublicID, err := platformid.NewPublicID(now)
	if err != nil {
		return Article{}, err
	}
	return s.repository.CreateDraft(ctx, kind, contentPublicID, revisionInput{
		PublicID:     revisionPublicID,
		Title:        input.Title,
		Slug:         input.Slug,
		SlugKey:      input.slugKey,
		Excerpt:      input.Excerpt,
		BodyMarkdown: input.BodyMarkdown,
		Reason:       "create",
	}, input.CategoryID, input.TagIDs, now)
}

func (s *Service) UpdateDraft(ctx context.Context, id, expectedVersion int64, input DraftInput) (Article, error) {
	return s.updateDraft(ctx, "article", id, expectedVersion, input)
}

func (s *Service) UpdatePageDraft(ctx context.Context, id, expectedVersion int64, input DraftInput) (Article, error) {
	return s.updateDraft(ctx, "page", id, expectedVersion, input)
}

func (s *Service) updateDraft(ctx context.Context, kind string, id, expectedVersion int64, input DraftInput) (Article, error) {
	if id < 1 || expectedVersion < 1 {
		return Article{}, ErrNotFound
	}
	input, err := validateInput(kind, input)
	if err != nil {
		return Article{}, err
	}
	now := s.now()
	revisionPublicID, err := platformid.NewPublicID(now)
	if err != nil {
		return Article{}, err
	}
	return s.repository.UpdateDraft(ctx, kind, id, expectedVersion, revisionInput{
		PublicID:     revisionPublicID,
		Title:        input.Title,
		Slug:         input.Slug,
		SlugKey:      input.slugKey,
		Excerpt:      input.Excerpt,
		BodyMarkdown: input.BodyMarkdown,
		Reason:       "save",
	}, input.CategoryID, input.TagIDs, now)
}

func (s *Service) Publish(ctx context.Context, id, expectedVersion int64) (Article, error) {
	if id < 1 || expectedVersion < 1 {
		return Article{}, ErrNotFound
	}
	return s.repository.Publish(ctx, "article", id, expectedVersion, s.now())
}

func (s *Service) PublishPage(ctx context.Context, id, expectedVersion int64) (Article, error) {
	if id < 1 || expectedVersion < 1 {
		return Article{}, ErrNotFound
	}
	return s.repository.Publish(ctx, "page", id, expectedVersion, s.now())
}

func (s *Service) Article(ctx context.Context, id int64) (Article, error) {
	if id < 1 {
		return Article{}, ErrNotFound
	}
	return s.repository.Content(ctx, "article", id)
}

func (s *Service) Page(ctx context.Context, id int64) (Article, error) {
	if id < 1 {
		return Article{}, ErrNotFound
	}
	return s.repository.Content(ctx, "page", id)
}

func (s *Service) PublicArticle(ctx context.Context, slug string) (Article, error) {
	_, slugKey, err := platformslug.Normalize(slug)
	if err != nil {
		return Article{}, ErrNotFound
	}
	return s.repository.PublicContent(ctx, "article", slugKey)
}

func (s *Service) PublicArticleByID(ctx context.Context, id int64) (Article, error) {
	if id < 1 {
		return Article{}, ErrNotFound
	}
	return s.repository.PublicContentByID(ctx, "article", id)
}

func (s *Service) PublicPage(ctx context.Context, slug string) (Article, error) {
	_, slugKey, err := platformslug.Normalize(slug)
	if err != nil {
		return Article{}, ErrNotFound
	}
	return s.repository.PublicContent(ctx, "page", slugKey)
}

func (s *Service) Articles(ctx context.Context) ([]Article, error) {
	return s.repository.Contents(ctx, "article")
}

func (s *Service) Pages(ctx context.Context) ([]Article, error) {
	return s.repository.Contents(ctx, "page")
}

func (s *Service) PublishedArticles(ctx context.Context, limit int) ([]Article, error) {
	if limit < 1 {
		limit = 1
	}
	if limit > 100 {
		limit = 100
	}
	return s.repository.PublishedArticles(ctx, limit)
}

func (s *Service) Categories(ctx context.Context) ([]organization.Category, error) {
	return s.repository.organization.Categories(ctx)
}

func (s *Service) Tags(ctx context.Context) ([]organization.Tag, error) {
	return s.repository.organization.Tags(ctx)
}

func (s *Service) NavigationContentOptions(ctx context.Context) ([]organization.ContentOption, error) {
	articles, err := s.Articles(ctx)
	if err != nil {
		return nil, err
	}
	pages, err := s.Pages(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]organization.ContentOption, 0, len(articles)+len(pages))
	for _, content := range append(articles, pages...) {
		if content.Status == "published" {
			result = append(result, organization.ContentOption{ID: content.ID, Kind: content.Kind, Title: content.Title})
		}
	}
	return result, nil
}

func validateInput(kind string, input DraftInput) (DraftInput, error) {
	input.Title = strings.TrimSpace(input.Title)
	input.Excerpt = strings.TrimSpace(input.Excerpt)
	if !utf8.ValidString(input.Title) || utf8.RuneCountInString(input.Title) < 1 || utf8.RuneCountInString(input.Title) > 200 {
		return DraftInput{}, ValidationError{Message: "标题需要 1–200 个字符"}
	}
	displaySlug, slugKey, err := platformslug.Normalize(input.Slug)
	if err != nil {
		return DraftInput{}, ValidationError{Message: "固定链接需要 1–120 位字母、数字或单横线分隔词"}
	}
	input.Slug = displaySlug
	input.slugKey = slugKey
	if !utf8.ValidString(input.Excerpt) || utf8.RuneCountInString(input.Excerpt) > 500 {
		return DraftInput{}, ValidationError{Message: "摘要不能超过 500 个字符"}
	}
	if !utf8.ValidString(input.BodyMarkdown) || len(input.BodyMarkdown) > 2<<20 {
		return DraftInput{}, ValidationError{Message: "Markdown 正文不能超过 2 MiB"}
	}
	if kind == "page" && (input.CategoryID != 0 || len(input.TagIDs) != 0) {
		return DraftInput{}, ValidationError{Message: "页面不能使用文章分类或标签"}
	}
	return input, nil
}
