package organization

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/zhushilin/blog-project/internal/platform/database"
	platformid "github.com/zhushilin/blog-project/internal/platform/id"
	"github.com/zhushilin/blog-project/internal/platform/pagination"
	platformslug "github.com/zhushilin/blog-project/internal/platform/slug"
)

type Service struct {
	repository     *Repository
	database       *database.DB
	now            func() time.Time
	cacheMu        sync.RWMutex
	cacheEpoch     int64
	categories     []PublicCategorySummary
	tags           []PublicTagSummary
	taxonomyReady  bool
	taxonomyFlight *publicTaxonomyFlight
	navigation     map[string][]NavigationItem
}

type publicTaxonomyFlight struct {
	epoch      int64
	done       chan struct{}
	categories []PublicCategorySummary
	tags       []PublicTagSummary
	err        error
}

func NewService(db *database.DB) *Service {
	return &Service{repository: NewRepository(db), database: db, now: func() time.Time { return time.Now().UTC() }, navigation: make(map[string][]NavigationItem)}
}

func (s *Service) Categories(ctx context.Context) ([]Category, error) {
	return s.repository.Categories(ctx)
}
func (s *Service) Tags(ctx context.Context) ([]Tag, error) { return s.repository.Tags(ctx) }

func (s *Service) PublicCategories(ctx context.Context) ([]PublicCategorySummary, error) {
	categories, _, err := s.publicTaxonomySnapshot(ctx)
	return categories, err
}

func (s *Service) PublicTags(ctx context.Context) ([]PublicTagSummary, error) {
	_, tags, err := s.publicTaxonomySnapshot(ctx)
	return tags, err
}

