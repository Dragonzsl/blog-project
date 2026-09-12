package publishing

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const maxBulkItems = 50

var (
	ErrBulkInvalid     = errors.New("bulk operation is invalid")
	ErrBulkInProgress  = errors.New("bulk operation is already processing")
	ErrBulkKeyConflict = errors.New("bulk operation key was reused")
)

type BulkActionRequest struct {
	Kind            string
	Action          string
	IDs             []int64
	ExpectedVersion map[int64]int64
	ScheduleAt      *time.Time
	OperationKey    string
}

type BulkItemResult struct {
	ID       int64  `json:"id"`
	PublicID string `json:"public_id,omitempty"`
	Title    string `json:"title,omitempty"`
	Status   string `json:"status"`
	Error    string `json:"error,omitempty"`
}

type BulkResult struct {
	OperationKey string           `json:"operation_key"`
	Action       string           `json:"action"`
	Succeeded    int              `json:"succeeded"`
	Skipped      int              `json:"skipped"`
	Conflicts    int              `json:"conflicts"`
	Failed       int              `json:"failed"`
	Items        []BulkItemResult `json:"items"`
}

func (s *Service) ApplyBulk(ctx context.Context, request BulkActionRequest) (BulkResult, error) {
	if request.Kind != "article" && request.Kind != "page" || request.Action == "" || strings.TrimSpace(request.OperationKey) == "" || len(request.OperationKey) > 180 {
		return BulkResult{}, ErrBulkInvalid
	}
	if strings.ContainsAny(request.OperationKey, "\r\n") {
		return BulkResult{}, ErrBulkInvalid
	}
	switch request.Action {
	case "publish", "schedule", "cancel_schedule", "unpublish", "trash", "restore":
	default:
		return BulkResult{}, ErrBulkInvalid
	}
	ids := uniqueIDs(request.IDs)
	if len(ids) == 0 || len(ids) > maxBulkItems {
		return BulkResult{}, ErrBulkInvalid
	}
	if request.Action == "schedule" && (request.ScheduleAt == nil || request.ScheduleAt.IsZero()) {
		return BulkResult{}, ErrBulkInvalid
	}
	if request.Action == "schedule" && !request.ScheduleAt.After(s.now()) {
		return BulkResult{}, ErrBulkInvalid
	}
	input := struct {
		Kind       string     `json:"kind"`
		Action     string     `json:"action"`
		IDs        []int64    `json:"ids"`
		ScheduleAt *time.Time `json:"schedule_at,omitempty"`
	}{request.Kind, request.Action, ids, request.ScheduleAt}
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return BulkResult{}, err
	}
	claimed, existing, err := s.claimBulk(ctx, request.OperationKey, request.Kind, request.Action, string(inputJSON))
	if err != nil {
		return BulkResult{}, err
	}
	if !claimed {
		return existing, nil
	}
	result := BulkResult{OperationKey: request.OperationKey, Action: request.Action, Items: make([]BulkItemResult, 0, len(ids))}
	for _, id := range ids {
		item := s.applyBulkItem(ctx, request, id)
		result.Items = append(result.Items, item)
		switch item.Status {
		case "succeeded":
			result.Succeeded++
		case "skipped":
			result.Skipped++
		case "conflict":
			result.Conflicts++
		default:
			result.Failed++
		}
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return result, err
	}
	if err := s.finishBulk(ctx, request.OperationKey, string(encoded)); err != nil {
		return result, err
	}
	return result, nil
}

