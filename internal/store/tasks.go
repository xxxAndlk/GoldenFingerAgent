package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrConflict 在 Transition 的 CAS 前置条件不满足时返回。
var ErrConflict = errors.New("store: status conflict")

type TaskRepo struct{ q Querier }

func NewTaskRepo(q Querier) *TaskRepo { return &TaskRepo{q: q} }

const taskCols = `id, owner_user_id, kind, schema_jsonb, time_expr_raw, abs_time, deadline,
	status, confidence, source_msg_id, linked_person_id, event_template,
	created_at, updated_at, deleted_at`

func scanTask(row pgx.Row) (*Task, error) {
	t := &Task{}
	var schema []byte
	err := row.Scan(&t.ID, &t.OwnerUserID, &t.Kind, &schema, &t.TimeExprRaw, &t.AbsTime, &t.Deadline,
		&t.Status, &t.Confidence, &t.SourceMsgID, &t.LinkedPersonID, &t.EventTemplate,
		&t.CreatedAt, &t.UpdatedAt, &t.DeletedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	t.Schema = schema
	return t, nil
}

func (r *TaskRepo) Create(ctx context.Context, t *Task) error {
	return r.q.QueryRow(ctx, `
		INSERT INTO task (owner_user_id, kind, schema_jsonb, time_expr_raw, abs_time, deadline,
			status, confidence, source_msg_id, linked_person_id, event_template)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING id, created_at, updated_at`,
		t.OwnerUserID, t.Kind, notNullJSON(t.Schema), t.TimeExprRaw, t.AbsTime, t.Deadline,
		t.Status, t.Confidence, t.SourceMsgID, t.LinkedPersonID, t.EventTemplate,
	).Scan(&t.ID, &t.CreatedAt, &t.UpdatedAt)
}

func (r *TaskRepo) Get(ctx context.Context, ownerID, id string) (*Task, error) {
	t, err := scanTask(r.q.QueryRow(ctx, `
		SELECT `+taskCols+` FROM task
		WHERE id = $1 AND owner_user_id = $2 AND deleted_at IS NULL`, id, ownerID))
	if err != nil {
		return nil, err
	}
	return t, nil
}

// GetByID 不做 owner 过滤地取回任务（调度器内部使用）。
func (r *TaskRepo) GetByID(ctx context.Context, id string) (*Task, error) {
	return scanTask(r.q.QueryRow(ctx, `
		SELECT `+taskCols+` FROM task WHERE id = $1 AND deleted_at IS NULL`, id))
}

// Transition 执行一次 CAS 状态变更；可选变更在同一 UPDATE 中生效。
func (r *TaskRepo) Transition(ctx context.Context, id string, from, to string, mutate func(*Task)) error {
	// 事务内先取后改，使 CAS 语义可观测。
	return withTx(ctx, r.q, func(q Querier) error {
		t, err := scanTask(q.QueryRow(ctx, `SELECT `+taskCols+` FROM task WHERE id = $1 FOR UPDATE`, id))
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
		_, err = q.Exec(ctx, `
			UPDATE task SET status = $2, abs_time = $3, deadline = $4, updated_at = now()
			WHERE id = $1`, id, t.Status, t.AbsTime, t.Deadline)
		return err
	})
}

// ListByOwner 返回任务，可选用状态与种类过滤。
func (r *TaskRepo) ListByOwner(ctx context.Context, ownerID string, statuses []string, kind string) ([]Task, error) {
	query := `SELECT ` + taskCols + ` FROM task WHERE owner_user_id = $1 AND deleted_at IS NULL`
	args := []any{ownerID}
	if len(statuses) > 0 {
		args = append(args, statuses)
		query += fmt.Sprintf(` AND status = ANY($%d)`, len(args))
	}
	if kind != "" {
		args = append(args, kind)
		query += fmt.Sprintf(` AND kind = $%d`, len(args))
	}
	query += ` ORDER BY abs_time NULLS LAST, created_at`
	rows, err := r.q.Query(ctx, query, args...)
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
	_, err := r.q.Exec(ctx,
		`UPDATE task SET schema_jsonb = $2, updated_at = now() WHERE id = $1`, id, notNullJSON(schema))
	return err
}

// ExpireOverdue 把超过截止时间的 scheduled/snoozed/notified 任务标记为过期。
func (r *TaskRepo) ExpireOverdue(ctx context.Context, now time.Time) ([]Task, error) {
	rows, err := r.q.Query(ctx, `
		UPDATE task SET status = 'expired', updated_at = now()
		WHERE deleted_at IS NULL
		  AND status IN ('scheduled', 'snoozed', 'notified')
		  AND deadline IS NOT NULL AND deadline < $1
		RETURNING `+taskCols, now)
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
