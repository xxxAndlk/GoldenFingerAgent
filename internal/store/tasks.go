package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ErrConflict 在 Transition 的 CAS 前置条件不满足时返回。
var ErrConflict = errors.New("store: status conflict")

type TaskRepo struct{ q Querier }

func NewTaskRepo(q Querier) *TaskRepo { return &TaskRepo{q: q} }

const taskCols = `id, owner_user_id, kind, schema_json, time_expr_raw, abs_time, deadline,
	status, confidence, source_msg_id, linked_person_id, event_template,
	created_at, updated_at, deleted_at`

func scanTask(row Row) (*Task, error) {
	t := &Task{}
	var schema []byte
	err := row.Scan(&t.ID, &t.OwnerUserID, &t.Kind, &schema, &t.TimeExprRaw, &t.AbsTime, &t.Deadline,
		&t.Status, &t.Confidence, &t.SourceMsgID, &t.LinkedPersonID, &t.EventTemplate,
		&t.CreatedAt, &t.UpdatedAt, &t.DeletedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	t.Schema = schema
	return t, nil
}

func (r *TaskRepo) Create(ctx context.Context, t *Task) error {
	t.ID = uuid.NewString()
	return r.q.QueryRowContext(ctx, `
		INSERT INTO task (id, owner_user_id, kind, schema_json, time_expr_raw, abs_time, deadline,
			status, confidence, source_msg_id, linked_person_id, event_template, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING created_at, updated_at`,
		t.ID, t.OwnerUserID, t.Kind, notNullJSON(t.Schema), t.TimeExprRaw, t.AbsTime, t.Deadline,
		t.Status, t.Confidence, t.SourceMsgID, t.LinkedPersonID, t.EventTemplate, nowForDB(), nowForDB(),
	).Scan(&t.CreatedAt, &t.UpdatedAt)
}

func (r *TaskRepo) Get(ctx context.Context, ownerID, id string) (*Task, error) {
	return scanTask(r.q.QueryRowContext(ctx, `
		SELECT `+taskCols+` FROM task
		WHERE id = ? AND owner_user_id = ? AND deleted_at IS NULL`, id, ownerID))
}

// GetByID 不做 owner 过滤地取回任务（调度器内部使用）。
func (r *TaskRepo) GetByID(ctx context.Context, id string) (*Task, error) {
	return scanTask(r.q.QueryRowContext(ctx, `
		SELECT `+taskCols+` FROM task WHERE id = ? AND deleted_at IS NULL`, id))
}

// Transition 执行一次 CAS 状态变更；可选变更在同一 UPDATE 中生效。
func (r *TaskRepo) Transition(ctx context.Context, id string, from, to string, mutate func(*Task)) error {
	// 事务内先取后改，使 CAS 语义可观测（单进程下无需行锁）。
	return withTx(ctx, r.q, func(q Querier) error {
		t, err := scanTask(q.QueryRowContext(ctx, `SELECT `+taskCols+` FROM task WHERE id = ?`, id))
		if err != nil {
			return err
		}
		if t.Status != from {
			return fmt.Errorf("%w: task %s is %s, want %s", ErrConflict, id, t.Status, from)
		}
		t.Status = to
		if mutate != nil {
			mutate(t)
		}
		_, err = q.ExecContext(ctx, `
			UPDATE task SET status = ?, abs_time = ?, deadline = ?, updated_at = ?
			WHERE id = ?`, t.Status, t.AbsTime, t.Deadline, nowForDB(), id)
		return err
	})
}

// ListByOwner 返回任务，可选用状态与种类过滤。
func (r *TaskRepo) ListByOwner(ctx context.Context, ownerID string, statuses []string, kind string) ([]Task, error) {
	query := `SELECT ` + taskCols + ` FROM task WHERE owner_user_id = ? AND deleted_at IS NULL`
	args := []any{ownerID}
	if len(statuses) > 0 {
		ph := strings.TrimSuffix(strings.Repeat("?,", len(statuses)), ",")
		for _, s := range statuses {
			args = append(args, s)
		}
		query += fmt.Sprintf(` AND status IN (%s)`, ph)
	}
	if kind != "" {
		args = append(args, kind)
		query += ` AND kind = ?`
	}
	query += ` ORDER BY abs_time NULLS LAST, created_at`
	rows, err := r.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		var t Task
		var schema []byte
		if err := rows.Scan(&t.ID, &t.OwnerUserID, &t.Kind, &schema, &t.TimeExprRaw, &t.AbsTime, &t.Deadline,
			&t.Status, &t.Confidence, &t.SourceMsgID, &t.LinkedPersonID, &t.EventTemplate,
			&t.CreatedAt, &t.UpdatedAt, &t.DeletedAt); err != nil {
			return nil, err
		}
		t.Schema = schema
		out = append(out, t)
	}
	return out, rows.Err()
}

// SetSchema 修补任务负载（用于 note→episode 关联等）。
func (r *TaskRepo) SetSchema(ctx context.Context, id string, schema json.RawMessage) error {
	_, err := r.q.ExecContext(ctx,
		`UPDATE task SET schema_json = ?, updated_at = ? WHERE id = ?`, notNullJSON(schema), nowForDB(), id)
	return err
}

// ExpireOverdue 把超过截止时间的 scheduled/snoozed/notified 任务标记为过期。
func (r *TaskRepo) ExpireOverdue(ctx context.Context, now time.Time) ([]Task, error) {
	rows, err := r.q.QueryContext(ctx, `
		UPDATE task SET status = 'expired', updated_at = ?
		WHERE deleted_at IS NULL
		  AND status IN ('scheduled', 'snoozed', 'notified')
		  AND deadline IS NOT NULL AND deadline < ?
		RETURNING `+taskCols, nowForDB(), now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		var t Task
		var schema []byte
		if err := rows.Scan(&t.ID, &t.OwnerUserID, &t.Kind, &schema, &t.TimeExprRaw, &t.AbsTime, &t.Deadline,
			&t.Status, &t.Confidence, &t.SourceMsgID, &t.LinkedPersonID, &t.EventTemplate,
			&t.CreatedAt, &t.UpdatedAt, &t.DeletedAt); err != nil {
			return nil, err
		}
		t.Schema = schema
		out = append(out, t)
	}
	return out, rows.Err()
}
