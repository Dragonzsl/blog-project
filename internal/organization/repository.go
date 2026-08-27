package organization

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/mattn/go-sqlite3"
	"github.com/zhushilin/blog-project/internal/platform/database"
)

type Repository struct{ database *database.DB }

func NewRepository(db *database.DB) *Repository { return &Repository{database: db} }

func (r *Repository) Categories(ctx context.Context) ([]Category, error) {
	rows, err := r.database.Reader.QueryContext(ctx, `SELECT id, public_id, slug, name, description, sort_order, created_at, updated_at FROM categories ORDER BY sort_order, name, id`)
	if err != nil {
		return nil, fmt.Errorf("list categories: %w", err)
	}
	defer rows.Close()
	var result []Category
	for rows.Next() {
		var value Category
		var created, updated int64
		if err := rows.Scan(&value.ID, &value.PublicID, &value.Slug, &value.Name, &value.Description, &value.SortOrder, &created, &updated); err != nil {
			return nil, fmt.Errorf("scan category: %w", err)
		}
		value.CreatedAt = fromMillis(created)
		value.UpdatedAt = fromMillis(updated)
		result = append(result, value)
	}
	return result, rows.Err()
}

func (r *Repository) Tags(ctx context.Context) ([]Tag, error) {
	rows, err := r.database.Reader.QueryContext(ctx, `SELECT id, public_id, slug, name, description, created_at, updated_at FROM tags ORDER BY name, id`)
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	defer rows.Close()
	var result []Tag
	for rows.Next() {
		var value Tag
		var created, updated int64
		if err := rows.Scan(&value.ID, &value.PublicID, &value.Slug, &value.Name, &value.Description, &created, &updated); err != nil {
			return nil, fmt.Errorf("scan tag: %w", err)
		}
		value.CreatedAt = fromMillis(created)
		value.UpdatedAt = fromMillis(updated)
		result = append(result, value)
	}
	return result, rows.Err()
}

func (r *Repository) Redirects(ctx context.Context, limit int) ([]Redirect, error) {
	if limit < 1 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := r.database.Reader.QueryContext(ctx, `SELECT id,source_path,target_path,status_code,reason,created_at,updated_at FROM redirects ORDER BY updated_at DESC,id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Redirect
	for rows.Next() {
		var item Redirect
		var created, updated int64
		if err := rows.Scan(&item.ID, &item.SourcePath, &item.TargetPath, &item.StatusCode, &item.Reason, &created, &updated); err != nil {
			return nil, err
		}
		item.CreatedAt, item.UpdatedAt = fromMillis(created), fromMillis(updated)
		result = append(result, item)
	}
	return result, rows.Err()
}

func (r *Repository) CreateRedirect(ctx context.Context, input RedirectInput, now time.Time) error {
	result, err := r.database.Writer.ExecContext(ctx, `INSERT INTO redirects(source_path,source_path_key,target_path,target_path_key,status_code,reason,created_at,updated_at) VALUES(?,?,?,?,?,'manual',?,?)`, input.SourcePath, strings.ToLower(input.SourcePath), input.TargetPath, strings.ToLower(input.TargetPath), input.StatusCode, millis(now), millis(now))
	if err != nil {
		return mapWriteError(err)
	}
	if affected(result) != 1 {
		return ErrNotFound
	}
	_, _ = r.database.Writer.ExecContext(ctx, `INSERT INTO audit_entries(action,object_kind,result,context_json,created_at) VALUES('organization.redirect.created','redirect','succeeded',?,?)`, fmt.Sprintf(`{"source":%q,"target":%q}`, input.SourcePath, input.TargetPath), millis(now))
	return nil
}

func (r *Repository) DeleteRedirect(ctx context.Context, id int64, now time.Time) error {
	result, err := r.database.Writer.ExecContext(ctx, "DELETE FROM redirects WHERE id=? AND reason='manual'", id)
	if err != nil {
		return err
	}
	if affected(result) != 1 {
		return ErrNotFound
	}
	_, _ = r.database.Writer.ExecContext(ctx, `INSERT INTO audit_entries(action,object_kind,result,context_json,created_at) VALUES('organization.redirect.deleted','redirect','succeeded',?,?)`, fmt.Sprintf(`{"id":%d}`, id), millis(now))
	return nil
}

func (r *Repository) CreateCategory(ctx context.Context, publicID []byte, input TermInput, slugKey string, now time.Time) (Category, error) {
	result, err := r.write(ctx, "organization.category.created", "category", publicID, now, func(tx *sql.Tx) (sql.Result, error) {
		if err := ensureTermPathAvailable(ctx, tx, "/categories/"+slugKey); err != nil {
			return nil, err
		}
		return tx.ExecContext(ctx, `INSERT INTO categories(public_id,slug,slug_key,name,description,sort_order,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, publicID, input.Slug, slugKey, input.Name, input.Description, input.SortOrder, millis(now), millis(now))
	})
	if err != nil {
		return Category{}, mapWriteError(err)
	}
	id, _ := result.LastInsertId()
	return r.category(ctx, id)
}

