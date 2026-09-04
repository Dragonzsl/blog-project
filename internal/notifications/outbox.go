package notifications

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/zhushilin/blog-project/internal/operations"
	"github.com/zhushilin/blog-project/internal/platform/database"
)

const notificationMaxAttempts = operations.DefaultTaskMaxAttempts

type Outbox struct {
	db     *database.DB
	Mailer Mailer
	now    func() time.Time
	queue  *operations.TaskQueue
}

func NewOutbox(db *database.DB, mailer Mailer) *Outbox {
	if mailer == nil {
		mailer = NoopMailer{}
	}
	return &Outbox{db: db, Mailer: mailer, now: func() time.Time { return time.Now().UTC() }}
}

func (o *Outbox) SetTaskQueue(queue *operations.TaskQueue) { o.queue = queue }

func (o *Outbox) Enqueue(ctx context.Context, kind, recipient, subject, body string, availableAt time.Time) error {
	return o.EnqueueIdempotent(ctx, kind, recipient, subject, body, "", availableAt)
}

func (o *Outbox) EnqueueIdempotent(ctx context.Context, kind, recipient, subject, body, idempotencyKey string, availableAt time.Time) error {
	if o.db == nil {
		return errors.New("notification outbox is not configured")
	}
	tx, err := o.db.Writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := o.enqueueTx(ctx, tx, kind, recipient, subject, body, idempotencyKey, availableAt); err != nil {
		return err
	}
	return tx.Commit()
}

// EnqueueTx has a concrete signature so comments and other core writers can
// atomically add their notification and the shared queue job.
func (o *Outbox) EnqueueTx(ctx context.Context, tx *sql.Tx, kind, recipient, subject, body string, availableAt time.Time) error {
	return o.enqueueTx(ctx, tx, kind, recipient, subject, body, "", availableAt)
}

func (o *Outbox) enqueueTx(ctx context.Context, tx *sql.Tx, kind, recipient, subject, body, idempotencyKey string, availableAt time.Time) error {
	if o.db == nil || tx == nil || kind == "" || recipient == "" || subject == "" {
		return errors.New("notification fields are required")
	}
	if availableAt.IsZero() {
		availableAt = o.now()
	}
	if len(kind) > 120 || len(recipient) > 320 || len(subject) > 500 || len(body) > 256<<10 || len(idempotencyKey) > 256 {
		return errors.New("notification fields are too large")
	}
	now := o.now().UTC()
	result, err := tx.ExecContext(ctx, `INSERT INTO notification_outbox(kind,recipient,subject,body,status,available_at,attempts,last_error,created_at,sent_at,idempotency_key,lease_expires_at,updated_at) VALUES(?,?,?,?,'pending',?,0,'',?,?,?,NULL,?) ON CONFLICT(idempotency_key) WHERE idempotency_key <> '' DO NOTHING`, kind, recipient, subject, body, availableAt.UTC().UnixMilli(), now.UnixMilli(), nil, idempotencyKey, now.UnixMilli())
	if err != nil {
		return err
	}
	inserted, err := result.RowsAffected()
	if err != nil || inserted == 0 {
		return err
	}
	if o.queue == nil {
		return nil
	}
	id, err := result.LastInsertId()
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(notificationTask{NotificationID: id})
	return o.queue.EnqueueTx(ctx, tx, operations.Task{Kind: "core:notification_send", PayloadVersion: 1, Payload: payload, IdempotencyKey: fmt.Sprintf("notification:%d", id), AvailableAt: availableAt})
}

type notificationTask struct {
	NotificationID int64 `json:"notification_id"`
}

func (o *Outbox) ProcessTask(ctx context.Context, payload []byte) error {
	if o == nil || o.db == nil {
		return errors.New("notification outbox is not configured")
	}
	var task notificationTask
	if err := json.Unmarshal(payload, &task); err != nil || task.NotificationID < 1 {
		return errors.New("invalid notification task payload")
	}
	// A job that reached the queue's terminal attempt leaves the delivery in a
	// terminal state. An operator retry resets the job, and this idempotent
	// transition reopens the corresponding notification exactly once.
	_, _ = o.db.Writer.ExecContext(ctx, `UPDATE notification_outbox SET status='pending',attempts=0,last_error='',available_at=?,lease_expires_at=NULL,updated_at=? WHERE id=? AND status='failed'`, o.now().UnixMilli(), o.now().UnixMilli(), task.NotificationID)
	_, err := o.sendOne(ctx, task.NotificationID)
	return err
}

