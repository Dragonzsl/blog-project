package comments

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zhushilin/blog-project/internal/extensions"
	"github.com/zhushilin/blog-project/internal/platform/database"
	platformid "github.com/zhushilin/blog-project/internal/platform/id"
	"github.com/zhushilin/blog-project/internal/platform/publicwrite"
	"github.com/zhushilin/blog-project/internal/presentation"
	"github.com/zhushilin/blog-project/internal/publishing"
)

var (
	ErrNotFound = errors.New("comment not found")
	ErrInvalid  = errors.New("comment input is invalid")
	ErrDepth    = errors.New("comment reply depth exceeded")
)

type Comment struct {
	ID           int64
	PublicID     []byte
	ContentID    int64
	ParentID     *int64
	DisplayName  string
	Website      string
	BodyMarkdown string
	BodyHTML     string
	Status       string
	CreatedAt    time.Time
	ApprovedAt   *time.Time
}

type AdminComment struct {
	Comment
	ContentTitle string
	ContentSlug  string
}

type AdminCommentFilter struct {
	Status      string
	Query       string
	ArticleSlug string
	From        *time.Time
	To          *time.Time
	Page        int
	PerPage     int
}

type AdminCommentPage struct {
	Comments []AdminComment
	Total    int
	Page     int
	PerPage  int
	HasMore  bool
}