func (s *Service) applyBulkItem(ctx context.Context, request BulkActionRequest, id int64) BulkItemResult {
	item := BulkItemResult{ID: id}
	var content Article
	var err error
	if request.Action == "restore" {
		content, err = s.repository.TrashedContent(ctx, request.Kind, id)
	} else {
		content, err = s.repository.Content(ctx, request.Kind, id)
	}
	if err != nil {
		item.Status, item.Error = "skipped", "内容不存在或已移入回收站"
		return item
	}
	item.Title = content.Title
	item.PublicID = hex.EncodeToString(content.PublicID)
	if expected := request.ExpectedVersion[id]; expected > 0 && expected != content.LockVersion {
		item.Status, item.Error = "conflict", "内容已被其他操作修改"
		return item
	}
	var operationErr error
	switch request.Action {
	case "publish":
		if request.Kind == "page" {
			_, operationErr = s.PublishPage(ctx, id, content.LockVersion)
		} else {
			_, operationErr = s.Publish(ctx, id, content.LockVersion)
		}
	case "schedule":
		_, operationErr = s.Schedule(ctx, request.Kind, id, content.LockVersion, *request.ScheduleAt)
	case "cancel_schedule":
		_, operationErr = s.CancelSchedule(ctx, request.Kind, id, content.LockVersion)
	case "unpublish":
		_, operationErr = s.Unpublish(ctx, request.Kind, id, content.LockVersion)
	case "trash":
		operationErr = s.Trash(ctx, request.Kind, id, content.LockVersion)
	case "restore":
		_, operationErr = s.RestoreFromTrash(ctx, id)
	default:
		operationErr = ErrBulkInvalid
	}
	if operationErr == nil {
		item.Status = "succeeded"
		return item
	}
	if errors.Is(operationErr, ErrConflict) {
		item.Status, item.Error = "conflict", "内容已被其他操作修改"
		return item
	}
	if errors.Is(operationErr, ErrInvalidTransition) || errors.Is(operationErr, ErrNotFound) {
		item.Status, item.Error = "skipped", "当前状态不支持此操作"
		return item
	}
	item.Status, item.Error = "failed", "操作失败"
	return item
}

func (s *Service) claimBulk(ctx context.Context, key, kind, action, inputJSON string) (bool, BulkResult, error) {
	tx, err := s.repository.database.Writer.BeginTx(ctx, nil)
	if err != nil {
		return false, BulkResult{}, err
	}
	defer tx.Rollback()
	var storedKind, storedAction, storedInput, status, encoded string
	var updatedAt int64
	err = tx.QueryRowContext(ctx, "SELECT object_kind,action,input_json,status,result_json,updated_at FROM bulk_operations WHERE operation_key=?", key).Scan(&storedKind, &storedAction, &storedInput, &status, &encoded, &updatedAt)
	if err == nil {
		if storedKind != kind || storedAction != action || storedInput != inputJSON {
			return false, BulkResult{}, ErrBulkKeyConflict
		}
		if status == "succeeded" {
			var result BulkResult
			if json.Unmarshal([]byte(encoded), &result) != nil {
				return false, BulkResult{}, ErrBulkKeyConflict
			}
			return false, result, tx.Commit()
		}
		if status == "processing" && s.now().UnixMilli()-updatedAt < int64((5*time.Minute)/time.Millisecond) {
			return false, BulkResult{}, ErrBulkInProgress
		}
		if _, err := tx.ExecContext(ctx, "UPDATE bulk_operations SET status='processing',result_json='{}',updated_at=? WHERE operation_key=?", s.now().UnixMilli(), key); err != nil {
			return false, BulkResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return false, BulkResult{}, err
		}
		return true, BulkResult{}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, BulkResult{}, err
	}
	now := s.now().UnixMilli()
	if _, err := tx.ExecContext(ctx, "INSERT INTO bulk_operations(operation_key,object_kind,action,input_json,status,result_json,created_at,updated_at) VALUES(?,?,?,?, 'processing','{}',?,?)", key, kind, action, inputJSON, now, now); err != nil {
		return false, BulkResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return false, BulkResult{}, err
	}
	return true, BulkResult{}, nil
}

func (s *Service) finishBulk(ctx context.Context, key, encoded string) error {
	if len(encoded) > 16384 {
		return ErrBulkInvalid
	}
	_, err := s.repository.database.Writer.ExecContext(ctx, "UPDATE bulk_operations SET status='succeeded',result_json=?,updated_at=? WHERE operation_key=? AND status='processing'", encoded, s.now().UnixMilli(), key)
	return err
}

func uniqueIDs(ids []int64) []int64 {
	result := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id < 1 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result
}

type ScheduledContent struct {
	Article
}

func (s *Service) ScheduledContents(ctx context.Context, kind string, start, end time.Time, limit int) ([]ScheduledContent, bool, error) {
	if !validKind(kind) || end.Before(start) {
		return nil, false, ErrNotFound
	}
	if limit < 1 {
		limit = 100
	}
	if limit > 200 {
		limit = 200
	}
	return s.repository.ScheduledContents(ctx, kind, start, end, limit)
}