// PrepareTaskRetryTx reopens the durable notification record together with its
// failed generic job. If the job failed before the mail attempt, the pending
// row is intentionally left unchanged.
func (o *Outbox) PrepareTaskRetryTx(ctx context.Context, tx *sql.Tx, task operations.Task) error {
	var payload notificationTask
	if err := json.Unmarshal(task.Payload, &payload); err != nil || payload.NotificationID < 1 {
		return errors.New("invalid notification retry payload")
	}
	_, err := tx.ExecContext(ctx, `UPDATE notification_outbox SET status='pending',attempts=0,last_error='',available_at=?,lease_expires_at=NULL,updated_at=? WHERE id=? AND status='failed'`, o.now().UnixMilli(), o.now().UnixMilli(), payload.NotificationID)
	return err
}

// ProcessOne remains a compatibility drain for rows created by versions
// before core:notification_send existed.
func (o *Outbox) ProcessOne(ctx context.Context) (bool, error) {
	if o == nil || o.db == nil {
		return false, nil
	}
	now := o.now().UnixMilli()
	var id int64
	err := o.db.Reader.QueryRowContext(ctx, `SELECT id FROM notification_outbox WHERE status='pending' AND available_at<=? AND (lease_expires_at IS NULL OR lease_expires_at<=?) ORDER BY id LIMIT 1`, now, now).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return o.sendOne(ctx, id)
}

func (o *Outbox) sendOne(ctx context.Context, id int64) (bool, error) {
	now := o.now().UTC()
	lease := now.Add(operations.DefaultTaskLease).UnixMilli()
	result, err := o.db.Writer.ExecContext(ctx, `UPDATE notification_outbox SET lease_expires_at=?,attempts=attempts+1,updated_at=? WHERE id=? AND status='pending' AND available_at<=? AND (lease_expires_at IS NULL OR lease_expires_at<=?)`, lease, now.UnixMilli(), id, now.UnixMilli(), now.UnixMilli())
	if err != nil {
		return false, err
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return false, nil
	}
	var recipient, subject, body string
	var attempts int
	if err := o.db.Reader.QueryRowContext(ctx, `SELECT recipient,subject,body,attempts FROM notification_outbox WHERE id=?`, id).Scan(&recipient, &subject, &body, &attempts); err != nil {
		return true, err
	}
	if err := o.Mailer.Send(ctx, Message{To: recipient, Subject: subject, Text: body}); err != nil {
		safe := operations.SafeError(err)
		status := "pending"
		availableAt := now.Add(notificationRetryDelay(attempts))
		if operations.IsPermanent(err) || attempts >= notificationMaxAttempts {
			status = "failed"
			availableAt = now
		}
		_, _ = o.db.Writer.ExecContext(ctx, `UPDATE notification_outbox SET status=?,available_at=?,lease_expires_at=NULL,last_error=?,updated_at=? WHERE id=?`, status, availableAt.UnixMilli(), safe, now.UnixMilli(), id)
		if operations.IsPermanent(err) {
			return true, operations.Permanent(errors.New(safe))
		}
		return true, errors.New(safe)
	}
	_, err = o.db.Writer.ExecContext(ctx, `UPDATE notification_outbox SET status='sent',sent_at=?,lease_expires_at=NULL,last_error='',updated_at=? WHERE id=?`, now.UnixMilli(), now.UnixMilli(), id)
	return true, err
}

func (o *Outbox) Retry(ctx context.Context, id int64) error {
	now := o.now().UnixMilli()
	result, err := o.db.Writer.ExecContext(ctx, `UPDATE notification_outbox SET status='pending',available_at=?,attempts=0,last_error='',lease_expires_at=NULL,updated_at=? WHERE id=? AND status='failed'`, now, now, id)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return operations.ErrTaskNotFound
	}
	return nil
}

func notificationRetryDelay(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	if attempts > notificationMaxAttempts {
		attempts = notificationMaxAttempts
	}
	return time.Duration(1<<(attempts-1)) * time.Second
}
