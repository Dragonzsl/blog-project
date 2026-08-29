package publishing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mattn/go-sqlite3"
	"github.com/zhushilin/blog-project/internal/media"
	"github.com/zhushilin/blog-project/internal/organization"
	"github.com/zhushilin/blog-project/internal/platform/database"
	"github.com/zhushilin/blog-project/internal/platform/pagination"
)

type Repository struct {
	database        *database.DB
	organization    *organization.Service
	mediaReferences *media.ReferenceRepository
}

func NewRepository(db *database.DB) *Repository {
	return &Repository{database: db, organization: organization.NewService(db), mediaReferences: media.NewReferenceRepository()}
}

func (r *Repository) CreateDraft(ctx context.Context, kind string, publicID []byte, revision revisionInput, categoryID int64, tagIDs []int64, now time.Time) (Article, error) {
	tx, err := r.database.Writer.BeginTx(ctx, nil)
	if err != nil {
		return Article{}, fmt.Errorf("begin %s creation: %w", kind, err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		INSERT INTO contents (
			public_id, kind, status, slug, slug_key, title, excerpt, seo_title, seo_description, body_markdown,
			lock_version, created_at, updated_at
		) VALUES (?, ?, 'draft', ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)
	`, publicID, kind, revision.Slug, revision.SlugKey, revision.Title, revision.Excerpt,
		revision.SEOTitle, revision.SEODescription, revision.BodyMarkdown, millis(now), millis(now))
	if err != nil {
		return Article{}, mapWriteError(err)
	}
	contentID, err := result.LastInsertId()
	if err != nil {
		return Article{}, fmt.Errorf("read %s ID: %w", kind, err)
	}
	if kind == "article" {
		revision.CategoryPublicID, revision.TagPublicIDsJSON, err = r.organization.ReplaceArticleTaxonomyTx(ctx, tx, contentID, categoryID, tagIDs, now)
		if err != nil {
			return Article{}, err
		}
	} else {
		revision.TagPublicIDsJSON = "[]"
	}
	if err := r.replaceMediaReferences(ctx, tx, contentID, revision.BodyMarkdown, now); err != nil {
		return Article{}, err
	}
	revisionID, err := insertRevision(ctx, tx, contentID, 1, revision, now)
	if err != nil {
		return Article{}, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE contents SET current_revision_id = ? WHERE id = ?", revisionID, contentID); err != nil {
		return Article{}, fmt.Errorf("attach current revision: %w", err)
	}
	if err := reservePath(ctx, tx, contentID, kind, revision.Slug, revision.SlugKey, "draft", now); err != nil {
		return Article{}, err
	}
	if err := insertAudit(ctx, tx, "publishing."+kind+".created", kind, publicID, now); err != nil {
		return Article{}, err
	}
	if err := tx.Commit(); err != nil {
		return Article{}, fmt.Errorf("commit %s creation: %w", kind, err)
	}
	return r.Content(ctx, kind, contentID)
}

func (r *Repository) UpdateDraft(ctx context.Context, kind string, id, expectedVersion int64, revision revisionInput, categoryID int64, tagIDs []int64, now time.Time, configuredLimit ...int) (Article, error) {
	tx, err := r.database.Writer.BeginTx(ctx, nil)
	if err != nil {
		return Article{}, fmt.Errorf("begin %s update: %w", kind, err)
	}
	defer tx.Rollback()
	var currentSlugKey string
	var currentVersion, nextRevision int64
	var publicID []byte
	err = tx.QueryRowContext(ctx, `
		SELECT slug_key, lock_version, public_id,
		       (SELECT COALESCE(MAX(revision_number), 0) + 1 FROM content_revisions WHERE content_id = contents.id)
		FROM contents WHERE id = ? AND kind = ? AND trashed_at IS NULL
	`, id, kind).Scan(&currentSlugKey, &currentVersion, &publicID, &nextRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return Article{}, ErrNotFound
	}
	if err != nil {
		return Article{}, fmt.Errorf("read %s for update: %w", kind, err)
	}
	if currentVersion != expectedVersion {
		return Article{}, ErrConflict
	}
	if revision.SlugKey != currentSlugKey {
		if err := reservePath(ctx, tx, id, kind, revision.Slug, revision.SlugKey, "draft", now); err != nil {
			return Article{}, err
		}
	}
	if kind == "article" {
		revision.CategoryPublicID, revision.TagPublicIDsJSON, err = r.organization.ReplaceArticleTaxonomyTx(ctx, tx, id, categoryID, tagIDs, now)
		if err != nil {
			return Article{}, err
		}
	} else {
		revision.TagPublicIDsJSON = "[]"
	}
	if err := r.replaceMediaReferences(ctx, tx, id, revision.BodyMarkdown, now); err != nil {
		return Article{}, err
	}
	revisionID, err := insertRevision(ctx, tx, id, nextRevision, revision, now)
	if err != nil {
		return Article{}, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE contents
		SET slug = ?, slug_key = ?, title = ?, excerpt = ?, seo_title = ?, seo_description = ?, body_markdown = ?,
		    current_revision_id = ?, lock_version = lock_version + 1, updated_at = ?
		WHERE id = ? AND kind = ? AND lock_version = ?
	`, revision.Slug, revision.SlugKey, revision.Title, revision.Excerpt, revision.SEOTitle,
		revision.SEODescription, revision.BodyMarkdown, revisionID, millis(now), id, kind, expectedVersion)
	if err != nil {
		return Article{}, mapWriteError(err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		if err != nil {
			return Article{}, fmt.Errorf("check %s update: %w", kind, err)
		}
		return Article{}, ErrConflict
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM editing_snapshots WHERE content_id=?", id); err != nil {
		return Article{}, err
	}
	limit := 50
	if len(configuredLimit) > 0 {
		limit = configuredLimit[0]
	}
	if err := pruneRevisions(ctx, tx, id, limit); err != nil {
		return Article{}, fmt.Errorf("prune content revisions: %w", err)
	}
	if err := insertAudit(ctx, tx, "publishing."+kind+".saved", kind, publicID, now); err != nil {
		return Article{}, err
	}
	if err := tx.Commit(); err != nil {
		return Article{}, fmt.Errorf("commit %s update: %w", kind, err)
	}
	return r.Content(ctx, kind, id)
}

func (r *Repository) Publish(ctx context.Context, kind string, id, expectedVersion int64, now time.Time) (Article, error) {
	tx, err := r.database.Writer.BeginTx(ctx, nil)
	if err != nil {
		return Article{}, fmt.Errorf("begin %s publication: %w", kind, err)
	}
	defer tx.Rollback()
	var currentRevision, currentVersion int64
	var publicID []byte
	var slug, slugKey, bodyMarkdown string
	var publishedSlug, publishedSlugKey sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT current_revision_id, lock_version, public_id, slug, slug_key, published_slug, published_slug_key, body_markdown FROM contents WHERE id = ? AND kind = ? AND trashed_at IS NULL`, id, kind).Scan(&currentRevision, &currentVersion, &publicID, &slug, &slugKey, &publishedSlug, &publishedSlugKey, &bodyMarkdown)
	if errors.Is(err, sql.ErrNoRows) {
		return Article{}, ErrNotFound
	}
	if err != nil {
		return Article{}, fmt.Errorf("read %s for publication: %w", kind, err)
	}
	if currentVersion != expectedVersion {
		return Article{}, ErrConflict
	}
	if _, err := tx.ExecContext(ctx, "UPDATE content_revisions SET is_publication_checkpoint = 1 WHERE id = ? AND content_id = ?", currentRevision, id); err != nil {
		return Article{}, fmt.Errorf("mark publication revision: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE contents
		SET status = 'published', published_revision_id = current_revision_id,
		    published_slug = slug, published_slug_key = slug_key,
		    published_at = COALESCE(published_at, ?), scheduled_at = NULL,
		    withdrawn_at = NULL,
		    lock_version = lock_version + 1, updated_at = ?
		WHERE id = ? AND kind = ? AND lock_version = ?
	`, millis(now), millis(now), id, kind, expectedVersion)
	if err != nil {
		return Article{}, fmt.Errorf("publish %s: %w", kind, err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		if err != nil {
			return Article{}, fmt.Errorf("check publication: %w", err)
		}
		return Article{}, ErrConflict
	}
	if err := rebaseEditingSnapshot(ctx, tx, id, expectedVersion); err != nil {
		return Article{}, err
	}
	if publishedSlugKey.Valid && publishedSlugKey.String != slugKey {
		if err := saveRedirect(ctx, tx, publicPath(kind, publishedSlug.String), publicPath(kind, publishedSlugKey.String), publicPath(kind, slug), publicPath(kind, slugKey), "content_slug", now); err != nil {
			return Article{}, err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE reserved_paths SET reason='historical' WHERE content_id=? AND path_key=?", id, publicPath(kind, publishedSlugKey.String)); err != nil {
			return Article{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE reserved_paths SET reason = 'published' WHERE content_id = ? AND path_key = ?", id, publicPath(kind, slugKey)); err != nil {
		return Article{}, fmt.Errorf("publish reserved path: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE system_state SET render_epoch = render_epoch + 1, updated_at = ? WHERE id = 1", millis(now)); err != nil {
		return Article{}, fmt.Errorf("invalidate public rendering: %w", err)
	}
	if err := r.replaceMediaReferences(ctx, tx, id, bodyMarkdown, now); err != nil {
		return Article{}, err
	}
	if err := insertAudit(ctx, tx, "publishing."+kind+".published", kind, publicID, now); err != nil {
		return Article{}, err
	}
	if err := tx.Commit(); err != nil {
		return Article{}, fmt.Errorf("commit %s publication: %w", kind, err)
	}
	return r.Content(ctx, kind, id)
}

func (r *Repository) Content(ctx context.Context, kind string, id int64) (Article, error) {
	content, err := scanContent(r.database.Reader.QueryRowContext(ctx, contentSelect+" WHERE c.id = ? AND c.kind = ? AND c.trashed_at IS NULL", id, kind))
	if err != nil {
		return Article{}, err
	}
	return r.enrichTaxonomy(ctx, content)
}

func (r *Repository) PublicContent(ctx context.Context, kind, slugKey string) (Article, error) {
	content, err := scanContent(r.database.Reader.QueryRowContext(ctx, publicContentSelect+` WHERE c.published_slug_key = ? AND c.kind = ? AND c.status = 'published' AND c.trashed_at IS NULL`, slugKey, kind))
	if err != nil {
		return Article{}, err
	}
	return r.enrichPublishedTaxonomy(ctx, content)
}

func (r *Repository) PublicContentByID(ctx context.Context, kind string, id int64) (Article, error) {
	content, err := scanContent(r.database.Reader.QueryRowContext(ctx, publicContentSelect+` WHERE c.id = ? AND c.kind = ? AND c.status = 'published' AND c.trashed_at IS NULL`, id, kind))
	if err != nil {
		return Article{}, err
	}
	return r.enrichPublishedTaxonomy(ctx, content)
}

func (r *Repository) Contents(ctx context.Context, kind string) ([]Article, error) {
	rows, err := r.database.Reader.QueryContext(ctx, contentSelect+" WHERE c.kind = ? AND c.trashed_at IS NULL ORDER BY c.updated_at DESC, c.id DESC", kind)
	if err != nil {
		return nil, fmt.Errorf("list %ss: %w", kind, err)
	}
	defer rows.Close()
	var contents []Article
	for rows.Next() {
		content, err := scanContent(rows)
		if err != nil {
			return nil, err
		}
		contents = append(contents, content)
	}
	return contents, rows.Err()
}

func (r *Repository) CountAdminContents(ctx context.Context, kind string, filter AdminContentFilter) (int, error) {
	where, args := adminContentWhere(kind, filter)
	var total int
	if err := r.database.Reader.QueryRowContext(ctx, "SELECT COUNT(*) FROM contents c "+where, args...).Scan(&total); err != nil {
		return 0, fmt.Errorf("count admin %ss: %w", kind, err)
	}
	return total, nil
}

func (r *Repository) AdminContents(ctx context.Context, kind string, filter AdminContentFilter, info pagination.Info) ([]Article, error) {
	where, args := adminContentWhere(kind, filter)
	query := contentSelect + " " + where + " ORDER BY " + adminContentOrder(filter.Sort) + " LIMIT ? OFFSET ?"
	args = append(args, info.PerPage, info.Offset())
	rows, err := r.database.Reader.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list admin %ss: %w", kind, err)
	}
	defer rows.Close()
	contents := make([]Article, 0, info.PerPage)
	for rows.Next() {
		content, err := scanContent(rows)
		if err != nil {
			return nil, err
		}
		contents = append(contents, content)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for index := range contents {
		contents[index], err = r.enrichTaxonomy(ctx, contents[index])
		if err != nil {
			return nil, err
		}
	}
	return contents, nil
}

func adminContentWhere(kind string, filter AdminContentFilter) (string, []any) {
	clauses := []string{"c.kind = ?", "c.trashed_at IS NULL"}
	args := []any{kind}
	if status := normalizeAdminContentStatus(filter.Status); status != "all" {
		clauses = append(clauses, "c.status = ?")
		args = append(args, status)
	}
	if filter.Query != "" {
		pattern := "%" + escapeLikePattern(filter.Query) + "%"
		clauses = append(clauses, `(
			c.title LIKE ? COLLATE NOCASE ESCAPE char(92) OR
			c.slug LIKE ? COLLATE NOCASE ESCAPE char(92) OR
			c.excerpt LIKE ? COLLATE NOCASE ESCAPE char(92) OR
			EXISTS (
				SELECT 1 FROM categories search_category
				WHERE search_category.id = c.category_id AND (
					search_category.name LIKE ? COLLATE NOCASE ESCAPE char(92) OR
					search_category.slug_key LIKE ? COLLATE NOCASE ESCAPE char(92)
				)
			) OR
			EXISTS (
				SELECT 1 FROM content_tags search_content_tag
				JOIN tags search_tag ON search_tag.id = search_content_tag.tag_id
				WHERE search_content_tag.content_id = c.id AND (
					search_tag.name LIKE ? COLLATE NOCASE ESCAPE char(92) OR
					search_tag.slug_key LIKE ? COLLATE NOCASE ESCAPE char(92)
				)
			)
		)`)
		for range 7 {
			args = append(args, pattern)
		}
	}
	if category := strings.TrimSpace(filter.Category); category != "" {
		clauses = append(clauses, "EXISTS (SELECT 1 FROM categories filter_category WHERE filter_category.id = c.category_id AND filter_category.slug_key = ?)")
		args = append(args, strings.ToLower(category))
	}
	if tag := strings.TrimSpace(filter.Tag); tag != "" {
		clauses = append(clauses, "EXISTS (SELECT 1 FROM content_tags filter_content_tag JOIN tags filter_tag ON filter_tag.id = filter_content_tag.tag_id WHERE filter_content_tag.content_id = c.id AND filter_tag.slug_key = ?)")
		args = append(args, strings.ToLower(tag))
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

func adminContentOrder(sortBy string) string {
	switch normalizeAdminContentSort(sortBy) {
	case "published":
		return "COALESCE(c.published_at, 0) DESC, c.updated_at DESC, c.id DESC"
	case "title":
		return "c.title COLLATE NOCASE ASC, c.id ASC"
	default:
		return "c.updated_at DESC, c.id DESC"
	}
}

func escapeLikePattern(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "%", `\%`)
	return strings.ReplaceAll(value, "_", `\_`)
}

func (r *Repository) TrashedContents(ctx context.Context) ([]Article, error) {
	rows, err := r.database.Reader.QueryContext(ctx, contentSelect+" WHERE c.trashed_at IS NOT NULL ORDER BY c.trashed_at DESC, c.id DESC")
	if err != nil {
		return nil, fmt.Errorf("list trashed contents: %w", err)
	}
	defer rows.Close()
	var contents []Article
	for rows.Next() {
		content, err := scanContent(rows)
		if err != nil {
			return nil, err
		}
		contents = append(contents, content)
	}
	return contents, rows.Err()
}

func (r *Repository) PublishedArticles(ctx context.Context, limit int) ([]Article, error) {
	return r.publishedContents(ctx, "article", limit)
}

func (r *Repository) PublicArticlesPage(ctx context.Context, request pagination.Request) (PublicArticlePage, error) {
	total, err := r.countPublishedContents(ctx, "article")
	if err != nil {
		return PublicArticlePage{}, err
	}
	info := pagination.NewInfo(total, request)
	articles, err := r.publishedContentsPage(ctx, "article", info)
	if err != nil {
		return PublicArticlePage{}, err
	}
	return PublicArticlePage{Articles: articles, Pagination: info}, nil
}

func (r *Repository) PublishedPages(ctx context.Context, limit int) ([]Article, error) {
	return r.publishedContents(ctx, "page", limit)
}

func (r *Repository) publishedContents(ctx context.Context, kind string, limit int) ([]Article, error) {
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	rows, err := r.database.Reader.QueryContext(ctx, publicContentSelect+` WHERE c.kind = ? AND c.status = 'published' AND c.trashed_at IS NULL ORDER BY c.published_at DESC, c.id DESC LIMIT ?`, kind, limit)
	if err != nil {
		return nil, fmt.Errorf("list published %ss: %w", kind, err)
	}
	defer rows.Close()
	articles := make([]Article, 0, limit)
	for rows.Next() {
		article, err := scanContent(rows)
		if err != nil {
			return nil, err
		}
		articles = append(articles, article)
	}
	for index := range articles {
		articles[index], err = r.enrichPublishedTaxonomy(ctx, articles[index])
		if err != nil {
			return nil, err
		}
	}
	return articles, rows.Err()
}

func (r *Repository) publishedContentsPage(ctx context.Context, kind string, info pagination.Info) ([]Article, error) {
	rows, err := r.database.Reader.QueryContext(ctx, publicContentSelect+` WHERE c.kind = ? AND c.status = 'published' AND c.trashed_at IS NULL ORDER BY c.published_at DESC, c.id DESC LIMIT ? OFFSET ?`, kind, info.PerPage, info.Offset())
	if err != nil {
		return nil, fmt.Errorf("list published %ss page: %w", kind, err)
	}
	defer rows.Close()
	articles := make([]Article, 0, info.PerPage)
	for rows.Next() {
		article, err := scanContent(rows)
		if err != nil {
			return nil, err
		}
		articles = append(articles, article)
	}
	for index := range articles {
		articles[index], err = r.enrichPublishedTaxonomy(ctx, articles[index])
		if err != nil {
			return nil, err
		}
	}
	return articles, rows.Err()
}

func (r *Repository) countPublishedContents(ctx context.Context, kind string) (int, error) {
	var total int
	err := r.database.Reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM contents WHERE kind=? AND status='published' AND trashed_at IS NULL`, kind).Scan(&total)
	return total, err
}

func (r *Repository) PublicArticleNavigation(ctx context.Context, articleID int64, relatedLimit int) (PublicArticleNavigation, error) {
	current, err := r.PublicContentByID(ctx, "article", articleID)
	if err != nil {
		return PublicArticleNavigation{}, err
	}
	if current.PublishedRevisionID < 1 || current.PublishedAt == nil {
		return PublicArticleNavigation{}, ErrNotFound
	}
	previous, err := r.neighbor(ctx, current, true)
	if err != nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, sql.ErrNoRows) {
		return PublicArticleNavigation{}, err
	}
	var previousArticle *Article
	if err == nil {
		previousArticle = &previous
	}
	next, err := r.neighbor(ctx, current, false)
	if err != nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, sql.ErrNoRows) {
		return PublicArticleNavigation{}, err
	}
	var nextArticle *Article
	if err == nil {
		nextArticle = &next
	}
	related, err := r.related(ctx, current, relatedLimit)
	if err != nil {
		return PublicArticleNavigation{}, err
	}
	return PublicArticleNavigation{Previous: previousArticle, Next: nextArticle, Related: related}, nil
}

func (r *Repository) neighbor(ctx context.Context, current Article, older bool) (Article, error) {
	operator, order := "<", "DESC"
	if !older {
		operator, order = ">", "ASC"
	}
	query := publicContentSelect + ` WHERE c.kind='article' AND c.status='published' AND c.trashed_at IS NULL
		AND (c.published_at ` + operator + ` ? OR (c.published_at = ? AND c.id ` + operator + ` ?))
		ORDER BY c.published_at ` + order + `, c.id ` + order + ` LIMIT 1`
	return scanContent(r.database.Reader.QueryRowContext(ctx, query, current.PublishedAt.UnixMilli(), current.PublishedAt.UnixMilli(), current.ID))
}

func (r *Repository) related(ctx context.Context, current Article, limit int) ([]Article, error) {
	conditions := make([]string, 0, 2)
	args := make([]any, 0, 6)
	if len(current.publishedCategoryPublicID) > 0 {
		conditions = append(conditions, "r.category_public_id = ?")
		args = append(args, current.publishedCategoryPublicID)
	}
	for _, tag := range current.Tags {
		if len(tag.PublicID) == 0 {
			continue
		}
		conditions = append(conditions, "EXISTS (SELECT 1 FROM json_each(r.tag_public_ids_json) WHERE json_each.value = lower(hex(?)))")
		args = append(args, tag.PublicID)
	}
	if len(conditions) == 0 {
		return nil, nil
	}
	query := publicContentSelect + ` WHERE c.kind='article' AND c.status='published' AND c.trashed_at IS NULL AND c.id <> ? AND (` + strings.Join(conditions, " OR ") + `)
		ORDER BY c.published_at DESC,c.id DESC LIMIT ?`
	queryArgs := []any{current.ID}
	queryArgs = append(queryArgs, args...)
	queryArgs = append(queryArgs, limit)
	rows, err := r.database.Reader.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return nil, fmt.Errorf("list related articles: %w", err)
	}
	defer rows.Close()
	result := make([]Article, 0, limit)
	for rows.Next() {
		article, err := scanContent(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, article)
	}
	return result, rows.Err()
}

func (r *Repository) enrichTaxonomy(ctx context.Context, content Article) (Article, error) {
	if content.Kind != "article" {
		return content, nil
	}
	taxonomy, err := r.organization.ArticleTaxonomy(ctx, content.ID)
	if err != nil {
		return Article{}, fmt.Errorf("read article taxonomy: %w", err)
	}
	content.Category = taxonomy.Category
	content.Tags = taxonomy.Tags
	return content, nil
}

const contentSelect = `
	SELECT c.id, c.public_id, c.kind, c.status, c.slug, COALESCE(c.published_slug,''), c.title, c.excerpt, c.seo_title, c.seo_description, c.body_markdown,
	       NULL, NULL,
	       c.current_revision_id, c.published_revision_id, c.published_at,
	       (SELECT r.created_at FROM content_revisions r WHERE r.id = c.published_revision_id),
	       c.scheduled_at, c.withdrawn_at, c.trashed_at,
	       c.lock_version, c.created_at, c.updated_at
	FROM contents c`

const publicContentSelect = `
	SELECT c.id, c.public_id, c.kind, c.status, c.published_slug, c.published_slug,
	       r.title, r.excerpt, r.seo_title, r.seo_description, r.body_markdown,
	       r.category_public_id, r.tag_public_ids_json,
	       c.current_revision_id, c.published_revision_id, c.published_at,
	       r.created_at, c.scheduled_at, c.withdrawn_at, c.trashed_at,
	       c.lock_version, c.created_at, c.updated_at
	FROM contents c
	JOIN content_revisions r ON r.id = c.published_revision_id`

type scanner interface{ Scan(dest ...any) error }

func scanContent(row scanner) (Article, error) {
	var content Article
	var currentRevision, publishedRevision sql.NullInt64
	var publishedAt, publishedRevisionAt, scheduledAt, withdrawnAt, trashedAt sql.NullInt64
	var publishedCategoryPublicID []byte
	var publishedTagPublicIDsJSON sql.NullString
	var createdAt, updatedAt int64
	err := row.Scan(
		&content.ID, &content.PublicID, &content.Kind, &content.Status, &content.Slug,
		&content.PublishedSlug,
		&content.Title, &content.Excerpt, &content.SEOTitle, &content.SEODescription, &content.BodyMarkdown,
		&publishedCategoryPublicID, &publishedTagPublicIDsJSON,
		&currentRevision, &publishedRevision, &publishedAt, &publishedRevisionAt,
		&scheduledAt, &withdrawnAt, &trashedAt,
		&content.LockVersion, &createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Article{}, ErrNotFound
	}
	if err != nil {
		return Article{}, fmt.Errorf("scan content: %w", err)
	}
	content.CurrentRevisionID = currentRevision.Int64
	content.publishedCategoryPublicID = publishedCategoryPublicID
	content.publishedTagPublicIDsJSON = publishedTagPublicIDsJSON.String
	content.PublishedRevisionID = publishedRevision.Int64
	if publishedAt.Valid {
		value := fromMillis(publishedAt.Int64)
		content.PublishedAt = &value
	}
	if publishedRevisionAt.Valid {
		value := fromMillis(publishedRevisionAt.Int64)
		content.PublishedRevisionAt = &value
	}
	if scheduledAt.Valid {
		value := fromMillis(scheduledAt.Int64)
		content.ScheduledAt = &value
	}
	if withdrawnAt.Valid {
		value := fromMillis(withdrawnAt.Int64)
		content.WithdrawnAt = &value
	}
	if trashedAt.Valid {
		value := fromMillis(trashedAt.Int64)
		content.TrashedAt = &value
	}
	content.CreatedAt = fromMillis(createdAt)
	content.UpdatedAt = fromMillis(updatedAt)
	return content, nil
}

func (r *Repository) enrichPublishedTaxonomy(ctx context.Context, content Article) (Article, error) {
	if content.Kind != "article" {
		return content, nil
	}
	taxonomy, err := r.organization.TaxonomyBySnapshot(ctx, content.publishedCategoryPublicID, content.publishedTagPublicIDsJSON)
	if err != nil {
		return Article{}, fmt.Errorf("read published taxonomy: %w", err)
	}
	content.Category = taxonomy.Category
	content.Tags = taxonomy.Tags
	return content, nil
}

func insertRevision(ctx context.Context, tx *sql.Tx, contentID, revisionNumber int64, revision revisionInput, now time.Time) (int64, error) {
	result, err := tx.ExecContext(ctx, `
		INSERT INTO content_revisions (
			public_id, content_id, revision_number, title, slug, excerpt, seo_title, seo_description,
			body_markdown, reason, category_public_id, tag_public_ids_json, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, revision.PublicID, contentID, revisionNumber, revision.Title, revision.Slug,
		revision.Excerpt, revision.SEOTitle, revision.SEODescription, revision.BodyMarkdown, revision.Reason, nullableBytes(revision.CategoryPublicID),
		revision.TagPublicIDsJSON, millis(now))
	if err != nil {
		return 0, fmt.Errorf("save content revision: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read revision ID: %w", err)
	}
	return id, nil
}

func reservePath(ctx context.Context, tx *sql.Tx, contentID int64, kind, displaySlug, slugKey, reason string, now time.Time) error {
	path := publicPath(kind, displaySlug)
	pathKey := publicPath(kind, slugKey)
	result, err := tx.ExecContext(ctx, `
		INSERT INTO reserved_paths (path, path_key, content_id, reason, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(path_key) DO NOTHING
	`, path, pathKey, contentID, reason, millis(now))
	if err != nil {
		return mapWriteError(err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check reserved path: %w", err)
	}
	if inserted == 1 {
		return nil
	}
	return ErrSlugUnavailable
}

func publicPath(kind, slug string) string {
	if kind == "page" {
		return "/" + slug
	}
	return "/posts/" + slug
}

func saveRedirect(ctx context.Context, tx *sql.Tx, sourcePath, sourceKey, targetPath, targetKey, reason string, now time.Time) error {
	if sourceKey == targetKey {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE redirects SET target_path=?,target_path_key=?,updated_at=? WHERE target_path_key=?
	`, targetPath, targetKey, millis(now), sourceKey); err != nil {
		return fmt.Errorf("flatten redirect chain: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO redirects(source_path,source_path_key,target_path,target_path_key,status_code,reason,created_at,updated_at)
		VALUES(?,?,?,?,301,?,?,?)
		ON CONFLICT(source_path_key) DO UPDATE SET
			target_path=excluded.target_path,target_path_key=excluded.target_path_key,status_code=301,reason=excluded.reason,updated_at=excluded.updated_at
	`, sourcePath, sourceKey, targetPath, targetKey, reason, millis(now), millis(now)); err != nil {
		return fmt.Errorf("save redirect: %w", err)
	}
	return nil
}

func insertAudit(ctx context.Context, tx *sql.Tx, action, kind string, publicID []byte, now time.Time) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO audit_entries (action, object_kind, object_public_id, result, context_json, created_at)
		VALUES (?, ?, ?, 'succeeded', '{}', ?)
	`, action, kind, publicID, millis(now))
	if err != nil {
		return fmt.Errorf("save publishing audit: %w", err)
	}
	return nil
}

func nullableBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

func (r *Repository) replaceMediaReferences(ctx context.Context, tx *sql.Tx, contentID int64, markdown string, now time.Time) error {
	err := r.mediaReferences.ReplaceBodyReferencesTx(ctx, tx, contentID, markdown, now)
	var validation media.ValidationError
	if errors.As(err, &validation) {
		return ValidationError{Message: validation.Message}
	}
	return err
}

func mapWriteError(err error) error {
	var sqliteError sqlite3.Error
	if errors.As(err, &sqliteError) && sqliteError.ExtendedCode == sqlite3.ErrConstraintUnique {
		return ErrSlugUnavailable
	}
	return fmt.Errorf("write content: %w", err)
}

func millis(value time.Time) int64     { return value.UTC().UnixMilli() }
func fromMillis(value int64) time.Time { return time.UnixMilli(value).UTC() }
