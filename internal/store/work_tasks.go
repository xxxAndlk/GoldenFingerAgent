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

const (
	WorkTaskDraft           = "draft"
	WorkTaskQueued          = "queued"
	WorkTaskActive          = "active"
	WorkTaskWaitingUser     = "waiting_user"
	WorkTaskWaitingApproval = "waiting_approval"
	WorkTaskWaitingOther    = "waiting_other"
	WorkTaskWaitingDevice   = "waiting_device"
	WorkTaskWaitingExternal = "waiting_external"
	WorkTaskPaused          = "paused"
	WorkTaskPendingAccept   = "pending_acceptance"
	WorkTaskCompleted       = "completed"
	WorkTaskFailed          = "failed"
	WorkTaskCancelled       = "cancelled"
	WorkTaskProfileRead     = "read"
	WorkTaskProfileInternal = "internal"
	WorkTaskProfileExternal = "external"
	WaitingNone             = "none"
	WaitingUser             = "user"
	WaitingApproval         = "approval"
	WaitingOther            = "other"
	WaitingDevice           = "device"
	WaitingExternal         = "external"
	WaitingTimer            = "timer"
	InboxSourceUser         = "user"
	InboxSourceThread       = "thread"
	InboxSourceControl      = "control"
	InboxSourceSystem       = "system"
	InboxKindSteer          = "steer"
	InboxKindPause          = "pause"
	InboxKindResume         = "resume"
	InboxKindCancel         = "cancel"
	InboxKindRecover        = "recover"
	InboxKindSystem         = "system"
	InboxQueued             = "queued"
	InboxConsumed           = "consumed"
	InboxCancelled          = "cancelled"
	WorkTaskRelationOrigin  = "origin"
	WorkTaskRelationRelated = "related"
	WorkTaskRelationOutput  = "output"
	AgentJobRolePrimary     = "primary"
	AgentJobRoleDelegated   = "delegated"
)

var workTaskTransitions = map[string]map[string]bool{
	WorkTaskDraft: {
		WorkTaskQueued: true, WorkTaskCancelled: true,
	},
	WorkTaskQueued: {
		WorkTaskActive: true, WorkTaskPaused: true, WorkTaskCancelled: true, WorkTaskFailed: true,
	},
	WorkTaskActive: {
		WorkTaskWaitingUser: true, WorkTaskWaitingApproval: true, WorkTaskWaitingOther: true,
		WorkTaskWaitingDevice: true, WorkTaskWaitingExternal: true, WorkTaskPaused: true,
		WorkTaskPendingAccept: true, WorkTaskFailed: true, WorkTaskCancelled: true,
	},
	WorkTaskWaitingUser: {
		WorkTaskActive: true, WorkTaskPaused: true, WorkTaskFailed: true, WorkTaskCancelled: true,
	},
	WorkTaskWaitingApproval: {
		WorkTaskActive: true, WorkTaskPaused: true, WorkTaskFailed: true, WorkTaskCancelled: true,
	},
	WorkTaskWaitingOther: {
		WorkTaskActive: true, WorkTaskPaused: true, WorkTaskFailed: true, WorkTaskCancelled: true,
	},
	WorkTaskWaitingDevice: {
		WorkTaskActive: true, WorkTaskPaused: true, WorkTaskFailed: true, WorkTaskCancelled: true,
	},
	WorkTaskWaitingExternal: {
		WorkTaskActive: true, WorkTaskPaused: true, WorkTaskFailed: true, WorkTaskCancelled: true,
	},
	WorkTaskPaused: {
		WorkTaskQueued: true, WorkTaskCancelled: true,
	},
	WorkTaskPendingAccept: {
		WorkTaskCompleted: true, WorkTaskActive: true, WorkTaskCancelled: true,
	},
	WorkTaskFailed: {
		WorkTaskQueued: true, WorkTaskCancelled: true,
	},
	WorkTaskCompleted: {
		WorkTaskActive: true,
	},
	WorkTaskCancelled: {},
}

func ValidWorkTaskTransition(from, to string) bool {
	if from == to {
		return true
	}
	return workTaskTransitions[from][to]
}

func IsWaitingStatus(status string) bool {
	switch status {
	case WorkTaskWaitingUser, WorkTaskWaitingApproval, WorkTaskWaitingOther,
		WorkTaskWaitingDevice, WorkTaskWaitingExternal:
		return true
	}
	return false
}

