package media

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/zhushilin/blog-project/internal/platform/database"
)

type Repository struct{ database *database.DB }

func NewRepository(db *database.DB) *Repository { return &Repository{database: db} }

func (r *Repository) Create(ctx context.Context, item Item, now time.Time) (Item, error) {
	tx, err := r.database.Writer.BeginTx(ctx, nil)
	if err != nil {
		return Item{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO media(public_id,original_name,mime_type,size_bytes,width,height,content_hash,alt_text,object_key,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, item.PublicID, item.OriginalName, item.MIMEType, item.SizeBytes, nullableInt(item.Width), nullableInt(item.Height), item.ContentHash, item.AltText, item.ObjectKey, millis(now), millis(now))
	if err != nil {
		return Item{}, fmt.Errorf("save media: %w", err)
	}
	item.ID, err = result.LastInsertId()
	if err != nil {
		return Item{}, err
	}
	for _, variant := range item.Variants {
		if _, err := tx.ExecContext(ctx, `INSERT INTO media_variants(media_id,variant_key,width,height,mime_type,size_bytes,content_hash,object_key,status,created_at) VALUES(?,?,?,?,?,?,?,?, 'ready',?)`, item.ID, variant.Key, variant.Width, variant.Height, variant.MIMEType, variant.SizeBytes, variant.ContentHash, variant.ObjectKey, millis(now)); err != nil {
			return Item{}, fmt.Errorf("save media variant: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_entries(action,object_kind,object_public_id,result,context_json,created_at) VALUES('media.uploaded','media',?,'succeeded','{}',?)`, item.PublicID, millis(now)); err != nil {
		return Item{}, err
	}
	if err := tx.Commit(); err != nil {
		return Item{}, err
	}
	return r.Item(ctx, item.ID)
}

func (r *Repository) Items(ctx context.Context) ([]Item, error) {
	rows, err := r.database.Reader.QueryContext(ctx, mediaSelect+` ORDER BY m.created_at DESC,m.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []Item
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		item.Variants, err = r.variants(ctx, item.ID)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
func (r *Repository) Item(ctx context.Context, id int64) (Item, error) {
	item, err := scanItem(r.database.Reader.QueryRowContext(ctx, mediaSelect+` WHERE m.id=?`, id))
	if err != nil {
		return Item{}, err
	}
	item.Variants, err = r.variants(ctx, item.ID)
	return item, err
}
func (r *Repository) ItemByPublicID(ctx context.Context, publicID []byte) (Item, error) {
	item, err := scanItem(r.database.Reader.QueryRowContext(ctx, mediaSelect+` WHERE m.public_id=?`, publicID))
	if err != nil {
		return Item{}, err
	}
	item.Variants, err = r.variants(ctx, item.ID)
	return item, err
}

func (r *Repository) Asset(ctx context.Context, publicID []byte, variantKey string) (Asset, error) {
	item, err := r.ItemByPublicID(ctx, publicID)
	if err != nil {
		return Asset{}, err
	}
	asset := Asset{Item: item, VariantKey: "original", MIMEType: item.MIMEType, SizeBytes: item.SizeBytes, ContentHash: item.ContentHash, ObjectKey: item.ObjectKey, CreatedAt: item.CreatedAt}
	if variantKey == "original" {
		return asset, nil
	}
	for _, variant := range item.Variants {
		if variant.Key == variantKey {
			asset.VariantKey = variant.Key
			asset.MIMEType = variant.MIMEType
			asset.SizeBytes = variant.SizeBytes
			asset.ContentHash = variant.ContentHash
			asset.ObjectKey = variant.ObjectKey
			return asset, nil
		}
	}
	return Asset{}, ErrNotFound
}

func (r *Repository) Delete(ctx context.Context, id int64, now time.Time) (Item, error) {
	tx, err := r.database.Writer.BeginTx(ctx, nil)
	if err != nil {
		return Item{}, err
	}
	defer tx.Rollback()
	var references int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM media_references WHERE media_id=?", id).Scan(&references); err != nil {
		return Item{}, err
	}
	if references > 0 {
		return Item{}, ErrInUse
	}
	item, err := scanItem(tx.QueryRowContext(ctx, mediaSelect+` WHERE m.id=?`, id))
	if err != nil {
		return Item{}, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM media WHERE id=?", id); err != nil {
		return Item{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_entries(action,object_kind,object_public_id,result,context_json,created_at) VALUES('media.deleted','media',?,'succeeded','{}',?)`, item.PublicID, millis(now)); err != nil {
		return Item{}, err
	}
	if err := tx.Commit(); err != nil {
		return Item{}, err
	}
	return item, nil
}

func (r *Repository) variants(ctx context.Context, mediaID int64) ([]Variant, error) {
	rows, err := r.database.Reader.QueryContext(ctx, `SELECT variant_key,width,height,mime_type,size_bytes,content_hash,object_key FROM media_variants WHERE media_id=? AND status='ready' ORDER BY width`, mediaID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Variant
	for rows.Next() {
		var value Variant
		if err := rows.Scan(&value.Key, &value.Width, &value.Height, &value.MIMEType, &value.SizeBytes, &value.ContentHash, &value.ObjectKey); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

const mediaSelect = `SELECT m.id,m.public_id,m.original_name,m.mime_type,m.size_bytes,m.width,m.height,m.content_hash,m.alt_text,m.object_key,m.version,(SELECT COUNT(*) FROM media_references mr WHERE mr.media_id=m.id),m.created_at FROM media m`

type scanner interface{ Scan(dest ...any) error }

func scanItem(row scanner) (Item, error) {
	var item Item
	var width, height sql.NullInt64
	var created int64
	err := row.Scan(&item.ID, &item.PublicID, &item.OriginalName, &item.MIMEType, &item.SizeBytes, &width, &height, &item.ContentHash, &item.AltText, &item.ObjectKey, &item.Version, &item.ReferenceCount, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Item{}, ErrNotFound
	}
	if err != nil {
		return Item{}, err
	}
	item.Width = int(width.Int64)
	item.Height = int(height.Int64)
	item.CreatedAt = time.UnixMilli(created).UTC()
	return item, nil
}
func nullableInt(value int) any {
	if value < 1 {
		return nil
	}
	return value
}
func millis(value time.Time) int64 { return value.UTC().UnixMilli() }
