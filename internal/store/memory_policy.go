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
	MemoryInferred   = "inferred"
	MemoryConfirmed  = "confirmed"
	MemoryDraft      = "draft"
	MemoryConflicted = "conflicted"
)

const (
	PolicyDraft    = "draft"
	PolicyActive   = "active"
	PolicyDisabled = "disabled"
)

type PersonalPolicy struct {
	ID          string          `json:"id"`
	OwnerUserID string          `json:"owner_user_id"`
	PolicyType  string          `json:"policy_type"`
	Name        string          `json:"name"`
	Content     json.RawMessage `json:"content,omitempty"`
	ScopeType   string          `json:"scope_type"`
	ScopeID     string          `json:"scope_id,omitempty"`
	Status      string          `json:"status"`
	Version     int             `json:"version"`
	ValidFrom   *time.Time      `json:"valid_from,omitempty"`
	ValidTo     *time.Time      `json:"valid_to,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

type DeviceEnvironment struct {
	ID          string     `json:"id"`
	OwnerUserID string     `json:"owner_user_id"`
	Name        string     `json:"name"`
	DeviceType  string     `json:"device_type"`
	Online      bool       `json:"online"`
	LastSeenAt  *time.Time `json:"last_seen_at,omitempty"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

type DeviceCapability struct {
	ID          string          `json:"id"`
	OwnerUserID string          `json:"owner_user_id"`
	DeviceID    string          `json:"device_id"`
	ToolName    string          `json:"tool_name"`
	Resource    json.RawMessage `json:"resource_scope,omitempty"`
	WorkDir     string          `json:"work_dir,omitempty"`
	Limits      json.RawMessage `json:"limits,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
}

type TaskDeviceBinding struct {
	OwnerUserID  string    `json:"owner_user_id"`
	TaskID       string    `json:"task_id"`
	DeviceID     string    `json:"device_id"`
	CapabilityID string    `json:"capability_id,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type PersonalPolicyRepo struct{ q Querier }

func NewPersonalPolicyRepo(q Querier) *PersonalPolicyRepo { return &PersonalPolicyRepo{q: q} }

func (d *DB) PersonalPolicies() *PersonalPolicyRepo { return NewPersonalPolicyRepo(d) }

const policyColumns = `id, owner_user_id, policy_type, name, content_json, scope_type,
	scope_id, status, version, valid_from, valid_to, created_at, updated_at`

func scanPolicy(row interface{ Scan(...any) error }) (*PersonalPolicy, error) {
	var p PersonalPolicy
	var content []byte
	var from, to sql.NullTime
	if err := row.Scan(&p.ID, &p.OwnerUserID, &p.PolicyType, &p.Name, &content, &p.ScopeType,
		&p.ScopeID, &p.Status, &p.Version, &from, &to, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	p.Content = content
	p.ValidFrom = timePointer(from)
	p.ValidTo = timePointer(to)
	return &p, nil
}

func (r *PersonalPolicyRepo) Create(ctx context.Context, p *PersonalPolicy) error {
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	if p.Status == "" {
		p.Status = PolicyDraft
	}
	if p.ScopeType == "" {
		p.ScopeType = "global"
	}
	if len(p.Content) == 0 {
		p.Content = json.RawMessage(`{}`)
	}
	now := nowForDB()
	p.CreatedAt, p.UpdatedAt = now, now
	_, err := r.q.ExecContext(ctx, `INSERT INTO personal_policy
		(id, owner_user_id, policy_type, name, content_json, scope_type, scope_id, status,
		 version, valid_from, valid_to, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?)`,
		p.ID, p.OwnerUserID, p.PolicyType, p.Name, string(p.Content), p.ScopeType,
		p.ScopeID, p.Status, p.ValidFrom, p.ValidTo, now, now)
	return err
}

func (r *PersonalPolicyRepo) Get(ctx context.Context, ownerID, id string) (*PersonalPolicy, error) {
	p, err := scanPolicy(r.q.QueryRowContext(ctx,
		`SELECT `+policyColumns+` FROM personal_policy WHERE owner_user_id = ? AND id = ?`, ownerID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

func (r *PersonalPolicyRepo) ListActive(ctx context.Context, ownerID, scopeType, scopeID string, at time.Time) ([]PersonalPolicy, error) {
	query := `SELECT ` + policyColumns + ` FROM personal_policy
		WHERE owner_user_id = ? AND status = 'active'
		  AND (valid_from IS NULL OR valid_from <= ?)
		  AND (valid_to IS NULL OR valid_to > ?)`
	args := []any{ownerID, at, at}
	if scopeType != "" {
		query += ` AND scope_type = ?`
		args = append(args, scopeType)
		if scopeID != "" {
			query += ` AND scope_id = ?`
			args = append(args, scopeID)
		}
	}
	query += ` ORDER BY created_at`
	rows, err := r.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]PersonalPolicy, 0)
	for rows.Next() {
		p, err := scanPolicy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (r *PersonalPolicyRepo) ListAll(ctx context.Context, ownerID string, limit int) ([]PersonalPolicy, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := r.q.QueryContext(ctx, `SELECT `+policyColumns+` FROM personal_policy
		WHERE owner_user_id = ? ORDER BY updated_at DESC LIMIT ?`, ownerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]PersonalPolicy, 0)
	for rows.Next() {
		p, err := scanPolicy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (r *PersonalPolicyRepo) SetStatus(ctx context.Context, ownerID, id string, expectedVersion int, status string, at time.Time) (bool, error) {
	res, err := r.q.ExecContext(ctx, `UPDATE personal_policy SET status = ?, version = version + 1,
		updated_at = ? WHERE owner_user_id = ? AND id = ? AND version = ?`,
		status, at, ownerID, id, expectedVersion)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

type DeviceRepo struct{ q Querier }

func NewDeviceRepo(q Querier) *DeviceRepo { return &DeviceRepo{q: q} }

func (d *DB) Devices() *DeviceRepo { return NewDeviceRepo(d) }

func (r *DeviceRepo) Upsert(ctx context.Context, d *DeviceEnvironment) error {
	if d.ID == "" {
		d.ID = uuid.NewString()
	}
	now := nowForDB()
	d.CreatedAt, d.UpdatedAt = now, now
	_, err := r.q.ExecContext(ctx, `INSERT INTO device_environment
		(id, owner_user_id, name, device_type, online, last_seen_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET name = excluded.name, device_type = excluded.device_type,
			online = excluded.online, last_seen_at = excluded.last_seen_at,
			updated_at = excluded.updated_at
		WHERE device_environment.owner_user_id = excluded.owner_user_id`,
		d.ID, d.OwnerUserID, d.Name, d.DeviceType, boolInt(d.Online), d.LastSeenAt, now, now)
	return err
}

const deviceColumns = `id, owner_user_id, name, device_type, online, last_seen_at,
	revoked_at, created_at, updated_at`

func scanDevice(row interface{ Scan(...any) error }) (*DeviceEnvironment, error) {
	var d DeviceEnvironment
	var online int
	var seen, revoked sql.NullTime
	if err := row.Scan(&d.ID, &d.OwnerUserID, &d.Name, &d.DeviceType, &online, &seen,
		&revoked, &d.CreatedAt, &d.UpdatedAt); err != nil {
		return nil, err
	}
	d.Online = online == 1
	d.LastSeenAt = timePointer(seen)
	d.RevokedAt = timePointer(revoked)
	return &d, nil
}

func (r *DeviceRepo) Get(ctx context.Context, ownerID, id string) (*DeviceEnvironment, error) {
	d, err := scanDevice(r.q.QueryRowContext(ctx,
		`SELECT `+deviceColumns+` FROM device_environment WHERE owner_user_id = ? AND id = ?`, ownerID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return d, err
}

func (r *DeviceRepo) List(ctx context.Context, ownerID string) ([]DeviceEnvironment, error) {
	rows, err := r.q.QueryContext(ctx,
		`SELECT `+deviceColumns+` FROM device_environment WHERE owner_user_id = ? ORDER BY updated_at DESC`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]DeviceEnvironment, 0)
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

func (r *DeviceRepo) SetOnline(ctx context.Context, ownerID, id string, online bool, at time.Time) error {
	_, err := r.q.ExecContext(ctx, `UPDATE device_environment SET online = ?, last_seen_at = ?,
		updated_at = ? WHERE owner_user_id = ? AND id = ? AND revoked_at IS NULL`,
		boolInt(online), at, at, ownerID, id)
	return err
}

func (r *DeviceRepo) Revoke(ctx context.Context, ownerID, id string, at time.Time) (bool, error) {
	res, err := r.q.ExecContext(ctx, `UPDATE device_environment SET revoked_at = ?, online = 0,
		updated_at = ? WHERE owner_user_id = ? AND id = ? AND revoked_at IS NULL`,
		at, at, ownerID, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (r *DeviceRepo) AddCapability(ctx context.Context, c *DeviceCapability) error {
	if c.ID == "" {
		c.ID = uuid.NewString()
	}
	if len(c.Resource) == 0 {
		c.Resource = json.RawMessage(`{}`)
	}
	if len(c.Limits) == 0 {
		c.Limits = json.RawMessage(`{}`)
	}
	c.CreatedAt = nowForDB()
	_, err := r.q.ExecContext(ctx, `INSERT INTO device_capability
		(id, owner_user_id, device_id, tool_name, resource_scope_json, work_dir, limits_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (device_id, tool_name) DO UPDATE SET
			resource_scope_json = excluded.resource_scope_json,
			work_dir = excluded.work_dir, limits_json = excluded.limits_json`,
		c.ID, c.OwnerUserID, c.DeviceID, c.ToolName, string(c.Resource), c.WorkDir,
		string(c.Limits), c.CreatedAt)
	return err
}

func (r *DeviceRepo) Capabilities(ctx context.Context, ownerID, deviceID string) ([]DeviceCapability, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT id, owner_user_id, device_id, tool_name,
		resource_scope_json, work_dir, limits_json, created_at FROM device_capability
		WHERE owner_user_id = ? AND device_id = ? ORDER BY tool_name`, ownerID, deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]DeviceCapability, 0)
	for rows.Next() {
		var c DeviceCapability
		var resource, limits []byte
		if err := rows.Scan(&c.ID, &c.OwnerUserID, &c.DeviceID, &c.ToolName, &resource,
			&c.WorkDir, &limits, &c.CreatedAt); err != nil {
			return nil, err
		}
		c.Resource = resource
		c.Limits = limits
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *DeviceRepo) BindTask(ctx context.Context, b *TaskDeviceBinding) error {
	if b.CreatedAt.IsZero() {
		b.CreatedAt = nowForDB()
	}
	_, err := r.q.ExecContext(ctx, `INSERT INTO work_task_device_binding
		(owner_user_id, task_id, device_id, capability_id, created_at)
		VALUES (?, ?, ?, ?, ?) ON CONFLICT (task_id, device_id) DO UPDATE SET
		capability_id = excluded.capability_id`, b.OwnerUserID, b.TaskID, b.DeviceID,
		b.CapabilityID, b.CreatedAt)
	return err
}

func (r *DeviceRepo) TaskDevices(ctx context.Context, ownerID, taskID string) ([]TaskDeviceBinding, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT owner_user_id, task_id, device_id,
		capability_id, created_at FROM work_task_device_binding
		WHERE owner_user_id = ? AND task_id = ? ORDER BY created_at`, ownerID, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]TaskDeviceBinding, 0)
	for rows.Next() {
		var b TaskDeviceBinding
		if err := rows.Scan(&b.OwnerUserID, &b.TaskID, &b.DeviceID, &b.CapabilityID,
			&b.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
