package discovery

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"html"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/zhushilin/blog-project/internal/platform/database"
)

var markdownMarkup = regexp.MustCompile(`(?s)<[^>]*>|!\[[^]]*\]\([^)]*\)|\[([^]]+)\]\([^)]*\)|[` + "`" + `*_>#~|=-]+`)

type Repository struct{ database *database.DB }

func NewRepository(db *database.DB) *Repository { return &Repository{database: db} }

func (r *Repository) SyncDirty(ctx context.Context, limit int) (int, error) {
	tx, err := r.database.Writer.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin search synchronization: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT content_id FROM search_dirty ORDER BY queued_at,content_id LIMIT ?", limit)
	if err != nil {
		return 0, fmt.Errorf("list dirty search documents: %w", err)
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
	if err := rows.Close(); err != nil {
		return 0, err
	}
	for _, id := range ids {
		if err := r.syncDocument(ctx, tx, id); err != nil {
			return 0, fmt.Errorf("synchronize search document %d: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM search_dirty WHERE content_id=?", id); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit search synchronization: %w", err)
	}
	return len(ids), nil
}

type indexDocument struct {
	kind, path, categorySlug, tagSlugs string
	title, excerpt, body, taxonomy     string
	publishedAt                        int64
}

func (r *Repository) syncDocument(ctx context.Context, tx *sql.Tx, contentID int64) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM search_documents WHERE rowid=?", contentID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM search_grams WHERE content_id=?", contentID); err != nil {
		return err
	}
	var document indexDocument
	var categoryName, tagNames string
	err := tx.QueryRowContext(ctx, `
		SELECT c.kind,
		       CASE c.kind WHEN 'page' THEN '/' || c.published_slug ELSE '/posts/' || c.published_slug END,
		       c.published_at,
		       revision.title, revision.excerpt, revision.body_markdown,
		       COALESCE(category.slug_key,''),
		       COALESCE(category.name,''),
		       COALESCE((
		           SELECT group_concat(tag.slug_key,' ')
		           FROM tags tag
		           JOIN json_each(revision.tag_public_ids_json) snapshot
		             ON snapshot.value = lower(hex(tag.public_id))
		       ),''),
		       COALESCE((
		           SELECT group_concat(tag.name,' ')
		           FROM tags tag
		           JOIN json_each(revision.tag_public_ids_json) snapshot
		             ON snapshot.value = lower(hex(tag.public_id))
		       ),'')
		FROM contents c
		JOIN content_revisions revision ON revision.id=c.published_revision_id
		LEFT JOIN categories category ON category.public_id=revision.category_public_id
		WHERE c.id=? AND c.status='published' AND c.trashed_at IS NULL
		  AND c.published_slug IS NOT NULL
	`, contentID).Scan(
		&document.kind, &document.path, &document.publishedAt,
		&document.title, &document.excerpt, &document.body,
		&document.categorySlug, &categoryName, &document.tagSlugs, &tagNames,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	document.body = plainText(document.body)
	document.taxonomy = strings.TrimSpace(categoryName + " " + tagNames)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO search_documents(
			rowid,kind,path,published_at,category_slug,tag_slugs,title,excerpt,body,taxonomy
		) VALUES(?,?,?,?,?,?,?,?,?,?)
	`, contentID, document.kind, document.path, document.publishedAt, document.categorySlug,
		document.tagSlugs, document.title, document.excerpt, document.body, document.taxonomy); err != nil {
		return err
	}
	searchable := strings.Join([]string{document.title, document.excerpt, document.body, document.taxonomy}, " ")
	insertGram, err := tx.PrepareContext(ctx, "INSERT INTO search_grams(content_id,gram) VALUES(?,?)")
	if err != nil {
		return err
	}
	defer insertGram.Close()
	for _, gram := range chineseBigrams(searchable, 4096) {
		if _, err := insertGram.ExecContext(ctx, contentID, gram); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) Search(ctx context.Context, query SearchQuery) ([]SearchResult, error) {
	grams := chineseBigrams(query.Text, 32)
	if len(grams) > 0 {
		return r.searchChinese(ctx, query, grams)
	}
	match := ftsQuery(query.Text)
	if match == "" {
		return nil, ErrInvalidQuery
	}
	statement := `SELECT d.kind,d.path,d.title,d.excerpt,d.published_at
		FROM search_documents d WHERE search_documents MATCH ?`
	args := []any{match}
	statement, args = addSearchFilters(statement, args, query)
	if query.Sort == "newest" {
		statement += " ORDER BY CAST(d.published_at AS INTEGER) DESC,d.rowid DESC"
	} else {
		statement += " ORDER BY bm25(search_documents,0,0,0,0,0,12.0,5.0,1.0,3.0),CAST(d.published_at AS INTEGER) DESC"
	}
	statement += " LIMIT ?"
	args = append(args, query.Limit)
	return scanSearchResults(r.database.Reader.QueryContext(ctx, statement, args...))
}

func (r *Repository) searchChinese(ctx context.Context, query SearchQuery, grams []string) ([]SearchResult, error) {
	placeholders := strings.TrimRight(strings.Repeat("?,", len(grams)), ",")
	statement := `SELECT d.kind,d.path,d.title,d.excerpt,d.published_at
		FROM search_documents d JOIN search_grams g ON g.content_id=d.rowid
		WHERE g.gram IN (` + placeholders + `)`
	args := make([]any, 0, len(grams)+4)
	for _, gram := range grams {
		args = append(args, gram)
	}
	if match := nonHanFTSQuery(query.Text); match != "" {
		statement += " AND search_documents MATCH ?"
		args = append(args, match)
	}
	statement, args = addSearchFilters(statement, args, query)
	statement += " GROUP BY d.rowid HAVING COUNT(DISTINCT g.gram)=?"
	args = append(args, len(grams))
	if query.Sort == "newest" {
		statement += " ORDER BY CAST(d.published_at AS INTEGER) DESC,d.rowid DESC"
	} else {
		statement += " ORDER BY CASE WHEN d.title LIKE ? THEN 0 WHEN d.excerpt LIKE ? THEN 1 ELSE 2 END,CAST(d.published_at AS INTEGER) DESC"
		like := "%" + escapeLike(query.Text) + "%"
		args = append(args, like, like)
	}
	statement += " LIMIT ?"
	args = append(args, query.Limit)
	return scanSearchResults(r.database.Reader.QueryContext(ctx, statement, args...))
}

func addSearchFilters(statement string, args []any, query SearchQuery) (string, []any) {
	if query.Kind != "" {
		statement += " AND d.kind=?"
		args = append(args, query.Kind)
	}
	if query.CategorySlug != "" {
		statement += " AND d.category_slug=?"
		args = append(args, query.CategorySlug)
	}
	if query.TagSlug != "" {
		statement += " AND (' ' || d.tag_slugs || ' ') LIKE ? ESCAPE '\\'"
		args = append(args, "% "+escapeLike(query.TagSlug)+" %")
	}
	return statement, args
}

func scanSearchResults(rows *sql.Rows, err error) ([]SearchResult, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var results []SearchResult
	for rows.Next() {
		var result SearchResult
		var publishedAt int64
		if err := rows.Scan(&result.Kind, &result.Path, &result.Title, &result.Excerpt, &publishedAt); err != nil {
			return nil, err
		}
		result.PublishedAt = time.UnixMilli(publishedAt).UTC()
		results = append(results, result)
	}
	return results, rows.Err()
}

func (r *Repository) Feed(ctx context.Context, limit int) ([]FeedItem, error) {
	rows, err := r.database.Reader.QueryContext(ctx, `
		SELECT '/posts/' || c.published_slug,revision.title,revision.excerpt,
		       c.published_at,revision.created_at
		FROM contents c JOIN content_revisions revision ON revision.id=c.published_revision_id
		WHERE c.kind='article' AND c.status='published' AND c.trashed_at IS NULL
		ORDER BY c.published_at DESC,c.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []FeedItem
	for rows.Next() {
		var item FeedItem
		var publishedAt, updatedAt int64
		if err := rows.Scan(&item.Path, &item.Title, &item.Excerpt, &publishedAt, &updatedAt); err != nil {
			return nil, err
		}
		item.PublishedAt = time.UnixMilli(publishedAt).UTC()
		item.UpdatedAt = time.UnixMilli(updatedAt).UTC()
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *Repository) Sitemap(ctx context.Context, limit int) ([]SitemapEntry, error) {
	rows, err := r.database.Reader.QueryContext(ctx, `
		SELECT path,last_modified FROM (
			SELECT '/' AS path,COALESCE(MAX(revision.created_at),0) AS last_modified,0 AS rank
			FROM contents c LEFT JOIN content_revisions revision ON revision.id=c.published_revision_id
			WHERE c.status='published' AND c.trashed_at IS NULL
			UNION ALL
			SELECT CASE c.kind WHEN 'page' THEN '/' || c.published_slug ELSE '/posts/' || c.published_slug END,
			       revision.created_at,1
			FROM contents c JOIN content_revisions revision ON revision.id=c.published_revision_id
			WHERE c.status='published' AND c.trashed_at IS NULL
			UNION ALL
			SELECT '/categories/' || category.slug,category.updated_at,2
			FROM categories category WHERE EXISTS(
				SELECT 1 FROM contents c JOIN content_revisions revision ON revision.id=c.published_revision_id
				WHERE c.status='published' AND c.trashed_at IS NULL AND revision.category_public_id=category.public_id
			)
			UNION ALL
			SELECT '/tags/' || tag.slug,tag.updated_at,3
			FROM tags tag WHERE EXISTS(
				SELECT 1 FROM contents c JOIN content_revisions revision ON revision.id=c.published_revision_id
				WHERE c.status='published' AND c.trashed_at IS NULL AND EXISTS(
					SELECT 1 FROM json_each(revision.tag_public_ids_json) WHERE json_each.value=lower(hex(tag.public_id))
				)
			)
		) ORDER BY rank,path LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []SitemapEntry
	for rows.Next() {
		var entry SitemapEntry
		var modified int64
		if err := rows.Scan(&entry.Path, &modified); err != nil {
			return nil, err
		}
		if modified > 0 {
			entry.LastModified = time.UnixMilli(modified).UTC()
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (r *Repository) ResolveRedirect(ctx context.Context, pathKey string) (Redirect, error) {
	var redirect Redirect
	err := r.database.Reader.QueryRowContext(ctx, "SELECT target_path,status_code FROM redirects WHERE source_path_key=?", pathKey).Scan(&redirect.TargetPath, &redirect.StatusCode)
	if errors.Is(err, sql.ErrNoRows) {
		return Redirect{}, sql.ErrNoRows
	}
	return redirect, err
}

func plainText(value string) string {
	value = markdownMarkup.ReplaceAllString(value, " $1 ")
	value = html.UnescapeString(value)
	return strings.Join(strings.Fields(value), " ")
}

func chineseBigrams(value string, limit int) []string {
	seen := make(map[string]struct{})
	result := make([]string, 0, 32)
	var previous rune
	havePrevious := false
	for _, current := range strings.ToLower(value) {
		if unicode.Is(unicode.Han, current) {
			if havePrevious {
				gram := string([]rune{previous, current})
				if _, exists := seen[gram]; !exists {
					seen[gram] = struct{}{}
					result = append(result, gram)
					if len(result) >= limit {
						return result
					}
				}
			}
			previous, havePrevious = current, true
		} else {
			havePrevious = false
		}
	}
	return result
}

func ftsQuery(value string) string {
	fields := strings.Fields(value)
	terms := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.Trim(field, `"'(){}[]`)
		if field == "" {
			continue
		}
		terms = append(terms, `"`+strings.ReplaceAll(field, `"`, `""`)+`"*`)
	}
	return strings.Join(terms, " AND ")
}

func nonHanFTSQuery(value string) string {
	value = strings.Map(func(current rune) rune {
		if unicode.Is(unicode.Han, current) {
			return ' '
		}
		return current
	}, value)
	return ftsQuery(value)
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	return strings.ReplaceAll(value, `_`, `\_`)
}
