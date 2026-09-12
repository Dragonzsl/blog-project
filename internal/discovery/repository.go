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
	"github.com/zhushilin/blog-project/internal/platform/pagination"
)

var markdownMarkup = regexp.MustCompile(`(?s)<[^>]*>|!\[[^]]*\]\([^)]*\)|\[([^]]+)\]\([^)]*\)|[` + "`" + `*_>#~|=-]+`)

type Repository struct{ database *database.DB }

type searchDirtyRecord struct {
	contentID int64
	queuedAt  int64
}

func NewRepository(db *database.DB) *Repository { return &Repository{database: db} }

func (r *Repository) SyncDirty(ctx context.Context, limit int) (int, error) {
	if limit < 1 {
		limit = 20
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := r.database.Reader.QueryContext(ctx, "SELECT content_id,queued_at FROM search_dirty ORDER BY queued_at,content_id LIMIT ?", limit)
	if err != nil {
		return 0, fmt.Errorf("list dirty search documents: %w", err)
	}
	defer rows.Close()
	dirty := make([]searchDirtyRecord, 0, limit)
	for rows.Next() {
		var item searchDirtyRecord
		if err := rows.Scan(&item.contentID, &item.queuedAt); err != nil {
			return 0, err
		}
		dirty = append(dirty, item)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(dirty) == 0 {
		return 0, nil
	}
	documents, err := r.readIndexDocuments(ctx, dirty)
	if err != nil {
		return 0, fmt.Errorf("read search documents: %w", err)
	}
	type snapshot struct {
		searchDirtyRecord
		document *indexDocument
	}
	snapshots := make([]snapshot, 0, len(dirty))
	for _, item := range dirty {
		snapshots = append(snapshots, snapshot{searchDirtyRecord: item, document: documents[item.contentID]})
	}

	tx, err := r.database.Writer.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin search synchronization: %w", err)
	}
	defer tx.Rollback()
	states, err := readSearchStatesTx(ctx, tx, dirty)
	if err != nil {
		return 0, err
	}
	insertGram, err := tx.PrepareContext(ctx, "INSERT INTO search_grams(content_id,gram) VALUES(?,?)")
	if err != nil {
		return 0, err
	}
	defer insertGram.Close()
	processed := 0
	for _, item := range snapshots {
		state, exists := states[item.contentID]
		if !exists || state.status != "published" || state.trashed || item.document == nil {
			if err := deleteSearchDocumentTx(ctx, tx, item.contentID); err != nil {
				return 0, err
			}
			if err := deleteDirtyTx(ctx, tx, item.searchDirtyRecord); err != nil {
				return 0, err
			}
			processed++
			continue
		}
		if state.publishedRevisionID != item.document.PublishedRevisionID {
			// A publication raced the Reader snapshot. The trigger has kept this
			// row dirty; leave it for the next bounded batch.
			continue
		}
		if err := r.syncDocumentTx(ctx, tx, insertGram, *item.document); err != nil {
			return 0, fmt.Errorf("synchronize search document %d: %w", item.contentID, err)
		}
		if err := deleteDirtyTx(ctx, tx, item.searchDirtyRecord); err != nil {
			return 0, err
		}
		processed++
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit search synchronization: %w", err)
	}
	return processed, nil
}

type indexDocument struct {
	ContentID, PublishedRevisionID     int64
	kind, path, categorySlug, tagSlugs string
	title, excerpt, body, taxonomy     string
	publishedAt                        int64
}

func (r *Repository) readIndexDocuments(ctx context.Context, dirty []searchDirtyRecord) (map[int64]*indexDocument, error) {
	ids := make([]any, 0, len(dirty))
	for _, item := range dirty {
		ids = append(ids, item.contentID)
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
	rows, err := r.database.Reader.QueryContext(ctx, `
		SELECT c.id,c.published_revision_id,c.kind,
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
		WHERE c.id IN (`+placeholders+`) AND c.status='published' AND c.trashed_at IS NULL
		  AND c.published_slug IS NOT NULL
	`, ids...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	documents := make(map[int64]*indexDocument, len(dirty))
	for rows.Next() {
		var document indexDocument
		var categoryName, tagNames string
		if err := rows.Scan(
			&document.ContentID, &document.PublishedRevisionID,
			&document.kind, &document.path, &document.publishedAt,
			&document.title, &document.excerpt, &document.body,
			&document.categorySlug, &categoryName, &document.tagSlugs, &tagNames,
		); err != nil {
			return nil, err
		}
		document.body = plainText(document.body)
		document.taxonomy = strings.TrimSpace(categoryName + " " + tagNames)
		copy := document
		documents[document.ContentID] = &copy
	}
	return documents, rows.Err()
}

type searchState struct {
	publishedRevisionID int64
	status              string
	trashed             bool
}

func readSearchStatesTx(ctx context.Context, tx *sql.Tx, dirty []searchDirtyRecord) (map[int64]searchState, error) {
	args := make([]any, 0, len(dirty))
	for _, item := range dirty {
		args = append(args, item.contentID)
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(args)), ",")
	rows, err := tx.QueryContext(ctx, `SELECT id,published_revision_id,status,trashed_at FROM contents WHERE id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	states := make(map[int64]searchState, len(dirty))
	for rows.Next() {
		var id int64
		var revisionID sql.NullInt64
		var trashedAt sql.NullInt64
		var state searchState
		if err := rows.Scan(&id, &revisionID, &state.status, &trashedAt); err != nil {
			return nil, err
		}
		state.publishedRevisionID = revisionID.Int64
		state.trashed = trashedAt.Valid
		states[id] = state
	}
	return states, rows.Err()
}

func (r *Repository) syncDocumentTx(ctx context.Context, tx *sql.Tx, insertGram *sql.Stmt, document indexDocument) error {
	if err := deleteSearchGramsTx(ctx, tx, document.ContentID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT OR REPLACE INTO search_documents(
			rowid,kind,path,published_at,category_slug,tag_slugs,title,excerpt,body,taxonomy
		) VALUES(?,?,?,?,?,?,?,?,?,?)
	`, document.ContentID, document.kind, document.path, document.publishedAt, document.categorySlug,
		document.tagSlugs, document.title, document.excerpt, document.body, document.taxonomy); err != nil {
		return err
	}
	searchable := strings.Join([]string{document.title, document.excerpt, document.body, document.taxonomy}, " ")
	for _, gram := range chineseBigrams(searchable, 4096) {
		if _, err := insertGram.ExecContext(ctx, document.ContentID, gram); err != nil {
			return err
		}
	}
	return nil
}

func deleteSearchDocumentTx(ctx context.Context, tx *sql.Tx, contentID int64) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM search_documents WHERE rowid=?", contentID); err != nil {
		return err
	}
	return deleteSearchGramsTx(ctx, tx, contentID)
}

