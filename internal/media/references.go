package media

import (
	"context"
	"database/sql"
	"regexp"
	"time"

	platformid "github.com/zhushilin/blog-project/internal/platform/id"
)

var mediaURLPattern = regexp.MustCompile(`/media/([0-9a-fA-F]{32})/(?:original|w[0-9]+)/`)

type ReferenceRepository struct{}

func NewReferenceRepository() *ReferenceRepository { return &ReferenceRepository{} }

// ReplaceReferencesTx rebuilds references for the editable/current
// representation and, while published, the immutable public representation.
func (r *ReferenceRepository) ReplaceReferencesTx(ctx context.Context, tx *sql.Tx, contentID int64, markdown string, coverMediaID int64, now time.Time) error {
	var publishedBody sql.NullString
	var publishedCover []byte
	err := tx.QueryRowContext(ctx, `SELECT CASE WHEN c.status='published' THEN pr.body_markdown END, CASE WHEN c.status='published' THEN pr.cover_media_public_id END FROM contents c LEFT JOIN content_revisions pr ON pr.id=c.published_revision_id WHERE c.id=?`, contentID).Scan(&publishedBody, &publishedCover)
	if err != nil && err != sql.ErrNoRows {
		return err
	}

	if _, err := tx.ExecContext(ctx, "DELETE FROM media_references WHERE content_id=?", contentID); err != nil {
		return err
	}
	createdAt := now.UTC().UnixMilli()
	bodyIDs, err := resolveMediaIDs(ctx, tx, markdown, publishedBody)
	if err != nil {
		return err
	}
	for _, mediaID := range bodyIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO media_references(content_id,media_id,relation,created_at) VALUES(?,?,'body',?)`, contentID, mediaID, createdAt); err != nil {
			return err
		}
	}

	coverIDs := make(map[int64]struct{})
	if coverMediaID > 0 {
		coverIDs[coverMediaID] = struct{}{}
	}
	if len(publishedCover) > 0 {
		mediaID, err := mediaIDByPublicID(ctx, tx, publishedCover)
		if err != nil {
			return err
		}
		coverIDs[mediaID] = struct{}{}
	}
	for mediaID := range coverIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO media_references(content_id,media_id,relation,created_at) VALUES(?,?,'cover',?)`, contentID, mediaID, createdAt); err != nil {
			return err
		}
	}
	return nil
}

// ReplaceBodyReferencesTx remains available for callers that only need the
// body-reference operation. Publishing writes use ReplaceReferencesTx.
func (r *ReferenceRepository) ReplaceBodyReferencesTx(ctx context.Context, tx *sql.Tx, contentID int64, markdown string, now time.Time) error {
	var publishedBody sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT CASE WHEN c.status='published' THEN pr.body_markdown END FROM contents c LEFT JOIN content_revisions pr ON pr.id=c.published_revision_id WHERE c.id=?`, contentID).Scan(&publishedBody)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM media_references WHERE content_id=? AND relation='body'", contentID); err != nil {
		return err
	}
	ids, err := resolveMediaIDs(ctx, tx, markdown, publishedBody)
	if err != nil {
		return err
	}
	for _, mediaID := range ids {
		if _, err := tx.ExecContext(ctx, `INSERT INTO media_references(content_id,media_id,relation,created_at) VALUES(?,?,'body',?)`, contentID, mediaID, now.UTC().UnixMilli()); err != nil {
			return err
		}
	}
	return nil
}

func resolveMediaIDs(ctx context.Context, tx *sql.Tx, markdown string, published sql.NullString) ([]int64, error) {
	if published.Valid {
		markdown += "\n" + published.String
	}
	seen := make(map[string]struct{})
	var ids []int64
	for _, match := range mediaURLPattern.FindAllStringSubmatch(markdown, -1) {
		value := match[1]
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		publicID, err := platformid.DecodePublicID(value)
		if err != nil {
			continue
		}
		mediaID, err := mediaIDByPublicID(ctx, tx, publicID)
		if err != nil {
			return nil, err
		}
		ids = append(ids, mediaID)
	}
	return ids, nil
}

func mediaIDByPublicID(ctx context.Context, tx *sql.Tx, publicID []byte) (int64, error) {
	var mediaID int64
	if err := tx.QueryRowContext(ctx, "SELECT id FROM media WHERE public_id=?", publicID).Scan(&mediaID); err == sql.ErrNoRows {
		return 0, ValidationError{Message: "正文或封面引用了不存在的媒体"}
	} else if err != nil {
		return 0, err
	}
	return mediaID, nil
}
