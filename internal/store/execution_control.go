package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

const (
	CheckpointTurnStart = "turn_start"
	CheckpointToolDone  = "tool_done"
	CheckpointWaiting   = "waiting"
	CheckpointTurnDone  = "turn_done"
	CheckpointRecovery  = "recovery"
)

const (
	GrantRead          = "read"
	GrantPrepare       = "prepare"
	GrantExternalWrite = "external_write"
	GrantSensitive     = "sensitive"
)

const (
	GrantActive    = "active"
	GrantRevoked   = "revoked"
	GrantExpired   = "expired"
	GrantExhausted = "exhausted"
)

const (
	ToolProposed = "proposed"
	ToolApproved = "approved"
	ToolRunning  = "running"
	ToolSuccess  = "succeeded"
	ToolFailed   = "failed"
	ToolUnknown  = "unknown"
	ToolDenied   = "denied"
	ToolCancel   = "cancelled"
)

type WorkTaskCheckpoint struct {
	ID          string          `json:"id"`
	OwnerUserID string          `json:"owner_user_id"`
	TaskID      string          `json:"task_id"`
	JobID       string          `json:"job_id"`
	SessionID   string          `json:"session_id,omitempty"`
	Seq         int             `json:"seq"`
	TaskVersion int             `json:"task_version"`
	Kind        string          `json:"kind"`
	Summary     string          `json:"summary,omitempty"`
	NextAction  string          `json:"next_action,omitempty"`
	Payload     json.RawMessage `json:"payload,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
}

type ActionGrant struct {
	ID            string          `json:"id"`
	OwnerUserID   string          `json:"owner_user_id"`
	TaskID        string          `json:"task_id,omitempty"`
	ActionType    string          `json:"action_type"`
	ResourceScope json.RawMessage `json:"resource_scope,omitempty"`
	RiskLevel     string          `json:"risk_level"`
	Status        string          `json:"status"`
	RemainingUses int             `json:"remaining_uses"`
	ApprovedBy    string          `json:"approved_by,omitempty"`
	ApprovedAt    *time.Time      `json:"approved_at,omitempty"`
	ExpiresAt     *time.Time      `json:"expires_at,omitempty"`
	RevokedAt     *time.Time      `json:"revoked_at,omitempty"`
	Version       int             `json:"version"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

type ToolInvocation struct {
	ID             string          `json:"id"`
	OwnerUserID    string          `json:"owner_user_id"`
	TaskID         string          `json:"task_id,omitempty"`
	JobID          string          `json:"job_id,omitempty"`
	SessionID      string          `json:"session_id,omitempty"`
	ToolCallID     string          `json:"tool_call_id,omitempty"`
	ToolName       string          `json:"tool_name"`
	RiskLevel      string          `json:"risk_level"`
	ArgsRedacted   json.RawMessage `json:"args_redacted,omitempty"`
	ArgsHash       string          `json:"args_hash"`
	GrantID        string          `json:"grant_id,omitempty"`
	IdempotencyKey string          `json:"idempotency_key"`
	Status         string          `json:"status"`
	ResultSummary  string          `json:"result_summary,omitempty"`
	EffectReceipt  json.RawMessage `json:"effect_receipt,omitempty"`
	StartedAt      *time.Time      `json:"started_at,omitempty"`
	FinishedAt     *time.Time      `json:"finished_at,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
}

type CheckpointRepo struct{ q Querier }

func NewCheckpointRepo(q Querier) *CheckpointRepo { return &CheckpointRepo{q: q} }

func (d *DB) Checkpoints() *CheckpointRepo { return NewCheckpointRepo(d) }

const checkpointColumns = `id, owner_user_id, task_id, job_id, session_id, seq, task_version,
	kind, summary, next_action, payload_json, created_at`

func scanCheckpoint(row interface{ Scan(...any) error }) (*WorkTaskCheckpoint, error) {
	var c WorkTaskCheckpoint
	var payload []byte
	if err := row.Scan(&c.ID, &c.OwnerUserID, &c.TaskID, &c.JobID, &c.SessionID, &c.Seq,
		&c.TaskVersion, &c.Kind, &c.Summary, &c.NextAction, &payload, &c.CreatedAt); err != nil {
		return nil, err
	}
	c.Payload = payload
	return &c, nil
}

// Append 自动分配 job 内递增 seq。
func (r *CheckpointRepo) Append(ctx context.Context, c *WorkTaskCheckpoint) error {
	if c.ID == "" {
		c.ID = uuid.NewString()
	}
	if len(c.Payload) == 0 {
		c.Payload = json.RawMessage(`{}`)
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = nowForDB()
	}
	var seq int
	if err := r.q.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1
		FROM work_task_checkpoint WHERE job_id = ?`, c.JobID).Scan(&seq); err != nil {
		return err
	}
	c.Seq = seq
	_, err := r.q.ExecContext(ctx, `INSERT INTO work_task_checkpoint
		(id, owner_user_id, task_id, job_id, session_id, seq, task_version, kind,
		 summary, next_action, payload_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.OwnerUserID, c.TaskID, c.JobID, c.SessionID, seq, c.TaskVersion, c.Kind,
		c.Summary, c.NextAction, string(c.Payload), c.CreatedAt)
	return err
}

func (r *CheckpointRepo) LatestForJob(ctx context.Context, ownerID, jobID string) (*WorkTaskCheckpoint, error) {
	c, err := scanCheckpoint(r.q.QueryRowContext(ctx,
		`SELECT `+checkpointColumns+` FROM work_task_checkpoint
		 WHERE owner_user_id = ? AND job_id = ? ORDER BY seq DESC LIMIT 1`, ownerID, jobID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

func (r *CheckpointRepo) LatestForTask(ctx context.Context, ownerID, taskID string) (*WorkTaskCheckpoint, error) {
	c, err := scanCheckpoint(r.q.QueryRowContext(ctx,
		`SELECT `+checkpointColumns+` FROM work_task_checkpoint
		 WHERE owner_user_id = ? AND task_id = ? ORDER BY created_at DESC, seq DESC LIMIT 1`,
		ownerID, taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

func (r *CheckpointRepo) ListForTask(ctx context.Context, ownerID, taskID string, limit int) ([]WorkTaskCheckpoint, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.q.QueryContext(ctx, `SELECT `+checkpointColumns+` FROM work_task_checkpoint
		WHERE owner_user_id = ? AND task_id = ? ORDER BY created_at DESC, seq DESC LIMIT ?`,
		ownerID, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]WorkTaskCheckpoint, 0)
	for rows.Next() {
		c, err := scanCheckpoint(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

type ActionGrantRepo struct{ q Querier }

func NewActionGrantRepo(q Querier) *ActionGrantRepo { return &ActionGrantRepo{q: q} }

func (d *DB) Grants() *ActionGrantRepo { return NewActionGrantRepo(d) }

const grantColumns = `id, owner_user_id, task_id, action_type, resource_scope_json, risk_level,
	status, remaining_uses, approved_by, approved_at, expires_at, revoked_at, version,
	created_at, updated_at`

func scanGrant(row interface{ Scan(...any) error }) (*ActionGrant, error) {
	var g ActionGrant
	var scope []byte
	var approved, expires, revoked sql.NullTime
	if err := row.Scan(&g.ID, &g.OwnerUserID, &g.TaskID, &g.ActionType, &scope, &g.RiskLevel,
		&g.Status, &g.RemainingUses, &g.ApprovedBy, &approved, &expires, &revoked,
		&g.Version, &g.CreatedAt, &g.UpdatedAt); err != nil {
		return nil, err
	}
	g.ResourceScope = scope
	g.ApprovedAt = timePointer(approved)
	g.ExpiresAt = timePointer(expires)
	g.RevokedAt = timePointer(revoked)
	return &g, nil
}

func (r *ActionGrantRepo) Create(ctx context.Context, g *ActionGrant) error {
	if g.ID == "" {
		g.ID = uuid.NewString()
	}
	if g.RiskLevel == "" {
		g.RiskLevel = GrantPrepare
	}
	if g.Status == "" {
		g.Status = GrantActive
	}
	if g.RemainingUses == 0 {
		g.RemainingUses = -1
	}
	if len(g.ResourceScope) == 0 {
		g.ResourceScope = json.RawMessage(`{}`)
	}
	now := nowForDB()
	g.CreatedAt, g.UpdatedAt = now, now
	_, err := r.q.ExecContext(ctx, `INSERT INTO action_grant
		(id, owner_user_id, task_id, action_type, resource_scope_json, risk_level, status,
		 remaining_uses, approved_by, approved_at, expires_at, version, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`,
		g.ID, g.OwnerUserID, g.TaskID, g.ActionType, string(g.ResourceScope), g.RiskLevel,
		g.Status, g.RemainingUses, g.ApprovedBy, g.ApprovedAt, g.ExpiresAt, now, now)
	return err
}

func (r *ActionGrantRepo) Get(ctx context.Context, ownerID, id string) (*ActionGrant, error) {
	g, err := scanGrant(r.q.QueryRowContext(ctx,
		`SELECT `+grantColumns+` FROM action_grant WHERE owner_user_id = ? AND id = ?`, ownerID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return g, err
}

func (r *ActionGrantRepo) ListByTask(ctx context.Context, ownerID, taskID string, limit int) ([]ActionGrant, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.q.QueryContext(ctx, `SELECT `+grantColumns+` FROM action_grant
		WHERE owner_user_id = ? AND task_id = ? ORDER BY created_at DESC LIMIT ?`,
		ownerID, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]ActionGrant, 0)
	for rows.Next() {
		g, err := scanGrant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *g)
	}
	return out, rows.Err()
}

// ActiveForTask 返回任务下仍在有效期内的授权；内容 hash 校验由调用方完成。
func (r *ActionGrantRepo) ActiveForTask(ctx context.Context, ownerID, taskID string, at time.Time) ([]ActionGrant, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT `+grantColumns+` FROM action_grant
		WHERE owner_user_id = ? AND task_id = ? AND status = 'active'
		  AND (expires_at IS NULL OR expires_at > ?)
		  AND remaining_uses <> 0 ORDER BY created_at DESC`, ownerID, taskID, at)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]ActionGrant, 0)
	for rows.Next() {
		g, err := scanGrant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *g)
	}
	return out, rows.Err()
}

