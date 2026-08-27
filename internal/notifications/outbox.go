package notifications

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/zhushilin/blog-project/internal/platform/database"
)

type Outbox struct {
	db     *database.DB
	Mailer Mailer
	now    func() time.Time
}

func NewOutbox(db *database.DB, mailer Mailer) *Outbox {
	if mailer == nil {
		mailer = NoopMailer{}
	}
	return &Outbox{db: db, Mailer: mailer, now: func() time.Time { return time.Now().UTC() }}
}

func (o *Outbox) Enqueue(ctx context.Context, kind, recipient, subject, body string, availableAt time.Time) error {
	if o.db == nil || kind == "" || recipient == "" || subject == "" {
		return errors.New("notification fields are required")
	}
	if availableAt.IsZero() {
		availableAt = o.now()
	}
	_, err := o.db.Writer.ExecContext(ctx, `INSERT INTO notification_outbox(kind,recipient,subject,body,status,available_at,created_at) VALUES(?,?,?,?,'pending',?,?)`, kind, recipient, subject, body, availableAt.UTC().UnixMilli(), o.now().UnixMilli())
	return err
}

func (o *Outbox) ProcessOne(ctx context.Context) (bool, error) {
	row := o.db.Writer.QueryRowContext(ctx, `SELECT id,recipient,subject,body FROM notification_outbox WHERE status='pending' AND available_at<=? ORDER BY id LIMIT 1`, o.now().UnixMilli())
	var id int64
	var recipient, subject, body string
	if err := row.Scan(&id, &recipient, &subject, &body); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	result, err := o.db.Writer.ExecContext(ctx, `UPDATE notification_outbox SET status='pending',attempts=attempts+1 WHERE id=? AND status='pending'`, id)
	if err != nil {
		return false, err
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		return false, nil
	}
	err = o.Mailer.Send(ctx, Message{To: recipient, Subject: subject, Text: body})
	if err != nil {
		_, _ = o.db.Writer.ExecContext(ctx, `UPDATE notification_outbox SET status='failed',last_error=? WHERE id=?`, err.Error(), id)
		return true, err
	}
	_, err = o.db.Writer.ExecContext(ctx, `UPDATE notification_outbox SET status='sent',sent_at=? WHERE id=?`, o.now().UnixMilli(), id)
	return true, err
}