// publicTaxonomySnapshot loads both public term lists together. The source
// repository already provides bounded queries; this shared, epoch-scoped
// flight prevents the first home/search/directory requests after an epoch
// change from repeating the same category and tag scans.
func (s *Service) publicTaxonomySnapshot(ctx context.Context) ([]PublicCategorySummary, []PublicTagSummary, error) {
	for {
		epoch := s.database.RenderEpoch()
		s.cacheMu.Lock()
		if s.cacheEpoch == epoch && s.taxonomyReady {
			categories := clonePublicCategories(s.categories)
			tags := clonePublicTags(s.tags)
			s.cacheMu.Unlock()
			return categories, tags, nil
		}
		if flight := s.taxonomyFlight; flight != nil {
			done := flight.done
			matchingEpoch := flight.epoch == epoch
			s.cacheMu.Unlock()
			select {
			case <-done:
				if !matchingEpoch {
					continue
				}
				if flight.err != nil {
					return nil, nil, flight.err
				}
				return clonePublicCategories(flight.categories), clonePublicTags(flight.tags), nil
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
		}
		flight := &publicTaxonomyFlight{epoch: epoch, done: make(chan struct{})}
		s.taxonomyFlight = flight
		s.cacheMu.Unlock()

		loadContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		categories, loadErr := s.repository.PublicCategories(loadContext)
		var tags []PublicTagSummary
		if loadErr == nil {
			tags, loadErr = s.repository.PublicTags(loadContext)
		}
		cancel()
		s.cacheMu.Lock()
		if loadErr == nil {
			categoryCacheable := publicCategoryCacheSize(categories) <= publicTaxonomyCacheMaxBytes
			tagCacheable := publicTagCacheSize(tags) <= publicTaxonomyCacheMaxBytes
			if epoch == s.database.RenderEpoch() && categoryCacheable && tagCacheable {
				if s.cacheEpoch != epoch {
					s.categories, s.tags = nil, nil
					s.navigation = make(map[string][]NavigationItem)
					s.cacheEpoch = epoch
				}
				s.categories = clonePublicCategories(categories)
				s.tags = clonePublicTags(tags)
				s.taxonomyReady = true
			}
		}
		flight.categories = clonePublicCategories(categories)
		flight.tags = clonePublicTags(tags)
		flight.err = loadErr
		if s.taxonomyFlight == flight {
			s.taxonomyFlight = nil
		}
		close(flight.done)
		s.cacheMu.Unlock()
		return categories, tags, loadErr
	}
}

const publicTaxonomyCacheMaxBytes = 512 << 10

func publicCategoryCacheSize(values []PublicCategorySummary) int {
	size := 0
	for _, value := range values {
		size += len(value.Category.PublicID) + len(value.Category.Slug) + len(value.Category.Name) + len(value.Category.Description) + len(value.LatestTitle) + len(value.LatestPath) + 128
	}
	return size
}

func publicTagCacheSize(values []PublicTagSummary) int {
	size := 0
	for _, value := range values {
		size += len(value.Tag.PublicID) + len(value.Tag.Slug) + len(value.Tag.Name) + len(value.Tag.Description) + len(value.LatestTitle) + len(value.LatestPath) + 128
	}
	return size
}

func clonePublicCategories(values []PublicCategorySummary) []PublicCategorySummary {
	result := make([]PublicCategorySummary, len(values))
	copy(result, values)
	for index := range result {
		result[index].Category.PublicID = append([]byte(nil), values[index].Category.PublicID...)
	}
	return result
}

func clonePublicTags(values []PublicTagSummary) []PublicTagSummary {
	result := make([]PublicTagSummary, len(values))
	copy(result, values)
	for index := range result {
		result[index].Tag.PublicID = append([]byte(nil), values[index].Tag.PublicID...)
	}
	return result
}

func (s *Service) PublicTaxonomyRebuildState(ctx context.Context) (PublicTaxonomyRebuildState, error) {
	return s.repository.PublicTaxonomyRebuildState(ctx)
}

func (s *Service) RebuildPublicTaxonomy(ctx context.Context, batchSize int) (int, bool, error) {
	return s.repository.RebuildPublicTaxonomy(ctx, batchSize)
}

func (s *Service) ReplacePublishedTaxonomyTx(ctx context.Context, tx *sql.Tx, contentID, revisionID int64, kind string, categoryPublicID []byte, tagPublicIDsJSON, title, slug string, publishedAt time.Time) error {
	return s.repository.ReplacePublishedTaxonomyTx(ctx, tx, contentID, revisionID, kind, categoryPublicID, tagPublicIDsJSON, title, slug, publishedAt)
}

func (s *Service) ClearPublishedTaxonomyTx(ctx context.Context, tx *sql.Tx, contentID int64) error {
	return s.repository.ClearPublishedTaxonomyTx(ctx, tx, contentID)
}

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

func (s *Service) Redirects(ctx context.Context, limit int) ([]Redirect, error) {
	return s.repository.Redirects(ctx, limit)
}

func (s *Service) CreateRedirect(ctx context.Context, input RedirectInput) error {
	cleanSource, sourceKey, err := normalizeRedirectPath(input.SourcePath)
	if err != nil {
		return err
	}
	cleanTarget, targetKey, err := normalizeRedirectPath(input.TargetPath)
	if err != nil {
		return err
	}
	if sourceKey == targetKey {
		return ValidationError{Message: "重定向源地址与目标地址不能相同"}
	}
	if input.StatusCode != 301 && input.StatusCode != 308 {
		return ValidationError{Message: "状态码只能是 301 或 308"}
	}
	if sourceKey == "/admin" || strings.HasPrefix(sourceKey, "/admin/") || sourceKey == "/assets" || strings.HasPrefix(sourceKey, "/assets/") {
		return ValidationError{Message: "系统路径不能创建手动重定向"}
	}
	input.SourcePath, input.TargetPath = cleanSource, cleanTarget
	// Follow the existing target chain with a small bound to reject direct and
	// indirect cycles before the new row is written.
	current := targetKey
	for depth := 0; depth < 32; depth++ {
		if current == sourceKey {
			return ValidationError{Message: "重定向会形成循环"}
		}
		var next string
		err := s.repository.database.Reader.QueryRowContext(ctx, "SELECT target_path_key FROM redirects WHERE source_path_key=?", current).Scan(&next)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return err
		}
		current = next
	}
	return s.repository.CreateRedirect(ctx, input, s.now())
}

func (s *Service) DeleteRedirect(ctx context.Context, id int64) error {
	if id < 1 {
		return ErrNotFound
	}
	return s.repository.DeleteRedirect(ctx, id, s.now())
}

