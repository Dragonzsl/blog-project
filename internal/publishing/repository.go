package publishing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mattn/go-sqlite3"
	"github.com/zhushilin/blog-project/internal/extensions"
	"github.com/zhushilin/blog-project/internal/media"
	"github.com/zhushilin/blog-project/internal/organization"
	"github.com/zhushilin/blog-project/internal/platform/database"
	"github.com/zhushilin/blog-project/internal/platform/pagination"
)

type Repository struct {
	database        *database.DB
	organization    *organization.Service
	mediaReferences *media.ReferenceRepository
	eventRecorder   interface {
		RecordEventTx(context.Context, *sql.Tx, extensions.Event) error
	}
}

func NewRepository(db *database.DB) *Repository {
	return &Repository{database: db, organization: organization.NewService(db), mediaReferences: media.NewReferenceRepository()}
}

func (r *Repository) SetEventRecorder(recorder interface {
	RecordEventTx(context.Context, *sql.Tx, extensions.Event) error
}) {
	r.eventRecorder = recorder
}

func (r *Repository) CreateDraft(ctx context.Context, kind string, publicID []byte, revision revisionInput, categoryID int64, tagIDs []int64, now time.Time) (Article, error) {
	tx, err := r.database.Writer.BeginTx(ctx, nil)
	if err != nil {
		return Article{}, fmt.Errorf("begin %s creation: %w", kind, err)
	}
	defer tx.Rollback()
	contentID, err := r.createDraftTx(ctx, tx, kind, publicID, revision, categoryID, tagIDs, now)
	if err != nil {
		return Article{}, err
	}
	if err := tx.Commit(); err != nil {
		return Article{}, fmt.Errorf("commit %s creation: %w", kind, err)
	}
	return r.Content(ctx, kind, contentID)
}