// Consume 原子消耗一次授权；-1 表示任务范围内不限次数。
func (r *ActionGrantRepo) Consume(ctx context.Context, ownerID, id string, at time.Time) (bool, error) {
	res, err := r.q.ExecContext(ctx, `UPDATE action_grant
		SET remaining_uses = CASE WHEN remaining_uses < 0 THEN -1 ELSE remaining_uses - 1 END,
			status = CASE WHEN remaining_uses = 1 THEN 'exhausted' ELSE status END,
			version = version + 1, updated_at = ?
		WHERE owner_user_id = ? AND id = ? AND status = 'active'
		  AND (expires_at IS NULL OR expires_at > ?) AND remaining_uses <> 0`,
		at, ownerID, id, at)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (r *ActionGrantRepo) Revoke(ctx context.Context, ownerID, id string, at time.Time) (bool, error) {
	res, err := r.q.ExecContext(ctx, `UPDATE action_grant
		SET status = 'revoked', revoked_at = ?, version = version + 1, updated_at = ?
		WHERE owner_user_id = ? AND id = ? AND status = 'active'`, at, at, ownerID, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (r *ActionGrantRepo) RevokeForTask(ctx context.Context, ownerID, taskID string, at time.Time) (int64, error) {
	res, err := r.q.ExecContext(ctx, `UPDATE action_grant
		SET status = 'revoked', revoked_at = ?, version = version + 1, updated_at = ?
		WHERE owner_user_id = ? AND task_id = ? AND status = 'active'`, at, at, ownerID, taskID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

type ToolInvocationRepo struct{ q Querier }

func NewToolInvocationRepo(q Querier) *ToolInvocationRepo { return &ToolInvocationRepo{q: q} }

func (d *DB) ToolInvocations() *ToolInvocationRepo { return NewToolInvocationRepo(d) }

const invocationColumns = `id, owner_user_id, task_id, job_id, session_id, tool_call_id,
	tool_name, risk_level, args_redacted_json, args_hash, grant_id, idempotency_key, status,
	result_summary, effect_receipt_json, started_at, finished_at, created_at`

func scanInvocation(row interface{ Scan(...any) error }) (*ToolInvocation, error) {
	var i ToolInvocation
	var args, receipt []byte
	var started, finished sql.NullTime
	if err := row.Scan(&i.ID, &i.OwnerUserID, &i.TaskID, &i.JobID, &i.SessionID, &i.ToolCallID,
		&i.ToolName, &i.RiskLevel, &args, &i.ArgsHash, &i.GrantID, &i.IdempotencyKey,
		&i.Status, &i.ResultSummary, &receipt, &started, &finished, &i.CreatedAt); err != nil {
		return nil, err
	}
	i.ArgsRedacted = args
	i.EffectReceipt = receipt
	i.StartedAt = timePointer(started)
	i.FinishedAt = timePointer(finished)
	return &i, nil
}

func (r *ToolInvocationRepo) Create(ctx context.Context, i *ToolInvocation) error {
	if i.ID == "" {
		i.ID = uuid.NewString()
	}
	if i.Status == "" {
		i.Status = ToolProposed
	}
	if len(i.ArgsRedacted) == 0 {
		i.ArgsRedacted = json.RawMessage(`{}`)
	}
	if len(i.EffectReceipt) == 0 {
		i.EffectReceipt = json.RawMessage(`{}`)
	}
	if i.CreatedAt.IsZero() {
		i.CreatedAt = nowForDB()
	}
	_, err := r.q.ExecContext(ctx, `INSERT INTO tool_invocation
		(id, owner_user_id, task_id, job_id, session_id, tool_call_id, tool_name, risk_level,
		 args_redacted_json, args_hash, grant_id, idempotency_key, status, result_summary,
		 effect_receipt_json, started_at, finished_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		i.ID, i.OwnerUserID, i.TaskID, i.JobID, i.SessionID, i.ToolCallID, i.ToolName,
		i.RiskLevel, string(i.ArgsRedacted), i.ArgsHash, i.GrantID, i.IdempotencyKey,
		i.Status, i.ResultSummary, string(i.EffectReceipt), i.StartedAt, i.FinishedAt,
		i.CreatedAt)
	if isUniqueViolation(err) {
		return ErrConflict
	}
	return err
}

func (r *ToolInvocationRepo) GetByKey(ctx context.Context, ownerID, key string) (*ToolInvocation, error) {
	i, err := scanInvocation(r.q.QueryRowContext(ctx,
		`SELECT `+invocationColumns+` FROM tool_invocation
		 WHERE owner_user_id = ? AND idempotency_key = ?`, ownerID, key))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return i, err
}

func (r *ToolInvocationRepo) Get(ctx context.Context, ownerID, id string) (*ToolInvocation, error) {
	i, err := scanInvocation(r.q.QueryRowContext(ctx,
		`SELECT `+invocationColumns+` FROM tool_invocation WHERE owner_user_id = ? AND id = ?`, ownerID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return i, err
}

func (r *ToolInvocationRepo) ListByTask(ctx context.Context, ownerID, taskID string, limit int) ([]ToolInvocation, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.q.QueryContext(ctx, `SELECT `+invocationColumns+` FROM tool_invocation
		WHERE owner_user_id = ? AND task_id = ? ORDER BY created_at DESC LIMIT ?`,
		ownerID, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]ToolInvocation, 0)
	for rows.Next() {
		i, err := scanInvocation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *i)
	}
	return out, rows.Err()
}

func (r *ToolInvocationRepo) UpdateStatus(ctx context.Context, ownerID, id, status, result string,
	receipt json.RawMessage, at time.Time) (bool, error) {
	if len(receipt) == 0 {
		receipt = json.RawMessage(`{}`)
	}
	res, err := r.q.ExecContext(ctx, `UPDATE tool_invocation
		SET status = ?, result_summary = ?, effect_receipt_json = ?, finished_at = ?, started_at =
			CASE WHEN started_at IS NULL AND ? = 'running' THEN ? ELSE started_at END
		WHERE owner_user_id = ? AND id = ?`,
		status, result, string(receipt), at, status, at, ownerID, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}
