package comments

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zhushilin/blog-project/internal/extensions"
	"github.com/zhushilin/blog-project/internal/platform/database"
	platformid "github.com/zhushilin/blog-project/internal/platform/id"
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
}

func (s *Service) SetNotifier(notifier Notifier, recipient string) {
	s.notifier, s.notificationTo = notifier, strings.TrimSpace(recipient)
}

func (s *Service) SetEventSink(sink interface {
	Dispatch(context.Context, extensions.Event) []error
}) {
	s.events = sink
}

func NewService(db *database.DB, lookup ContentLookup, markdown *presentation.Markdown, requireModeration bool) *Service {
	if markdown == nil {
		markdown = presentation.NewMarkdown()
	}
	return &Service{db: db, lookup: lookup, markdown: markdown, requireModeration: requireModeration, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) Create(ctx context.Context, slug string, input Input) (Comment, error) {
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
	result, err := s.db.Writer.ExecContext(ctx, `INSERT INTO comments(public_id,content_id,parent_id,display_name,email_hash,website,body_markdown,body_html,status,created_at,approved_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, publicID, article.ID, parent, name, emailHash, website, body, string(html), status, now.UnixMilli(), nullableTime(status == "approved", now), now.UnixMilli())
	if err != nil {
		return Comment{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Comment{}, err
	}
	_, _ = s.db.Writer.ExecContext(ctx, `INSERT INTO audit_entries(action,object_kind,object_public_id,result,context_json,created_at) VALUES('comments.created','comment',?,'succeeded',?,?)`, publicID, fmt.Sprintf(`{"content_id":%d,"status":%q}`, article.ID, status), now.UnixMilli())
	if status == "pending" && s.notifier != nil && s.notificationTo != "" {
		_ = s.notifier.Enqueue(ctx, "comment.pending", s.notificationTo, "有新的评论待审核", fmt.Sprintf("文章《%s》收到来自 %s 的新评论。", article.Title, name), now)
	}
	return s.Get(ctx, id)
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

func (s *Service) Approved(ctx context.Context, contentID int64) ([]Comment, error) {
	rows, err := s.db.Reader.QueryContext(ctx, `SELECT id,public_id,content_id,parent_id,display_name,website,body_markdown,body_html,status,created_at,approved_at,updated_at FROM comments WHERE content_id=? AND status='approved' ORDER BY created_at,id`, contentID)
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

func (s *Service) Moderate(ctx context.Context, id int64, status string) error {
	if status != "approved" && status != "spam" && status != "trash" && status != "pending" {
		return ErrInvalid
	}
	now := s.now()
	result, err := s.db.Writer.ExecContext(ctx, "UPDATE comments SET status=?,approved_at=?,updated_at=? WHERE id=?", status, nullableTime(status == "approved", now), now.UnixMilli(), id)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return ErrNotFound
	}
	_, _ = s.db.Writer.ExecContext(ctx, `INSERT INTO audit_entries(action,object_kind,result,context_json,created_at) VALUES('comments.moderated','comment','succeeded',?,?)`, fmt.Sprintf(`{"comment_id":%d,"status":%q}`, id, status), now.UnixMilli())
	if status == "approved" && s.events != nil {
		if comment, err := s.Get(ctx, id); err == nil {
			_ = s.events.Dispatch(ctx, extensions.Event{Name: "CommentApproved.v1", Version: 1, ObjectID: append([]byte(nil), comment.PublicID...), Payload: map[string]any{"content_id": comment.ContentID, "comment_id": comment.ID}})
		}
	}
	return nil
}

func nullableTime(ok bool, value time.Time) any {
	if ok {
		return value.UnixMilli()
	}
	return nil
}
