package publishing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/mattn/go-sqlite3"
	"github.com/zhushilin/blog-project/internal/media"
	"github.com/zhushilin/blog-project/internal/organization"
	"github.com/zhushilin/blog-project/internal/platform/database"
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
			public_id, kind, status, slug, slug_key, title, excerpt, body_markdown,
			lock_version, created_at, updated_at
		) VALUES (?, ?, 'draft', ?, ?, ?, ?, ?, 1, ?, ?)
	`, publicID, kind, revision.Slug, revision.SlugKey, revision.Title, revision.Excerpt,
		revision.BodyMarkdown, millis(now), millis(now))
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
	var publishedRevision sql.NullInt64
	var currentVersion, nextRevision int64
	var publicID []byte
	err = tx.QueryRowContext(ctx, `
		SELECT slug_key, published_revision_id, lock_version, public_id,
		       (SELECT COALESCE(MAX(revision_number), 0) + 1 FROM content_revisions WHERE content_id = contents.id)
		FROM contents WHERE id = ? AND kind = ? AND trashed_at IS NULL
	`, id, kind).Scan(&currentSlugKey, &publishedRevision, &currentVersion, &publicID, &nextRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return Article{}, ErrNotFound
	}
	if err != nil {
		return Article{}, fmt.Errorf("read %s for update: %w", kind, err)
	}
	if currentVersion != expectedVersion {
		return Article{}, ErrConflict
	}
	if revision.SlugKey != currentSlugKey && publishedRevision.Valid {
		return Article{}, ErrPublishedSlugImmutable
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
		SET slug = ?, slug_key = ?, title = ?, excerpt = ?, body_markdown = ?,
		    current_revision_id = ?, lock_version = lock_version + 1, updated_at = ?
		WHERE id = ? AND kind = ? AND lock_version = ?
	`, revision.Slug, revision.SlugKey, revision.Title, revision.Excerpt,
		revision.BodyMarkdown, revisionID, millis(now), id, kind, expectedVersion)
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
	var slugKey, bodyMarkdown string
	err = tx.QueryRowContext(ctx, `SELECT current_revision_id, lock_version, public_id, slug_key, body_markdown FROM contents WHERE id = ? AND kind = ? AND trashed_at IS NULL`, id, kind).Scan(&currentRevision, &currentVersion, &publicID, &slugKey, &bodyMarkdown)
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
	content, err := scanContent(r.database.Reader.QueryRowContext(ctx, publicContentSelect+` WHERE c.slug_key = ? AND c.kind = ? AND c.status = 'published' AND c.trashed_at IS NULL`, slugKey, kind))
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
	rows, err := r.database.Reader.QueryContext(ctx, publicContentSelect+` WHERE c.kind = 'article' AND c.status = 'published' AND c.trashed_at IS NULL ORDER BY c.published_at DESC, c.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list published articles: %w", err)
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
	return articles, rows.Err()
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
	SELECT c.id, c.public_id, c.kind, c.status, c.slug, c.title, c.excerpt, c.body_markdown,
	       NULL, NULL,
	       c.current_revision_id, c.published_revision_id, c.published_at,
	       (SELECT r.created_at FROM content_revisions r WHERE r.id = c.published_revision_id),
	       c.scheduled_at, c.withdrawn_at, c.trashed_at,
	       c.lock_version, c.created_at, c.updated_at
	FROM contents c`

const publicContentSelect = `
	SELECT c.id, c.public_id, c.kind, c.status, c.slug,
	       r.title, r.excerpt, r.body_markdown,
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
		&content.Title, &content.Excerpt, &content.BodyMarkdown,
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
			public_id, content_id, revision_number, title, slug, excerpt,
			body_markdown, reason, category_public_id, tag_public_ids_json, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, revision.PublicID, contentID, revisionNumber, revision.Title, revision.Slug,
		revision.Excerpt, revision.BodyMarkdown, revision.Reason, nullableBytes(revision.CategoryPublicID),
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
	var existingContentID sql.NullInt64
	if err := tx.QueryRowContext(ctx, "SELECT content_id FROM reserved_paths WHERE path_key = ?", pathKey).Scan(&existingContentID); err != nil {
		return fmt.Errorf("read reserved path owner: %w", err)
	}
	if !existingContentID.Valid || existingContentID.Int64 != contentID {
		return ErrSlugUnavailable
	}
	return nil
}

func publicPath(kind, slug string) string {
	if kind == "page" {
		return "/" + slug
	}
	return "/posts/" + slug
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