func (r *Repository) UpdateCategory(ctx context.Context, id int64, input TermInput, slugKey string, now time.Time) (Category, error) {
	result, err := r.write(ctx, "organization.category.updated", "category", nil, now, func(tx *sql.Tx) (sql.Result, error) {
		var oldSlug, oldKey string
		if err := tx.QueryRowContext(ctx, "SELECT slug,slug_key FROM categories WHERE id=?", id).Scan(&oldSlug, &oldKey); err != nil {
			return nil, err
		}
		result, err := tx.ExecContext(ctx, `UPDATE categories SET slug=?,slug_key=?,name=?,description=?,sort_order=?,updated_at=? WHERE id=?`, input.Slug, slugKey, input.Name, input.Description, input.SortOrder, millis(now), id)
		if err == nil && oldKey != slugKey {
			err = saveTermRedirect(ctx, tx, "/categories/"+oldSlug, "/categories/"+oldKey, "/categories/"+input.Slug, "/categories/"+slugKey, "category_slug", now)
		}
		return result, err
	})
	if err != nil {
		return Category{}, mapWriteError(err)
	}
	if affected(result) != 1 {
		return Category{}, ErrNotFound
	}
	return r.category(ctx, id)
}

func (r *Repository) DeleteCategory(ctx context.Context, id int64, now time.Time) error {
	result, err := r.write(ctx, "organization.category.deleted", "category", nil, now, func(tx *sql.Tx) (sql.Result, error) {
		return tx.ExecContext(ctx, "DELETE FROM categories WHERE id = ?", id)
	})
	if err != nil {
		return err
	}
	if affected(result) != 1 {
		return ErrNotFound
	}
	return nil
}