func deleteSearchGramsTx(ctx context.Context, tx *sql.Tx, contentID int64) error {
	_, err := tx.ExecContext(ctx, "DELETE FROM search_grams WHERE content_id=?", contentID)
	return err
}

func deleteDirtyTx(ctx context.Context, tx *sql.Tx, item searchDirtyRecord) error {
	_, err := tx.ExecContext(ctx, "DELETE FROM search_dirty WHERE content_id=? AND queued_at=?", item.contentID, item.queuedAt)
	return err
}

func (r *Repository) Search(ctx context.Context, query SearchQuery) ([]SearchResult, error) {
	page, err := r.SearchPage(ctx, query)
	if err != nil {
		return nil, err
	}
	return page.Results, nil
}

func (r *Repository) SearchPage(ctx context.Context, query SearchQuery) (SearchPage, error) {
	grams := chineseBigrams(query.Text, 32)
	request := pagination.Normalize(pagination.Request{Page: query.Page, PerPage: query.Limit}, query.Limit, query.Limit)
	if request.PerPage < 1 {
		request.PerPage = 20
	}
	if query.PerPage > 0 {
		request.PerPage = query.PerPage
	}
	if query.Page > 0 {
		request.Page = query.Page
	}
	query.Limit = request.PerPage
	total, err := r.searchCount(ctx, query, grams)
	if err != nil {
		return SearchPage{}, err
	}
	info := pagination.NewInfo(total, request)
	query.Offset = info.Offset()
	if len(grams) > 0 {
		results, err := r.searchChinese(ctx, query, grams)
		return SearchPage{Results: results, Pagination: info}, err
	}
	match := ftsQuery(query.Text)
	if match == "" {
		return SearchPage{}, ErrInvalidQuery
	}
	statement := `SELECT d.kind,d.path,d.title,d.excerpt,r.cover_media_public_id,d.published_at
		FROM search_documents d JOIN contents c ON c.id=d.rowid JOIN content_revisions r ON r.id=c.published_revision_id WHERE search_documents MATCH ?`
	args := []any{match}
	statement, args = addSearchFilters(statement, args, query)
	if query.Sort == "newest" {
		statement += " ORDER BY CAST(d.published_at AS INTEGER) DESC,d.rowid DESC"
	} else {
		statement += " ORDER BY bm25(search_documents,0,0,0,0,0,12.0,5.0,1.0,3.0),CAST(d.published_at AS INTEGER) DESC"
	}
	statement += " LIMIT ? OFFSET ?"
	args = append(args, query.Limit, query.Offset)
	results, err := scanSearchResults(r.database.Reader.QueryContext(ctx, statement, args...))
	return SearchPage{Results: results, Pagination: info}, err
}

