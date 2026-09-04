package operations

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/zhushilin/blog-project/internal/platform/database"
)

var (
	ErrInvalidTask  = errors.New("task is invalid")
	ErrTaskNotFound = errors.New("task not found")
)

type permanentTaskError struct{ err error }

func (e *permanentTaskError) Error() string { return e.err.Error() }
func (e *permanentTaskError) Unwrap() error { return e.err }

// Permanent marks a handler error that cannot be repaired by waiting for a
// remote retry, such as a blocked destination or an invalid request contract.
// Operators can still reopen the task explicitly from the operations page.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &permanentTaskError{err: err}
}

func IsPermanent(err error) bool {
	var target *permanentTaskError
	return errors.As(err, &target)
}

const (
	DefaultTaskMaxAttempts = 5
	DefaultTaskLease       = 30 * time.Second
	DefaultTaskRetryBase   = time.Second
	DefaultTaskRetryMax    = 5 * time.Minute
	maxTaskKindLength      = 160
	maxTaskKeyLength       = 256
	maxTaskPayloadBytes    = 64 << 10
	maxTaskScan            = 64
	maxTaskErrorLength     = 512
)

type Task struct {
	ID             int64
	Kind           string
	PayloadVersion int
	Payload        []byte
	IdempotencyKey string
	Status         string
	AvailableAt    time.Time
	LeaseExpiresAt *time.Time
	Attempts       int
	LastError      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type TaskHandler func(context.Context, Task) error
type TaskRetryHook func(context.Context, *sql.Tx, Task) error

type TaskQueueOptions struct {
	MaxAttempts int
	Lease       time.Duration
	RetryBase   time.Duration
	RetryMax    time.Duration
	BatchSize   int
	Now         func() time.Time
}

type TaskQueue struct {
	db          *database.DB
	maxAttempts int
	lease       time.Duration
	retryBase   time.Duration
	retryMax    time.Duration
	batchSize   int
	now         func() time.Time
}

func NewTaskQueue(db *database.DB, configured ...TaskQueueOptions) *TaskQueue {
	options := TaskQueueOptions{
		MaxAttempts: DefaultTaskMaxAttempts,
		Lease:       DefaultTaskLease,
		RetryBase:   DefaultTaskRetryBase,
		RetryMax:    DefaultTaskRetryMax,
		BatchSize:   maxTaskScan,
		Now:         func() time.Time { return time.Now().UTC() },
	}
	if len(configured) > 0 {
		provided := configured[0]
		if provided.MaxAttempts > 0 {
			options.MaxAttempts = provided.MaxAttempts
		}
		if provided.Lease > 0 {
			options.Lease = provided.Lease
		}
		if provided.RetryBase > 0 {
			options.RetryBase = provided.RetryBase
		}
		if provided.RetryMax > 0 {
			options.RetryMax = provided.RetryMax
		}
		if provided.BatchSize > 0 {
			options.BatchSize = provided.BatchSize
		}
		if provided.Now != nil {
			options.Now = provided.Now
		}
	}
	if options.MaxAttempts > DefaultTaskMaxAttempts {
		options.MaxAttempts = DefaultTaskMaxAttempts
	}
	if options.MaxAttempts < 1 {
		options.MaxAttempts = DefaultTaskMaxAttempts
	}
	if options.Lease > 5*time.Minute {
		options.Lease = 5 * time.Minute
	}
	if options.BatchSize > maxTaskScan {
		options.BatchSize = maxTaskScan
	}
	return &TaskQueue{db: db, maxAttempts: options.MaxAttempts, lease: options.Lease, retryBase: options.RetryBase, retryMax: options.RetryMax, batchSize: options.BatchSize, now: options.Now}
}

func (q *TaskQueue) Enqueue(ctx context.Context, task Task) error {
	if q == nil || q.db == nil || q.db.Writer == nil {
		return errors.New("task queue is not configured")
	}
	tx, err := q.db.Writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := q.EnqueueTx(ctx, tx, task); err != nil {
		return err
	}
	return tx.Commit()
}

func (q *TaskQueue) EnqueueTx(ctx context.Context, tx *sql.Tx, task Task) error {
	if q == nil || tx == nil {
		return errors.New("task queue transaction is not configured")
	}
	if err := validateTask(task); err != nil {
		return err
	}
	now := q.now().UTC()
	availableAt := task.AvailableAt
	if availableAt.IsZero() {
		availableAt = now
	}
	version := task.PayloadVersion
	if version < 1 {
		version = 1
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO jobs(kind,payload_version,payload,idempotency_key,status,available_at,attempts,created_at,updated_at)
		VALUES(?,?,?,?,'pending',?,0,?,?)
		ON CONFLICT(idempotency_key) DO NOTHING
	`, task.Kind, version, task.Payload, task.IdempotencyKey, availableAt.UTC().UnixMilli(), now.UnixMilli(), now.UnixMilli())
	return err
}

// ProcessOne claims one runnable task selected by the caller, runs its
// handler outside the SQLite transaction, and persists completion or bounded
// retry state. A selector returning false leaves the task pending, which is
// how disabled plugins retain their work without consuming it.
func (q *TaskQueue) ProcessOne(ctx context.Context, selector func(Task) (TaskHandler, bool)) (bool, error) {
	if q == nil || q.db == nil {
		return false, errors.New("task queue is not configured")
	}
	if err := q.RecoverExpired(ctx); err != nil {
		return false, err
	}
	task, handler, ok, err := q.claim(ctx, selector)
	if err != nil || !ok {
		return false, err
	}
	if err := handler(ctx, task); err != nil {
		safe := SafeError(err)
		var updateErr error
		if IsPermanent(err) {
			updateErr = q.FailPermanent(ctx, task.ID, task.Attempts, safe)
		} else {
			updateErr = q.Fail(ctx, task.ID, task.Attempts, safe)
		}
		if updateErr != nil {
			return true, errors.Join(err, updateErr)
		}
		return true, errors.New(safe)
	}
	if err := q.Complete(ctx, task.ID); err != nil {
		return true, err
	}
	return true, nil
}

func (q *TaskQueue) claim(ctx context.Context, selector func(Task) (TaskHandler, bool)) (Task, TaskHandler, bool, error) {
	if selector == nil {
		return Task{}, nil, false, errors.New("task selector is required")
	}
	now := q.now().UTC()
	tx, err := q.db.Writer.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, nil, false, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `
		SELECT id,kind,payload_version,payload,idempotency_key,status,available_at,lease_expires_at,attempts,COALESCE(last_error,''),created_at,updated_at
		FROM jobs
		WHERE status='pending' AND available_at<=? AND attempts<?
		ORDER BY available_at,id LIMIT ?
	`, now.UnixMilli(), q.maxAttempts, q.batchSize)
	if err != nil {
		return Task{}, nil, false, err
	}
	defer rows.Close()
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return Task{}, nil, false, err
		}
		handler, selected := selector(task)
		if !selected || handler == nil {
			continue
		}
		leaseExpires := now.Add(q.lease).UnixMilli()
		result, err := tx.ExecContext(ctx, `UPDATE jobs SET status='running',attempts=attempts+1,lease_expires_at=?,updated_at=? WHERE id=? AND status='pending'`, leaseExpires, now.UnixMilli(), task.ID)
		if err != nil {
			return Task{}, nil, false, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return Task{}, nil, false, err
		}
		if affected != 1 {
			continue
		}
		if err := rows.Close(); err != nil {
			return Task{}, nil, false, err
		}
		if err := tx.Commit(); err != nil {
			return Task{}, nil, false, err
		}
		task.Status = "running"
		task.Attempts++
		expires := now.Add(q.lease)
		task.LeaseExpiresAt = &expires
		task.UpdatedAt = now
		return task, handler, true, nil
	}
	if err := rows.Err(); err != nil {
		return Task{}, nil, false, err
	}
	if err := rows.Close(); err != nil {
		return Task{}, nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return Task{}, nil, false, err
	}
	return Task{}, nil, false, nil
}

func (q *TaskQueue) Complete(ctx context.Context, id int64) error {
	if id < 1 {
		return ErrTaskNotFound
	}
	result, err := q.db.Writer.ExecContext(ctx, `UPDATE jobs SET status='succeeded',lease_expires_at=NULL,last_error=NULL,updated_at=? WHERE id=? AND status='running'`, q.now().UTC().UnixMilli(), id)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return ErrTaskNotFound
	}
	return nil
}

func (q *TaskQueue) Fail(ctx context.Context, id int64, attempts int, safeError string) error {
	return q.fail(ctx, id, attempts, safeError, false)
}

func (q *TaskQueue) FailPermanent(ctx context.Context, id int64, attempts int, safeError string) error {
	return q.fail(ctx, id, attempts, safeError, true)
}

func (q *TaskQueue) fail(ctx context.Context, id int64, attempts int, safeError string, permanent bool) error {
	if id < 1 {
		return ErrTaskNotFound
	}
	if attempts < 1 {
		attempts = 1
	}
	now := q.now().UTC()
	status := "pending"
	availableAt := now.Add(q.retryDelay(attempts))
	if permanent || attempts >= q.maxAttempts {
		status = "failed"
		availableAt = now
	}
	result, err := q.db.Writer.ExecContext(ctx, `UPDATE jobs SET status=?,available_at=?,lease_expires_at=NULL,last_error=?,updated_at=? WHERE id=? AND status='running'`, status, availableAt.UnixMilli(), truncateError(safeError), now.UnixMilli(), id)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return ErrTaskNotFound
	}
	return nil
}

func (q *TaskQueue) retryDelay(attempts int) time.Duration {
	delay := q.retryBase
	for i := 1; i < attempts && delay < q.retryMax; i++ {
		delay *= 2
	}
	if delay > q.retryMax {
		return q.retryMax
	}
	return delay
}

func (q *TaskQueue) RecoverExpired(ctx context.Context) error {
	if q == nil || q.db == nil {
		return errors.New("task queue is not configured")
	}
	now := q.now().UTC().UnixMilli()
	_, err := q.db.Writer.ExecContext(ctx, `
		UPDATE jobs
		SET status=CASE WHEN attempts>=? THEN 'failed' ELSE 'pending' END,
		    available_at=CASE WHEN attempts>=? THEN ? ELSE ? END,
		    lease_expires_at=NULL,
		    last_error=CASE WHEN attempts>=? AND COALESCE(last_error,'')='' THEN 'task lease expired after maximum attempts' ELSE last_error END,
		    updated_at=?
		WHERE status='running' AND lease_expires_at IS NOT NULL AND lease_expires_at<?
	`, q.maxAttempts, q.maxAttempts, now, now, q.maxAttempts, now, now)
	return err
}

func (q *TaskQueue) Retry(ctx context.Context, id int64) error {
	return q.RetryWith(ctx, id, nil)
}

// RetryWith reopens a failed task using the same row and idempotency key. A
// module may use the transaction hook to reset its own delivery record before
// the job becomes runnable again; both changes are committed atomically.
func (q *TaskQueue) RetryWith(ctx context.Context, id int64, hook TaskRetryHook) error {
	if id < 1 {
		return ErrTaskNotFound
	}
	now := q.now().UTC()
	tx, err := q.db.Writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	task, err := scanTask(tx.QueryRowContext(ctx, `SELECT id,kind,payload_version,payload,idempotency_key,status,available_at,lease_expires_at,attempts,COALESCE(last_error,''),created_at,updated_at FROM jobs WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrTaskNotFound
	}
	if err != nil {
		return err
	}
	if task.Status != "failed" {
		return ErrTaskNotFound
	}
	if hook != nil {
		if err := hook(ctx, tx, task); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE jobs SET status='pending',available_at=?,lease_expires_at=NULL,attempts=0,last_error=NULL,updated_at=? WHERE id=? AND status='failed'`, now.UnixMilli(), now.UnixMilli(), id)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return ErrTaskNotFound
	}
	contextJSON, _ := json.Marshal(map[string]any{"kind": task.Kind, "attempts": task.Attempts})
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_entries(action,object_kind,result,context_json,created_at) VALUES('operations.task.retried','job','succeeded',?,?)`, string(contextJSON), now.UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}

func (q *TaskQueue) Get(ctx context.Context, id int64) (Task, error) {
	var task Task
	var available, created, updated int64
	var lease sql.NullInt64
	err := q.db.Reader.QueryRowContext(ctx, `
		SELECT id,kind,payload_version,payload,idempotency_key,status,available_at,lease_expires_at,attempts,COALESCE(last_error,''),created_at,updated_at
		FROM jobs WHERE id=?
	`, id).Scan(&task.ID, &task.Kind, &task.PayloadVersion, &task.Payload, &task.IdempotencyKey, &task.Status, &available, &lease, &task.Attempts, &task.LastError, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrTaskNotFound
	}
	task.AvailableAt = time.UnixMilli(available).UTC()
	if lease.Valid {
		value := time.UnixMilli(lease.Int64).UTC()
		task.LeaseExpiresAt = &value
	}
	task.CreatedAt = time.UnixMilli(created).UTC()
	task.UpdatedAt = time.UnixMilli(updated).UTC()
	return task, err
}

type TaskSummary struct {
	ID             int64
	Kind           string
	Status         string
	Attempts       int
	AvailableAt    time.Time
	LeaseExpiresAt *time.Time
	LastError      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (q *TaskQueue) List(ctx context.Context, limit int) ([]TaskSummary, error) {
	if limit < 1 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	rows, err := q.db.Reader.QueryContext(ctx, `SELECT id,kind,status,attempts,available_at,lease_expires_at,COALESCE(last_error,''),created_at,updated_at FROM jobs ORDER BY CASE status WHEN 'failed' THEN 0 WHEN 'running' THEN 1 ELSE 2 END,updated_at DESC,id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]TaskSummary, 0, limit)
	for rows.Next() {
		var item TaskSummary
		var available, created, updated int64
		var leaseValue sql.NullInt64
		if err := rows.Scan(&item.ID, &item.Kind, &item.Status, &item.Attempts, &available, &leaseValue, &item.LastError, &created, &updated); err != nil {
			return nil, err
		}
		item.AvailableAt = time.UnixMilli(available).UTC()
		if leaseValue.Valid {
			value := time.UnixMilli(leaseValue.Int64).UTC()
			item.LeaseExpiresAt = &value
		}
		item.LastError = truncateError(item.LastError)
		item.CreatedAt = time.UnixMilli(created).UTC()
		item.UpdatedAt = time.UnixMilli(updated).UTC()
		result = append(result, item)
	}
	return result, rows.Err()
}

type TaskCounts struct {
	Pending int
	Running int
	Failed  int
}

func (q *TaskQueue) Counts(ctx context.Context) (TaskCounts, error) {
	var counts TaskCounts
	err := q.db.Reader.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM jobs WHERE status='pending'),
		(SELECT count(*) FROM jobs WHERE status='running'),
		(SELECT count(*) FROM jobs WHERE status='failed')
	`).Scan(&counts.Pending, &counts.Running, &counts.Failed)
	return counts, err
}

func validateTask(task Task) error {
	if strings.TrimSpace(task.Kind) == "" || len(task.Kind) > maxTaskKindLength || strings.ContainsAny(task.Kind, "\r\n") {
		return fmt.Errorf("%w: kind", ErrInvalidTask)
	}
	if strings.TrimSpace(task.IdempotencyKey) == "" || len(task.IdempotencyKey) > maxTaskKeyLength || strings.ContainsAny(task.IdempotencyKey, "\r\n") {
		return fmt.Errorf("%w: idempotency key", ErrInvalidTask)
	}
	if task.PayloadVersion < 0 {
		return fmt.Errorf("%w: payload version", ErrInvalidTask)
	}
	if len(task.Payload) > maxTaskPayloadBytes {
		return fmt.Errorf("%w: payload too large", ErrInvalidTask)
	}
	return nil
}

func scanTask(row interface{ Scan(...any) error }) (Task, error) {
	var task Task
	var available, created, updated int64
	var lease sql.NullInt64
	err := row.Scan(&task.ID, &task.Kind, &task.PayloadVersion, &task.Payload, &task.IdempotencyKey, &task.Status, &available, &lease, &task.Attempts, &task.LastError, &created, &updated)
	if err != nil {
		return Task{}, err
	}
	task.AvailableAt = time.UnixMilli(available).UTC()
	if lease.Valid {
		value := time.UnixMilli(lease.Int64).UTC()
		task.LeaseExpiresAt = &value
	}
	task.CreatedAt = time.UnixMilli(created).UTC()
	task.UpdatedAt = time.UnixMilli(updated).UTC()
	return task, nil
}

var (
	bearerPattern = regexp.MustCompile(`(?i)(bearer\s+)[^\s,]+`)
	secretPattern = regexp.MustCompile(`(?i)((?:token|secret|password|authorization|api[_-]?key|email)(?:[=:]|%3d)[^\s,;]+)`)
	emailPattern  = regexp.MustCompile(`(?i)\b[[:alnum:]._%+\-]+@[[:alnum:].\-]+\.[[:alpha:]]{2,}\b`)
	ipPattern     = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	ipv6Pattern   = regexp.MustCompile(`(?i)(?:\[[0-9a-f:]+(?:%[0-9a-z_.-]+)?\]|(?:[0-9a-f]{1,4}:){2,}[0-9a-f:.]+)`)
)

// SafeError keeps operator-useful categories while removing common secret,
// email, and raw-address forms before errors reach logs or task status pages.
func SafeError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	message = bearerPattern.ReplaceAllString(message, `${1}[redacted]`)
	message = secretPattern.ReplaceAllString(message, `${1}[redacted]`)
	message = emailPattern.ReplaceAllString(message, "[email-redacted]")
	message = ipPattern.ReplaceAllString(message, "[address-redacted]")
	message = ipv6Pattern.ReplaceAllString(message, "[address-redacted]")
	if strings.Contains(strings.ToLower(message), "context deadline exceeded") {
		message = "external operation timed out"
	}
	if message == "" {
		message = "task failed"
	}
	return truncateError(message)
}

func truncateError(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= maxTaskErrorLength {
		return value
	}
	return value[:maxTaskErrorLength] + "…"
}

// ValidateExternalURL performs the syntax-only part of the outbound URL
// contract. Runtime address checks belong to netguard because DNS can change.
func ValidateExternalURL(value string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" || len(parsed.String()) > 2048 {
		return nil, errors.New("external endpoint must be an absolute HTTP or HTTPS URL without credentials or fragment")
	}
	if parsed.Hostname() == "" {
		return nil, errors.New("external endpoint host is required")
	}
	return parsed, nil
}

func IsPrivateAddress(address net.IP) bool {
	if address == nil {
		return true
	}
	return address.IsUnspecified() || address.IsLoopback() || address.IsPrivate() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsMulticast()
}
