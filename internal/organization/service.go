package organization

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zhushilin/blog-project/internal/platform/database"
	platformid "github.com/zhushilin/blog-project/internal/platform/id"
	platformslug "github.com/zhushilin/blog-project/internal/platform/slug"
)

type Service struct {
	repository *Repository
	now        func() time.Time
}

func NewService(db *database.DB) *Service {
	return &Service{repository: NewRepository(db), now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) Categories(ctx context.Context) ([]Category, error) {
	return s.repository.Categories(ctx)
}
func (s *Service) Tags(ctx context.Context) ([]Tag, error) { return s.repository.Tags(ctx) }

func (s *Service) CreateCategory(ctx context.Context, input TermInput) (Category, error) {
	input, slugKey, err := validateTerm(input, true)
	if err != nil {
		return Category{}, err
	}
	publicID, err := platformid.NewPublicID(s.now())
	if err != nil {
		return Category{}, err
	}
	return s.repository.CreateCategory(ctx, publicID, input, slugKey, s.now())
}

func (s *Service) UpdateCategory(ctx context.Context, id int64, input TermInput) (Category, error) {
	input, slugKey, err := validateTerm(input, true)
	if err != nil {
		return Category{}, err
	}
	return s.repository.UpdateCategory(ctx, id, input, slugKey, s.now())
}

func (s *Service) DeleteCategory(ctx context.Context, id int64) error {
	if id < 1 {
		return ErrNotFound
	}
	return s.repository.DeleteCategory(ctx, id, s.now())
}

func (s *Service) CreateTag(ctx context.Context, input TermInput) (Tag, error) {
	input, slugKey, err := validateTerm(input, false)
	if err != nil {
		return Tag{}, err
	}
	publicID, err := platformid.NewPublicID(s.now())
	if err != nil {
		return Tag{}, err
	}
	return s.repository.CreateTag(ctx, publicID, input, slugKey, s.now())
}

func (s *Service) UpdateTag(ctx context.Context, id int64, input TermInput) (Tag, error) {
	input, slugKey, err := validateTerm(input, false)
	if err != nil {
		return Tag{}, err
	}
	return s.repository.UpdateTag(ctx, id, input, slugKey, s.now())
}

func (s *Service) DeleteTag(ctx context.Context, id int64) error {
	if id < 1 {
		return ErrNotFound
	}
	return s.repository.DeleteTag(ctx, id, s.now())
}

func (s *Service) NavigationItems(ctx context.Context) ([]NavigationItem, error) {
	return s.repository.NavigationItems(ctx, "", false)
}

func (s *Service) PublicNavigation(ctx context.Context, location string) ([]NavigationItem, error) {
	if location != "primary" && location != "footer" {
		return nil, ErrNotFound
	}
	return s.repository.NavigationItems(ctx, location, true)
}

func (s *Service) CreateNavigationItem(ctx context.Context, input NavigationInput) (NavigationItem, error) {
	input, err := validateNavigation(input)
	if err != nil {
		return NavigationItem{}, err
	}
	return s.repository.CreateNavigationItem(ctx, input, s.now())
}

func (s *Service) UpdateNavigationItem(ctx context.Context, id int64, input NavigationInput) (NavigationItem, error) {
	input, err := validateNavigation(input)
	if err != nil {
		return NavigationItem{}, err
	}
	return s.repository.UpdateNavigationItem(ctx, id, input, s.now())
}

func (s *Service) DeleteNavigationItem(ctx context.Context, id int64) error {
	if id < 1 {
		return ErrNotFound
	}
	return s.repository.DeleteNavigationItem(ctx, id, s.now())
}

func (s *Service) ArticleTaxonomy(ctx context.Context, contentID int64) (Taxonomy, error) {
	return s.repository.ArticleTaxonomy(ctx, contentID)
}

func (s *Service) TaxonomyBySnapshot(ctx context.Context, categoryPublicID []byte, tagPublicIDsJSON string) (Taxonomy, error) {
	return s.repository.TaxonomyBySnapshot(ctx, categoryPublicID, tagPublicIDsJSON)
}

func (s *Service) ReplaceArticleTaxonomyTx(ctx context.Context, tx *sql.Tx, contentID, categoryID int64, tagIDs []int64, now time.Time) ([]byte, string, error) {
	if len(tagIDs) > 30 {
		return nil, "", ValidationError{Message: "一篇文章最多选择 30 个标签"}
	}
	unique := make(map[int64]struct{}, len(tagIDs))
	clean := make([]int64, 0, len(tagIDs))
	for _, id := range tagIDs {
		if id < 1 {
			return nil, "", ValidationError{Message: "标签选择无效"}
		}
		if _, exists := unique[id]; !exists {
			unique[id] = struct{}{}
			clean = append(clean, id)
		}
	}
	sort.Slice(clean, func(i, j int) bool { return clean[i] < clean[j] })
	return s.repository.ReplaceArticleTaxonomyTx(ctx, tx, contentID, categoryID, clean, now)
}

func (s *Service) PublicCategory(ctx context.Context, slug string, limit int) (Category, []int64, error) {
	_, key, err := platformslug.Normalize(slug)
	if err != nil {
		return Category{}, nil, ErrNotFound
	}
	return s.repository.PublicCategory(ctx, key, boundedLimit(limit))
}

func (s *Service) PublicTag(ctx context.Context, slug string, limit int) (Tag, []int64, error) {
	_, key, err := platformslug.Normalize(slug)
	if err != nil {
		return Tag{}, nil, ErrNotFound
	}
	return s.repository.PublicTag(ctx, key, boundedLimit(limit))
}

func validateTerm(input TermInput, category bool) (TermInput, string, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	if !utf8.ValidString(input.Name) || utf8.RuneCountInString(input.Name) < 1 || utf8.RuneCountInString(input.Name) > 100 {
		return TermInput{}, "", ValidationError{Message: "名称需要 1–100 个字符"}
	}
	if !utf8.ValidString(input.Description) || utf8.RuneCountInString(input.Description) > 500 {
		return TermInput{}, "", ValidationError{Message: "描述不能超过 500 个字符"}
	}
	display, key, err := platformslug.Normalize(input.Slug)
	if err != nil {
		return TermInput{}, "", ValidationError{Message: "Slug 只能包含字母、数字和单横线"}
	}
	input.Slug = display
	if !category {
		input.SortOrder = 0
	}
	return input, key, nil
}

func validateNavigation(input NavigationInput) (NavigationInput, error) {
	input.Label = strings.TrimSpace(input.Label)
	input.ExternalURL = strings.TrimSpace(input.ExternalURL)
	if input.Location != "primary" && input.Location != "footer" {
		return NavigationInput{}, ValidationError{Message: "导航位置无效"}
	}
	if utf8.RuneCountInString(input.Label) < 1 || utf8.RuneCountInString(input.Label) > 100 {
		return NavigationInput{}, ValidationError{Message: "导航文字需要 1–100 个字符"}
	}
	switch input.TargetKind {
	case "content", "category", "tag":
		if input.TargetID < 1 || input.ExternalURL != "" {
			return NavigationInput{}, ErrInvalidTarget
		}
	case "external":
		parsed, err := url.ParseRequestURI(input.ExternalURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return NavigationInput{}, ValidationError{Message: "外部链接必须是完整的 HTTP 或 HTTPS 地址"}
		}
		input.TargetID = 0
	default:
		return NavigationInput{}, ErrInvalidTarget
	}
	return input, nil
}

func boundedLimit(limit int) int {
	if limit < 1 {
		return 1
	}
	if limit > 100 {
		return 100
	}
	return limit
}

func IsUserFacing(err error) bool {
	var validation ValidationError
	return errors.As(err, &validation) || errors.Is(err, ErrSlugUnavailable) || errors.Is(err, ErrInvalidTarget)
}
