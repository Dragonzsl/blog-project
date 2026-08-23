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

func (r *ReferenceRepository) ReplaceBodyReferencesTx(ctx context.Context, tx *sql.Tx, contentID int64, markdown string, now time.Time) error {
	var publishedBody sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT CASE WHEN c.status='published' THEN pr.body_markdown END FROM contents c LEFT JOIN content_revisions pr ON pr.id=c.published_revision_id WHERE c.id=?`, contentID).Scan(&publishedBody)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if publishedBody.Valid {
		markdown += "\n" + publishedBody.String
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM media_references WHERE content_id=? AND relation='body'", contentID); err != nil {
		return err
	}
	seen := make(map[string]struct{})
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
		var mediaID int64
		if err := tx.QueryRowContext(ctx, "SELECT id FROM media WHERE public_id=?", publicID).Scan(&mediaID); err == sql.ErrNoRows {
			return ValidationError{Message: "正文引用了不存在的媒体"}
		} else if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO media_references(content_id,media_id,relation,created_at) VALUES(?,?,'body',?)`, contentID, mediaID, now.UTC().UnixMilli()); err != nil {
			return err
		}
	}
	return nil
}