func (r *Repository) createDraftTx(ctx context.Context, tx *sql.Tx, kind string, publicID []byte, revision revisionInput, categoryID int64, tagIDs []int64, now time.Time) (int64, error) {
	if err := r.resolveCoverMediaTx(ctx, tx, &revision); err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO contents (
			public_id, kind, status, slug, slug_key, title, excerpt, seo_title, seo_description, body_markdown, cover_media_id,
			lock_version, created_at, updated_at
		) VALUES (?, ?, 'draft', ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)
	`, publicID, kind, revision.Slug, revision.SlugKey, revision.Title, revision.Excerpt,
		revision.SEOTitle, revision.SEODescription, revision.BodyMarkdown, nullableInt64(revision.CoverMediaID), millis(now), millis(now))
	if err != nil {
		return 0, mapWriteError(err)
	}
	contentID, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read %s ID: %w", kind, err)
	}
	if kind == "article" {
		revision.CategoryPublicID, revision.TagPublicIDsJSON, err = r.organization.ReplaceArticleTaxonomyTx(ctx, tx, contentID, categoryID, tagIDs, now)
		if err != nil {
			return 0, err
		}
	} else {
		revision.TagPublicIDsJSON = "[]"
	}
	coverPublicID, err := validateCoverMediaTx(ctx, tx, revision.CoverMediaID)
	if err != nil {
		return 0, err
	}
	revision.CoverMediaPublicID = coverPublicID
	revision.CoverSnapshotVersion = 1
	if err := r.replaceMediaReferences(ctx, tx, contentID, revision.BodyMarkdown, revision.CoverMediaID, now); err != nil {
		return 0, err
	}
	revisionNumber := revision.RevisionNumber
	if revisionNumber < 1 {
		revisionNumber = 1
	}
	revisionID, err := insertRevision(ctx, tx, contentID, revisionNumber, revision, now)
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE contents SET current_revision_id = ? WHERE id = ?", revisionID, contentID); err != nil {
		return 0, fmt.Errorf("attach current revision: %w", err)
	}
	if err := reservePath(ctx, tx, contentID, kind, revision.Slug, revision.SlugKey, "draft", now); err != nil {
		return 0, err
	}
	if err := insertAudit(ctx, tx, "publishing."+kind+".created", kind, publicID, now); err != nil {
		return 0, err
	}
	return contentID, nil
}

func (r *Repository) UpdateDraft(ctx context.Context, kind string, id, expectedVersion int64, revision revisionInput, categoryID int64, tagIDs []int64, now time.Time, configuredLimit ...int) (Article, error) {
	tx, err := r.database.Writer.BeginTx(ctx, nil)
	if err != nil {
		return Article{}, fmt.Errorf("begin %s update: %w", kind, err)
	}
	defer tx.Rollback()
	contentID, err := r.updateDraftTx(ctx, tx, kind, id, expectedVersion, revision, categoryID, tagIDs, now, configuredLimit...)
	if err != nil {
		return Article{}, err
	}
	if err := tx.Commit(); err != nil {
		return Article{}, fmt.Errorf("commit %s update: %w", kind, err)
	}
	return r.Content(ctx, kind, contentID)
}

func (r *Repository) updateDraftTx(ctx context.Context, tx *sql.Tx, kind string, id, expectedVersion int64, revision revisionInput, categoryID int64, tagIDs []int64, now time.Time, configuredLimit ...int) (int64, error) {
	var currentSlugKey string
	var currentVersion, nextRevision int64
	var currentCoverID sql.NullInt64
	var publicID []byte
	var err error
	err = tx.QueryRowContext(ctx, `
		SELECT slug_key, lock_version, public_id, cover_media_id,
		       (SELECT COALESCE(MAX(revision_number), 0) + 1 FROM content_revisions WHERE content_id = contents.id)
		FROM contents WHERE id = ? AND kind = ? AND trashed_at IS NULL
	`, id, kind).Scan(&currentSlugKey, &currentVersion, &publicID, &currentCoverID, &nextRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("read %s for update: %w", kind, err)
	}
	if currentVersion != expectedVersion {
		return 0, ErrConflict
	}
	if revision.RevisionNumber > 0 {
		nextRevision = revision.RevisionNumber
	}
	if revision.SlugKey != currentSlugKey {
		if err := reservePath(ctx, tx, id, kind, revision.Slug, revision.SlugKey, "draft", now); err != nil {
			return 0, err
		}
	}
	if kind == "article" {
		revision.CategoryPublicID, revision.TagPublicIDsJSON, err = r.organization.ReplaceArticleTaxonomyTx(ctx, tx, id, categoryID, tagIDs, now)
		if err != nil {
			return 0, err
		}
	} else {
		revision.TagPublicIDsJSON = "[]"
	}
	// A caller that omits the cover field for an older editor payload keeps the
	// current cover. Explicit zero clears it.
	if revision.CoverMediaID < 0 {
		revision.CoverMediaID = currentCoverID.Int64
	}
	if err := r.resolveCoverMediaTx(ctx, tx, &revision); err != nil {
		return 0, err
	}
	coverPublicID, err := validateCoverMediaTx(ctx, tx, revision.CoverMediaID)
	if err != nil {
		return 0, err
	}
	revision.CoverMediaPublicID = coverPublicID
	revision.CoverSnapshotVersion = 1
	if err := r.replaceMediaReferences(ctx, tx, id, revision.BodyMarkdown, revision.CoverMediaID, now); err != nil {
		return 0, err
	}
	revisionID, err := insertRevision(ctx, tx, id, nextRevision, revision, now)
	if err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE contents
		SET slug = ?, slug_key = ?, title = ?, excerpt = ?, seo_title = ?, seo_description = ?, body_markdown = ?, cover_media_id = ?,
		    current_revision_id = ?, lock_version = lock_version + 1, updated_at = ?
		WHERE id = ? AND kind = ? AND lock_version = ?
	`, revision.Slug, revision.SlugKey, revision.Title, revision.Excerpt, revision.SEOTitle,
		revision.SEODescription, revision.BodyMarkdown, nullableInt64(revision.CoverMediaID), revisionID, millis(now), id, kind, expectedVersion)
	if err != nil {
		return 0, mapWriteError(err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		if err != nil {
			return 0, fmt.Errorf("check %s update: %w", kind, err)
		}
		return 0, ErrConflict
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM editing_snapshots WHERE content_id=?", id); err != nil {
		return 0, err
	}
	limit := 50
	if len(configuredLimit) > 0 {
		limit = configuredLimit[0]
	}
	if err := pruneRevisions(ctx, tx, id, limit); err != nil {
		return 0, fmt.Errorf("prune content revisions: %w", err)
	}
	if err := insertAudit(ctx, tx, "publishing."+kind+".saved", kind, publicID, now); err != nil {
		return 0, err
	}
	return id, nil
}

type draftRevisionUpdate struct {
	revision   revisionInput
	categoryID int64
	tagIDs     []int64
}