func (r *Repository) searchCount(ctx context.Context, query SearchQuery, grams []string) (int, error) {
	if len(grams) > 0 {
		statement := `WITH candidates AS MATERIALIZED (
			` + chineseCandidatesSQL(len(grams)) + `
		)
		SELECT COUNT(*)
		FROM candidates candidate
		JOIN search_documents d ON d.rowid=candidate.content_id
		WHERE 1=1`
		args := make([]any, 0, len(grams)+4)
		for _, gram := range grams {
			args = append(args, gram)
		}
		if match := nonHanFTSQuery(query.Text); match != "" {
			statement += " AND search_documents MATCH ?"
			args = append(args, match)
		}
		statement, args = addSearchFilters(statement, args, query)
		var total int
		err := r.database.Reader.QueryRowContext(ctx, statement, args...).Scan(&total)
		return total, err
	}
	match := ftsQuery(query.Text)
	if match == "" {
		return 0, ErrInvalidQuery
	}
	statement := `SELECT COUNT(*) FROM search_documents d WHERE search_documents MATCH ?`
	args := []any{match}
	statement, args = addSearchFilters(statement, args, query)
	var total int
	err := r.database.Reader.QueryRowContext(ctx, statement, args...).Scan(&total)
	return total, err
}

func (r *Repository) searchChinese(ctx context.Context, query SearchQuery, grams []string) ([]SearchResult, error) {
	statement := `WITH candidates AS MATERIALIZED (
			` + chineseCandidatesSQL(len(grams)) + `
		), ranked AS (
			SELECT d.rowid,d.kind,d.path,d.title,d.excerpt,d.published_at
			FROM candidates candidate
			JOIN search_documents d ON d.rowid=candidate.content_id
			WHERE 1=1`
	args := make([]any, 0, len(grams)+4)
	for _, gram := range grams {
		args = append(args, gram)
	}
	if match := nonHanFTSQuery(query.Text); match != "" {
		statement += " AND search_documents MATCH ?"
		args = append(args, match)
	}
	statement, args = addSearchFilters(statement, args, query)
	if query.Sort == "newest" {
		statement += " ORDER BY CAST(d.published_at AS INTEGER) DESC,d.rowid DESC"
	} else {
		statement += " ORDER BY CASE WHEN d.title LIKE ? THEN 0 WHEN d.excerpt LIKE ? THEN 1 ELSE 2 END,CAST(d.published_at AS INTEGER) DESC"
		like := "%" + escapeLike(query.Text) + "%"
		args = append(args, like, like)
	}
	statement += ` LIMIT ? OFFSET ?
		)
		SELECT ranked.kind,ranked.path,ranked.title,ranked.excerpt,r.cover_media_public_id,ranked.published_at
		FROM ranked
		JOIN contents c ON c.id=ranked.rowid
		JOIN content_revisions r ON r.id=c.published_revision_id`
	args = append(args, query.Limit, query.Offset)
	return scanSearchResults(r.database.Reader.QueryContext(ctx, statement, args...))
}