type ModerationItemResult struct {
	ID     int64  `json:"id"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type ModerationBulkResult struct {
	OperationKey string                 `json:"operation_key"`
	Action       string                 `json:"action"`
	Succeeded    int                    `json:"succeeded"`
	NotFound     int                    `json:"not_found"`
	Invalid      int                    `json:"invalid"`
	Items        []ModerationItemResult `json:"items"`
}

var (
	ErrBulkInProgress  = errors.New("comment bulk operation is already being processed")
	ErrBulkKeyConflict = errors.New("comment bulk operation key was reused")
)

type Input struct {
	DisplayName string
	Email       string
	Website     string
	Body        string
	ParentID    int64
}

type ContentLookup interface {
	PublicArticle(context.Context, string) (publishing.Article, error)
}

type Notifier interface {
	Enqueue(context.Context, string, string, string, string, time.Time) error
}

type transactionalNotifier interface {
	EnqueueTx(context.Context, *sql.Tx, string, string, string, string, time.Time) error
}

type WriteRequest struct {
	IdempotencyKey string
	ClientIdentity string
}

type Service struct {
	db                *database.DB
	lookup            ContentLookup
	markdown          *presentation.Markdown
	requireModeration bool
	now               func() time.Time
	notifier          Notifier
	notificationTo    string
	events            interface {
		Dispatch(context.Context, extensions.Event) []error
	}
	eventRecorder interface {
		RecordEventTx(context.Context, *sql.Tx, extensions.Event) error
	}
	writeGuard *publicwrite.Guard
}

func (s *Service) SetNotifier(notifier Notifier, recipient string) {
	s.notifier, s.notificationTo = notifier, strings.TrimSpace(recipient)
}

func (s *Service) SetEventSink(sink interface {
	Dispatch(context.Context, extensions.Event) []error
}) {
	s.events = sink
	if recorder, ok := sink.(interface {
		RecordEventTx(context.Context, *sql.Tx, extensions.Event) error
	}); ok {
		s.eventRecorder = recorder
	}
}

func NewService(db *database.DB, lookup ContentLookup, markdown *presentation.Markdown, requireModeration bool, secret ...[]byte) *Service {
	if markdown == nil {
		markdown = presentation.NewMarkdown()
	}
	service := &Service{db: db, lookup: lookup, markdown: markdown, requireModeration: requireModeration, now: func() time.Time { return time.Now().UTC() }}
	if len(secret) > 0 && len(secret[0]) > 0 {
		service.writeGuard = publicwrite.NewGuard(db, secret[0])
	}
	return service
}

func (s *Service) SetWriteGuard(guard *publicwrite.Guard) { s.writeGuard = guard }

func (s *Service) Cleanup(ctx context.Context) (int64, error) {
	if s == nil || s.writeGuard == nil {
		return 0, nil
	}
	return s.writeGuard.Cleanup(ctx)
}

func (s *Service) Create(ctx context.Context, slug string, input Input) (Comment, error) {
	return s.CreateWithRequest(ctx, slug, input, WriteRequest{})
}

func (s *Service) CreateWithRequest(ctx context.Context, slug string, input Input, request WriteRequest) (Comment, error) {
	if s.lookup == nil {
		return Comment{}, ErrInvalid
	}
	article, err := s.lookup.PublicArticle(ctx, slug)
	if err != nil {
		return Comment{}, ErrNotFound
	}
	name := strings.TrimSpace(input.DisplayName)
	body := strings.TrimSpace(input.Body)
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 120 || body == "" || !utf8.ValidString(body) || utf8.RuneCountInString(body) > 10000 {
		return Comment{}, fmt.Errorf("%w: name or body length", ErrInvalid)
	}
	website := strings.TrimSpace(input.Website)
	if website != "" {
		parsed, err := url.Parse(website)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
			return Comment{}, fmt.Errorf("%w: website must be http or https", ErrInvalid)
		}
		website = parsed.String()
	}
	var parent *int64
	if input.ParentID > 0 {
		var parentContent int64
		var parentParent sql.NullInt64
		if err := s.db.Reader.QueryRowContext(ctx, "SELECT content_id,parent_id FROM comments WHERE id=? AND status IN ('pending','approved')", input.ParentID).Scan(&parentContent, &parentParent); err != nil {
			return Comment{}, ErrInvalid
		}
		if parentContent != article.ID {
			return Comment{}, ErrInvalid
		}
		if parentParent.Valid {
			return Comment{}, ErrDepth
		}
		parent = &input.ParentID
	}
	html, err := s.markdown.Render(body)
	if err != nil {
		return Comment{}, err
	}
	publicID, err := platformid.NewPublicID(s.now())
	if err != nil {
		return Comment{}, err
	}
	emailHash := []byte{}
	if strings.TrimSpace(input.Email) != "" {
		value := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(input.Email))))
		emailHash = value[:]
	}
	status := "pending"
	if !s.requireModeration {
		status = "approved"
	}
	now := s.now()
	tx, err := s.db.Writer.BeginTx(ctx, nil)
	if err != nil {
		return Comment{}, err
	}
	defer tx.Rollback()
	if s.writeGuard != nil && strings.TrimSpace(request.IdempotencyKey) != "" {
		fingerprint := commentFingerprint(slug, request.ClientIdentity, name, input.Email, website, body, input.ParentID)
		claim, err := s.writeGuard.ClaimTx(ctx, tx, "comments.create", request.IdempotencyKey, fingerprint)
		if err != nil {
			return Comment{}, err
		}
		if claim.Completed {
			if err := tx.Commit(); err != nil {
				return Comment{}, err
			}
			if len(claim.ResultPublicID) == 0 {
				return Comment{}, ErrNotFound
			}
			return s.GetByPublicID(ctx, claim.ResultPublicID)
		}
	} else if s.writeGuard != nil {
		fingerprint := commentFingerprint(slug, request.ClientIdentity, name, input.Email, website, body, input.ParentID)
		if _, err := s.writeGuard.ClaimFingerprintTx(ctx, tx, "comments.create", fingerprint, publicID); err != nil {
			return Comment{}, err
		}
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO comments(public_id,content_id,parent_id,display_name,email_hash,website,body_markdown,body_html,status,created_at,approved_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, publicID, article.ID, parent, name, emailHash, website, body, string(html), status, now.UnixMilli(), nullableTime(status == "approved", now), now.UnixMilli())
	if err != nil {
		return Comment{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Comment{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_entries(action,object_kind,object_public_id,result,context_json,created_at) VALUES('comments.created','comment',?,'succeeded',?,?)`, publicID, fmt.Sprintf(`{"status":%q}`, status), now.UnixMilli()); err != nil {
		return Comment{}, err
	}
	if status == "pending" && s.notifier != nil && s.notificationTo != "" {
		message := fmt.Sprintf("文章《%s》收到新的待审核评论。", article.Title)
		if notifier, ok := s.notifier.(transactionalNotifier); ok {
			if err := notifier.EnqueueTx(ctx, tx, "comment.pending", s.notificationTo, "有新的评论待审核", message, now); err != nil {
				return Comment{}, err
			}
		}
	}
	if s.writeGuard != nil && strings.TrimSpace(request.IdempotencyKey) != "" {
		responseBody, _ := json.Marshal(map[string]any{"status": status, "public_id": hex.EncodeToString(publicID)})
		if err := s.writeGuard.CompleteTx(ctx, tx, "comments.create", request.IdempotencyKey, httpCreated, publicID, responseBody); err != nil {
			return Comment{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Comment{}, err
	}
	if status == "pending" && s.notifier != nil && s.notificationTo != "" {
		if _, ok := s.notifier.(transactionalNotifier); !ok {
			_ = s.notifier.Enqueue(ctx, "comment.pending", s.notificationTo, "有新的评论待审核", fmt.Sprintf("文章《%s》收到新的待审核评论。", article.Title), now)
		}
	}
	return s.Get(ctx, id)
}

const httpCreated = 201

func commentFingerprint(slug, client, name, email, website, body string, parentID int64) string {
	return strings.Join([]string{strings.TrimSpace(slug), strings.TrimSpace(client), strings.TrimSpace(name), strings.ToLower(strings.TrimSpace(email)), strings.TrimSpace(website), strings.TrimSpace(body), fmt.Sprint(parentID)}, "\x00")
}

func (s *Service) Get(ctx context.Context, id int64) (Comment, error) {
	var result Comment
	var parent sql.NullInt64
	var approved sql.NullInt64
	var created, updated int64
	err := s.db.Reader.QueryRowContext(ctx, `SELECT id,public_id,content_id,parent_id,display_name,website,body_markdown,body_html,status,created_at,approved_at,updated_at FROM comments WHERE id=?`, id).Scan(&result.ID, &result.PublicID, &result.ContentID, &parent, &result.DisplayName, &result.Website, &result.BodyMarkdown, &result.BodyHTML, &result.Status, &created, &approved, &updated)
	if err == sql.ErrNoRows {
		return Comment{}, ErrNotFound
	}
	if err != nil {
		return Comment{}, err
	}
	if parent.Valid {
		value := parent.Int64
		result.ParentID = &value
	}
	result.CreatedAt = time.UnixMilli(created).UTC()
	if approved.Valid {
		value := time.UnixMilli(approved.Int64).UTC()
		result.ApprovedAt = &value
	}
	_ = updated
	return result, nil
}

func (s *Service) GetByPublicID(ctx context.Context, publicID []byte) (Comment, error) {
	if len(publicID) == 0 {
		return Comment{}, ErrNotFound
	}
	var id int64
	if err := s.db.Reader.QueryRowContext(ctx, "SELECT id FROM comments WHERE public_id=?", publicID).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Comment{}, ErrNotFound
		}
		return Comment{}, err
	}
	return s.Get(ctx, id)
}