// CreateDraftWithRevisions imports one content item and its historical draft
// revisions in a single write transaction. Archive adapters use this boundary
// so a later revision failure cannot leave a half-imported content item.
func (r *Repository) CreateDraftWithRevisions(ctx context.Context, kind string, publicID []byte, first revisionInput, firstCategoryID int64, firstTagIDs []int64, updates []draftRevisionUpdate, now time.Time, revisionLimit int) (Article, error) {
	tx, err := r.database.Writer.BeginTx(ctx, nil)
	if err != nil {
		return Article{}, fmt.Errorf("begin %s archive import: %w", kind, err)
	}
	defer tx.Rollback()
	contentID, err := r.createDraftTx(ctx, tx, kind, publicID, first, firstCategoryID, firstTagIDs, now)
	if err != nil {
		return Article{}, err
	}
	version := int64(1)
	for _, update := range updates {
		if _, err := r.updateDraftTx(ctx, tx, kind, contentID, version, update.revision, update.categoryID, update.tagIDs, now, revisionLimit); err != nil {
			return Article{}, err
		}
		version++
	}
	if err := tx.Commit(); err != nil {
		return Article{}, fmt.Errorf("commit %s archive import: %w", kind, err)
	}
	return r.Content(ctx, kind, contentID)
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
	var coverMediaID sql.NullInt64
	var publishedSlug, publishedSlugKey sql.NullString
	var existingPublishedAt sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT current_revision_id, lock_version, public_id, slug, slug_key, published_slug, published_slug_key, body_markdown, cover_media_id, published_at FROM contents WHERE id = ? AND kind = ? AND trashed_at IS NULL`, id, kind).Scan(&currentRevision, &currentVersion, &publicID, &slug, &slugKey, &publishedSlug, &publishedSlugKey, &bodyMarkdown, &coverMediaID, &existingPublishedAt)
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
	publishedAtMillis := millis(now)
	if existingPublishedAt.Valid {
		publishedAtMillis = existingPublishedAt.Int64
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE contents
		SET status = 'published', published_revision_id = current_revision_id,
		    published_slug = slug, published_slug_key = slug_key,
		    published_at = ?, scheduled_at = NULL,
		    withdrawn_at = NULL,
		    lock_version = lock_version + 1, updated_at = ?
		WHERE id = ? AND kind = ? AND lock_version = ?
	`, publishedAtMillis, millis(now), id, kind, expectedVersion)
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
	var revisionTitle, tagPublicIDsJSON string
	var categoryPublicID []byte
	if err := tx.QueryRowContext(ctx, `SELECT title,category_public_id,tag_public_ids_json FROM content_revisions WHERE id=? AND content_id=?`, currentRevision, id).Scan(&revisionTitle, &categoryPublicID, &tagPublicIDsJSON); err != nil {
		return Article{}, fmt.Errorf("read published taxonomy: %w", err)
	}
	if err := r.organization.ReplacePublishedTaxonomyTx(ctx, tx, id, currentRevision, kind, categoryPublicID, tagPublicIDsJSON, revisionTitle, slug, time.UnixMilli(publishedAtMillis).UTC()); err != nil {
		return Article{}, fmt.Errorf("update public taxonomy projection: %w", err)
	}
	if err := r.replaceMediaReferences(ctx, tx, id, bodyMarkdown, coverMediaID.Int64, now); err != nil {
		return Article{}, err
	}
	if err := insertAudit(ctx, tx, "publishing."+kind+".published", kind, publicID, now); err != nil {
		return Article{}, err
	}
	if r.eventRecorder != nil {
		if err := r.eventRecorder.RecordEventTx(ctx, tx, extensions.Event{
			Name: "ContentPublished.v1", Version: 1, ObjectID: append([]byte(nil), publicID...),
			Payload: map[string]any{"kind": kind, "slug": slug}, OccurredAt: now,
		}); err != nil {
			return Article{}, fmt.Errorf("record publication event: %w", err)
		}
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

func (r *Repository) TrashedContent(ctx context.Context, kind string, id int64) (Article, error) {
	content, err := scanContent(r.database.Reader.QueryRowContext(ctx, contentSelect+" WHERE c.id=? AND c.kind=? AND c.trashed_at IS NOT NULL", id, kind))
	if err != nil {
		return Article{}, err
	}
	return r.enrichTaxonomy(ctx, content)
}

func (r *Repository) ContentPublicIDExists(ctx context.Context, kind string, publicID []byte) (bool, error) {
	var exists bool
	err := r.database.Reader.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM contents WHERE kind=? AND public_id=?)", kind, publicID).Scan(&exists)
	return exists, err
}

func (r *Repository) RevisionPublicIDExists(ctx context.Context, publicID []byte) (bool, error) {
	var exists bool
	err := r.database.Reader.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM content_revisions WHERE public_id=?)", publicID).Scan(&exists)
	return exists, err
}