// chineseCandidatesSQL intersects one indexed gram lookup per bigram. The
// previous IN/GROUP BY form had to materialize and deduplicate every matching
// row before it could test completeness; INTERSECT keeps each lookup ordered
// by the existing (gram,content_id) index and avoids the DISTINCT aggregate.
func chineseCandidatesSQL(count int) string {
	parts := make([]string, 0, count)
	for index := 0; index < count; index++ {
		parts = append(parts, "SELECT content_id FROM search_grams WHERE gram=?")
	}
	return strings.Join(parts, " INTERSECT ")
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
		if err := rows.Scan(&result.Kind, &result.Path, &result.Title, &result.Excerpt, &result.CoverMediaPublicID, &publishedAt); err != nil {
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
		       substr(revision.body_markdown,1,65536),revision.cover_media_public_id,c.published_at,revision.created_at
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
		if err := rows.Scan(&item.Path, &item.Title, &item.Excerpt, &item.BodyMarkdown, &item.CoverMediaPublicID, &publishedAt, &updatedAt); err != nil {
			return nil, err
		}
		item.PublishedAt = time.UnixMilli(publishedAt).UTC()
		item.UpdatedAt = time.UnixMilli(updatedAt).UTC()
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *Repository) ArchiveIndex(ctx context.Context) ([]ArchiveYear, error) {
	rows, err := r.database.Reader.QueryContext(ctx, `
		SELECT CAST(strftime('%Y', c.published_at / 1000, 'unixepoch') AS INTEGER) AS archive_year,
		       CAST(strftime('%m', c.published_at / 1000, 'unixepoch') AS INTEGER) AS archive_month,
		       COUNT(*)
		FROM contents c
		WHERE c.kind='article' AND c.status='published' AND c.trashed_at IS NULL
		GROUP BY archive_year, archive_month
		ORDER BY archive_year DESC, archive_month DESC`)
	if err != nil {
		return nil, fmt.Errorf("list archive index: %w", err)
	}
	defer rows.Close()
	var result []ArchiveYear
	for rows.Next() {
		var month ArchiveMonth
		if err := rows.Scan(&month.Year, &month.Month, &month.Count); err != nil {
			return nil, fmt.Errorf("scan archive month: %w", err)
		}
		if len(result) == 0 || result[len(result)-1].Year != month.Year {
			result = append(result, ArchiveYear{Year: month.Year})
		}
		result[len(result)-1].Months = append(result[len(result)-1].Months, month)
		result[len(result)-1].Total += month.Count
	}
	return result, rows.Err()
}

func (r *Repository) ArchiveMonthPage(ctx context.Context, year, month int, request pagination.Request) (ArchivePage, error) {
	start := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	startMillis, endMillis := start.UnixMilli(), end.UnixMilli()
	var total int
	if err := r.database.Reader.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM contents
		WHERE kind='article' AND status='published' AND trashed_at IS NULL
		  AND published_at >= ? AND published_at < ?`, startMillis, endMillis).Scan(&total); err != nil {
		return ArchivePage{}, fmt.Errorf("count archive month: %w", err)
	}
	if total == 0 {
		return ArchivePage{}, ErrNotFound
	}
	info := pagination.NewInfo(total, request)
	rows, err := r.database.Reader.QueryContext(ctx, `
		SELECT '/posts/' || c.published_slug, revision.title, revision.excerpt,
		       revision.cover_media_public_id,c.published_at
		FROM contents c
		JOIN content_revisions revision ON revision.id=c.published_revision_id
		WHERE c.kind='article' AND c.status='published' AND c.trashed_at IS NULL
		  AND c.published_at >= ? AND c.published_at < ?
		ORDER BY c.published_at DESC, c.id DESC LIMIT ? OFFSET ?`, startMillis, endMillis, info.PerPage, info.Offset())
	if err != nil {
		return ArchivePage{}, fmt.Errorf("list archive month: %w", err)
	}
	defer rows.Close()
	results := make([]SearchResult, 0, info.PerPage)
	for rows.Next() {
		var result SearchResult
		var publishedAt int64
		if err := rows.Scan(&result.Path, &result.Title, &result.Excerpt, &result.CoverMediaPublicID, &publishedAt); err != nil {
			return ArchivePage{}, fmt.Errorf("scan archive article: %w", err)
		}
		result.Kind = "article"
		result.PublishedAt = time.UnixMilli(publishedAt).UTC()
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return ArchivePage{}, err
	}
	return ArchivePage{Year: year, Month: month, Results: results, Pagination: info}, nil
}

func (r *Repository) Sitemap(ctx context.Context, limit int) ([]SitemapEntry, error) {
	rows, err := r.database.Reader.QueryContext(ctx, `
		SELECT path,last_modified FROM (
			SELECT '/' AS path,COALESCE(MAX(revision.created_at),0) AS last_modified,0 AS rank
			FROM contents c LEFT JOIN content_revisions revision ON revision.id=c.published_revision_id
			WHERE c.status='published' AND c.trashed_at IS NULL
			UNION ALL
			SELECT '/articles',0,1
			UNION ALL
			SELECT '/archive',0,1
			UNION ALL
			SELECT '/categories',0,1
			UNION ALL
			SELECT '/tags',0,1
			UNION ALL
			SELECT CASE c.kind WHEN 'page' THEN '/' || c.published_slug ELSE '/posts/' || c.published_slug END,
			       revision.created_at,2
			FROM contents c JOIN content_revisions revision ON revision.id=c.published_revision_id
			WHERE c.status='published' AND c.trashed_at IS NULL
			UNION ALL
			SELECT '/categories/' || category.slug,category.updated_at,3
			FROM categories category WHERE EXISTS(
				SELECT 1 FROM contents c JOIN content_revisions revision ON revision.id=c.published_revision_id
				WHERE c.status='published' AND c.trashed_at IS NULL AND revision.category_public_id=category.public_id
			)
			UNION ALL
			SELECT '/tags/' || tag.slug,tag.updated_at,4
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