func (r *Repository) CreateTag(ctx context.Context, publicID []byte, input TermInput, slugKey string, now time.Time) (Tag, error) {
	result, err := r.write(ctx, "organization.tag.created", "tag", publicID, now, func(tx *sql.Tx) (sql.Result, error) {
		if err := ensureTermPathAvailable(ctx, tx, "/tags/"+slugKey); err != nil {
			return nil, err
		}
		return tx.ExecContext(ctx, `INSERT INTO tags(public_id,slug,slug_key,name,description,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, publicID, input.Slug, slugKey, input.Name, input.Description, millis(now), millis(now))
	})
	if err != nil {
		return Tag{}, mapWriteError(err)
	}
	id, _ := result.LastInsertId()
	return r.tag(ctx, id)
}
func (r *Repository) UpdateTag(ctx context.Context, id int64, input TermInput, slugKey string, now time.Time) (Tag, error) {
	result, err := r.write(ctx, "organization.tag.updated", "tag", nil, now, func(tx *sql.Tx) (sql.Result, error) {
		var oldSlug, oldKey string
		if err := tx.QueryRowContext(ctx, "SELECT slug,slug_key FROM tags WHERE id=?", id).Scan(&oldSlug, &oldKey); err != nil {
			return nil, err
		}
		result, err := tx.ExecContext(ctx, `UPDATE tags SET slug=?,slug_key=?,name=?,description=?,updated_at=? WHERE id=?`, input.Slug, slugKey, input.Name, input.Description, millis(now), id)
		if err == nil && oldKey != slugKey {
			err = saveTermRedirect(ctx, tx, "/tags/"+oldSlug, "/tags/"+oldKey, "/tags/"+input.Slug, "/tags/"+slugKey, "tag_slug", now)
		}
		return result, err
	})
	if err != nil {
		return Tag{}, mapWriteError(err)
	}
	if affected(result) != 1 {
		return Tag{}, ErrNotFound
	}
	return r.tag(ctx, id)
}
func (r *Repository) DeleteTag(ctx context.Context, id int64, now time.Time) error {
	result, err := r.write(ctx, "organization.tag.deleted", "tag", nil, now, func(tx *sql.Tx) (sql.Result, error) { return tx.ExecContext(ctx, "DELETE FROM tags WHERE id = ?", id) })
	if err != nil {
		return err
	}
	if affected(result) != 1 {
		return ErrNotFound
	}
	return nil
}

func (r *Repository) category(ctx context.Context, id int64) (Category, error) {
	var value Category
	var created, updated int64
	err := r.database.Reader.QueryRowContext(ctx, `SELECT id,public_id,slug,name,description,sort_order,created_at,updated_at FROM categories WHERE id=?`, id).Scan(&value.ID, &value.PublicID, &value.Slug, &value.Name, &value.Description, &value.SortOrder, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Category{}, ErrNotFound
	}
	if err != nil {
		return Category{}, err
	}
	value.CreatedAt = fromMillis(created)
	value.UpdatedAt = fromMillis(updated)
	return value, nil
}
func (r *Repository) tag(ctx context.Context, id int64) (Tag, error) {
	var value Tag
	var created, updated int64
	err := r.database.Reader.QueryRowContext(ctx, `SELECT id,public_id,slug,name,description,created_at,updated_at FROM tags WHERE id=?`, id).Scan(&value.ID, &value.PublicID, &value.Slug, &value.Name, &value.Description, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Tag{}, ErrNotFound
	}
	if err != nil {
		return Tag{}, err
	}
	value.CreatedAt = fromMillis(created)
	value.UpdatedAt = fromMillis(updated)
	return value, nil
}

func (r *Repository) NavigationItems(ctx context.Context, location string, publicOnly bool) ([]NavigationItem, error) {
	query := `SELECT ni.id,nm.location,COALESCE(ni.parent_id,0),ni.label,ni.target_kind,COALESCE(ni.content_id,ni.category_id,ni.tag_id,0),COALESCE(ni.external_url,''),ni.sort_order,c.kind,c.status,COALESCE(c.published_slug,c.slug),ca.slug,t.slug FROM navigation_items ni JOIN navigation_menus nm ON nm.id=ni.menu_id LEFT JOIN contents c ON c.id=ni.content_id LEFT JOIN categories ca ON ca.id=ni.category_id LEFT JOIN tags t ON t.id=ni.tag_id`
	args := []any{}
	if location != "" {
		query += " WHERE nm.location = ?"
		args = append(args, location)
	}
	query += " ORDER BY nm.location, COALESCE(ni.parent_id,ni.id), ni.parent_id IS NOT NULL, ni.sort_order, ni.id"
	rows, err := r.database.Reader.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list navigation: %w", err)
	}
	defer rows.Close()
	var result []NavigationItem
	for rows.Next() {
		var item NavigationItem
		var kind, status, contentSlug, categorySlug, tagSlug sql.NullString
		if err := rows.Scan(&item.ID, &item.Location, &item.ParentID, &item.Label, &item.TargetKind, &item.TargetID, &item.ExternalURL, &item.SortOrder, &kind, &status, &contentSlug, &categorySlug, &tagSlug); err != nil {
			return nil, err
		}
		item.URL = targetURL(item.TargetKind, item.ExternalURL, kind.String, status.String, contentSlug.String, categorySlug.String, tagSlug.String)
		if publicOnly && item.URL == "" {
			continue
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (r *Repository) CreateNavigationItem(ctx context.Context, input NavigationInput, now time.Time) (NavigationItem, error) {
	return r.saveNavigationItem(ctx, 0, input, now)
}
func (r *Repository) UpdateNavigationItem(ctx context.Context, id int64, input NavigationInput, now time.Time) (NavigationItem, error) {
	return r.saveNavigationItem(ctx, id, input, now)
}
func (r *Repository) saveNavigationItem(ctx context.Context, id int64, input NavigationInput, now time.Time) (NavigationItem, error) {
	tx, err := r.database.Writer.BeginTx(ctx, nil)
	if err != nil {
		return NavigationItem{}, err
	}
	defer tx.Rollback()
	var menuID int64
	if err := tx.QueryRowContext(ctx, "SELECT id FROM navigation_menus WHERE location=?", input.Location).Scan(&menuID); err != nil {
		return NavigationItem{}, ErrNotFound
	}
	if input.ParentID > 0 {
		var parentMenu int64
		var parentParent sql.NullInt64
		if err := tx.QueryRowContext(ctx, "SELECT menu_id,parent_id FROM navigation_items WHERE id=?", input.ParentID).Scan(&parentMenu, &parentParent); err != nil || parentMenu != menuID || parentParent.Valid {
			return NavigationItem{}, ErrInvalidTarget
		}
	}
	var contentID, categoryID, tagID any
	var external any
	switch input.TargetKind {
	case "content":
		contentID = input.TargetID
	case "category":
		categoryID = input.TargetID
	case "tag":
		tagID = input.TargetID
	case "external":
		external = input.ExternalURL
	}
	var result sql.Result
	if id == 0 {
		result, err = tx.ExecContext(ctx, `INSERT INTO navigation_items(menu_id,parent_id,label,target_kind,content_id,category_id,tag_id,external_url,sort_order,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, menuID, nullableID(input.ParentID), input.Label, input.TargetKind, contentID, categoryID, tagID, external, input.SortOrder, millis(now), millis(now))
	} else {
		result, err = tx.ExecContext(ctx, `UPDATE navigation_items SET menu_id=?,parent_id=?,label=?,target_kind=?,content_id=?,category_id=?,tag_id=?,external_url=?,sort_order=?,updated_at=? WHERE id=?`, menuID, nullableID(input.ParentID), input.Label, input.TargetKind, contentID, categoryID, tagID, external, input.SortOrder, millis(now), id)
	}
	if err != nil {
		return NavigationItem{}, mapNavigationError(err)
	}
	if affected(result) != 1 {
		return NavigationItem{}, ErrNotFound
	}
	if id == 0 {
		id, _ = result.LastInsertId()
	}
	if err := touch(ctx, tx, "organization.navigation.saved", "navigation", nil, now); err != nil {
		return NavigationItem{}, err
	}
	if err := tx.Commit(); err != nil {
		return NavigationItem{}, err
	}
	items, err := r.NavigationItems(ctx, "", false)
	if err != nil {
		return NavigationItem{}, err
	}
	for _, item := range items {
		if item.ID == id {
			return item, nil
		}
	}
	return NavigationItem{}, ErrNotFound
}
func (r *Repository) DeleteNavigationItem(ctx context.Context, id int64, now time.Time) error {
	result, err := r.write(ctx, "organization.navigation.deleted", "navigation", nil, now, func(tx *sql.Tx) (sql.Result, error) {
		return tx.ExecContext(ctx, "DELETE FROM navigation_items WHERE id=?", id)
	})
	if err != nil {
		return err
	}
	if affected(result) != 1 {
		return ErrNotFound
	}
	return nil
}

func (r *Repository) ArticleTaxonomy(ctx context.Context, contentID int64) (Taxonomy, error) {
	var result Taxonomy
	var categoryID sql.NullInt64
	if err := r.database.Reader.QueryRowContext(ctx, "SELECT category_id FROM contents WHERE id=? AND kind='article'", contentID).Scan(&categoryID); errors.Is(err, sql.ErrNoRows) {
		return Taxonomy{}, ErrNotFound
	} else if err != nil {
		return Taxonomy{}, err
	}
	if categoryID.Valid {
		category, err := r.category(ctx, categoryID.Int64)
		if err != nil {
			return Taxonomy{}, err
		}
		result.Category = &category
	}
	rows, err := r.database.Reader.QueryContext(ctx, `SELECT t.id,t.public_id,t.slug,t.name,t.description,t.created_at,t.updated_at FROM tags t JOIN content_tags ct ON ct.tag_id=t.id WHERE ct.content_id=? ORDER BY t.name,t.id`, contentID)
	if err != nil {
		return Taxonomy{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var tag Tag
		var created, updated int64
		if err := rows.Scan(&tag.ID, &tag.PublicID, &tag.Slug, &tag.Name, &tag.Description, &created, &updated); err != nil {
			return Taxonomy{}, err
		}
		tag.CreatedAt = fromMillis(created)
		tag.UpdatedAt = fromMillis(updated)
		result.Tags = append(result.Tags, tag)
	}
	return result, rows.Err()
}

func (r *Repository) TaxonomyBySnapshot(ctx context.Context, categoryPublicID []byte, tagPublicIDsJSON string) (Taxonomy, error) {
	var result Taxonomy
	if len(categoryPublicID) > 0 {
		var category Category
		var created, updated int64
		err := r.database.Reader.QueryRowContext(ctx, `SELECT id,public_id,slug,name,description,sort_order,created_at,updated_at FROM categories WHERE public_id=?`, categoryPublicID).Scan(&category.ID, &category.PublicID, &category.Slug, &category.Name, &category.Description, &category.SortOrder, &created, &updated)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return Taxonomy{}, err
		}
		if err == nil {
			category.CreatedAt = fromMillis(created)
			category.UpdatedAt = fromMillis(updated)
			result.Category = &category
		}
	}
	var encoded []string
	if tagPublicIDsJSON == "" {
		tagPublicIDsJSON = "[]"
	}
	if err := json.Unmarshal([]byte(tagPublicIDsJSON), &encoded); err != nil {
		return Taxonomy{}, fmt.Errorf("decode tag snapshot: %w", err)
	}
	for _, value := range encoded {
		publicID, err := hex.DecodeString(value)
		if err != nil || len(publicID) != 16 {
			return Taxonomy{}, fmt.Errorf("decode tag public ID")
		}
		var tag Tag
		var created, updated int64
		err = r.database.Reader.QueryRowContext(ctx, `SELECT id,public_id,slug,name,description,created_at,updated_at FROM tags WHERE public_id=?`, publicID).Scan(&tag.ID, &tag.PublicID, &tag.Slug, &tag.Name, &tag.Description, &created, &updated)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return Taxonomy{}, err
		}
		tag.CreatedAt = fromMillis(created)
		tag.UpdatedAt = fromMillis(updated)
		result.Tags = append(result.Tags, tag)
	}
	return result, nil
}

func (r *Repository) ReplaceArticleTaxonomyTx(ctx context.Context, tx *sql.Tx, contentID, categoryID int64, tagIDs []int64, now time.Time) ([]byte, string, error) {
	var categoryPublicID []byte
	if categoryID > 0 {
		if err := tx.QueryRowContext(ctx, "SELECT public_id FROM categories WHERE id=?", categoryID).Scan(&categoryPublicID); errors.Is(err, sql.ErrNoRows) {
			return nil, "", ValidationError{Message: "所选分类不存在"}
		} else if err != nil {
			return nil, "", err
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE contents SET category_id=? WHERE id=? AND kind='article'", nullableID(categoryID), contentID); err != nil {
		return nil, "", err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM content_tags WHERE content_id=?", contentID); err != nil {
		return nil, "", err
	}
	encoded := make([]string, 0, len(tagIDs))
	for _, tagID := range tagIDs {
		var publicID []byte
		if err := tx.QueryRowContext(ctx, "SELECT public_id FROM tags WHERE id=?", tagID).Scan(&publicID); errors.Is(err, sql.ErrNoRows) {
			return nil, "", ValidationError{Message: "所选标签不存在"}
		} else if err != nil {
			return nil, "", err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO content_tags(content_id,tag_id) VALUES(?,?)", contentID, tagID); err != nil {
			return nil, "", err
		}
		encoded = append(encoded, hex.EncodeToString(publicID))
	}
	jsonValue, err := json.Marshal(encoded)
	if err != nil {
		return nil, "", err
	}
	_ = now
	return categoryPublicID, string(jsonValue), nil
}

func (r *Repository) PublicCategory(ctx context.Context, key string, limit int) (Category, []int64, error) {
	var category Category
	var created, updated int64
	err := r.database.Reader.QueryRowContext(ctx, `SELECT id,public_id,slug,name,description,sort_order,created_at,updated_at FROM categories WHERE slug_key=?`, key).Scan(&category.ID, &category.PublicID, &category.Slug, &category.Name, &category.Description, &category.SortOrder, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Category{}, nil, ErrNotFound
	}
	if err != nil {
		return Category{}, nil, err
	}
	category.CreatedAt = fromMillis(created)
	category.UpdatedAt = fromMillis(updated)
	ids, err := r.publicContentIDs(ctx, `
		SELECT c.id
		FROM contents c
		JOIN content_revisions revision ON revision.id = c.published_revision_id
		WHERE revision.category_public_id = ?
		  AND c.kind='article' AND c.status='published' AND c.trashed_at IS NULL
		ORDER BY c.published_at DESC,c.id DESC LIMIT ?`, category.PublicID, limit)
	return category, ids, err
}
func (r *Repository) PublicTag(ctx context.Context, key string, limit int) (Tag, []int64, error) {
	var tag Tag
	var created, updated int64
	err := r.database.Reader.QueryRowContext(ctx, `SELECT id,public_id,slug,name,description,created_at,updated_at FROM tags WHERE slug_key=?`, key).Scan(&tag.ID, &tag.PublicID, &tag.Slug, &tag.Name, &tag.Description, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Tag{}, nil, ErrNotFound
	}
	if err != nil {
		return Tag{}, nil, err
	}
	tag.CreatedAt = fromMillis(created)
	tag.UpdatedAt = fromMillis(updated)
	ids, err := r.publicContentIDs(ctx, `
		SELECT c.id
		FROM contents c
		JOIN content_revisions revision ON revision.id = c.published_revision_id
		WHERE EXISTS (
			SELECT 1 FROM json_each(revision.tag_public_ids_json)
			WHERE json_each.value = lower(hex(?))
		)
		  AND c.kind='article' AND c.status='published' AND c.trashed_at IS NULL
		ORDER BY c.published_at DESC,c.id DESC LIMIT ?`, tag.PublicID, limit)
	return tag, ids, err
}
func (r *Repository) publicContentIDs(ctx context.Context, query string, args ...any) ([]int64, error) {
	rows, err := r.database.Reader.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (r *Repository) write(ctx context.Context, action, kind string, publicID []byte, now time.Time, operation func(*sql.Tx) (sql.Result, error)) (sql.Result, error) {
	tx, err := r.database.Writer.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	result, err := operation(tx)
	if err != nil {
		return nil, err
	}
	if affected(result) == 0 {
		return nil, ErrNotFound
	}
	if err := touch(ctx, tx, action, kind, publicID, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}
func touch(ctx context.Context, tx *sql.Tx, action, kind string, publicID []byte, now time.Time) error {
	if _, err := tx.ExecContext(ctx, "UPDATE system_state SET render_epoch=render_epoch+1,updated_at=? WHERE id=1", millis(now)); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO audit_entries(action,object_kind,object_public_id,result,context_json,created_at) VALUES(?,?,?,'succeeded','{}',?)`, action, kind, publicID, millis(now))
	return err
}
func targetURL(target, external, kind, status, contentSlug, categorySlug, tagSlug string) string {
	switch target {
	case "external":
		if parsed, err := url.ParseRequestURI(external); err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") {
			return external
		}
	case "content":
		if status != "published" {
			return ""
		}
		if kind == "article" {
			return "/posts/" + url.PathEscape(contentSlug)
		}
		if kind == "page" {
			return "/" + url.PathEscape(contentSlug)
		}
	case "category":
		return "/categories/" + url.PathEscape(categorySlug)
	case "tag":
		return "/tags/" + url.PathEscape(tagSlug)
	}
	return ""
}
func nullableID(value int64) any {
	if value < 1 {
		return nil
	}
	return value
}
func affected(result sql.Result) int64 {
	if result == nil {
		return 0
	}
	count, _ := result.RowsAffected()
	return count
}
func mapWriteError(err error) error {
	var sqliteError sqlite3.Error
	if errors.As(err, &sqliteError) && sqliteError.ExtendedCode == sqlite3.ErrConstraintUnique {
		return ErrSlugUnavailable
	}
	return err
}
func mapNavigationError(err error) error {
	var sqliteError sqlite3.Error
	if errors.As(err, &sqliteError) && (sqliteError.ExtendedCode == sqlite3.ErrConstraintForeignKey || sqliteError.Code == sqlite3.ErrConstraint) {
		return ErrInvalidTarget
	}
	return err
}

func saveTermRedirect(ctx context.Context, tx *sql.Tx, sourcePath, sourceKey, targetPath, targetKey, reason string, now time.Time) error {
	var targetReserved bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM redirects WHERE source_path_key=?)", targetKey).Scan(&targetReserved); err != nil {
		return err
	}
	if targetReserved {
		return ErrSlugUnavailable
	}
	if _, err := tx.ExecContext(ctx, "UPDATE redirects SET target_path=?,target_path_key=?,updated_at=? WHERE target_path_key=?", targetPath, targetKey, millis(now), sourceKey); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO redirects(source_path,source_path_key,target_path,target_path_key,status_code,reason,created_at,updated_at)
		VALUES(?,?,?,?,301,?,?,?)
		ON CONFLICT(source_path_key) DO UPDATE SET target_path=excluded.target_path,target_path_key=excluded.target_path_key,status_code=301,reason=excluded.reason,updated_at=excluded.updated_at
	`, sourcePath, sourceKey, targetPath, targetKey, reason, millis(now), millis(now))
	return err
}

func ensureTermPathAvailable(ctx context.Context, tx *sql.Tx, pathKey string) error {
	var reserved bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM redirects WHERE source_path_key=?)", pathKey).Scan(&reserved); err != nil {
		return err
	}
	if reserved {
		return ErrSlugUnavailable
	}
	return nil
}
func millis(value time.Time) int64     { return value.UTC().UnixMilli() }
func fromMillis(value int64) time.Time { return time.UnixMilli(value).UTC() }
