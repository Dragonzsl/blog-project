package publishing

import (
	"context"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	platformid "github.com/zhushilin/blog-project/internal/platform/id"
)

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type Service struct {
	repository *Repository
	now        func() time.Time
}

func NewService(repository *Repository) *Service {
	return &Service{repository: repository, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) CreateDraft(ctx context.Context, input DraftInput) (Article, error) {
	input, err := validateInput(input)
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
	return s.repository.CreateDraft(ctx, contentPublicID, revisionInput{
		PublicID:     revisionPublicID,
		Title:        input.Title,
		Slug:         input.Slug,
		Excerpt:      input.Excerpt,
		BodyMarkdown: input.BodyMarkdown,
		Reason:       "create",
	}, now)
}

func (s *Service) UpdateDraft(ctx context.Context, id, expectedVersion int64, input DraftInput) (Article, error) {
	if id < 1 || expectedVersion < 1 {
		return Article{}, ErrNotFound
	}
	input, err := validateInput(input)
	if err != nil {
		return Article{}, err
	}
	now := s.now()
	revisionPublicID, err := platformid.NewPublicID(now)
	if err != nil {
		return Article{}, err
	}
	return s.repository.UpdateDraft(ctx, id, expectedVersion, revisionInput{
		PublicID:     revisionPublicID,
		Title:        input.Title,
		Slug:         input.Slug,
		Excerpt:      input.Excerpt,
		BodyMarkdown: input.BodyMarkdown,
		Reason:       "save",
	}, now)
}

func (s *Service) Publish(ctx context.Context, id, expectedVersion int64) (Article, error) {
	if id < 1 || expectedVersion < 1 {
		return Article{}, ErrNotFound
	}
	return s.repository.Publish(ctx, id, expectedVersion, s.now())
}

func (s *Service) Article(ctx context.Context, id int64) (Article, error) {
	if id < 1 {
		return Article{}, ErrNotFound
	}
	return s.repository.Article(ctx, id)
}

func (s *Service) PublicArticle(ctx context.Context, slug string) (Article, error) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if !slugPattern.MatchString(slug) {
		return Article{}, ErrNotFound
	}
	return s.repository.PublicArticle(ctx, slug)
}

func (s *Service) Articles(ctx context.Context) ([]Article, error) {
	return s.repository.Articles(ctx)
}

func validateInput(input DraftInput) (DraftInput, error) {
	input.Title = strings.TrimSpace(input.Title)
	input.Slug = strings.ToLower(strings.TrimSpace(input.Slug))
	input.Excerpt = strings.TrimSpace(input.Excerpt)
	if !utf8.ValidString(input.Title) || utf8.RuneCountInString(input.Title) < 1 || utf8.RuneCountInString(input.Title) > 200 {
		return DraftInput{}, ValidationError{Message: "标题需要 1–200 个字符"}
	}
	if !slugPattern.MatchString(input.Slug) || len(input.Slug) > 120 {
		return DraftInput{}, ValidationError{Message: "固定链接需要 1–120 位小写字母、数字或单横线分隔词"}
	}
	if !utf8.ValidString(input.Excerpt) || utf8.RuneCountInString(input.Excerpt) > 500 {
		return DraftInput{}, ValidationError{Message: "摘要不能超过 500 个字符"}
	}
	if !utf8.ValidString(input.BodyMarkdown) || len(input.BodyMarkdown) > 2<<20 {
		return DraftInput{}, ValidationError{Message: "Markdown 正文不能超过 2 MiB"}
	}
	return input, nil
}
