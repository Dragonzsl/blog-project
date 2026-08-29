package publishing

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zhushilin/blog-project/internal/extensions"
	"github.com/zhushilin/blog-project/internal/organization"
	platformid "github.com/zhushilin/blog-project/internal/platform/id"
	"github.com/zhushilin/blog-project/internal/platform/pagination"
	platformslug "github.com/zhushilin/blog-project/internal/platform/slug"
)

type Service struct {
	repository *Repository
	options    Options
	now        func() time.Time
	events     interface {
		Dispatch(context.Context, extensions.Event) []error
	}
}

type Options struct {
	SchedulerBatchSize int
	SnapshotInterval   time.Duration
	RevisionLimit      int
	TrashRetention     time.Duration
}

func DefaultOptions() Options {
	return Options{SchedulerBatchSize: 20, SnapshotInterval: 15 * time.Second, RevisionLimit: 50, TrashRetention: 30 * 24 * time.Hour}
}

func NewService(repository *Repository, configured ...Options) *Service {
	options := DefaultOptions()
	if len(configured) > 0 {
		options = configured[0]
		defaults := DefaultOptions()
		if options.SchedulerBatchSize < 1 {
			options.SchedulerBatchSize = defaults.SchedulerBatchSize
		}
		if options.SnapshotInterval <= 0 {
			options.SnapshotInterval = defaults.SnapshotInterval
		}
		if options.RevisionLimit < 1 {
			options.RevisionLimit = defaults.RevisionLimit
		}
		if options.TrashRetention <= 0 {
			options.TrashRetention = defaults.TrashRetention
		}
	}
	return &Service{repository: repository, options: options, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) SetEventSink(sink interface {
	Dispatch(context.Context, extensions.Event) []error
}) {
	s.events = sink
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
	for attempt := 1; attempt <= 100; attempt++ {
		candidate := input
		if attempt > 1 {
			candidate.Slug, candidate.slugKey, err = platformslug.AppendSuffix(input.Slug, attempt)
			if err != nil {
				return Article{}, err
			}
		}
		article, createErr := s.repository.CreateDraft(ctx, kind, contentPublicID, revisionInput{
			PublicID:       revisionPublicID,
			Title:          candidate.Title,
			Slug:           candidate.Slug,
			SlugKey:        candidate.slugKey,
			Excerpt:        candidate.Excerpt,
			SEOTitle:       candidate.SEOTitle,
			SEODescription: candidate.SEODescription,
			BodyMarkdown:   candidate.BodyMarkdown,
			Reason:         "create",
		}, candidate.CategoryID, candidate.TagIDs, now)
		if createErr == nil || !input.AutoSlug || !errors.Is(createErr, ErrSlugUnavailable) {
			return article, createErr
		}
	}
	return Article{}, ErrSlugUnavailable
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
	for attempt := 1; attempt <= 100; attempt++ {
		candidate := input
		if attempt > 1 {
			candidate.Slug, candidate.slugKey, err = platformslug.AppendSuffix(input.Slug, attempt)
			if err != nil {
				return Article{}, err
			}
		}
		article, updateErr := s.repository.UpdateDraft(ctx, kind, id, expectedVersion, revisionInput{
			PublicID:       revisionPublicID,
			Title:          candidate.Title,
			Slug:           candidate.Slug,
			SlugKey:        candidate.slugKey,
			Excerpt:        candidate.Excerpt,
			SEOTitle:       candidate.SEOTitle,
			SEODescription: candidate.SEODescription,
			BodyMarkdown:   candidate.BodyMarkdown,
			Reason:         "save",
		}, candidate.CategoryID, candidate.TagIDs, now, s.options.RevisionLimit)
		if updateErr == nil || !input.AutoSlug || !errors.Is(updateErr, ErrSlugUnavailable) {
			return article, updateErr
		}
	}
	return Article{}, ErrSlugUnavailable
}

func (s *Service) Publish(ctx context.Context, id, expectedVersion int64) (Article, error) {
	if id < 1 || expectedVersion < 1 {
		return Article{}, ErrNotFound
	}
	article, err := s.repository.Publish(ctx, "article", id, expectedVersion, s.now())
	if err == nil && s.events != nil {
		_ = s.events.Dispatch(ctx, extensions.Event{Name: "ContentPublished.v1", Version: 1, ObjectID: append([]byte(nil), article.PublicID...), Payload: map[string]any{"kind": "article", "slug": article.Slug}})
	}
	return article, err
}

func (s *Service) PublishPage(ctx context.Context, id, expectedVersion int64) (Article, error) {
	if id < 1 || expectedVersion < 1 {
		return Article{}, ErrNotFound
	}
	page, err := s.repository.Publish(ctx, "page", id, expectedVersion, s.now())
	if err == nil && s.events != nil {
		_ = s.events.Dispatch(ctx, extensions.Event{Name: "ContentPublished.v1", Version: 1, ObjectID: append([]byte(nil), page.PublicID...), Payload: map[string]any{"kind": "page", "slug": page.Slug}})
	}
	return page, err
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

func (s *Service) TrashedContents(ctx context.Context) ([]Article, error) {
	return s.repository.TrashedContents(ctx)
}

// AdminContentsPage returns a bounded slice for the authenticated content
// workspace. Keeping the count and page query in the repository avoids
// loading the entire editorial collection just to render a table.
func (s *Service) AdminContentsPage(ctx context.Context, kind string, filter AdminContentFilter, request pagination.Request) (AdminContentPage, error) {
	if !validKind(kind) {
		return AdminContentPage{}, ErrNotFound
	}
	filter.Query = strings.TrimSpace(filter.Query)
	filter.Status = normalizeAdminContentStatus(filter.Status)
	filter.Category = strings.TrimSpace(filter.Category)
	filter.Tag = strings.TrimSpace(filter.Tag)
	filter.Sort = normalizeAdminContentSort(filter.Sort)
	request = pagination.Normalize(request, 20, 50)
	total, err := s.repository.CountAdminContents(ctx, kind, filter)
	if err != nil {
		return AdminContentPage{}, err
	}
	info := pagination.NewInfo(total, request)
	contents, err := s.repository.AdminContents(ctx, kind, filter, info)
	if err != nil {
		return AdminContentPage{}, err
	}
	return AdminContentPage{Contents: contents, Pagination: info}, nil
}

// Dashboard summarizes the current editorial state and keeps the recent
// lists intentionally small. It uses the service clock so the result remains
// deterministic in tests and during scheduled lifecycle work.
func (s *Service) Dashboard(ctx context.Context) (DashboardSummary, error) {
	articles, err := s.Articles(ctx)
	if err != nil {
		return DashboardSummary{}, err
	}
	pages, err := s.Pages(ctx)
	if err != nil {
		return DashboardSummary{}, err
	}
	trashed, err := s.TrashedContents(ctx)
	if err != nil {
		return DashboardSummary{}, err
	}
	all := append(append(make([]Article, 0, len(articles)+len(pages)), articles...), pages...)
	summary := DashboardSummary{TrashedCount: len(trashed)}
	cutoff := s.now().UTC().Add(-7 * 24 * time.Hour)
	for _, content := range all {
		switch content.Status {
		case "draft":
			summary.DraftCount++
		case "scheduled":
			summary.ScheduledCount++
		case "published":
			summary.PublishedCount++
		}
		if strings.TrimSpace(content.Excerpt) == "" {
			summary.WithoutExcerptCount++
		}
		if content.PublishedAt != nil && !content.PublishedAt.Before(cutoff) {
			summary.PublishedThisWeek++
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].UpdatedAt.Equal(all[j].UpdatedAt) {
			return all[i].ID > all[j].ID
		}
		return all[i].UpdatedAt.After(all[j].UpdatedAt)
	})
	for _, content := range all[:minDashboardItems(len(all))] {
		summary.RecentEdits = append(summary.RecentEdits, summarizeAdminContent(content))
	}
	published := make([]Article, 0, len(all))
	for _, content := range all {
		if content.PublishedAt != nil && content.Status == "published" {
			published = append(published, content)
		}
	}
	sort.SliceStable(published, func(i, j int) bool {
		if published[i].PublishedAt.Equal(*published[j].PublishedAt) {
			return published[i].ID > published[j].ID
		}
		return published[i].PublishedAt.After(*published[j].PublishedAt)
	})
	for _, content := range published[:minDashboardItems(len(published))] {
		summary.RecentPublished = append(summary.RecentPublished, summarizeAdminContent(content))
	}
	return summary, nil
}

func summarizeAdminContent(content Article) AdminContentSummary {
	return AdminContentSummary{ID: content.ID, Kind: content.Kind, Status: content.Status, Slug: content.Slug, Title: content.Title, UpdatedAt: content.UpdatedAt, PublishedAt: content.PublishedAt}
}

func minDashboardItems(length int) int {
	if length < 5 {
		return length
	}
	return 5
}

func normalizeAdminContentStatus(status string) string {
	switch status {
	case "draft", "scheduled", "published":
		return status
	default:
		return "all"
	}
}

func normalizeAdminContentSort(sortBy string) string {
	switch sortBy {
	case "published", "title":
		return sortBy
	default:
		return "updated"
	}
}

func (s *Service) Revisions(ctx context.Context, kind string, contentID int64) ([]Revision, error) {
	if !validKind(kind) || contentID < 1 {
		return nil, ErrNotFound
	}
	return s.repository.Revisions(ctx, kind, contentID)
}

func (s *Service) Revision(ctx context.Context, kind string, contentID, revisionID int64) (Revision, error) {
	if !validKind(kind) || contentID < 1 || revisionID < 1 {
		return Revision{}, ErrNotFound
	}
	return s.repository.Revision(ctx, kind, contentID, revisionID)
}

func (s *Service) RestoreRevision(ctx context.Context, kind string, contentID, revisionID, expectedVersion int64) (Article, error) {
	if !validKind(kind) || contentID < 1 || revisionID < 1 || expectedVersion < 1 {
		return Article{}, ErrNotFound
	}
	revision, err := s.repository.Revision(ctx, kind, contentID, revisionID)
	if err != nil {
		return Article{}, err
	}
	current, err := s.repository.Content(ctx, kind, contentID)
	if err != nil {
		return Article{}, err
	}
	taxonomy, err := s.repository.organization.TaxonomyBySnapshot(ctx, revision.CategoryPublicID, revision.TagPublicIDsJSON)
	if err != nil {
		return Article{}, err
	}
	input := DraftInput{Title: revision.Title, Slug: revision.Slug, Excerpt: revision.Excerpt, SEOTitle: revision.SEOTitle, SEODescription: revision.SEODescription, BodyMarkdown: revision.BodyMarkdown}
	if current.PublishedRevisionID > 0 {
		input.Slug = current.Slug
	}
	if taxonomy.Category != nil {
		input.CategoryID = taxonomy.Category.ID
	}
	for _, tag := range taxonomy.Tags {
		input.TagIDs = append(input.TagIDs, tag.ID)
	}
	input, err = validateInput(kind, input)
	if err != nil {
		return Article{}, err
	}
	now := s.now()
	publicID, err := platformid.NewPublicID(now)
	if err != nil {
		return Article{}, err
	}
	return s.repository.UpdateDraft(ctx, kind, contentID, expectedVersion, revisionInput{PublicID: publicID, Title: input.Title, Slug: input.Slug, SlugKey: input.slugKey, Excerpt: input.Excerpt, SEOTitle: input.SEOTitle, SEODescription: input.SEODescription, BodyMarkdown: input.BodyMarkdown, Reason: "restore"}, input.CategoryID, input.TagIDs, now, s.options.RevisionLimit)
}

func (s *Service) SaveEditingSnapshot(ctx context.Context, kind string, snapshot EditingSnapshot) error {
	if !validKind(kind) || snapshot.ContentID < 1 || snapshot.BaseLockVersion < 1 || snapshot.BrowserVersion < 1 {
		return ErrNotFound
	}
	if err := validateSnapshotInput(kind, snapshot.Input); err != nil {
		return err
	}
	return s.repository.SaveEditingSnapshot(ctx, kind, snapshot, s.now())
}

func (s *Service) EditingSnapshot(ctx context.Context, kind string, contentID int64) (EditingSnapshot, error) {
	if !validKind(kind) || contentID < 1 {
		return EditingSnapshot{}, ErrNotFound
	}
	return s.repository.EditingSnapshot(ctx, kind, contentID)
}

func (s *Service) Schedule(ctx context.Context, kind string, id, expectedVersion int64, publishAt time.Time) (Article, error) {
	now := s.now()
	publishAt = publishAt.UTC()
	if !validKind(kind) || id < 1 || expectedVersion < 1 {
		return Article{}, ErrNotFound
	}
	if !publishAt.After(now) || publishAt.After(now.AddDate(10, 0, 0)) {
		return Article{}, ValidationError{Message: "定时发布时间必须在未来十年内"}
	}
	return s.repository.Schedule(ctx, kind, id, expectedVersion, publishAt, now)
}

func (s *Service) Unpublish(ctx context.Context, kind string, id, expectedVersion int64) (Article, error) {
	if !validKind(kind) || id < 1 || expectedVersion < 1 {
		return Article{}, ErrNotFound
	}
	return s.repository.Unpublish(ctx, kind, id, expectedVersion, s.now())
}

func (s *Service) CancelSchedule(ctx context.Context, kind string, id, expectedVersion int64) (Article, error) {
	if !validKind(kind) || id < 1 || expectedVersion < 1 {
		return Article{}, ErrNotFound
	}
	return s.repository.CancelSchedule(ctx, kind, id, expectedVersion, s.now())
}

func (s *Service) Trash(ctx context.Context, kind string, id, expectedVersion int64) error {
	if !validKind(kind) || id < 1 || expectedVersion < 1 {
		return ErrNotFound
	}
	return s.repository.Trash(ctx, kind, id, expectedVersion, s.now())
}

func (s *Service) RestoreFromTrash(ctx context.Context, id int64) (Article, error) {
	if id < 1 {
		return Article{}, ErrNotFound
	}
	return s.repository.RestoreFromTrash(ctx, id, s.now())
}

func (s *Service) ProcessLifecycle(ctx context.Context) (published, purged int, err error) {
	published, err = s.ProcessScheduled(ctx)
	if err != nil {
		return published, 0, err
	}
	purged, err = s.PurgeExpiredTrash(ctx)
	return published, purged, err
}

func (s *Service) ProcessScheduled(ctx context.Context) (int, error) {
	return s.repository.PublishDue(ctx, s.now(), s.options.SchedulerBatchSize)
}

func (s *Service) PurgeExpiredTrash(ctx context.Context) (int, error) {
	now := s.now()
	return s.repository.PurgeExpiredTrash(ctx, now.Add(-s.options.TrashRetention), now, s.options.SchedulerBatchSize)
}

func (s *Service) SnapshotInterval() time.Duration { return s.options.SnapshotInterval }

func validKind(kind string) bool { return kind == "article" || kind == "page" }

func validateSnapshotInput(kind string, input DraftInput) error {
	if !utf8.ValidString(input.Title) || utf8.RuneCountInString(input.Title) > 200 {
		return ValidationError{Message: "编辑快照标题不能超过 200 个字符"}
	}
	if !utf8.ValidString(input.Slug) || utf8.RuneCountInString(input.Slug) > 120 {
		return ValidationError{Message: "编辑快照固定链接不能超过 120 个字符"}
	}
	if !utf8.ValidString(input.Excerpt) || utf8.RuneCountInString(input.Excerpt) > 500 {
		return ValidationError{Message: "编辑快照摘要不能超过 500 个字符"}
	}
	if !utf8.ValidString(input.SEOTitle) || utf8.RuneCountInString(input.SEOTitle) > 200 {
		return ValidationError{Message: "编辑快照 SEO 标题不能超过 200 个字符"}
	}
	if !utf8.ValidString(input.SEODescription) || utf8.RuneCountInString(input.SEODescription) > 500 {
		return ValidationError{Message: "编辑快照 SEO 描述不能超过 500 个字符"}
	}
	if !utf8.ValidString(input.BodyMarkdown) || len(input.BodyMarkdown) > 2<<20 {
		return ValidationError{Message: "编辑快照正文不能超过 2 MiB"}
	}
	if len(input.TagIDs) > 30 {
		return ValidationError{Message: "编辑快照最多包含 30 个标签"}
	}
	if input.CategoryID < 0 {
		return ValidationError{Message: "编辑快照分类无效"}
	}
	for _, tagID := range input.TagIDs {
		if tagID < 1 {
			return ValidationError{Message: "编辑快照标签无效"}
		}
	}
	if kind == "page" && (input.CategoryID > 0 || len(input.TagIDs) > 0) {
		return ValidationError{Message: "页面编辑快照不能包含分类或标签"}
	}
	return nil
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

func (s *Service) PublicArticlesPage(ctx context.Context, request pagination.Request) (PublicArticlePage, error) {
	request = pagination.Normalize(request, 20, 50)
	return s.repository.PublicArticlesPage(ctx, request)
}

func (s *Service) PublicArticleNavigation(ctx context.Context, articleID int64, relatedLimit int) (PublicArticleNavigation, error) {
	if articleID < 1 {
		return PublicArticleNavigation{}, ErrNotFound
	}
	if relatedLimit < 1 {
		relatedLimit = 3
	}
	if relatedLimit > 10 {
		relatedLimit = 10
	}
	return s.repository.PublicArticleNavigation(ctx, articleID, relatedLimit)
}

// PublishedPages returns only the currently published page revisions. It is
// bounded for the same reason as PublishedArticles: public consumers should
// never be able to request an unbounded database export.
func (s *Service) PublishedPages(ctx context.Context, limit int) ([]Article, error) {
	if limit < 1 {
		limit = 1
	}
	if limit > 100 {
		limit = 100
	}
	return s.repository.PublishedPages(ctx, limit)
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
	input.SEOTitle = strings.TrimSpace(input.SEOTitle)
	input.SEODescription = strings.TrimSpace(input.SEODescription)
	if !utf8.ValidString(input.Title) || utf8.RuneCountInString(input.Title) < 1 || utf8.RuneCountInString(input.Title) > 200 {
		return DraftInput{}, ValidationError{Message: "标题需要 1–200 个字符"}
	}
	if input.AutoSlug || strings.TrimSpace(input.Slug) == "" {
		fallback := "article"
		if kind == "page" {
			fallback = "page"
		}
		generated, _, err := platformslug.FromTitle(input.Title, fallback)
		if err != nil {
			return DraftInput{}, ValidationError{Message: "无法根据标题生成有效固定链接"}
		}
		input.Slug = generated
		input.AutoSlug = true
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
	if !utf8.ValidString(input.SEOTitle) || utf8.RuneCountInString(input.SEOTitle) > 200 {
		return DraftInput{}, ValidationError{Message: "SEO 标题不能超过 200 个字符"}
	}
	if !utf8.ValidString(input.SEODescription) || utf8.RuneCountInString(input.SEODescription) > 500 {
		return DraftInput{}, ValidationError{Message: "SEO 描述不能超过 500 个字符"}
	}
	if !utf8.ValidString(input.BodyMarkdown) || len(input.BodyMarkdown) > 2<<20 {
		return DraftInput{}, ValidationError{Message: "Markdown 正文不能超过 2 MiB"}
	}
	if kind == "page" && (input.CategoryID != 0 || len(input.TagIDs) != 0) {
		return DraftInput{}, ValidationError{Message: "页面不能使用文章分类或标签"}
	}
	return input, nil
}
