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
	ThreadStatusActive   = "active"
	ThreadStatusArchived = "archived"
)

const (
	TriggerStatusNew      = "new"
	TriggerStatusFiltered = "filtered"
	TriggerStatusQueued   = "queued"
	TriggerStatusIgnored  = "ignored"
	TriggerStatusRouted   = "routed"
	TriggerStatusDone     = "done"
	TriggerStatusFailed   = "failed"
)

const (
	OutboxDraft           = "draft"
	OutboxPendingApproval = "pending_approval"
	OutboxApproved        = "approved"
	OutboxSending         = "sending"
	OutboxSent            = "sent"
	OutboxFailed          = "failed"
	OutboxSendUnknown     = "send_unknown"
	OutboxRejected        = "rejected"
	OutboxCancelled       = "cancelled"
)

type Thread struct {
	ID               string          `json:"id"`
	OwnerUserID      string          `json:"owner_user_id"`
	Channel          string          `json:"channel"`
	ExternalID       string          `json:"external_id,omitempty"`
	Title            string          `json:"title,omitempty"`
	Participants     json.RawMessage `json:"participants,omitempty"`
	Summary          string          `json:"summary,omitempty"`
	SummaryEventID   int64           `json:"summary_event_id"`
	SummaryUpdatedAt *time.Time      `json:"summary_updated_at,omitempty"`
	Status           string          `json:"status"`
	RetentionClass   string          `json:"retention_class"`
	LastEventAt      *time.Time      `json:"last_event_at,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

type Trigger struct {
	ID           string          `json:"id"`
	OwnerUserID  string          `json:"owner_user_id"`
	Channel      string          `json:"channel"`
	ExternalID   string          `json:"external_id,omitempty"`
	ThreadID     string          `json:"thread_id,omitempty"`
	Actor        json.RawMessage `json:"actor,omitempty"`
	Kind         string          `json:"kind"`
	Title        string          `json:"title,omitempty"`
	Body         string          `json:"body,omitempty"`
	Payload      json.RawMessage `json:"payload,omitempty"`
	ReceivedAt   time.Time       `json:"received_at"`
	DedupeKey    string          `json:"dedupe_key"`
	PrivacyLevel string          `json:"privacy_level"`
	Trusted      bool            `json:"trusted"`
	Status       string          `json:"status"`
	RouteResult  json.RawMessage `json:"route_result,omitempty"`
	Error        string          `json:"error,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

type ThreadLink struct {
	ID             string    `json:"id"`
	OwnerUserID    string    `json:"owner_user_id"`
	ThreadID       string    `json:"thread_id"`
	LinkedThreadID string    `json:"linked_thread_id"`
	Relation       string    `json:"relation"`
	Confidence     float64   `json:"confidence"`
	Reason         string    `json:"reason,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

type ThreadSubscription struct {
	OwnerUserID  string    `json:"owner_user_id"`
	ThreadID     string    `json:"thread_id"`
	WorkerTaskID string    `json:"worker_task_id"`
	CreatedAt    time.Time `json:"created_at"`
}

type ActionOutbox struct {
	ID                string          `json:"id"`
	OwnerUserID       string          `json:"owner_user_id"`
	Channel           string          `json:"channel"`
	ThreadID          string          `json:"thread_id,omitempty"`
	WorkerTaskID      string          `json:"worker_task_id,omitempty"`
	WorkTaskID        string          `json:"work_task_id,omitempty"`
	ToolInvocationID  string          `json:"tool_invocation_id,omitempty"`
	ActionGrantID     string          `json:"action_grant_id,omitempty"`
	ExpectedEffect    json.RawMessage `json:"expected_effect,omitempty"`
	ReconciledAt      *time.Time      `json:"reconciled_at,omitempty"`
	Kind              string          `json:"kind"`
	Target            json.RawMessage `json:"target,omitempty"`
	Body              string          `json:"body"`
	Payload           json.RawMessage `json:"payload,omitempty"`
	Status            string          `json:"status"`
	RequiresApproval  bool            `json:"requires_approval"`
	IdempotencyKey    string          `json:"idempotency_key"`
	Version           int             `json:"version"`
	ApprovedBy        string          `json:"approved_by,omitempty"`
	ApprovedAt        *time.Time      `json:"approved_at,omitempty"`
	ProviderMessageID string          `json:"provider_message_id,omitempty"`
	LastError         string          `json:"last_error,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
	SentAt            *time.Time      `json:"sent_at,omitempty"`
}

type AgentEvent struct {
	ID          int64           `json:"id"`
	OwnerUserID string          `json:"owner_user_id"`
	Kind        string          `json:"kind"`
	EntityType  string          `json:"entity_type"`
	EntityID    string          `json:"entity_id"`
	Payload     json.RawMessage `json:"payload,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
}

type ThreadRepo struct{ q Querier }

func NewThreadRepo(q Querier) *ThreadRepo { return &ThreadRepo{q: q} }

func (d *DB) Threads() *ThreadRepo { return NewThreadRepo(d) }

const threadColumns = `id, owner_user_id, channel, external_id, title, participants_json, summary,
	summary_event_id, summary_updated_at, status, retention_class, last_event_at, created_at, updated_at`

func scanThread(row interface{ Scan(...any) error }) (*Thread, error) {
	var t Thread
	var participants []byte
	var summaryAt, lastEventAt sql.NullTime
	if err := row.Scan(&t.ID, &t.OwnerUserID, &t.Channel, &t.ExternalID, &t.Title, &participants,
		&t.Summary, &t.SummaryEventID, &summaryAt, &t.Status, &t.RetentionClass,
		&lastEventAt, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return nil, err
	}
	t.Participants = participants
	t.SummaryUpdatedAt = timePointer(summaryAt)
	t.LastEventAt = timePointer(lastEventAt)
	return &t, nil
}

func (r *ThreadRepo) Create(ctx context.Context, t *Thread) error {
	if t.ID == "" {
		t.ID = uuid.NewString()
	}
	if t.Status == "" {
		t.Status = ThreadStatusActive
	}
	if t.RetentionClass == "" {
		t.RetentionClass = "default"
	}
	if len(t.Participants) == 0 {
		t.Participants = json.RawMessage(`[]`)
	}
	now := nowForDB()
	err := r.q.QueryRowContext(ctx, `INSERT INTO agent_thread
		(id, owner_user_id, channel, external_id, title, participants_json, summary,
		 summary_event_id, status, retention_class, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, '', 0, ?, ?, ?, ?)
		RETURNING created_at, updated_at`,
		t.ID, t.OwnerUserID, t.Channel, t.ExternalID, t.Title, string(t.Participants),
		t.Status, t.RetentionClass, now, now).Scan(&t.CreatedAt, &t.UpdatedAt)
	if isUniqueViolation(err) {
		return ErrConflict
	}
	return err
}

func (r *ThreadRepo) Get(ctx context.Context, ownerID, id string) (*Thread, error) {
	t, err := scanThread(r.q.QueryRowContext(ctx,
		`SELECT `+threadColumns+` FROM agent_thread WHERE owner_user_id = ? AND id = ?`, ownerID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

func (r *ThreadRepo) GetByExternal(ctx context.Context, ownerID, channel, externalID string) (*Thread, error) {
	t, err := scanThread(r.q.QueryRowContext(ctx,
		`SELECT `+threadColumns+` FROM agent_thread
		 WHERE owner_user_id = ? AND channel = ? AND external_id = ?`, ownerID, channel, externalID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

func (r *ThreadRepo) List(ctx context.Context, ownerID, channel string, limit int) ([]Thread, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := `SELECT ` + threadColumns + ` FROM agent_thread WHERE owner_user_id = ?`
	args := []any{ownerID}
	if channel != "" {
		query += ` AND channel = ?`
		args = append(args, channel)
	}
	query += ` ORDER BY updated_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := r.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Thread, 0)
	for rows.Next() {
		t, err := scanThread(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func (r *ThreadRepo) Touch(ctx context.Context, ownerID, id string, at time.Time) error {
	_, err := r.q.ExecContext(ctx, `UPDATE agent_thread SET last_event_at = ?, updated_at = ?
		WHERE owner_user_id = ? AND id = ?`, at, at, ownerID, id)
	return err
}

// UpdateSummary 只允许覆盖更旧的事件范围，防止晚到的摘要覆盖新摘要。
func (r *ThreadRepo) UpdateSummary(ctx context.Context, ownerID, id string, eventID int64, summary string, at time.Time) (bool, error) {
	res, err := r.q.ExecContext(ctx, `UPDATE agent_thread
		SET summary = ?, summary_event_id = ?, summary_updated_at = ?, updated_at = ?
		WHERE owner_user_id = ? AND id = ? AND summary_event_id < ?`,
		summary, eventID, at, at, ownerID, id, eventID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

type TriggerRepo struct{ q Querier }

func NewTriggerRepo(q Querier) *TriggerRepo { return &TriggerRepo{q: q} }

func (d *DB) Triggers() *TriggerRepo { return NewTriggerRepo(d) }

const triggerColumns = `id, owner_user_id, channel, external_id, thread_id, actor_json, kind,
	title, body, payload_json, received_at, dedupe_key, privacy_level, trusted, status,
	route_result_json, error, created_at, updated_at`

func scanTrigger(row interface{ Scan(...any) error }) (*Trigger, error) {
	var t Trigger
	var threadID sql.NullString
	var actor, payload, route []byte
	var trusted int
	if err := row.Scan(&t.ID, &t.OwnerUserID, &t.Channel, &t.ExternalID, &threadID, &actor,
		&t.Kind, &t.Title, &t.Body, &payload, &t.ReceivedAt, &t.DedupeKey, &t.PrivacyLevel,
		&trusted, &t.Status, &route, &t.Error, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return nil, err
	}
	t.ThreadID = threadID.String
	t.Actor = actor
	t.Payload = payload
	t.RouteResult = route
	t.Trusted = trusted == 1
	return &t, nil
}

// Create 以 (owner, dedupe_key) 幂等写入；重复投递返回已存在记录而不是新记录。
func (r *TriggerRepo) Create(ctx context.Context, t *Trigger) (bool, *Trigger, error) {
	if t.ID == "" {
		t.ID = uuid.NewString()
	}
	if t.Status == "" {
		t.Status = TriggerStatusNew
	}
	if t.PrivacyLevel == "" {
		t.PrivacyLevel = "normal"
	}
	if len(t.Actor) == 0 {
		t.Actor = json.RawMessage(`[]`)
	}
	if len(t.Payload) == 0 {
		t.Payload = json.RawMessage(`{}`)
	}
	if len(t.RouteResult) == 0 {
		t.RouteResult = json.RawMessage(`{}`)
	}
	if t.ReceivedAt.IsZero() {
		t.ReceivedAt = nowForDB()
	}
	now := nowForDB()
	res, err := r.q.ExecContext(ctx, `INSERT INTO agent_trigger
		(id, owner_user_id, channel, external_id, thread_id, actor_json, kind, title, body,
		 payload_json, received_at, dedupe_key, privacy_level, trusted, status,
		 route_result_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (owner_user_id, dedupe_key) DO NOTHING`,
		t.ID, t.OwnerUserID, t.Channel, t.ExternalID, t.ThreadID, string(t.Actor), t.Kind,
		t.Title, t.Body, string(t.Payload), t.ReceivedAt, t.DedupeKey, t.PrivacyLevel,
		boolInt(t.Trusted), t.Status, string(t.RouteResult), now, now)
	if err != nil {
		return false, nil, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, nil, err
	}
	if n == 1 {
		t.CreatedAt, t.UpdatedAt = now, now
		return true, t, nil
	}
	existing, err := r.GetByDedupe(ctx, t.OwnerUserID, t.DedupeKey)
	return false, existing, err
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func (r *TriggerRepo) Get(ctx context.Context, ownerID, id string) (*Trigger, error) {
	t, err := scanTrigger(r.q.QueryRowContext(ctx,
		`SELECT `+triggerColumns+` FROM agent_trigger WHERE owner_user_id = ? AND id = ?`, ownerID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

func (r *TriggerRepo) GetByDedupe(ctx context.Context, ownerID, key string) (*Trigger, error) {
	t, err := scanTrigger(r.q.QueryRowContext(ctx,
		`SELECT `+triggerColumns+` FROM agent_trigger WHERE owner_user_id = ? AND dedupe_key = ?`, ownerID, key))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

func (r *TriggerRepo) List(ctx context.Context, ownerID, status, threadID string, limit int) ([]Trigger, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := `SELECT ` + triggerColumns + ` FROM agent_trigger WHERE owner_user_id = ?`
	args := []any{ownerID}
	if status != "" {
		query += ` AND status = ?`
		args = append(args, status)
	}
	if threadID != "" {
		query += ` AND thread_id = ?`
		args = append(args, threadID)
	}
	query += ` ORDER BY received_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := r.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Trigger, 0)
	for rows.Next() {
		t, err := scanTrigger(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// ListSince 返回 Thread 中 since 之后到达的 Trigger（时间正序），用于增量摘要。
func (r *TriggerRepo) ListSince(ctx context.Context, ownerID, threadID string, since time.Time, limit int) ([]Trigger, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := r.q.QueryContext(ctx, `SELECT `+triggerColumns+` FROM agent_trigger
		WHERE owner_user_id = ? AND thread_id = ? AND received_at >= ?
		ORDER BY received_at ASC, id ASC LIMIT ?`, ownerID, threadID, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Trigger, 0)
	for rows.Next() {
		t, err := scanTrigger(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// ListBetween 按时间窗和渠道读取原始事件，供每日简报和渠道统计使用。
func (r *TriggerRepo) ListBetween(ctx context.Context, ownerID, channel string, from, to time.Time, limit int) ([]Trigger, error) {
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	query := `SELECT ` + triggerColumns + ` FROM agent_trigger
		WHERE owner_user_id = ? AND received_at >= ? AND received_at < ?`
	args := []any{ownerID, from, to}
	if channel != "" {
		query += ` AND channel = ?`
		args = append(args, channel)
	}
	query += ` ORDER BY received_at ASC, id ASC LIMIT ?`
	args = append(args, limit)
	rows, err := r.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Trigger, 0)
	for rows.Next() {
		t, err := scanTrigger(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// Transition 以 from 状态集合做 CAS；返回 false 表示状态已被其他处理推进。
func (r *TriggerRepo) Transition(ctx context.Context, ownerID, id string, from []string, to string, routeResult json.RawMessage, errText string, at time.Time) (bool, error) {
	if len(from) == 0 {
		return false, fmt.Errorf("from statuses required")
	}
	if len(routeResult) == 0 {
		routeResult = json.RawMessage(`{}`)
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(from)), ",")
	args := []any{to, string(routeResult), errText, at, ownerID, id}
	for _, status := range from {
		args = append(args, status)
	}
	res, err := r.q.ExecContext(ctx, `UPDATE agent_trigger SET status = ?, route_result_json = ?,
		error = ?, updated_at = ? WHERE owner_user_id = ? AND id = ? AND status IN (`+placeholders+`)`,
		args...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (r *TriggerRepo) CountByStatus(ctx context.Context, ownerID string) (map[string]int, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT status, COUNT(*) FROM agent_trigger
		WHERE owner_user_id = ? GROUP BY status`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]int)
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		out[status] = count
	}
	return out, rows.Err()
}

type ThreadLinkRepo struct{ q Querier }

func NewThreadLinkRepo(q Querier) *ThreadLinkRepo { return &ThreadLinkRepo{q: q} }

func (d *DB) ThreadLinks() *ThreadLinkRepo { return NewThreadLinkRepo(d) }

func (r *ThreadLinkRepo) Add(ctx context.Context, link *ThreadLink) error {
	if link.ID == "" {
		link.ID = uuid.NewString()
	}
	if link.CreatedAt.IsZero() {
		link.CreatedAt = nowForDB()
	}
	_, err := r.q.ExecContext(ctx, `INSERT INTO agent_thread_link
		(id, owner_user_id, thread_id, linked_thread_id, relation, confidence, reason, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (owner_user_id, thread_id, linked_thread_id, relation) DO NOTHING`,
		link.ID, link.OwnerUserID, link.ThreadID, link.LinkedThreadID, link.Relation,
		link.Confidence, link.Reason, link.CreatedAt)
	return err
}

func (r *ThreadLinkRepo) Remove(ctx context.Context, ownerID, threadID, linkedThreadID, relation string) (bool, error) {
	res, err := r.q.ExecContext(ctx, `DELETE FROM agent_thread_link
		WHERE owner_user_id = ? AND thread_id = ? AND linked_thread_id = ? AND relation = ?`,
		ownerID, threadID, linkedThreadID, relation)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (r *ThreadLinkRepo) List(ctx context.Context, ownerID, threadID string) ([]ThreadLink, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT id, owner_user_id, thread_id, linked_thread_id,
		relation, confidence, reason, created_at FROM agent_thread_link
		WHERE owner_user_id = ? AND (thread_id = ? OR linked_thread_id = ?)
		ORDER BY created_at`, ownerID, threadID, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]ThreadLink, 0)
	for rows.Next() {
		var link ThreadLink
		if err := rows.Scan(&link.ID, &link.OwnerUserID, &link.ThreadID, &link.LinkedThreadID,
			&link.Relation, &link.Confidence, &link.Reason, &link.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, link)
	}
	return out, rows.Err()
}

type ThreadSubscriptionRepo struct{ q Querier }

func NewThreadSubscriptionRepo(q Querier) *ThreadSubscriptionRepo {
	return &ThreadSubscriptionRepo{q: q}
}

func (d *DB) ThreadSubscriptions() *ThreadSubscriptionRepo { return NewThreadSubscriptionRepo(d) }

func (r *ThreadSubscriptionRepo) Add(ctx context.Context, ownerID, threadID, workerTaskID string, at time.Time) error {
	_, err := r.q.ExecContext(ctx, `INSERT INTO agent_thread_subscription
		(owner_user_id, thread_id, worker_task_id, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (thread_id, worker_task_id) DO NOTHING`, ownerID, threadID, workerTaskID, at)
	return err
}

func (r *ThreadSubscriptionRepo) Remove(ctx context.Context, threadID, workerTaskID string) (bool, error) {
	res, err := r.q.ExecContext(ctx, `DELETE FROM agent_thread_subscription
		WHERE thread_id = ? AND worker_task_id = ?`, threadID, workerTaskID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (r *ThreadSubscriptionRepo) WorkersForThread(ctx context.Context, ownerID, threadID string) ([]string, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT worker_task_id FROM agent_thread_subscription
		WHERE owner_user_id = ? AND thread_id = ? ORDER BY created_at`, ownerID, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (r *ThreadSubscriptionRepo) ThreadsForWorker(ctx context.Context, ownerID, workerTaskID string) ([]string, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT thread_id FROM agent_thread_subscription
		WHERE owner_user_id = ? AND worker_task_id = ? ORDER BY created_at`, ownerID, workerTaskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

type ActionOutboxRepo struct{ q Querier }

func NewActionOutboxRepo(q Querier) *ActionOutboxRepo { return &ActionOutboxRepo{q: q} }

func (d *DB) ActionOutbox() *ActionOutboxRepo { return NewActionOutboxRepo(d) }

const actionOutboxColumns = `id, owner_user_id, channel, thread_id, worker_task_id, work_task_id,
	tool_invocation_id, action_grant_id, expected_effect_json, reconciled_at, kind,
	target_json, body, payload_json, status, requires_approval, idempotency_key, version,
	approved_by, approved_at, provider_message_id, last_error, created_at, updated_at, sent_at`

func scanActionOutbox(row interface{ Scan(...any) error }) (*ActionOutbox, error) {
	var o ActionOutbox
	var target, payload, expected []byte
	var requiresApproval int
	var approvedAt, sentAt, reconciledAt sql.NullTime
	if err := row.Scan(&o.ID, &o.OwnerUserID, &o.Channel, &o.ThreadID, &o.WorkerTaskID,
		&o.WorkTaskID, &o.ToolInvocationID, &o.ActionGrantID, &expected, &reconciledAt,
		&o.Kind, &target, &o.Body, &payload, &o.Status, &requiresApproval, &o.IdempotencyKey,
		&o.Version, &o.ApprovedBy, &approvedAt, &o.ProviderMessageID, &o.LastError,
		&o.CreatedAt, &o.UpdatedAt, &sentAt); err != nil {
		return nil, err
	}
	o.Target = target
	o.Payload = payload
	o.ExpectedEffect = expected
	o.RequiresApproval = requiresApproval == 1
	o.ApprovedAt = timePointer(approvedAt)
	o.SentAt = timePointer(sentAt)
	o.ReconciledAt = timePointer(reconciledAt)
	return &o, nil
}

func (r *ActionOutboxRepo) Create(ctx context.Context, o *ActionOutbox) error {
	if o.ID == "" {
		o.ID = uuid.NewString()
	}
	if o.Status == "" {
		if o.RequiresApproval {
			o.Status = OutboxPendingApproval
		} else {
			o.Status = OutboxDraft
		}
	}
	if len(o.Target) == 0 {
		o.Target = json.RawMessage(`{}`)
	}
	if len(o.Payload) == 0 {
		o.Payload = json.RawMessage(`{}`)
	}
	if len(o.ExpectedEffect) == 0 {
		o.ExpectedEffect = json.RawMessage(`{}`)
	}
	now := nowForDB()
	_, err := r.q.ExecContext(ctx, `INSERT INTO agent_action_outbox
		(id, owner_user_id, channel, thread_id, worker_task_id, work_task_id, tool_invocation_id,
		 action_grant_id, expected_effect_json, kind, target_json, body, payload_json, status,
		 requires_approval, idempotency_key, version, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`,
		o.ID, o.OwnerUserID, o.Channel, o.ThreadID, o.WorkerTaskID, o.WorkTaskID,
		o.ToolInvocationID, o.ActionGrantID, string(o.ExpectedEffect), o.Kind, string(o.Target),
		o.Body, string(o.Payload), o.Status, boolInt(o.RequiresApproval), o.IdempotencyKey, now, now)
	if isUniqueViolation(err) {
		return ErrConflict
	}
	if err == nil {
		o.CreatedAt, o.UpdatedAt = now, now
	}
	return err
}

func (r *ActionOutboxRepo) Get(ctx context.Context, ownerID, id string) (*ActionOutbox, error) {
	o, err := scanActionOutbox(r.q.QueryRowContext(ctx,
		`SELECT `+actionOutboxColumns+` FROM agent_action_outbox WHERE owner_user_id = ? AND id = ?`,
		ownerID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return o, err
}

func (r *ActionOutboxRepo) List(ctx context.Context, ownerID, status string, limit int) ([]ActionOutbox, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := `SELECT ` + actionOutboxColumns + ` FROM agent_action_outbox WHERE owner_user_id = ?`
	args := []any{ownerID}
	if status != "" {
		query += ` AND status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY updated_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := r.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]ActionOutbox, 0)
	for rows.Next() {
		o, err := scanActionOutbox(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *o)
	}
	return out, rows.Err()
}

// UpdateDraft 以版本 CAS 编辑草稿；每次编辑都递增版本，旧审批回调自然失效。
func (r *ActionOutboxRepo) UpdateDraft(ctx context.Context, ownerID, id string, version int, body string, target, payload json.RawMessage, at time.Time) (bool, error) {
	if len(target) == 0 {
		target = json.RawMessage(`{}`)
	}
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	res, err := r.q.ExecContext(ctx, `UPDATE agent_action_outbox
		SET body = ?, target_json = ?, payload_json = ?, action_grant_id = '',
			version = version + 1, updated_at = ?
		WHERE owner_user_id = ? AND id = ? AND version = ?
		  AND status IN ('draft', 'pending_approval')`,
		body, string(target), string(payload), at, ownerID, id, version)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (r *ActionOutboxRepo) Approve(ctx context.Context, ownerID, id string, version int, approver string, at time.Time) (bool, error) {
	res, err := r.q.ExecContext(ctx, `UPDATE agent_action_outbox
		SET status = 'approved', approved_by = ?, approved_at = ?, version = version + 1, updated_at = ?
		WHERE owner_user_id = ? AND id = ? AND version = ?
		  AND status IN ('draft', 'pending_approval')`,
		approver, at, at, ownerID, id, version)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (r *ActionOutboxRepo) Reject(ctx context.Context, ownerID, id string, version int, approver, reason string, at time.Time) (bool, error) {
	res, err := r.q.ExecContext(ctx, `UPDATE agent_action_outbox
		SET status = 'rejected', approved_by = ?, last_error = ?, version = version + 1, updated_at = ?
		WHERE owner_user_id = ? AND id = ? AND version = ?
		  AND status IN ('draft', 'pending_approval')`,
		approver, reason, at, ownerID, id, version)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// LinkGrant 在审批成功后把一次性授权挂到发件箱；内容再次修改会清空关联。
func (r *ActionOutboxRepo) LinkGrant(ctx context.Context, ownerID, id, grantID string, at time.Time) (bool, error) {
	res, err := r.q.ExecContext(ctx, `UPDATE agent_action_outbox SET action_grant_id = ?,
		updated_at = ? WHERE owner_user_id = ? AND id = ? AND status = 'approved'`,
		grantID, at, ownerID, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (r *ActionOutboxRepo) MarkReconciled(ctx context.Context, ownerID, id string, at time.Time) (bool, error) {
	res, err := r.q.ExecContext(ctx, `UPDATE agent_action_outbox SET reconciled_at = ?,
		updated_at = ? WHERE owner_user_id = ? AND id = ?`, at, at, ownerID, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

type AgentEventRepo struct{ q Querier }

func NewAgentEventRepo(q Querier) *AgentEventRepo { return &AgentEventRepo{q: q} }

func (d *DB) AgentEvents() *AgentEventRepo { return NewAgentEventRepo(d) }

func (r *AgentEventRepo) Append(ctx context.Context, e *AgentEvent) (int64, error) {
	if len(e.Payload) == 0 {
		e.Payload = json.RawMessage(`{}`)
	}
	res, err := r.q.ExecContext(ctx, `INSERT INTO agent_event
		(owner_user_id, kind, entity_type, entity_id, payload_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		e.OwnerUserID, e.Kind, e.EntityType, e.EntityID, string(e.Payload), nowForDB())
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	e.ID = id
	return id, nil
}

func (r *AgentEventRepo) After(ctx context.Context, ownerID string, afterID int64, limit int) ([]AgentEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := r.q.QueryContext(ctx, `SELECT id, owner_user_id, kind, entity_type, entity_id,
		payload_json, created_at FROM agent_event
		WHERE owner_user_id = ? AND id > ? ORDER BY id LIMIT ?`, ownerID, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]AgentEvent, 0)
	for rows.Next() {
		var e AgentEvent
		var payload []byte
		if err := rows.Scan(&e.ID, &e.OwnerUserID, &e.Kind, &e.EntityType, &e.EntityID,
			&payload, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.Payload = payload
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r *AgentEventRepo) AfterForEntity(ctx context.Context, ownerID, entityType, entityID string, afterID int64, limit int) ([]AgentEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := r.q.QueryContext(ctx, `SELECT id, owner_user_id, kind, entity_type, entity_id,
		payload_json, created_at FROM agent_event
		WHERE owner_user_id = ? AND entity_type = ? AND entity_id = ? AND id > ?
		ORDER BY id LIMIT ?`, ownerID, entityType, entityID, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]AgentEvent, 0)
	for rows.Next() {
		var e AgentEvent
		var payload []byte
		if err := rows.Scan(&e.ID, &e.OwnerUserID, &e.Kind, &e.EntityType, &e.EntityID,
			&payload, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.Payload = payload
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r *AgentEventRepo) LatestForEntity(ctx context.Context, ownerID, kind, entityType, entityID string) (*AgentEvent, error) {
	var e AgentEvent
	var payload []byte
	err := r.q.QueryRowContext(ctx, `SELECT id, owner_user_id, kind, entity_type, entity_id,
		payload_json, created_at FROM agent_event
		WHERE owner_user_id = ? AND kind = ? AND entity_type = ? AND entity_id = ?
		ORDER BY id DESC LIMIT 1`, ownerID, kind, entityType, entityID).
		Scan(&e.ID, &e.OwnerUserID, &e.Kind, &e.EntityType, &e.EntityID, &payload, &e.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	e.Payload = payload
	return &e, nil
}

func (r *AgentEventRepo) LatestID(ctx context.Context, ownerID string) (int64, error) {
	var id int64
	err := r.q.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM agent_event WHERE owner_user_id = ?`, ownerID).Scan(&id)
	return id, err
}

func (r *AgentEventRepo) Counts(ctx context.Context, ownerID string) (map[string]int, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT kind, COUNT(*) FROM agent_event
		WHERE owner_user_id = ? GROUP BY kind`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]int)
	for rows.Next() {
		var kind string
		var count int
		if err := rows.Scan(&kind, &count); err != nil {
			return nil, err
		}
		out[kind] = count
	}
	return out, rows.Err()
}