type WorkTask struct {
	ID                 string     `json:"id"`
	OwnerUserID        string     `json:"owner_user_id"`
	Title              string     `json:"title"`
	Goal               string     `json:"goal"`
	CompletionCriteria string     `json:"completion_criteria"`
	Status             string     `json:"status"`
	Priority           int        `json:"priority"`
	CurrentJobID       string     `json:"current_job_id,omitempty"`
	CurrentSessionID   string     `json:"current_session_id,omitempty"`
	Profile            string     `json:"profile"`
	SourceTriggerID    string     `json:"source_trigger_id,omitempty"`
	IdempotencyKey     string     `json:"idempotency_key"`
	Version            int        `json:"version"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	CompletedAt        *time.Time `json:"completed_at,omitempty"`
}

type WorkTaskLiveState struct {
	TaskID          string          `json:"task_id"`
	OwnerUserID     string          `json:"owner_user_id"`
	Stage           string          `json:"stage"`
	Summary         string          `json:"summary"`
	NextAction      string          `json:"next_action"`
	WaitingKind     string          `json:"waiting_kind"`
	WaitingPayload  json.RawMessage `json:"waiting_payload,omitempty"`
	OpenQuestions   json.RawMessage `json:"open_questions,omitempty"`
	ConfirmedPoints json.RawMessage `json:"confirmed_points,omitempty"`
	HandoffNote     string          `json:"handoff_note"`
	Version         int             `json:"version"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

type WorkTaskThread struct {
	ID          string    `json:"id"`
	OwnerUserID string    `json:"owner_user_id"`
	TaskID      string    `json:"task_id"`
	ThreadID    string    `json:"thread_id"`
	Relation    string    `json:"relation"`
	Confidence  float64   `json:"confidence"`
	Reason      string    `json:"reason,omitempty"`
	CreatedBy   string    `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
}

type WorkTaskInbox struct {
	ID              string          `json:"id"`
	OwnerUserID     string          `json:"owner_user_id"`
	TaskID          string          `json:"task_id"`
	SourceKind      string          `json:"source_kind"`
	SourceEventID   string          `json:"source_event_id,omitempty"`
	Kind            string          `json:"kind"`
	Body            string          `json:"body"`
	Payload         json.RawMessage `json:"payload,omitempty"`
	Priority        int             `json:"priority"`
	Status          string          `json:"status"`
	IdempotencyKey  string          `json:"idempotency_key"`
	ConsumedByJobID string          `json:"consumed_by_job_id,omitempty"`
	ConsumedAt      *time.Time      `json:"consumed_at,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
}

type WorkTaskRepo struct{ q Querier }

func NewWorkTaskRepo(q Querier) *WorkTaskRepo { return &WorkTaskRepo{q: q} }

func (d *DB) WorkTasks() *WorkTaskRepo { return NewWorkTaskRepo(d) }

const workTaskColumns = `id, owner_user_id, title, goal, completion_criteria, status, priority,
	current_job_id, current_session_id, profile, source_trigger_id, idempotency_key, version,
	created_at, updated_at, completed_at`

func scanWorkTask(row interface{ Scan(...any) error }) (*WorkTask, error) {
	var t WorkTask
	var completed sql.NullTime
	if err := row.Scan(&t.ID, &t.OwnerUserID, &t.Title, &t.Goal, &t.CompletionCriteria,
		&t.Status, &t.Priority, &t.CurrentJobID, &t.CurrentSessionID, &t.Profile,
		&t.SourceTriggerID, &t.IdempotencyKey, &t.Version, &t.CreatedAt, &t.UpdatedAt,
		&completed); err != nil {
		return nil, err
	}
	t.CompletedAt = timePointer(completed)
	return &t, nil
}

func (r *WorkTaskRepo) Create(ctx context.Context, t *WorkTask) error {
	if t.ID == "" {
		t.ID = uuid.NewString()
	}
	if t.Status == "" {
		t.Status = WorkTaskDraft
	}
	if t.Profile == "" {
		t.Profile = WorkTaskProfileRead
	}
	if t.Priority < 0 {
		t.Priority = 0
	}
	if t.Priority > 9 {
		t.Priority = 9
	}
	if strings.TrimSpace(t.IdempotencyKey) == "" {
		t.IdempotencyKey = t.ID
	}
	now := nowForDB()
	t.CreatedAt, t.UpdatedAt = now, now
	_, err := r.q.ExecContext(ctx, `INSERT INTO work_task
		(id, owner_user_id, title, goal, completion_criteria, status, priority,
		 current_job_id, current_session_id, profile, source_trigger_id, idempotency_key,
		 version, created_at, updated_at, completed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, NULL)`,
		t.ID, t.OwnerUserID, t.Title, t.Goal, t.CompletionCriteria, t.Status, t.Priority,
		t.CurrentJobID, t.CurrentSessionID, t.Profile, t.SourceTriggerID, t.IdempotencyKey,
		now, now)
	if isUniqueViolation(err) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	_, err = r.q.ExecContext(ctx, `INSERT INTO work_task_live_state
		(task_id, owner_user_id, stage, summary, next_action, waiting_kind,
		 waiting_payload_json, open_questions_json, confirmed_points_json,
		 handoff_note, version, updated_at)
		VALUES (?, ?, '', '', '', 'none', '{}', '[]', '[]', '', 0, ?)`,
		t.ID, t.OwnerUserID, now)
	return err
}

func (r *WorkTaskRepo) Get(ctx context.Context, ownerID, id string) (*WorkTask, error) {
	t, err := scanWorkTask(r.q.QueryRowContext(ctx,
		`SELECT `+workTaskColumns+` FROM work_task WHERE owner_user_id = ? AND id = ?`, ownerID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

func (r *WorkTaskRepo) GetByIdempotencyKey(ctx context.Context, ownerID, key string) (*WorkTask, error) {
	t, err := scanWorkTask(r.q.QueryRowContext(ctx,
		`SELECT `+workTaskColumns+` FROM work_task WHERE owner_user_id = ? AND idempotency_key = ?`, ownerID, key))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

func (r *WorkTaskRepo) GetBySourceTrigger(ctx context.Context, ownerID, triggerID string) (*WorkTask, error) {
	t, err := scanWorkTask(r.q.QueryRowContext(ctx,
		`SELECT `+workTaskColumns+` FROM work_task
		 WHERE owner_user_id = ? AND source_trigger_id = ? ORDER BY created_at DESC LIMIT 1`,
		ownerID, triggerID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

func (r *WorkTaskRepo) List(ctx context.Context, ownerID, status string, limit int) ([]WorkTask, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := `SELECT ` + workTaskColumns + ` FROM work_task WHERE owner_user_id = ?`
	args := []any{ownerID}
	if status != "" {
		query += ` AND status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY priority DESC, updated_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := r.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]WorkTask, 0)
	for rows.Next() {
		t, err := scanWorkTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// ListCandidates 为 Router 提供最多 limit 个活跃任务；不返回终态。
func (r *WorkTaskRepo) ListCandidates(ctx context.Context, ownerID string, limit int) ([]WorkTask, error) {
	if limit <= 0 || limit > 20 {
		limit = 20
	}
	rows, err := r.q.QueryContext(ctx, `SELECT `+workTaskColumns+` FROM work_task
		WHERE owner_user_id = ? AND status NOT IN ('completed', 'cancelled')
		ORDER BY priority DESC, updated_at DESC LIMIT ?`, ownerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]WorkTask, 0)
	for rows.Next() {
		t, err := scanWorkTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// ListNonTerminal 供 Watchdog 与后台恢复扫描。
func (r *WorkTaskRepo) ListNonTerminal(ctx context.Context, limit int) ([]WorkTask, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := r.q.QueryContext(ctx, `SELECT `+workTaskColumns+` FROM work_task
		WHERE status NOT IN ('completed', 'cancelled') ORDER BY updated_at LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]WorkTask, 0)
	for rows.Next() {
		t, err := scanWorkTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// Transition 使用 expected_version 做 CAS；调用方负责校验状态迁移合法性。
func (r *WorkTaskRepo) Transition(ctx context.Context, ownerID, id string, expectedVersion int, to string, at time.Time) (bool, error) {
	completed := any(nil)
	if to == WorkTaskCompleted {
		completed = at
	}
	res, err := r.q.ExecContext(ctx, `UPDATE work_task
		SET status = ?, version = version + 1, updated_at = ?, completed_at = ?
		WHERE owner_user_id = ? AND id = ? AND version = ?`,
		to, at, completed, ownerID, id, expectedVersion)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// ForceStatus 只供 Watchdog 在确认无法继续时使用；仍要求当前状态匹配。
func (r *WorkTaskRepo) ForceStatus(ctx context.Context, ownerID, id string, from []string, to string, at time.Time) (bool, error) {
	if len(from) == 0 {
		return false, fmt.Errorf("from statuses required")
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(from)), ",")
	args := []any{to, at, ownerID, id}
	for _, status := range from {
		args = append(args, status)
	}
	res, err := r.q.ExecContext(ctx, `UPDATE work_task
		SET status = ?, version = version + 1, updated_at = ?, completed_at =
			CASE WHEN ? = 'completed' THEN ? ELSE completed_at END
		WHERE owner_user_id = ? AND id = ? AND status IN (`+placeholders+`)`,
		append([]any{to, at, to, at}, args[2:]...)...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (r *WorkTaskRepo) SetCurrentJob(ctx context.Context, ownerID, id, jobID, sessionID string, at time.Time) error {
	_, err := r.q.ExecContext(ctx, `UPDATE work_task SET current_job_id = ?, current_session_id = ?,
		updated_at = ? WHERE owner_user_id = ? AND id = ?`,
		jobID, sessionID, at, ownerID, id)
	return err
}

type WorkTaskLiveStateRepo struct{ q Querier }

func NewWorkTaskLiveStateRepo(q Querier) *WorkTaskLiveStateRepo { return &WorkTaskLiveStateRepo{q: q} }

func (d *DB) WorkTaskLiveStates() *WorkTaskLiveStateRepo { return NewWorkTaskLiveStateRepo(d) }

const liveStateColumns = `task_id, owner_user_id, stage, summary, next_action, waiting_kind,
	waiting_payload_json, open_questions_json, confirmed_points_json, handoff_note, version, updated_at`

func scanLiveState(row interface{ Scan(...any) error }) (*WorkTaskLiveState, error) {
	var s WorkTaskLiveState
	var waiting, questions, points []byte
	if err := row.Scan(&s.TaskID, &s.OwnerUserID, &s.Stage, &s.Summary, &s.NextAction,
		&s.WaitingKind, &waiting, &questions, &points, &s.HandoffNote, &s.Version,
		&s.UpdatedAt); err != nil {
		return nil, err
	}
	s.WaitingPayload = waiting
	s.OpenQuestions = questions
	s.ConfirmedPoints = points
	return &s, nil
}

func (r *WorkTaskLiveStateRepo) Get(ctx context.Context, ownerID, taskID string) (*WorkTaskLiveState, error) {
	s, err := scanLiveState(r.q.QueryRowContext(ctx,
		`SELECT `+liveStateColumns+` FROM work_task_live_state
		 WHERE owner_user_id = ? AND task_id = ?`, ownerID, taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return s, err
}

// Update 以版本 CAS 更新实时状态；waiting_kind 与任务状态在同一事务内由服务层协调。
func (r *WorkTaskLiveStateRepo) Update(ctx context.Context, s *WorkTaskLiveState, expectedVersion int, at time.Time) (bool, error) {
	if s.WaitingKind == "" {
		s.WaitingKind = WaitingNone
	}
	if len(s.WaitingPayload) == 0 {
		s.WaitingPayload = json.RawMessage(`{}`)
	}
	if len(s.OpenQuestions) == 0 {
		s.OpenQuestions = json.RawMessage(`[]`)
	}
	if len(s.ConfirmedPoints) == 0 {
		s.ConfirmedPoints = json.RawMessage(`[]`)
	}
	res, err := r.q.ExecContext(ctx, `UPDATE work_task_live_state
		SET stage = ?, summary = ?, next_action = ?, waiting_kind = ?,
			waiting_payload_json = ?, open_questions_json = ?, confirmed_points_json = ?,
			handoff_note = ?, version = version + 1, updated_at = ?
		WHERE owner_user_id = ? AND task_id = ? AND version = ?`,
		s.Stage, s.Summary, s.NextAction, s.WaitingKind, string(s.WaitingPayload),
		string(s.OpenQuestions), string(s.ConfirmedPoints), s.HandoffNote, at,
		s.OwnerUserID, s.TaskID, expectedVersion)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if n == 1 {
		s.Version = expectedVersion + 1
		s.UpdatedAt = at
	}
	return n == 1, err
}

func (r *WorkTaskLiveStateRepo) SetWaiting(ctx context.Context, ownerID, taskID string, expectedVersion int,
	kind string, payload json.RawMessage, stage, summary, nextAction string, at time.Time) (bool, error) {
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	res, err := r.q.ExecContext(ctx, `UPDATE work_task_live_state
		SET waiting_kind = ?, waiting_payload_json = ?, stage = ?, summary = ?, next_action = ?,
			version = version + 1, updated_at = ?
		WHERE owner_user_id = ? AND task_id = ? AND version = ?`,
		kind, string(payload), stage, summary, nextAction, at, ownerID, taskID, expectedVersion)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

type WorkTaskThreadRepo struct{ q Querier }

func NewWorkTaskThreadRepo(q Querier) *WorkTaskThreadRepo { return &WorkTaskThreadRepo{q: q} }

func (d *DB) WorkTaskThreads() *WorkTaskThreadRepo { return NewWorkTaskThreadRepo(d) }

func (r *WorkTaskThreadRepo) Add(ctx context.Context, link *WorkTaskThread) error {
	if link.ID == "" {
		link.ID = uuid.NewString()
	}
	if link.Relation == "" {
		link.Relation = WorkTaskRelationRelated
	}
	if link.CreatedBy == "" {
		link.CreatedBy = "system"
	}
	if link.CreatedAt.IsZero() {
		link.CreatedAt = nowForDB()
	}
	_, err := r.q.ExecContext(ctx, `INSERT INTO work_task_thread
		(id, owner_user_id, task_id, thread_id, relation, confidence, reason, created_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (task_id, thread_id, relation) DO NOTHING`,
		link.ID, link.OwnerUserID, link.TaskID, link.ThreadID, link.Relation,
		link.Confidence, link.Reason, link.CreatedBy, link.CreatedAt)
	return err
}

func (r *WorkTaskThreadRepo) Remove(ctx context.Context, ownerID, taskID, threadID, relation string) (bool, error) {
	res, err := r.q.ExecContext(ctx, `DELETE FROM work_task_thread
		WHERE owner_user_id = ? AND task_id = ? AND thread_id = ? AND relation = ?`,
		ownerID, taskID, threadID, relation)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (r *WorkTaskThreadRepo) ListByTask(ctx context.Context, ownerID, taskID string) ([]WorkTaskThread, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT id, owner_user_id, task_id, thread_id, relation,
		confidence, reason, created_by, created_at FROM work_task_thread
		WHERE owner_user_id = ? AND task_id = ? ORDER BY created_at`, ownerID, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]WorkTaskThread, 0)
	for rows.Next() {
		var link WorkTaskThread
		if err := rows.Scan(&link.ID, &link.OwnerUserID, &link.TaskID, &link.ThreadID,
			&link.Relation, &link.Confidence, &link.Reason, &link.CreatedBy,
			&link.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, link)
	}
	return out, rows.Err()
}

func (r *WorkTaskThreadRepo) TasksForThread(ctx context.Context, ownerID, threadID string) ([]WorkTaskThread, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT id, owner_user_id, task_id, thread_id, relation,
		confidence, reason, created_by, created_at FROM work_task_thread
		WHERE owner_user_id = ? AND thread_id = ? ORDER BY created_at`, ownerID, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]WorkTaskThread, 0)
	for rows.Next() {
		var link WorkTaskThread
		if err := rows.Scan(&link.ID, &link.OwnerUserID, &link.TaskID, &link.ThreadID,
			&link.Relation, &link.Confidence, &link.Reason, &link.CreatedBy,
			&link.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, link)
	}
	return out, rows.Err()
}

type WorkTaskInboxRepo struct{ q Querier }

func NewWorkTaskInboxRepo(q Querier) *WorkTaskInboxRepo { return &WorkTaskInboxRepo{q: q} }

func (d *DB) WorkTaskInbox() *WorkTaskInboxRepo { return NewWorkTaskInboxRepo(d) }

const inboxColumns = `id, owner_user_id, task_id, source_kind, source_event_id, kind, body,
	payload_json, priority, status, idempotency_key, consumed_by_job_id, consumed_at, created_at`

func scanInbox(row interface{ Scan(...any) error }) (*WorkTaskInbox, error) {
	var item WorkTaskInbox
	var payload []byte
	var consumed sql.NullTime
	if err := row.Scan(&item.ID, &item.OwnerUserID, &item.TaskID, &item.SourceKind,
		&item.SourceEventID, &item.Kind, &item.Body, &payload, &item.Priority, &item.Status,
		&item.IdempotencyKey, &item.ConsumedByJobID, &consumed, &item.CreatedAt); err != nil {
		return nil, err
	}
	item.Payload = payload
	item.ConsumedAt = timePointer(consumed)
	return &item, nil
}

// Add 幂等写入 Inbox；重复键返回已存在记录。
func (r *WorkTaskInboxRepo) Add(ctx context.Context, item *WorkTaskInbox) (bool, *WorkTaskInbox, error) {
	if item.ID == "" {
		item.ID = uuid.NewString()
	}
	if item.SourceKind == "" {
		item.SourceKind = InboxSourceUser
	}
	if item.Kind == "" {
		item.Kind = InboxKindSteer
	}
	if item.Status == "" {
		item.Status = InboxQueued
	}
	if len(item.Payload) == 0 {
		item.Payload = json.RawMessage(`{}`)
	}
	if strings.TrimSpace(item.IdempotencyKey) == "" {
		item.IdempotencyKey = item.ID
	}
	now := nowForDB()
	res, err := r.q.ExecContext(ctx, `INSERT INTO work_task_inbox
		(id, owner_user_id, task_id, source_kind, source_event_id, kind, body, payload_json,
		 priority, status, idempotency_key, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (owner_user_id, idempotency_key) DO NOTHING`,
		item.ID, item.OwnerUserID, item.TaskID, item.SourceKind, item.SourceEventID,
		item.Kind, item.Body, string(item.Payload), item.Priority, item.Status,
		item.IdempotencyKey, now)
	if err != nil {
		return false, nil, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, nil, err
	}
	if n == 1 {
		item.CreatedAt = now
		return true, item, nil
	}
	existing, err := r.GetByIdempotencyKey(ctx, item.OwnerUserID, item.IdempotencyKey)
	return false, existing, err
}

func (r *WorkTaskInboxRepo) Get(ctx context.Context, ownerID, id string) (*WorkTaskInbox, error) {
	item, err := scanInbox(r.q.QueryRowContext(ctx,
		`SELECT `+inboxColumns+` FROM work_task_inbox WHERE owner_user_id = ? AND id = ?`, ownerID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return item, err
}

func (r *WorkTaskInboxRepo) GetByIdempotencyKey(ctx context.Context, ownerID, key string) (*WorkTaskInbox, error) {
	item, err := scanInbox(r.q.QueryRowContext(ctx,
		`SELECT `+inboxColumns+` FROM work_task_inbox WHERE owner_user_id = ? AND idempotency_key = ?`, ownerID, key))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return item, err
}

func (r *WorkTaskInboxRepo) Queued(ctx context.Context, ownerID, taskID string, limit int) ([]WorkTaskInbox, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := r.q.QueryContext(ctx, `SELECT `+inboxColumns+` FROM work_task_inbox
		WHERE owner_user_id = ? AND task_id = ? AND status = 'queued'
		ORDER BY priority DESC, created_at, id LIMIT ?`, ownerID, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]WorkTaskInbox, 0)
	for rows.Next() {
		item, err := scanInbox(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *item)
	}
	return out, rows.Err()
}

func (r *WorkTaskInboxRepo) ListByTask(ctx context.Context, ownerID, taskID string, limit int) ([]WorkTaskInbox, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.q.QueryContext(ctx, `SELECT `+inboxColumns+` FROM work_task_inbox
		WHERE owner_user_id = ? AND task_id = ? ORDER BY created_at DESC LIMIT ?`,
		ownerID, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]WorkTaskInbox, 0)
	for rows.Next() {
		item, err := scanInbox(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *item)
	}
	return out, rows.Err()
}

// Consume 在事务内把 queued 标记为 consumed 并记录消费 Job。
func (r *WorkTaskInboxRepo) Consume(ctx context.Context, ownerID, id, jobID string, at time.Time) (bool, error) {
	res, err := r.q.ExecContext(ctx, `UPDATE work_task_inbox
		SET status = 'consumed', consumed_by_job_id = ?, consumed_at = ?
		WHERE owner_user_id = ? AND id = ? AND status = 'queued'`, jobID, at, ownerID, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (r *WorkTaskInboxRepo) CancelQueuedByKind(ctx context.Context, ownerID, taskID, kind string) (int64, error) {
	res, err := r.q.ExecContext(ctx, `UPDATE work_task_inbox SET status = 'cancelled'
		WHERE owner_user_id = ? AND task_id = ? AND kind = ? AND status = 'queued'`,
		ownerID, taskID, kind)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (r *WorkTaskInboxRepo) CancelQueued(ctx context.Context, ownerID, taskID string) (int64, error) {
	res, err := r.q.ExecContext(ctx, `UPDATE work_task_inbox SET status = 'cancelled'
		WHERE owner_user_id = ? AND task_id = ? AND status = 'queued'`, ownerID, taskID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
