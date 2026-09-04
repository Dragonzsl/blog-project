package publishing

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/zhushilin/blog-project/internal/extensions"
)

func (r *Repository) Revisions(ctx context.Context, kind string, contentID int64) ([]Revision, error) {
	rows, err := r.database.Reader.QueryContext(ctx, `
		SELECT revision.id, revision.public_id, revision.content_id, revision.revision_number,
		       revision.title, revision.slug, revision.excerpt, '', '', '',
		       revision.category_public_id, revision.tag_public_ids_json, revision.reason,
		       revision.is_publication_checkpoint, revision.created_at
		FROM content_revisions revision
		JOIN contents content ON content.id = revision.content_id
		WHERE revision.content_id = ? AND content.kind = ? AND content.trashed_at IS NULL
		ORDER BY revision.revision_number DESC`, contentID, kind)
	if err != nil {
		return nil, fmt.Errorf("list content revisions: %w", err)
	}
	defer rows.Close()
	var revisions []Revision
	for rows.Next() {
		revision, err := scanRevision(rows)
		if err != nil {
			return nil, err
		}
		revisions = append(revisions, revision)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(revisions) == 0 {
		return nil, ErrNotFound
	}
	return revisions, nil
}

func (r *Repository) Revision(ctx context.Context, kind string, contentID, revisionID int64) (Revision, error) {
	revision, err := scanRevision(r.database.Reader.QueryRowContext(ctx, `
		SELECT revision.id, revision.public_id, revision.content_id, revision.revision_number,
		       revision.title, revision.slug, revision.excerpt, revision.seo_title, revision.seo_description, revision.body_markdown,
		       revision.category_public_id, revision.tag_public_ids_json, revision.reason,
		       revision.is_publication_checkpoint, revision.created_at
		FROM content_revisions revision
		JOIN contents content ON content.id = revision.content_id
		WHERE revision.id = ? AND revision.content_id = ? AND content.kind = ? AND content.trashed_at IS NULL`, revisionID, contentID, kind))
	if err != nil {
		return Revision{}, err
	}
	return revision, nil
}

func scanRevision(row scanner) (Revision, error) {
	var revision Revision
	var categoryPublicID []byte
	var tagPublicIDs sql.NullString
	var checkpoint bool
	var createdAt int64
	err := row.Scan(&revision.ID, &revision.PublicID, &revision.ContentID, &revision.Number,
		&revision.Title, &revision.Slug, &revision.Excerpt, &revision.SEOTitle, &revision.SEODescription, &revision.BodyMarkdown,
		&categoryPublicID, &tagPublicIDs, &revision.Reason, &checkpoint, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Revision{}, ErrNotFound
	}
	if err != nil {
		return Revision{}, fmt.Errorf("scan content revision: %w", err)
	}
	revision.CategoryPublicID = categoryPublicID
	revision.TagPublicIDsJSON = tagPublicIDs.String
	revision.IsPublicationCheckpoint = checkpoint
	revision.CreatedAt = fromMillis(createdAt)
	return revision, nil
}

func (r *Repository) SaveEditingSnapshot(ctx context.Context, kind string, snapshot EditingSnapshot, now time.Time) error {
	tagIDs, err := json.Marshal(snapshot.Input.TagIDs)
	if err != nil {
		return err
	}
	result, err := r.database.Writer.ExecContext(ctx, `
		INSERT INTO editing_snapshots (
			content_id, base_lock_version, browser_version, title, slug, excerpt,
			seo_title, seo_description, body_markdown, category_id, tag_ids_json, updated_at
		)
		SELECT id, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
		FROM contents
		WHERE id = ? AND kind = ? AND lock_version = ? AND trashed_at IS NULL
		ON CONFLICT(content_id) DO UPDATE SET
			base_lock_version=excluded.base_lock_version,
			browser_version=excluded.browser_version,
			title=excluded.title, slug=excluded.slug, excerpt=excluded.excerpt,
			seo_title=excluded.seo_title, seo_description=excluded.seo_description,
			body_markdown=excluded.body_markdown, category_id=excluded.category_id,
			tag_ids_json=excluded.tag_ids_json, updated_at=excluded.updated_at
		WHERE editing_snapshots.base_lock_version < excluded.base_lock_version
		   OR (editing_snapshots.base_lock_version = excluded.base_lock_version
		       AND editing_snapshots.browser_version < excluded.browser_version)`,
		snapshot.BaseLockVersion, snapshot.BrowserVersion, snapshot.Input.Title, snapshot.Input.Slug,
		snapshot.Input.Excerpt, snapshot.Input.SEOTitle, snapshot.Input.SEODescription, snapshot.Input.BodyMarkdown, nullableInt64(snapshot.Input.CategoryID),
		string(tagIDs), millis(now), snapshot.ContentID, kind, snapshot.BaseLockVersion)
	if err != nil {
		return fmt.Errorf("save editing snapshot: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrConflict
	}
	return nil
}

func (r *Repository) EditingSnapshot(ctx context.Context, kind string, contentID int64) (EditingSnapshot, error) {
	var snapshot EditingSnapshot
	var categoryID sql.NullInt64
	var tagIDsJSON string
	var updatedAt int64
	err := r.database.Reader.QueryRowContext(ctx, `
		SELECT snapshot.content_id, snapshot.base_lock_version, snapshot.browser_version,
		       snapshot.title, snapshot.slug, snapshot.excerpt, snapshot.seo_title, snapshot.seo_description, snapshot.body_markdown,
		       snapshot.category_id, snapshot.tag_ids_json, snapshot.updated_at
		FROM editing_snapshots snapshot
		JOIN contents content ON content.id = snapshot.content_id
		WHERE snapshot.content_id = ? AND content.kind = ? AND content.trashed_at IS NULL`, contentID, kind).Scan(
		&snapshot.ContentID, &snapshot.BaseLockVersion, &snapshot.BrowserVersion,
		&snapshot.Input.Title, &snapshot.Input.Slug, &snapshot.Input.Excerpt,
		&snapshot.Input.SEOTitle, &snapshot.Input.SEODescription, &snapshot.Input.BodyMarkdown, &categoryID, &tagIDsJSON, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return EditingSnapshot{}, ErrNotFound
	}
	if err != nil {
		return EditingSnapshot{}, fmt.Errorf("read editing snapshot: %w", err)
	}
	snapshot.Input.CategoryID = categoryID.Int64
	if err := json.Unmarshal([]byte(tagIDsJSON), &snapshot.Input.TagIDs); err != nil {
		return EditingSnapshot{}, fmt.Errorf("decode editing snapshot tags: %w", err)
	}
	snapshot.UpdatedAt = fromMillis(updatedAt)
	return snapshot, nil
}

func (r *Repository) Schedule(ctx context.Context, kind string, id, expectedVersion int64, publishAt, now time.Time) (Article, error) {
	tx, err := r.database.Writer.BeginTx(ctx, nil)
	if err != nil {
		return Article{}, err
	}
	defer tx.Rollback()
	var status string
	var publicID []byte
	if err := tx.QueryRowContext(ctx, `SELECT status, public_id FROM contents WHERE id=? AND kind=? AND lock_version=? AND trashed_at IS NULL`, id, kind, expectedVersion).Scan(&status, &publicID); errors.Is(err, sql.ErrNoRows) {
		return Article{}, ErrConflict
	} else if err != nil {
		return Article{}, err
	}
	if status != "draft" && status != "scheduled" {
		return Article{}, ErrInvalidTransition
	}
	result, err := tx.ExecContext(ctx, `UPDATE contents SET status='scheduled',scheduled_at=?,lock_version=lock_version+1,updated_at=? WHERE id=? AND kind=? AND lock_version=?`, millis(publishAt), millis(now), id, kind, expectedVersion)
	if err != nil {
		return Article{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return Article{}, ErrConflict
	}
	if err := rebaseEditingSnapshot(ctx, tx, id, expectedVersion); err != nil {
		return Article{}, err
	}
	if err := insertAudit(ctx, tx, "publishing."+kind+".scheduled", kind, publicID, now); err != nil {
		return Article{}, err
	}
	if err := tx.Commit(); err != nil {
		return Article{}, err
	}
	return r.Content(ctx, kind, id)
}

func (r *Repository) Unpublish(ctx context.Context, kind string, id, expectedVersion int64, now time.Time) (Article, error) {
	tx, err := r.database.Writer.BeginTx(ctx, nil)
	if err != nil {
		return Article{}, err
	}
	defer tx.Rollback()
	var status, body string
	var publicID []byte
	if err := tx.QueryRowContext(ctx, `SELECT status,public_id,body_markdown FROM contents WHERE id=? AND kind=? AND lock_version=? AND trashed_at IS NULL`, id, kind, expectedVersion).Scan(&status, &publicID, &body); errors.Is(err, sql.ErrNoRows) {
		return Article{}, ErrConflict
	} else if err != nil {
		return Article{}, err
	}
	if status != "published" {
		return Article{}, ErrInvalidTransition
	}
	result, err := tx.ExecContext(ctx, `UPDATE contents SET status='draft',scheduled_at=NULL,withdrawn_at=?,lock_version=lock_version+1,updated_at=? WHERE id=? AND kind=? AND lock_version=? AND status='published' AND trashed_at IS NULL`, millis(now), millis(now), id, kind, expectedVersion)
	if err != nil {
		return Article{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return Article{}, ErrConflict
	}
	if err := rebaseEditingSnapshot(ctx, tx, id, expectedVersion); err != nil {
		return Article{}, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE system_state SET render_epoch=render_epoch+1,updated_at=? WHERE id=1", millis(now)); err != nil {
		return Article{}, err
	}
	if err := r.replaceMediaReferences(ctx, tx, id, body, now); err != nil {
		return Article{}, err
	}
	if err := insertAudit(ctx, tx, "publishing."+kind+".unpublished", kind, publicID, now); err != nil {
		return Article{}, err
	}
	if err := tx.Commit(); err != nil {
		return Article{}, err
	}
	return r.Content(ctx, kind, id)
}

func (r *Repository) CancelSchedule(ctx context.Context, kind string, id, expectedVersion int64, now time.Time) (Article, error) {
	tx, err := r.database.Writer.BeginTx(ctx, nil)
	if err != nil {
		return Article{}, err
	}
	defer tx.Rollback()
	var publicID []byte
	if err := tx.QueryRowContext(ctx, `SELECT public_id FROM contents WHERE id=? AND kind=? AND trashed_at IS NULL`, id, kind).Scan(&publicID); errors.Is(err, sql.ErrNoRows) {
		return Article{}, ErrNotFound
	} else if err != nil {
		return Article{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE contents SET status='draft',scheduled_at=NULL,lock_version=lock_version+1,updated_at=? WHERE id=? AND kind=? AND lock_version=? AND status='scheduled' AND trashed_at IS NULL`, millis(now), id, kind, expectedVersion)
	if err != nil {
		return Article{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return Article{}, ErrInvalidTransition
	}
	if err := rebaseEditingSnapshot(ctx, tx, id, expectedVersion); err != nil {
		return Article{}, err
	}
	if err := insertAudit(ctx, tx, "publishing."+kind+".schedule_cancelled", kind, publicID, now); err != nil {
		return Article{}, err
	}
	if err := tx.Commit(); err != nil {
		return Article{}, err
	}
	return r.Content(ctx, kind, id)
}

func (r *Repository) Trash(ctx context.Context, kind string, id, expectedVersion int64, now time.Time) error {
	tx, err := r.database.Writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status string
	var publicID []byte
	err = tx.QueryRowContext(ctx, `SELECT status,public_id FROM contents WHERE id=? AND kind=? AND lock_version=? AND trashed_at IS NULL`, id, kind, expectedVersion).Scan(&status, &publicID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	withdrawnAt := any(nil)
	if status == "published" {
		withdrawnAt = millis(now)
	}
	result, err := tx.ExecContext(ctx, `UPDATE contents SET status='draft',scheduled_at=NULL,withdrawn_at=COALESCE(?,withdrawn_at),trashed_at=?,lock_version=lock_version+1,updated_at=? WHERE id=? AND kind=? AND lock_version=? AND trashed_at IS NULL`, withdrawnAt, millis(now), millis(now), id, kind, expectedVersion)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return ErrConflict
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM editing_snapshots WHERE content_id=?", id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE system_state SET render_epoch=render_epoch+1,updated_at=? WHERE id=1", millis(now)); err != nil {
		return err
	}
	if err := insertAudit(ctx, tx, "publishing."+kind+".trashed", kind, publicID, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) RestoreFromTrash(ctx context.Context, id int64, now time.Time) (Article, error) {
	tx, err := r.database.Writer.BeginTx(ctx, nil)
	if err != nil {
		return Article{}, err
	}
	defer tx.Rollback()
	var kind string
	var publicID []byte
	err = tx.QueryRowContext(ctx, `SELECT kind,public_id FROM contents WHERE id=? AND trashed_at IS NOT NULL`, id).Scan(&kind, &publicID)
	if errors.Is(err, sql.ErrNoRows) {
		return Article{}, ErrNotFound
	}
	if err != nil {
		return Article{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE contents SET trashed_at=NULL,status='draft',scheduled_at=NULL,lock_version=lock_version+1,updated_at=? WHERE id=? AND trashed_at IS NOT NULL`, millis(now), id)
	if err != nil {
		return Article{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return Article{}, ErrNotFound
	}
	if err := insertAudit(ctx, tx, "publishing."+kind+".restored_from_trash", kind, publicID, now); err != nil {
		return Article{}, err
	}
	if err := tx.Commit(); err != nil {
		return Article{}, err
	}
	return r.Content(ctx, kind, id)
}

func (r *Repository) PublishDue(ctx context.Context, now time.Time, limit int) (int, error) {
	rows, err := r.database.Reader.QueryContext(ctx, `SELECT id,kind FROM contents WHERE status='scheduled' AND scheduled_at<=? AND trashed_at IS NULL ORDER BY scheduled_at,id LIMIT ?`, millis(now), limit)
	if err != nil {
		return 0, err
	}
	type dueContent struct {
		id   int64
		kind string
	}
	var due []dueContent
	for rows.Next() {
		var item dueContent
		if err := rows.Scan(&item.id, &item.kind); err != nil {
			rows.Close()
			return 0, err
		}
		due = append(due, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	published := 0
	var problems []error
	for _, item := range due {
		claimed, err := r.publishScheduled(ctx, item.kind, item.id, now)
		if err != nil {
			problems = append(problems, fmt.Errorf("publish scheduled content %d: %w", item.id, err))
			continue
		}
		if claimed {
			published++
		}
	}
	return published, errors.Join(problems...)
}

func (r *Repository) publishScheduled(ctx context.Context, kind string, id int64, now time.Time) (bool, error) {
	tx, err := r.database.Writer.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var revisionID int64
	var publicID []byte
	var body, slug, slugKey string
	var publishedSlug, publishedSlugKey sql.NullString
	var scheduledAt, lockVersion int64
	err = tx.QueryRowContext(ctx, `SELECT current_revision_id,public_id,body_markdown,slug,slug_key,published_slug,published_slug_key,scheduled_at,lock_version FROM contents WHERE id=? AND kind=? AND status='scheduled' AND scheduled_at<=? AND trashed_at IS NULL`, id, kind, millis(now)).Scan(&revisionID, &publicID, &body, &slug, &slugKey, &publishedSlug, &publishedSlugKey, &scheduledAt, &lockVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE content_revisions SET is_publication_checkpoint=1 WHERE id=? AND content_id=?`, revisionID, id); err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE contents SET status='published',published_revision_id=current_revision_id,published_slug=slug,published_slug_key=slug_key,published_at=COALESCE(published_at,?),scheduled_at=NULL,withdrawn_at=NULL,lock_version=lock_version+1,updated_at=? WHERE id=? AND kind=? AND status='scheduled' AND scheduled_at<=? AND trashed_at IS NULL`, scheduledAt, millis(now), id, kind, millis(now))
	if err != nil {
		return false, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return false, nil
	}
	if err := rebaseEditingSnapshot(ctx, tx, id, lockVersion); err != nil {
		return false, err
	}
	if publishedSlugKey.Valid && publishedSlugKey.String != slugKey {
		if err := saveRedirect(ctx, tx, publicPath(kind, publishedSlug.String), publicPath(kind, publishedSlugKey.String), publicPath(kind, slug), publicPath(kind, slugKey), "content_slug", now); err != nil {
			return false, err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE reserved_paths SET reason='historical' WHERE content_id=? AND path_key=?", id, publicPath(kind, publishedSlugKey.String)); err != nil {
			return false, err
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE reserved_paths SET reason='published' WHERE content_id=? AND path_key=?", id, publicPath(kind, slugKey)); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE system_state SET render_epoch=render_epoch+1,updated_at=? WHERE id=1", millis(now)); err != nil {
		return false, err
	}
	if err := r.replaceMediaReferences(ctx, tx, id, body, now); err != nil {
		return false, err
	}
	if err := insertAudit(ctx, tx, "publishing."+kind+".scheduled_published", kind, publicID, now); err != nil {
		return false, err
	}
	if r.eventRecorder != nil {
		if err := r.eventRecorder.RecordEventTx(ctx, tx, extensions.Event{
			Name: "ContentPublished.v1", Version: 1, ObjectID: append([]byte(nil), publicID...),
			Payload: map[string]any{"kind": kind, "slug": slug}, OccurredAt: now,
		}); err != nil {
			return false, fmt.Errorf("record scheduled publication event: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (r *Repository) PurgeExpiredTrash(ctx context.Context, cutoff, now time.Time, limit int) (int, error) {
	rows, err := r.database.Reader.QueryContext(ctx, `SELECT id FROM contents WHERE trashed_at IS NOT NULL AND trashed_at<=? ORDER BY trashed_at,id LIMIT ?`, millis(cutoff), limit)
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	removed := 0
	var problems []error
	for _, id := range ids {
		tx, err := r.database.Writer.BeginTx(ctx, nil)
		if err != nil {
			problems = append(problems, fmt.Errorf("begin purge content %d: %w", id, err))
			continue
		}
		var kind string
		var publicID []byte
		err = tx.QueryRowContext(ctx, `SELECT kind,public_id FROM contents WHERE id=? AND trashed_at IS NOT NULL AND trashed_at<=?`, id, millis(cutoff)).Scan(&kind, &publicID)
		if errors.Is(err, sql.ErrNoRows) {
			tx.Rollback()
			continue
		}
		if err != nil {
			tx.Rollback()
			problems = append(problems, fmt.Errorf("read purge content %d: %w", id, err))
			continue
		}
		result, err := tx.ExecContext(ctx, "DELETE FROM contents WHERE id=? AND trashed_at IS NOT NULL AND trashed_at<=?", id, millis(cutoff))
		if err != nil {
			tx.Rollback()
			problems = append(problems, fmt.Errorf("delete purged content %d: %w", id, err))
			continue
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			tx.Rollback()
			continue
		}
		if err := insertAudit(ctx, tx, "publishing."+kind+".purged", kind, publicID, now); err != nil {
			tx.Rollback()
			problems = append(problems, fmt.Errorf("audit purged content %d: %w", id, err))
			continue
		}
		if err := tx.Commit(); err != nil {
			problems = append(problems, fmt.Errorf("commit purged content %d: %w", id, err))
			continue
		}
		removed++
	}
	return removed, errors.Join(problems...)
}

func pruneRevisions(ctx context.Context, tx *sql.Tx, contentID int64, limit int) error {
	_, err := tx.ExecContext(ctx, `
		DELETE FROM content_revisions
		WHERE content_id=? AND is_publication_checkpoint=0
		  AND id NOT IN (
			SELECT id FROM content_revisions
			WHERE content_id=? AND is_publication_checkpoint=0
			ORDER BY revision_number DESC LIMIT ?
		  )`, contentID, contentID, limit)
	return err
}

func nullableInt64(value int64) any {
	if value < 1 {
		return nil
	}
	return value
}

func rebaseEditingSnapshot(ctx context.Context, tx *sql.Tx, contentID, previousLockVersion int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE editing_snapshots SET base_lock_version=? WHERE content_id=? AND base_lock_version=?`, previousLockVersion+1, contentID, previousLockVersion)
	return err
}