// DeleteImportedDraft removes only a draft identified by the public ID. It is
// used to clean up an archive import that failed after earlier entries had
// committed; published, scheduled, trashed, or otherwise changed content is
// never eligible for this rollback path.
func (r *Repository) DeleteImportedDraft(ctx context.Context, kind string, publicID []byte, now time.Time) error {
	tx, err := r.database.Writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id int64
	var publishedRevision sql.NullInt64
	var status string
	var trashedAt sql.NullInt64
	err = tx.QueryRowContext(ctx, "SELECT id,status,published_revision_id,trashed_at FROM contents WHERE kind=? AND public_id=?", kind, publicID).Scan(&id, &status, &publishedRevision, &trashedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if status != "draft" || publishedRevision.Valid || trashedAt.Valid {
		return ErrConflict
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM reserved_paths WHERE content_id=? AND reason='draft'", id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM contents WHERE id=? AND kind=? AND status='draft' AND published_revision_id IS NULL AND trashed_at IS NULL", id, kind); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) PublicContent(ctx context.Context, kind, slugKey string) (Article, error) {
	content, err := scanContent(r.database.Reader.QueryRowContext(ctx, publicContentSelect+` WHERE c.published_slug_key = ? AND c.kind = ? AND c.status = 'published' AND c.trashed_at IS NULL`, slugKey, kind))
	if err != nil {
		return Article{}, err
	}
	return r.enrichPublishedTaxonomy(ctx, content)
}

func (r *Repository) PublicContentCard(ctx context.Context, kind, slugKey string) (Article, error) {
	content, err := scanContent(r.database.Reader.QueryRowContext(ctx, publicCardSelect+` WHERE c.published_slug_key = ? AND c.kind = ? AND c.status = 'published' AND c.trashed_at IS NULL`, slugKey, kind))
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

func (r *Repository) MediaIDByPublicID(ctx context.Context, publicID []byte) (int64, error) {
	var id int64
	if err := r.database.Reader.QueryRowContext(ctx, "SELECT id FROM media WHERE public_id=?", publicID).Scan(&id); errors.Is(err, sql.ErrNoRows) {
		return 0, ValidationError{Message: "修订版本引用的封面媒体不存在"}
	} else if err != nil {
		return 0, err
	}
	return id, nil
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
	query := contentListSelect + " " + where + " ORDER BY " + adminContentOrder(filter.Sort) + " LIMIT ? OFFSET ?"
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
	if err := r.enrichTaxonomyBatch(ctx, contents); err != nil {
		return nil, err
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
	rows, err := r.database.Reader.QueryContext(ctx, contentListSelect+" WHERE c.trashed_at IS NOT NULL ORDER BY c.trashed_at DESC, c.id DESC LIMIT ?", maxContentListRows)
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

// Dashboard returns bounded metadata and aggregate counters. It intentionally
// does not load article bodies; archive/export callers keep using the detail
// collection methods when a complete body is required.
func (r *Repository) Dashboard(ctx context.Context, cutoff time.Time) (DashboardSummary, error) {
	var summary DashboardSummary
	if err := r.database.Reader.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(CASE WHEN status='draft' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status='scheduled' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status='published' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN excerpt='' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status='published' AND published_at>=? THEN 1 ELSE 0 END),0)
		FROM contents WHERE trashed_at IS NULL`, cutoff.UTC().UnixMilli()).Scan(&summary.DraftCount, &summary.ScheduledCount, &summary.PublishedCount, &summary.WithoutExcerptCount, &summary.PublishedThisWeek); err != nil {
		return DashboardSummary{}, err
	}
	if err := r.database.Reader.QueryRowContext(ctx, "SELECT COUNT(*) FROM contents WHERE trashed_at IS NOT NULL").Scan(&summary.TrashedCount); err != nil {
		return DashboardSummary{}, err
	}
	queries := []string{
		contentListSelect + " WHERE c.trashed_at IS NULL ORDER BY c.updated_at DESC,c.id DESC LIMIT 5",
		contentListSelect + " WHERE c.status='published' AND c.trashed_at IS NULL ORDER BY c.published_at DESC,c.id DESC LIMIT 5",
	}
	for index, query := range queries {
		rows, err := r.database.Reader.QueryContext(ctx, query)
		if err != nil {
			return DashboardSummary{}, err
		}
		for rows.Next() {
			content, err := scanContent(rows)
			if err != nil {
				rows.Close()
				return DashboardSummary{}, err
			}
			item := summarizeAdminContent(content)
			if index == 0 {
				summary.RecentEdits = append(summary.RecentEdits, item)
			} else {
				summary.RecentPublished = append(summary.RecentPublished, item)
			}
		}
		if err := rows.Close(); err != nil {
			return DashboardSummary{}, err
		}
	}
	return summary, nil
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

// PublicArticleCardsByIDs loads only the published card projection for one
// bounded taxonomy page. The input order is retained so a taxonomy query and
// the subsequent rendering cannot accidentally reorder the public page.
func (r *Repository) PublicArticleCardsByIDs(ctx context.Context, ids []int64) ([]Article, error) {
	ids = uniquePositiveContentIDs(ids)
	if len(ids) == 0 {
		return nil, nil
	}
	if len(ids) > 50 {
		ids = ids[:50]
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := r.database.Reader.QueryContext(ctx, publicCardSelect+` WHERE c.id IN (`+placeholders+`) AND c.kind='article' AND c.status='published' AND c.trashed_at IS NULL ORDER BY c.published_at DESC,c.id DESC`, args...)
	if err != nil {
		return nil, fmt.Errorf("list public article cards: %w", err)
	}
	defer rows.Close()
	byID := make(map[int64]Article, len(ids))
	ordered := make([]Article, 0, len(ids))
	for rows.Next() {
		article, err := scanContent(rows)
		if err != nil {
			return nil, err
		}
		byID[article.ID] = article
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if article, ok := byID[id]; ok {
			ordered = append(ordered, article)
		}
	}
	if err := r.enrichPublishedTaxonomyBatch(ctx, ordered); err != nil {
		return nil, err
	}
	return ordered, nil
}

func (r *Repository) PublishedPages(ctx context.Context, limit int) ([]Article, error) {
	return r.publishedContents(ctx, "page", limit)
}

func (r *Repository) PublishedContentCursor(ctx context.Context, query PublicContentQuery) (PublicContentPage, error) {
	limit := query.Limit
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	clauses := []string{"c.kind=?", "c.status='published'", "c.trashed_at IS NULL"}
	args := []any{query.Kind}
	if query.Cursor != nil && !query.Cursor.PublishedAt.IsZero() && len(query.Cursor.PublicID) == 16 {
		clauses = append(clauses, "(c.published_at < ? OR (c.published_at = ? AND c.public_id < ?))")
		published := millis(query.Cursor.PublishedAt)
		args = append(args, published, published, query.Cursor.PublicID)
	}
	if query.UpdatedSince != nil && !query.UpdatedSince.IsZero() {
		clauses = append(clauses, "r.created_at >= ?")
		args = append(args, millis(*query.UpdatedSince))
	}
	if category := strings.TrimSpace(query.CategorySlug); category != "" {
		clauses = append(clauses, "r.category_public_id=(SELECT public_id FROM categories WHERE slug_key=? LIMIT 1)")
		args = append(args, strings.ToLower(category))
	}
	if tag := strings.TrimSpace(query.TagSlug); tag != "" {
		clauses = append(clauses, "EXISTS (SELECT 1 FROM tags filter_tag WHERE filter_tag.slug_key=? AND EXISTS (SELECT 1 FROM json_each(r.tag_public_ids_json) WHERE json_each.value=lower(hex(filter_tag.public_id))))")
		args = append(args, strings.ToLower(tag))
	}
	rows, err := r.database.Reader.QueryContext(ctx, publicCardSelect+" WHERE "+strings.Join(clauses, " AND ")+" ORDER BY c.published_at DESC,c.public_id DESC LIMIT ?", append(args, limit+1)...)
	if err != nil {
		return PublicContentPage{}, fmt.Errorf("list published content cursor: %w", err)
	}
	defer rows.Close()
	items := make([]Article, 0, limit)
	hasMore := false
	for rows.Next() {
		content, err := scanContent(rows)
		if err != nil {
			return PublicContentPage{}, err
		}
		if len(items) == limit {
			hasMore = true
			continue
		}
		items = append(items, content)
	}
	if err := rows.Err(); err != nil {
		return PublicContentPage{}, err
	}
	if err := r.enrichPublishedTaxonomyBatch(ctx, items); err != nil {
		return PublicContentPage{}, err
	}
	return PublicContentPage{Contents: items, HasMore: hasMore}, nil
}

func (r *Repository) PublishedContentPage(ctx context.Context, kind string, page, perPage int) (PublicContentPage, error) {
	if page < 1 {
		page = 1
	}
	if page > 100 {
		page = 100
	}
	if perPage < 1 {
		perPage = 20
	}
	if perPage > 100 {
		perPage = 100
	}
	offset := (page - 1) * perPage
	rows, err := r.database.Reader.QueryContext(ctx, publicCardSelect+" WHERE c.kind=? AND c.status='published' AND c.trashed_at IS NULL ORDER BY c.published_at DESC,c.public_id DESC LIMIT ? OFFSET ?", kind, perPage+1, offset)
	if err != nil {
		return PublicContentPage{}, fmt.Errorf("list published content page: %w", err)
	}
	defer rows.Close()
	items := make([]Article, 0, perPage)
	hasMore := false
	for rows.Next() {
		content, err := scanContent(rows)
		if err != nil {
			return PublicContentPage{}, err
		}
		if len(items) == perPage {
			hasMore = true
			continue
		}
		items = append(items, content)
	}
	if err := rows.Err(); err != nil {
		return PublicContentPage{}, err
	}
	if err := r.enrichPublishedTaxonomyBatch(ctx, items); err != nil {
		return PublicContentPage{}, err
	}
	return PublicContentPage{Contents: items, HasMore: hasMore}, nil
}

func (r *Repository) ScheduledContents(ctx context.Context, kind string, start, end time.Time, limit int) ([]ScheduledContent, bool, error) {
	if limit < 1 {
		limit = 100
	}
	if limit > 200 {
		limit = 200
	}
	rows, err := r.database.Reader.QueryContext(ctx, contentListSelect+` WHERE c.kind=? AND c.status='scheduled' AND c.trashed_at IS NULL AND c.scheduled_at>=? AND c.scheduled_at<? ORDER BY c.scheduled_at ASC,c.id ASC LIMIT ?`, kind, millis(start), millis(end), limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	items := make([]ScheduledContent, 0, limit)
	hasMore := false
	for rows.Next() {
		content, err := scanContent(rows)
		if err != nil {
			return nil, false, err
		}
		if len(items) == limit {
			hasMore = true
			continue
		}
		items = append(items, ScheduledContent{Article: content})
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return items, hasMore, nil
}

func (r *Repository) publishedContents(ctx context.Context, kind string, limit int) ([]Article, error) {
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	rows, err := r.database.Reader.QueryContext(ctx, publicCardSelect+` WHERE c.kind = ? AND c.status = 'published' AND c.trashed_at IS NULL ORDER BY c.published_at DESC, c.id DESC LIMIT ?`, kind, limit)
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
	if err := r.enrichPublishedTaxonomyBatch(ctx, articles); err != nil {
		return nil, err
	}
	return articles, rows.Err()
}

func (r *Repository) publishedContentsPage(ctx context.Context, kind string, info pagination.Info) ([]Article, error) {
	rows, err := r.database.Reader.QueryContext(ctx, publicCardSelect+` WHERE c.kind = ? AND c.status = 'published' AND c.trashed_at IS NULL ORDER BY c.published_at DESC, c.id DESC LIMIT ? OFFSET ?`, kind, info.PerPage, info.Offset())
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
	if err := r.enrichPublishedTaxonomyBatch(ctx, articles); err != nil {
		return nil, err
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
	return r.PublicArticleNavigationForArticle(ctx, current, relatedLimit)
}

// PublicArticleNavigationForArticle reuses the already loaded public article.
// The ID-based method remains for callers that do not have the article view;
// public HTTP detail rendering uses this method to avoid a duplicate article read.
func (r *Repository) PublicArticleNavigationForArticle(ctx context.Context, current Article, relatedLimit int) (PublicArticleNavigation, error) {
	if current.PublishedRevisionID < 1 || current.PublishedAt == nil {
		return PublicArticleNavigation{}, ErrNotFound
	}
	previous, previousErr := r.neighbor(ctx, current, true)
	if previousErr != nil && !errors.Is(previousErr, ErrNotFound) && !errors.Is(previousErr, sql.ErrNoRows) {
		return PublicArticleNavigation{}, previousErr
	}
	next, nextErr := r.neighbor(ctx, current, false)
	if nextErr != nil && !errors.Is(nextErr, ErrNotFound) && !errors.Is(nextErr, sql.ErrNoRows) {
		return PublicArticleNavigation{}, nextErr
	}
	related, err := r.related(ctx, current, relatedLimit)
	if err != nil {
		return PublicArticleNavigation{}, err
	}
	cards := make([]Article, 0, 2+len(related))
	previousIndex, nextIndex := -1, -1
	if previousErr == nil {
		previousIndex = len(cards)
		cards = append(cards, previous)
	}
	if nextErr == nil {
		nextIndex = len(cards)
		cards = append(cards, next)
	}
	relatedStart := len(cards)
	cards = append(cards, related...)
	if err := r.enrichPublishedTaxonomyBatch(ctx, cards); err != nil {
		return PublicArticleNavigation{}, err
	}
	var previousArticle, nextArticle *Article
	if previousIndex >= 0 {
		previousArticle = &cards[previousIndex]
	}
	if nextIndex >= 0 {
		nextArticle = &cards[nextIndex]
	}
	return PublicArticleNavigation{Previous: previousArticle, Next: nextArticle, Related: cards[relatedStart:]}, nil
}

func (r *Repository) neighbor(ctx context.Context, current Article, older bool) (Article, error) {
	operator, order := "<", "DESC"
	if !older {
		operator, order = ">", "ASC"
	}
	query := publicCardSelect + ` WHERE c.kind='article' AND c.status='published' AND c.trashed_at IS NULL
		AND (c.published_at,c.id) ` + operator + ` (?,?)
		ORDER BY c.published_at ` + order + `, c.id ` + order + ` LIMIT 1`
	return scanContent(r.database.Reader.QueryRowContext(ctx, query, current.PublishedAt.UnixMilli(), current.ID))
}

func (r *Repository) related(ctx context.Context, current Article, limit int) ([]Article, error) {
	tagPublicIDs := make([][]byte, 0, len(current.Tags))
	for _, tag := range current.Tags {
		tagPublicIDs = append(tagPublicIDs, tag.PublicID)
	}
	if ids, ready, err := r.organization.PublicRelatedArticleIDs(ctx, current.ID, current.publishedCategoryPublicID, tagPublicIDs, limit); err != nil {
		return nil, err
	} else if ready {
		return r.PublicArticleCardsByIDs(ctx, ids)
	}
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
	query := publicCardSelect + ` WHERE c.kind='article' AND c.status='published' AND c.trashed_at IS NULL AND c.id <> ? AND (` + strings.Join(conditions, " OR ") + `)
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

func (r *Repository) enrichTaxonomyBatch(ctx context.Context, contents []Article) error {
	ids := make([]int64, 0, len(contents))
	for _, content := range contents {
		if content.Kind == "article" {
			ids = append(ids, content.ID)
		}
	}
	taxonomies, err := r.organization.TaxonomiesByContentIDs(ctx, ids)
	if err != nil {
		return fmt.Errorf("read article taxonomy batch: %w", err)
	}
	for index := range contents {
		if taxonomy, ok := taxonomies[contents[index].ID]; ok {
			contents[index].Category = taxonomy.Category
			contents[index].Tags = taxonomy.Tags
		}
	}
	return nil
}

const contentSelect = `
	SELECT c.id, c.public_id, c.kind, c.status, c.slug, COALESCE(c.published_slug,''), c.title, c.excerpt, c.seo_title, c.seo_description, c.body_markdown,
	       c.cover_media_id, current_revision.cover_media_public_id, current_revision.cover_snapshot_version, NULL, NULL,
	       c.current_revision_id, c.published_revision_id, c.published_at,
	       (SELECT r.created_at FROM content_revisions r WHERE r.id = c.published_revision_id),
	       c.scheduled_at, c.withdrawn_at, c.trashed_at,
	       c.lock_version, c.created_at, c.updated_at
	FROM contents c
	LEFT JOIN content_revisions current_revision ON current_revision.id = c.current_revision_id`

// contentListSelect intentionally keeps the same scanner shape as
// contentSelect while replacing the potentially large canonical body with a
// constant. Editorial list pages only need metadata; detail/edit/archive
// paths continue to use contentSelect and receive the full body.
const contentListSelect = `
	SELECT c.id, c.public_id, c.kind, c.status, c.slug, COALESCE(c.published_slug,''), c.title, c.excerpt, c.seo_title, c.seo_description, '',
	       c.cover_media_id, current_revision.cover_media_public_id, current_revision.cover_snapshot_version, NULL, NULL,
	       c.current_revision_id, c.published_revision_id, c.published_at,
	       (SELECT r.created_at FROM content_revisions r WHERE r.id = c.published_revision_id),
	       c.scheduled_at, c.withdrawn_at, c.trashed_at,
	       c.lock_version, c.created_at, c.updated_at
	FROM contents c
	LEFT JOIN content_revisions current_revision ON current_revision.id = c.current_revision_id`

const publicContentSelect = `
	SELECT c.id, c.public_id, c.kind, c.status, c.published_slug, c.published_slug,
	       r.title, r.excerpt, r.seo_title, r.seo_description, r.body_markdown,
	       NULL, r.cover_media_public_id, r.cover_snapshot_version, r.category_public_id, r.tag_public_ids_json,
	       c.current_revision_id, c.published_revision_id, c.published_at,
	       r.created_at, c.scheduled_at, c.withdrawn_at, c.trashed_at,
	       c.lock_version, c.created_at, c.updated_at
	FROM contents c
	JOIN content_revisions r ON r.id = c.published_revision_id`

// publicCardSelect is the public list projection. Published revision
// metadata and immutable cover/taxonomy snapshots are retained; Markdown is
// deliberately not selected. A public article/page detail uses
// publicContentSelect instead.
const publicCardSelect = `
	SELECT c.id, c.public_id, c.kind, c.status, c.published_slug, c.published_slug,
	       r.title, r.excerpt, r.seo_title, r.seo_description, '',
	       NULL, r.cover_media_public_id, r.cover_snapshot_version, r.category_public_id, r.tag_public_ids_json,
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
	var currentCoverID, coverSnapshotVersion sql.NullInt64
	var coverMediaPublicID []byte
	var publishedCategoryPublicID []byte
	var publishedTagPublicIDsJSON sql.NullString
	var createdAt, updatedAt int64
	err := row.Scan(
		&content.ID, &content.PublicID, &content.Kind, &content.Status, &content.Slug,
		&content.PublishedSlug,
		&content.Title, &content.Excerpt, &content.SEOTitle, &content.SEODescription, &content.BodyMarkdown,
		&currentCoverID, &coverMediaPublicID, &coverSnapshotVersion, &publishedCategoryPublicID, &publishedTagPublicIDsJSON,
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
	content.CoverMediaID = currentCoverID.Int64
	content.CoverMediaPublicID = coverMediaPublicID
	content.CoverSnapshotVersion = int(coverSnapshotVersion.Int64)
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

func (r *Repository) enrichPublishedTaxonomyBatch(ctx context.Context, contents []Article) error {
	snapshots := make([]organization.TaxonomySnapshot, 0, len(contents))
	for _, content := range contents {
		if content.Kind != "article" {
			continue
		}
		snapshots = append(snapshots, organization.TaxonomySnapshot{
			ContentID: content.ID, CategoryPublicID: content.publishedCategoryPublicID, TagPublicIDsJSON: content.publishedTagPublicIDsJSON,
		})
	}
	taxonomies, err := r.organization.TaxonomiesBySnapshot(ctx, snapshots)
	if err != nil {
		return fmt.Errorf("read published taxonomy batch: %w", err)
	}
	for index := range contents {
		if taxonomy, ok := taxonomies[contents[index].ID]; ok {
			contents[index].Category = taxonomy.Category
			contents[index].Tags = taxonomy.Tags
		}
	}
	return nil
}

const maxContentListRows = 500

func uniquePositiveContentIDs(values []int64) []int64 {
	result := make([]int64, 0, len(values))
	seen := make(map[int64]struct{}, len(values))
	for _, value := range values {
		if value < 1 {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func insertRevision(ctx context.Context, tx *sql.Tx, contentID, revisionNumber int64, revision revisionInput, now time.Time) (int64, error) {
	result, err := tx.ExecContext(ctx, `
		INSERT INTO content_revisions (
			public_id, content_id, revision_number, title, slug, excerpt, seo_title, seo_description,
			body_markdown, cover_media_public_id, cover_snapshot_version, reason, category_public_id, tag_public_ids_json, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, revision.PublicID, contentID, revisionNumber, revision.Title, revision.Slug,
		revision.Excerpt, revision.SEOTitle, revision.SEODescription, revision.BodyMarkdown, nullableBytes(revision.CoverMediaPublicID), revision.CoverSnapshotVersion, revision.Reason, nullableBytes(revision.CategoryPublicID),
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

func (r *Repository) replaceMediaReferences(ctx context.Context, tx *sql.Tx, contentID int64, markdown string, coverMediaID int64, now time.Time) error {
	err := r.mediaReferences.ReplaceReferencesTx(ctx, tx, contentID, markdown, coverMediaID, now)
	var validation media.ValidationError
	if errors.As(err, &validation) {
		return ValidationError{Message: validation.Message}
	}
	return err
}

func validateCoverMediaTx(ctx context.Context, tx *sql.Tx, mediaID int64) ([]byte, error) {
	if mediaID == 0 {
		return nil, nil
	}
	if mediaID < 0 {
		return nil, ValidationError{Message: "封面媒体无效"}
	}
	var publicID []byte
	var mimeType string
	if err := tx.QueryRowContext(ctx, "SELECT public_id,mime_type FROM media WHERE id=?", mediaID).Scan(&publicID, &mimeType); errors.Is(err, sql.ErrNoRows) {
		return nil, ValidationError{Message: "封面媒体不存在"}
	} else if err != nil {
		return nil, err
	}
	if mimeType != "image/jpeg" && mimeType != "image/png" {
		return nil, ValidationError{Message: "封面只能使用 JPEG 或 PNG 图片"}
	}
	return publicID, nil
}

func (r *Repository) resolveCoverMediaTx(ctx context.Context, tx *sql.Tx, revision *revisionInput) error {
	if revision == nil || len(revision.CoverMediaPublicID) == 0 {
		return nil
	}
	if len(revision.CoverMediaPublicID) != 16 {
		return ValidationError{Message: "封面媒体公共 ID 无效"}
	}
	var mediaID int64
	err := tx.QueryRowContext(ctx, "SELECT id FROM media WHERE public_id=?", revision.CoverMediaPublicID).Scan(&mediaID)
	if errors.Is(err, sql.ErrNoRows) {
		return ValidationError{Message: "封面媒体不存在"}
	}
	if err != nil {
		return err
	}
	if revision.CoverMediaID > 0 && revision.CoverMediaID != mediaID {
		return ValidationError{Message: "封面媒体引用不一致"}
	}
	revision.CoverMediaID = mediaID
	return nil
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
