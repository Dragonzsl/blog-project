package publishing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mattn/go-sqlite3"
	"github.com/zhushilin/blog-project/internal/platform/database"
)

type Repository struct {
	database *database.DB
}

func NewRepository(database *database.DB) *Repository {
	return &Repository{database: database}
}

func (r *Repository) CreateDraft(ctx context.Context, publicID []byte, revision revisionInput, now time.Time) (Article, error) {
	tx, err := r.database.Writer.BeginTx(ctx, nil)
	if err != nil {
		return Article{}, fmt.Errorf("begin article creation: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		INSERT INTO contents (
			public_id, kind, status, slug, slug_key, title, excerpt, body_markdown,
			lock_version, created_at, updated_at
		) VALUES (?, 'article', 'draft', ?, ?, ?, ?, ?, 1, ?, ?)
	`, publicID, revision.Slug, revision.Slug, revision.Title, revision.Excerpt,
		revision.BodyMarkdown, millis(now), millis(now))
	if err != nil {
		return Article{}, mapWriteError(err)
	}
	contentID, err := result.LastInsertId()
	if err != nil {
		return Article{}, fmt.Errorf("read article ID: %w", err)
	}
	revisionID, err := insertRevision(ctx, tx, contentID, 1, revision, now)
	if err != nil {
		return Article{}, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE contents SET current_revision_id = ? WHERE id = ?", revisionID, contentID); err != nil {
		return Article{}, fmt.Errorf("attach current revision: %w", err)
	}
	if err := reservePath(ctx, tx, contentID, revision.Slug, "draft", now); err != nil {
		return Article{}, err
	}
	if err := insertAudit(ctx, tx, "publishing.article.created", publicID, now); err != nil {
		return Article{}, err
	}
	if err := tx.Commit(); err != nil {
		return Article{}, fmt.Errorf("commit article creation: %w", err)
	}
	return r.Article(ctx, contentID)
}

func (r *Repository) UpdateDraft(ctx context.Context, id, expectedVersion int64, revision revisionInput, now time.Time) (Article, error) {
	tx, err := r.database.Writer.BeginTx(ctx, nil)
	if err != nil {
		return Article{}, fmt.Errorf("begin article update: %w", err)
	}
	defer tx.Rollback()
	var currentSlug string
	var publishedRevision sql.NullInt64
	var currentVersion, nextRevision int64
	var publicID []byte
	err = tx.QueryRowContext(ctx, `
		SELECT slug, published_revision_id, lock_version, public_id,
		       (SELECT COALESCE(MAX(revision_number), 0) + 1 FROM content_revisions WHERE content_id = contents.id)
		FROM contents WHERE id = ? AND kind = 'article' AND trashed_at IS NULL
	`, id).Scan(&currentSlug, &publishedRevision, &currentVersion, &publicID, &nextRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return Article{}, ErrNotFound
	}
	if err != nil {
		return Article{}, fmt.Errorf("read article for update: %w", err)
	}
	if currentVersion != expectedVersion {
		return Article{}, ErrConflict
	}
	if revision.Slug != currentSlug && publishedRevision.Valid {
		return Article{}, ErrPublishedSlugImmutable
	}
	if revision.Slug != currentSlug {
		if err := reservePath(ctx, tx, id, revision.Slug, "draft", now); err != nil {
			return Article{}, err
		}
	}
	revisionID, err := insertRevision(ctx, tx, id, nextRevision, revision, now)
	if err != nil {
		return Article{}, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE contents
		SET slug = ?, slug_key = ?, title = ?, excerpt = ?, body_markdown = ?,
		    current_revision_id = ?, lock_version = lock_version + 1, updated_at = ?
		WHERE id = ? AND lock_version = ?
	`, revision.Slug, revision.Slug, revision.Title, revision.Excerpt,
		revision.BodyMarkdown, revisionID, millis(now), id, expectedVersion)
	if err != nil {
		return Article{}, mapWriteError(err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return Article{}, fmt.Errorf("check article update: %w", err)
	}
	if updated != 1 {
		return Article{}, ErrConflict
	}
	if err := insertAudit(ctx, tx, "publishing.article.saved", publicID, now); err != nil {
		return Article{}, err
	}
	if err := tx.Commit(); err != nil {
		return Article{}, fmt.Errorf("commit article update: %w", err)
	}
	return r.Article(ctx, id)
}

func (r *Repository) Publish(ctx context.Context, id, expectedVersion int64, now time.Time) (Article, error) {
	tx, err := r.database.Writer.BeginTx(ctx, nil)
	if err != nil {
		return Article{}, fmt.Errorf("begin article publication: %w", err)
	}
	defer tx.Rollback()
	var currentRevision, currentVersion int64
	var publicID []byte
	err = tx.QueryRowContext(ctx, `
		SELECT current_revision_id, lock_version, public_id
		FROM contents WHERE id = ? AND kind = 'article' AND trashed_at IS NULL
	`, id).Scan(&currentRevision, &currentVersion, &publicID)
	if errors.Is(err, sql.ErrNoRows) {
		return Article{}, ErrNotFound
	}
	if err != nil {
		return Article{}, fmt.Errorf("read article for publication: %w", err)
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
		    lock_version = lock_version + 1, updated_at = ?
		WHERE id = ? AND lock_version = ?
	`, millis(now), millis(now), id, expectedVersion)
	if err != nil {
		return Article{}, fmt.Errorf("publish article: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return Article{}, fmt.Errorf("check article publication: %w", err)
	}
	if updated != 1 {
		return Article{}, ErrConflict
	}
	if _, err := tx.ExecContext(ctx, "UPDATE reserved_paths SET reason = 'published' WHERE content_id = ? AND path_key = (SELECT '/posts/' || slug_key FROM contents WHERE id = ?)", id, id); err != nil {
		return Article{}, fmt.Errorf("publish reserved path: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE system_state SET render_epoch = render_epoch + 1, updated_at = ? WHERE id = 1", millis(now)); err != nil {
		return Article{}, fmt.Errorf("invalidate public rendering: %w", err)
	}
	if err := insertAudit(ctx, tx, "publishing.article.published", publicID, now); err != nil {
		return Article{}, err
	}
	if err := tx.Commit(); err != nil {
		return Article{}, fmt.Errorf("commit article publication: %w", err)
	}
	return r.Article(ctx, id)
}

func (r *Repository) Article(ctx context.Context, id int64) (Article, error) {
	row := r.database.Reader.QueryRowContext(ctx, articleSelect+" WHERE c.id = ? AND c.kind = 'article' AND c.trashed_at IS NULL", id)
	return scanArticle(row)
}

func (r *Repository) PublicArticle(ctx context.Context, slug string) (Article, error) {
	row := r.database.Reader.QueryRowContext(ctx, `
		SELECT c.id, c.public_id, c.status, c.slug,
		       r.title, r.excerpt, r.body_markdown,
		       c.current_revision_id, c.published_revision_id, c.published_at,
		       c.lock_version, c.created_at, c.updated_at
		FROM contents c
		JOIN content_revisions r ON r.id = c.published_revision_id
		WHERE c.slug_key = ? AND c.kind = 'article' AND c.status = 'published' AND c.trashed_at IS NULL
	`, slug)
	return scanArticle(row)
}

func (r *Repository) Articles(ctx context.Context) ([]Article, error) {
	rows, err := r.database.Reader.QueryContext(ctx, articleSelect+" WHERE c.kind = 'article' AND c.trashed_at IS NULL ORDER BY c.updated_at DESC, c.id DESC")
	if err != nil {
		return nil, fmt.Errorf("list articles: %w", err)
	}
	defer rows.Close()
	var articles []Article
	for rows.Next() {
		article, err := scanArticle(rows)
		if err != nil {
			return nil, err
		}
		articles = append(articles, article)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list articles: %w", err)
	}
	return articles, nil
}

const articleSelect = `
	SELECT c.id, c.public_id, c.status, c.slug, c.title, c.excerpt, c.body_markdown,
	       c.current_revision_id, c.published_revision_id, c.published_at,
	       c.lock_version, c.created_at, c.updated_at
	FROM contents c`

type scanner interface {
	Scan(dest ...any) error
}

func scanArticle(row scanner) (Article, error) {
	var article Article
	var currentRevision, publishedRevision sql.NullInt64
	var publishedAt sql.NullInt64
	var createdAt, updatedAt int64
	err := row.Scan(
		&article.ID,
		&article.PublicID,
		&article.Status,
		&article.Slug,
		&article.Title,
		&article.Excerpt,
		&article.BodyMarkdown,
		&currentRevision,
		&publishedRevision,
		&publishedAt,
		&article.LockVersion,
		&createdAt,
		&updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Article{}, ErrNotFound
	}
	if err != nil {
		return Article{}, fmt.Errorf("scan article: %w", err)
	}
	article.CurrentRevisionID = currentRevision.Int64
	article.PublishedRevisionID = publishedRevision.Int64
	if publishedAt.Valid {
		value := fromMillis(publishedAt.Int64)
		article.PublishedAt = &value
	}
	article.CreatedAt = fromMillis(createdAt)
	article.UpdatedAt = fromMillis(updatedAt)
	return article, nil
}

func insertRevision(ctx context.Context, tx *sql.Tx, contentID, revisionNumber int64, revision revisionInput, now time.Time) (int64, error) {
	result, err := tx.ExecContext(ctx, `
		INSERT INTO content_revisions (
			public_id, content_id, revision_number, title, slug, excerpt,
			body_markdown, reason, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, revision.PublicID, contentID, revisionNumber, revision.Title, revision.Slug,
		revision.Excerpt, revision.BodyMarkdown, revision.Reason, millis(now))
	if err != nil {
		return 0, fmt.Errorf("save article revision: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read revision ID: %w", err)
	}
	return id, nil
}

func reservePath(ctx context.Context, tx *sql.Tx, contentID int64, slug, reason string, now time.Time) error {
	path := "/posts/" + slug
	result, err := tx.ExecContext(ctx, `
		INSERT INTO reserved_paths (path, path_key, content_id, reason, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(path_key) DO NOTHING
	`, path, strings.ToLower(path), contentID, reason, millis(now))
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
	if err := tx.QueryRowContext(ctx, "SELECT content_id FROM reserved_paths WHERE path_key = ?", strings.ToLower(path)).Scan(&existingContentID); err != nil {
		return fmt.Errorf("read reserved path owner: %w", err)
	}
	if !existingContentID.Valid || existingContentID.Int64 != contentID {
		return ErrSlugUnavailable
	}
	return nil
}

func insertAudit(ctx context.Context, tx *sql.Tx, action string, publicID []byte, now time.Time) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO audit_entries (action, object_kind, object_public_id, result, context_json, created_at)
		VALUES (?, 'article', ?, 'succeeded', '{}', ?)
	`, action, publicID, millis(now))
	if err != nil {
		return fmt.Errorf("save publishing audit: %w", err)
	}
	return nil
}

func mapWriteError(err error) error {
	var sqliteError sqlite3.Error
	if errors.As(err, &sqliteError) && sqliteError.ExtendedCode == sqlite3.ErrConstraintUnique {
		return ErrSlugUnavailable
	}
	return fmt.Errorf("write article: %w", err)
}

func millis(value time.Time) int64 {
	return value.UTC().UnixMilli()
}

func fromMillis(value int64) time.Time {
	return time.UnixMilli(value).UTC()
}