func normalizeRedirectPath(value string) (string, string, error) {
	value = strings.TrimSpace(value)
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Path == "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Scheme != "" || parsed.Host != "" || !strings.HasPrefix(parsed.Path, "/") {
		return "", "", ValidationError{Message: "重定向地址必须是站内绝对路径"}
	}
	clean := path.Clean(parsed.Path)
	if clean == "." || strings.Contains(clean, "\x00") || strings.HasPrefix(clean, "/../") || clean == "/.." || len(clean) > 512 {
		return "", "", ValidationError{Message: "重定向地址无效"}
	}
	return clean, strings.ToLower(clean), nil
}

func (s *Service) PublicNavigation(ctx context.Context, location string) ([]NavigationItem, error) {
	if location != "primary" && location != "footer" {
		return nil, ErrNotFound
	}
	epoch := s.database.RenderEpoch()
	s.cacheMu.RLock()
	if s.cacheEpoch == epoch {
		if value, ok := s.navigation[location]; ok {
			result := append([]NavigationItem(nil), value...)
			s.cacheMu.RUnlock()
			return result, nil
		}
	}
	s.cacheMu.RUnlock()
	result, err := s.repository.NavigationItems(ctx, location, true)
	if err != nil {
		return nil, err
	}
	s.cacheMu.Lock()
	if epoch == s.database.RenderEpoch() {
		if s.cacheEpoch != epoch {
			s.categories, s.tags = nil, nil
			s.taxonomyReady = false
			s.navigation = make(map[string][]NavigationItem)
			s.cacheEpoch = epoch
		}
		s.navigation[location] = append([]NavigationItem(nil), result...)
	}
	s.cacheMu.Unlock()
	return result, nil
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

// TaxonomiesByContentIDs resolves the current editorial taxonomy for a
// bounded page with two fixed-shape queries. The result map contains an empty
// taxonomy for an article with no terms, so callers can safely enrich a page
// without probing every row independently.
func (s *Service) TaxonomiesByContentIDs(ctx context.Context, contentIDs []int64) (map[int64]Taxonomy, error) {
	return s.repository.TaxonomiesByContentIDs(ctx, contentIDs)
}

// TaxonomiesBySnapshot resolves published taxonomy snapshots in bounded
// batches. Snapshot identities remain authoritative for public rendering,
// while missing historical terms degrade to an empty term rather than a
// draft/current taxonomy.
func (s *Service) TaxonomiesBySnapshot(ctx context.Context, snapshots []TaxonomySnapshot) (map[int64]Taxonomy, error) {
	return s.repository.TaxonomiesBySnapshot(ctx, snapshots)
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
	page, err := s.PublicCategoryPage(ctx, slug, pagination.Request{PerPage: boundedLimit(limit)})
	if err != nil {
		return Category{}, nil, err
	}
	return page.Category, page.ArticleIDs, nil
}

func (s *Service) PublicCategoryPage(ctx context.Context, slug string, request pagination.Request) (PublicCategoryPage, error) {
	_, key, err := platformslug.Normalize(slug)
	if err != nil {
		return PublicCategoryPage{}, ErrNotFound
	}
	request = pagination.Normalize(request, 20, 50)
	return s.repository.PublicCategoryPage(ctx, key, request)
}

func (s *Service) PublicTag(ctx context.Context, slug string, limit int) (Tag, []int64, error) {
	page, err := s.PublicTagPage(ctx, slug, pagination.Request{PerPage: boundedLimit(limit)})
	if err != nil {
		return Tag{}, nil, err
	}
	return page.Tag, page.ArticleIDs, nil
}

func (s *Service) PublicTagPage(ctx context.Context, slug string, request pagination.Request) (PublicTagPage, error) {
	_, key, err := platformslug.Normalize(slug)
	if err != nil {
		return PublicTagPage{}, ErrNotFound
	}
	request = pagination.Normalize(request, 20, 50)
	return s.repository.PublicTagPage(ctx, key, request)
}

func (s *Service) PublicRelatedArticleIDs(ctx context.Context, currentID int64, categoryPublicID []byte, tagPublicIDs [][]byte, limit int) ([]int64, bool, error) {
	return s.repository.PublicRelatedArticleIDs(ctx, currentID, categoryPublicID, tagPublicIDs, limit)
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
