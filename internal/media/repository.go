package media

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zhushilin/blog-project/internal/platform/database"
	"github.com/zhushilin/blog-project/internal/platform/pagination"
)

type Repository struct{ database *database.DB }

const publicMediaBatchSize = 64

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

// ItemsPage is the bounded media-library projection. It loads metadata and a
// small variant summary for one page; object bytes are only opened by Asset.
func (r *Repository) ItemsPage(ctx context.Context, request pagination.Request) (Page, error) {
	total, err := r.countItems(ctx)
	if err != nil {
		return Page{}, err
	}
	request = pagination.Normalize(request, 40, 100)
	info := pagination.NewInfo(total, request)
	rows, err := r.database.Reader.QueryContext(ctx, mediaSelect+` ORDER BY m.created_at DESC,m.id DESC LIMIT ? OFFSET ?`, info.PerPage, info.Offset())
	if err != nil {
		return Page{}, err
	}
	defer rows.Close()
	items := make([]Item, 0, info.PerPage)
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return Page{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return Page{}, err
	}
	if err := r.batchVariants(ctx, items); err != nil {
		return Page{}, err
	}
	return Page{Items: items, Pagination: info}, nil
}

func (r *Repository) countItems(ctx context.Context) (int, error) {
	var total int
	if err := r.database.Reader.QueryRowContext(ctx, "SELECT COUNT(*) FROM media").Scan(&total); err != nil {
		return 0, err
	}
	return total, nil
}

func (r *Repository) batchVariants(ctx context.Context, items []Item) error {
	if len(items) == 0 {
		return nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(items)), ",")
	args := make([]any, 0, len(items))
	for _, item := range items {
		args = append(args, item.ID)
	}
	rows, err := r.database.Reader.QueryContext(ctx, `SELECT media_id,variant_key,width,height,mime_type,size_bytes,content_hash,object_key FROM media_variants WHERE media_id IN (`+placeholders+`) AND status='ready' ORDER BY media_id,width,variant_key`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	byID := make(map[int64][]Variant, len(items))
	for rows.Next() {
		var mediaID int64
		var value Variant
		if err := rows.Scan(&mediaID, &value.Key, &value.Width, &value.Height, &value.MIMEType, &value.SizeBytes, &value.ContentHash, &value.ObjectKey); err != nil {
			return err
		}
		// The editor only needs a bounded responsive summary. Asset lookup
		// still exposes every ready variant through the detail path.
		if len(byID[mediaID]) < 4 {
			byID[mediaID] = append(byID[mediaID], value)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for index := range items {
		items[index].Variants = byID[items[index].ID]
	}
	return nil
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

// ItemsByPublicIDs returns bounded metadata projections for public cards. It
// deliberately shares the variant batch query with the media library so a
// page of cards never turns cover resolution into one query per article.
func (r *Repository) ItemsByPublicIDs(ctx context.Context, publicIDs [][]byte) ([]Item, error) {
	unique := make([][]byte, 0, len(publicIDs))
	seen := make(map[string]struct{}, len(publicIDs))
	for _, publicID := range publicIDs {
		if len(publicID) == 0 {
			continue
		}
		key := hex.EncodeToString(publicID)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, append([]byte(nil), publicID...))
	}
	result := make([]Item, 0, len(unique))
	for start := 0; start < len(unique); start += publicMediaBatchSize {
		end := start + publicMediaBatchSize
		if end > len(unique) {
			end = len(unique)
		}
		chunk := unique[start:end]
		placeholders := strings.TrimRight(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, 0, len(chunk))
		for _, publicID := range chunk {
			args = append(args, publicID)
		}
		rows, err := r.database.Reader.QueryContext(ctx, mediaSelect+" WHERE m.public_id IN ("+placeholders+") ORDER BY m.created_at DESC,m.id DESC", args...)
		if err != nil {
			return nil, err
		}
		items := make([]Item, 0, len(chunk))
		for rows.Next() {
			item, err := scanItem(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			items = append(items, item)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if err := r.batchVariants(ctx, items); err != nil {
			return nil, err
		}
		result = append(result, items...)
	}
	return result, nil
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