func (s *Service) Approved(ctx context.Context, contentID int64) ([]Comment, error) {
	rows, err := s.db.Reader.QueryContext(ctx, `SELECT comment.id,comment.public_id,comment.content_id,comment.parent_id,comment.display_name,comment.website,comment.body_markdown,comment.body_html,comment.status,comment.created_at,comment.approved_at,comment.updated_at
		FROM comments comment JOIN contents content ON content.id=comment.content_id
		LEFT JOIN comments parent ON parent.id=comment.parent_id
		WHERE comment.content_id=? AND comment.status='approved' AND content.status='published' AND content.trashed_at IS NULL
			AND (parent.id IS NULL OR (parent.content_id=comment.content_id AND parent.status='approved'))
		ORDER BY comment.created_at,comment.id LIMIT 200`, contentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Comment
	for rows.Next() {
		var item Comment
		var parent, approved sql.NullInt64
		var created, updated int64
		if err := rows.Scan(&item.ID, &item.PublicID, &item.ContentID, &parent, &item.DisplayName, &item.Website, &item.BodyMarkdown, &item.BodyHTML, &item.Status, &created, &approved, &updated); err != nil {
			return nil, err
		}
		if parent.Valid {
			value := parent.Int64
			item.ParentID = &value
		}
		item.CreatedAt = time.UnixMilli(created).UTC()
		if approved.Valid {
			value := time.UnixMilli(approved.Int64).UTC()
			item.ApprovedAt = &value
		}
		_ = updated
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Service) Pending(ctx context.Context, limit int) ([]Comment, error) {
	if limit < 1 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	rows, err := s.db.Reader.QueryContext(ctx, `SELECT id,public_id,content_id,parent_id,display_name,website,body_markdown,body_html,status,created_at,approved_at,updated_at FROM comments WHERE status='pending' ORDER BY created_at,id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Comment
	for rows.Next() {
		var item Comment
		var parent, approved sql.NullInt64
		var created, updated int64
		if err := rows.Scan(&item.ID, &item.PublicID, &item.ContentID, &parent, &item.DisplayName, &item.Website, &item.BodyMarkdown, &item.BodyHTML, &item.Status, &created, &approved, &updated); err != nil {
			return nil, err
		}
		if parent.Valid {
			value := parent.Int64
			item.ParentID = &value
		}
		item.CreatedAt = time.UnixMilli(created).UTC()
		if approved.Valid {
			value := time.UnixMilli(approved.Int64).UTC()
			item.ApprovedAt = &value
		}
		_ = updated
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Service) PendingCount(ctx context.Context) (int, error) {
	var count int
	if err := s.db.Reader.QueryRowContext(ctx, "SELECT COUNT(*) FROM comments WHERE status='pending'").Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (s *Service) AdminComments(ctx context.Context, filter AdminCommentFilter) (AdminCommentPage, error) {
	filter.Status = strings.TrimSpace(filter.Status)
	if filter.Status == "" {
		filter.Status = "pending"
	}
	if filter.Status != "all" && filter.Status != "pending" && filter.Status != "approved" && filter.Status != "spam" && filter.Status != "trash" {
		return AdminCommentPage{}, ErrInvalid
	}
	filter.Query = strings.TrimSpace(filter.Query)
	if len([]rune(filter.Query)) > 100 {
		return AdminCommentPage{}, ErrInvalid
	}
	filter.ArticleSlug = strings.TrimSpace(filter.ArticleSlug)
	if len([]rune(filter.ArticleSlug)) > 120 || strings.ContainsAny(filter.ArticleSlug, "\r\n") {
		return AdminCommentPage{}, ErrInvalid
	}
	if filter.From != nil && filter.To != nil && !filter.From.Before(*filter.To) {
		return AdminCommentPage{}, ErrInvalid
	}
	if filter.Page < 1 {
		filter.Page = 1
	}
	if filter.Page > 100 {
		filter.Page = 100
	}
	if filter.PerPage < 1 {
		filter.PerPage = 20
	}
	if filter.PerPage > 50 {
		filter.PerPage = 50
	}
	where, args := adminCommentWhere(filter)
	var total int
	if err := s.db.Reader.QueryRowContext(ctx, "SELECT COUNT(*) FROM comments c JOIN contents content ON content.id=c.content_id "+where, args...).Scan(&total); err != nil {
		return AdminCommentPage{}, err
	}
	rows, err := s.db.Reader.QueryContext(ctx, `SELECT c.id,c.public_id,c.content_id,c.parent_id,c.display_name,c.website,c.body_markdown,c.body_html,c.status,c.created_at,c.approved_at,c.updated_at,content.title,COALESCE(content.published_slug,content.slug,'')
		FROM comments c JOIN contents content ON content.id=c.content_id `+where+` ORDER BY c.created_at DESC,c.id DESC LIMIT ? OFFSET ?`, append(args, filter.PerPage, (filter.Page-1)*filter.PerPage)...)
	if err != nil {
		return AdminCommentPage{}, err
	}
	defer rows.Close()
	comments := make([]AdminComment, 0, filter.PerPage)
	for rows.Next() {
		item, err := scanAdminComment(rows)
		if err != nil {
			return AdminCommentPage{}, err
		}
		comments = append(comments, item)
	}
	if err := rows.Err(); err != nil {
		return AdminCommentPage{}, err
	}
	return AdminCommentPage{Comments: comments, Total: total, Page: filter.Page, PerPage: filter.PerPage, HasMore: filter.Page*filter.PerPage < total}, nil
}

func adminCommentWhere(filter AdminCommentFilter) (string, []any) {
	clauses := []string{"1=1"}
	args := make([]any, 0, 9)
	if filter.Status != "all" {
		clauses = append(clauses, "c.status=?")
		args = append(args, filter.Status)
	}
	if filter.Query != "" {
		pattern := "%" + escapeCommentLike(filter.Query) + "%"
		clauses = append(clauses, `(c.display_name LIKE ? COLLATE NOCASE ESCAPE char(92) OR c.body_markdown LIKE ? COLLATE NOCASE ESCAPE char(92) OR content.title LIKE ? COLLATE NOCASE ESCAPE char(92))`)
		args = append(args, pattern, pattern, pattern)
	}
	if filter.ArticleSlug != "" {
		clauses = append(clauses, "COALESCE(content.published_slug,content.slug,'')=?")
		args = append(args, filter.ArticleSlug)
	}
	if filter.From != nil {
		clauses = append(clauses, "c.created_at>=?")
		args = append(args, filter.From.UTC().UnixMilli())
	}
	if filter.To != nil {
		clauses = append(clauses, "c.created_at<?")
		args = append(args, filter.To.UTC().UnixMilli())
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

func escapeCommentLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "%", `\%`)
	return strings.ReplaceAll(value, "_", `\_`)
}

type rowScanner interface {
	Scan(...any) error
}

func scanAdminComment(row rowScanner) (AdminComment, error) {
	var item AdminComment
	var parent, approved sql.NullInt64
	var created, updated int64
	if err := row.Scan(&item.ID, &item.PublicID, &item.ContentID, &parent, &item.DisplayName, &item.Website, &item.BodyMarkdown, &item.BodyHTML, &item.Status, &created, &approved, &updated, &item.ContentTitle, &item.ContentSlug); err != nil {
		return AdminComment{}, err
	}
	if parent.Valid {
		value := parent.Int64
		item.ParentID = &value
	}
	item.CreatedAt = time.UnixMilli(created).UTC()
	if approved.Valid {
		value := time.UnixMilli(approved.Int64).UTC()
		item.ApprovedAt = &value
	}
	return item, nil
}

func (s *Service) Moderate(ctx context.Context, id int64, status string) error {
	if status != "approved" && status != "spam" && status != "trash" && status != "pending" {
		return ErrInvalid
	}
	now := s.now()
	tx, err := s.db.Writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var publicID, contentPublicID []byte
	var commentContentID int64
	var contentStatus string
	var trashedAt sql.NullInt64
	var parentContentID sql.NullInt64
	var parentStatus sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT comment.public_id,content.public_id,comment.content_id,content.status,content.trashed_at,parent.content_id,parent.status
		FROM comments comment JOIN contents content ON content.id=comment.content_id
		LEFT JOIN comments parent ON parent.id=comment.parent_id WHERE comment.id=?`, id).Scan(&publicID, &contentPublicID, &commentContentID, &contentStatus, &trashedAt, &parentContentID, &parentStatus); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if parentContentID.Valid && parentContentID.Int64 != commentContentID {
		return ErrInvalid
	}
	if status == "approved" && (contentStatus != "published" || trashedAt.Valid || (parentContentID.Valid && parentStatus.String != "approved")) {
		return ErrInvalid
	}
	result, err := tx.ExecContext(ctx, "UPDATE comments SET status=?,approved_at=?,updated_at=? WHERE id=?", status, nullableTime(status == "approved", now), now.UnixMilli(), id)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_entries(action,object_kind,object_public_id,result,context_json,created_at) VALUES('comments.moderated','comment',?,'succeeded',?,?)`, publicID, fmt.Sprintf(`{"status":%q}`, status), now.UnixMilli()); err != nil {
		return err
	}
	if status == "approved" && s.eventRecorder != nil {
		if err := s.eventRecorder.RecordEventTx(ctx, tx, extensions.Event{
			Name: "CommentApproved.v1", Version: 1, ObjectID: append([]byte(nil), publicID...),
			Payload: map[string]any{"content_public_id": hex.EncodeToString(contentPublicID)}, OccurredAt: now,
		}); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if status == "approved" && s.eventRecorder == nil && s.events != nil {
		_ = s.events.Dispatch(ctx, extensions.Event{Name: "CommentApproved.v1", Version: 1, ObjectID: append([]byte(nil), publicID...), Payload: map[string]any{"content_public_id": hex.EncodeToString(contentPublicID)}})
	}
	return nil
}

func (s *Service) ModerateBulk(ctx context.Context, ids []int64, status, operationKey string) (ModerationBulkResult, error) {
	if status != "approved" && status != "spam" && status != "trash" && status != "pending" {
		return ModerationBulkResult{}, ErrInvalid
	}
	if len(ids) == 0 || len(ids) > 50 || strings.TrimSpace(operationKey) == "" || len(operationKey) > 180 || strings.ContainsAny(operationKey, "\r\n") {
		return ModerationBulkResult{}, ErrInvalid
	}
	unique := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id < 1 {
			return ModerationBulkResult{}, ErrInvalid
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	input, err := json.Marshal(struct {
		IDs    []int64 `json:"ids"`
		Status string  `json:"status"`
	}{unique, status})
	if err != nil {
		return ModerationBulkResult{}, err
	}
	now := s.now().UTC()
	if cached, found, err := s.claimBulk(ctx, operationKey, string(input), now); err != nil {
		return ModerationBulkResult{}, err
	} else if found {
		return cached, nil
	}
	result := ModerationBulkResult{OperationKey: operationKey, Action: status, Items: make([]ModerationItemResult, 0, len(unique))}
	for _, id := range unique {
		item := ModerationItemResult{ID: id, Status: "updated"}
		if err := s.Moderate(ctx, id, status); err != nil {
			if errors.Is(err, ErrNotFound) {
				item.Status, item.Error = "not_found", "评论不存在"
			} else if errors.Is(err, ErrInvalid) {
				item.Status, item.Error = "invalid", "评论状态无效"
			} else {
				_ = s.finishBulk(ctx, operationKey, result, false, now)
				return ModerationBulkResult{}, err
			}
		}
		result.Items = append(result.Items, item)
	}
	for _, item := range result.Items {
		switch item.Status {
		case "updated":
			result.Succeeded++
		case "not_found":
			result.NotFound++
		case "invalid":
			result.Invalid++
		}
	}
	if err := s.finishBulk(ctx, operationKey, result, true, now); err != nil {
		return ModerationBulkResult{}, err
	}
	return result, nil
}

func (s *Service) claimBulk(ctx context.Context, operationKey, input string, now time.Time) (ModerationBulkResult, bool, error) {
	tx, err := s.db.Writer.BeginTx(ctx, nil)
	if err != nil {
		return ModerationBulkResult{}, false, err
	}
	defer tx.Rollback()
	var storedInput, status, resultJSON string
	var updatedAt int64
	err = tx.QueryRowContext(ctx, "SELECT input_json,status,result_json,updated_at FROM bulk_operations WHERE operation_key=? AND object_kind='comment' AND action='moderate'", operationKey).Scan(&storedInput, &status, &resultJSON, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO bulk_operations(operation_key,object_kind,action,input_json,status,result_json,created_at,updated_at) VALUES(?,'comment','moderate',?,'processing','{}',?,?)`, operationKey, input, now.UnixMilli(), now.UnixMilli()); err != nil {
			return ModerationBulkResult{}, false, err
		}
		return ModerationBulkResult{}, false, tx.Commit()
	}
	if err != nil {
		return ModerationBulkResult{}, false, err
	}
	if storedInput != input {
		return ModerationBulkResult{}, false, ErrBulkKeyConflict
	}
	if status == "succeeded" {
		var cached ModerationBulkResult
		if err := json.Unmarshal([]byte(resultJSON), &cached); err != nil {
			return ModerationBulkResult{}, false, err
		}
		return cached, true, tx.Commit()
	}
	if status == "processing" && now.UnixMilli()-updatedAt < int64((5*time.Minute)/time.Millisecond) {
		return ModerationBulkResult{}, false, ErrBulkInProgress
	}
	if _, err := tx.ExecContext(ctx, "UPDATE bulk_operations SET status='processing',result_json='{}',updated_at=? WHERE operation_key=? AND object_kind='comment' AND action='moderate'", now.UnixMilli(), operationKey); err != nil {
		return ModerationBulkResult{}, false, err
	}
	return ModerationBulkResult{}, false, tx.Commit()
}

func (s *Service) finishBulk(ctx context.Context, operationKey string, result ModerationBulkResult, succeeded bool, now time.Time) error {
	payload, err := json.Marshal(result)
	if err != nil {
		return err
	}
	status := "failed"
	if succeeded {
		status = "succeeded"
	}
	_, err = s.db.Writer.ExecContext(ctx, "UPDATE bulk_operations SET status=?,result_json=?,updated_at=? WHERE operation_key=? AND object_kind='comment' AND action='moderate'", status, string(payload), now.UnixMilli(), operationKey)
	return err
}

func nullableTime(ok bool, value time.Time) any {
	if ok {
		return value.UnixMilli()
	}
	return nil
}
