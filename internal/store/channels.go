package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

// ChannelPolicy 是单用户下的渠道采集策略。source 为空表示该渠道默认策略；
// 具体来源（微信群/联系人/邮箱账号）优先于默认策略。
type ChannelPolicy struct {
	OwnerUserID string    `json:"owner_user_id"`
	Channel     string    `json:"channel"`
	Source      string    `json:"source"`
	Allowed     bool      `json:"allowed"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type ChannelPolicyRepo struct{ q Querier }

func NewChannelPolicyRepo(q Querier) *ChannelPolicyRepo { return &ChannelPolicyRepo{q: q} }
func (d *DB) ChannelPolicies() *ChannelPolicyRepo       { return NewChannelPolicyRepo(d) }

// Allowed 返回来源是否允许采集。WeChat 默认拒绝，直到用户显式配置允许来源；
// 其他渠道默认允许，避免普通聊天/提醒被误拦。
func (r *ChannelPolicyRepo) Allowed(ctx context.Context, ownerID, channel, source string) (bool, error) {
	if source != "" {
		var allowed int
		err := r.q.QueryRowContext(ctx, `SELECT allowed FROM agent_channel_policy
			WHERE owner_user_id = ? AND channel = ? AND source = ?`, ownerID, channel, source).Scan(&allowed)
		if err == nil {
			return allowed == 1, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
	}
	var allowed int
	err := r.q.QueryRowContext(ctx, `SELECT allowed FROM agent_channel_policy
		WHERE owner_user_id = ? AND channel = ? AND source = ''`, ownerID, channel).Scan(&allowed)
	if err == nil {
		return allowed == 1, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	return channel != "wechat", nil
}

func (r *ChannelPolicyRepo) Set(ctx context.Context, ownerID, channel, source string, allowed bool, at time.Time) error {
	value := 0
	if allowed {
		value = 1
	}
	_, err := r.q.ExecContext(ctx, `INSERT INTO agent_channel_policy
		(owner_user_id, channel, source, allowed, updated_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (owner_user_id, channel, source) DO UPDATE SET
		allowed = excluded.allowed, updated_at = excluded.updated_at`,
		ownerID, channel, source, value, at)
	return err
}

func (r *ChannelPolicyRepo) List(ctx context.Context, ownerID, channel string) ([]ChannelPolicy, error) {
	query := `SELECT owner_user_id, channel, source, allowed, updated_at FROM agent_channel_policy WHERE owner_user_id = ?`
	args := []any{ownerID}
	if channel != "" {
		query += ` AND channel = ?`
		args = append(args, channel)
	}
	query += ` ORDER BY channel, source`
	rows, err := r.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]ChannelPolicy, 0)
	for rows.Next() {
		var item ChannelPolicy
		var allowed int
		if err := rows.Scan(&item.OwnerUserID, &item.Channel, &item.Source, &allowed, &item.UpdatedAt); err != nil {
			return nil, err
		}
		item.Allowed = allowed == 1
		out = append(out, item)
	}
	return out, rows.Err()
}

type ChannelSkipRepo struct{ q Querier }

func NewChannelSkipRepo(q Querier) *ChannelSkipRepo { return &ChannelSkipRepo{q: q} }
func (d *DB) ChannelSkips() *ChannelSkipRepo        { return NewChannelSkipRepo(d) }

func (r *ChannelSkipRepo) Increment(ctx context.Context, ownerID, channel, day string, at time.Time) error {
	_, err := r.q.ExecContext(ctx, `INSERT INTO agent_channel_skip
		(owner_user_id, channel, day, count, updated_at) VALUES (?, ?, ?, 1, ?)
		ON CONFLICT (owner_user_id, channel, day) DO UPDATE SET
		count = count + 1, updated_at = excluded.updated_at`,
		ownerID, channel, day, at)
	return err
}

type ChannelSkip struct {
	OwnerUserID string    `json:"owner_user_id"`
	Channel     string    `json:"channel"`
	Day         string    `json:"day"`
	Count       int       `json:"count"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (r *ChannelSkipRepo) List(ctx context.Context, ownerID, day string) ([]ChannelSkip, error) {
	query := `SELECT owner_user_id, channel, day, count, updated_at FROM agent_channel_skip WHERE owner_user_id = ?`
	args := []any{ownerID}
	if day != "" {
		query += ` AND day = ?`
		args = append(args, day)
	}
	query += ` ORDER BY day DESC, channel`
	rows, err := r.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]ChannelSkip, 0)
	for rows.Next() {
		var item ChannelSkip
		if err := rows.Scan(&item.OwnerUserID, &item.Channel, &item.Day, &item.Count, &item.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

type ChannelCursorRepo struct{ q Querier }

func NewChannelCursorRepo(q Querier) *ChannelCursorRepo { return &ChannelCursorRepo{q: q} }
func (d *DB) ChannelCursors() *ChannelCursorRepo        { return NewChannelCursorRepo(d) }

func (r *ChannelCursorRepo) Get(ctx context.Context, ownerID, channel, source string) (string, error) {
	var cursor string
	err := r.q.QueryRowContext(ctx, `SELECT cursor FROM agent_channel_cursor
		WHERE owner_user_id = ? AND channel = ? AND source = ?`, ownerID, channel, source).Scan(&cursor)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return cursor, err
}

func (r *ChannelCursorRepo) Set(ctx context.Context, ownerID, channel, source, cursor string, at time.Time) error {
	_, err := r.q.ExecContext(ctx, `INSERT INTO agent_channel_cursor
		(owner_user_id, channel, source, cursor, updated_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (owner_user_id, channel, source) DO UPDATE SET
		cursor = excluded.cursor, updated_at = excluded.updated_at`,
		ownerID, channel, source, cursor, at)
	return err
}

// DailyBrief 是跨渠道每日简报。每次重新生成产生新版本，旧版本保留。
type DailyBrief struct {
	ID          string          `json:"id"`
	OwnerUserID string          `json:"owner_user_id"`
	BriefDate   string          `json:"brief_date"`
	Version     int             `json:"version"`
	ContentMD   string          `json:"content_md"`
	Sections    json.RawMessage `json:"sections"`
	SourceRefs  json.RawMessage `json:"source_refs"`
	Model       string          `json:"model"`
	Status      string          `json:"status"`
	DeliveredAt *time.Time      `json:"delivered_at,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

type DailyBriefRepo struct{ q Querier }

func NewDailyBriefRepo(q Querier) *DailyBriefRepo { return &DailyBriefRepo{q: q} }
func (d *DB) DailyBriefs() *DailyBriefRepo        { return NewDailyBriefRepo(d) }

func (r *DailyBriefRepo) NextVersion(ctx context.Context, ownerID, date string) (int, error) {
	var version int
	err := r.q.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) + 1 FROM agent_daily_brief
		WHERE owner_user_id = ? AND brief_date = ?`, ownerID, date).Scan(&version)
	return version, err
}

func (r *DailyBriefRepo) Create(ctx context.Context, brief *DailyBrief) error {
	brief.ID = uuid.NewString()
	brief.Sections = notNullJSON(brief.Sections)
	brief.SourceRefs = notNullJSON(brief.SourceRefs)
	if brief.Version <= 0 {
		brief.Version = 1
	}
	if brief.Status == "" {
		brief.Status = "ready"
	}
	now := nowForDB()
	err := r.q.QueryRowContext(ctx, `INSERT INTO agent_daily_brief
		(id, owner_user_id, brief_date, version, content_md, sections_json, source_refs_json,
		 model, status, delivered_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?)
		RETURNING created_at, updated_at`,
		brief.ID, brief.OwnerUserID, brief.BriefDate, brief.Version, brief.ContentMD,
		string(brief.Sections), string(brief.SourceRefs), brief.Model, brief.Status, now, now).
		Scan(&brief.CreatedAt, &brief.UpdatedAt)
	return err
}

func (r *DailyBriefRepo) Latest(ctx context.Context, ownerID, date string) (*DailyBrief, error) {
	row := r.q.QueryRowContext(ctx, `SELECT id, owner_user_id, brief_date, version, content_md,
		sections_json, source_refs_json, model, status, delivered_at, created_at, updated_at
		FROM agent_daily_brief WHERE owner_user_id = ? AND brief_date = ?
		ORDER BY version DESC LIMIT 1`, ownerID, date)
	brief, err := scanDailyBrief(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return brief, err
}

func (r *DailyBriefRepo) Get(ctx context.Context, ownerID, id string) (*DailyBrief, error) {
	row := r.q.QueryRowContext(ctx, `SELECT id, owner_user_id, brief_date, version, content_md,
		sections_json, source_refs_json, model, status, delivered_at, created_at, updated_at
		FROM agent_daily_brief WHERE owner_user_id = ? AND id = ?`, ownerID, id)
	brief, err := scanDailyBrief(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return brief, err
}

func (r *DailyBriefRepo) List(ctx context.Context, ownerID string, limit int) ([]DailyBrief, error) {
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	rows, err := r.q.QueryContext(ctx, `SELECT id, owner_user_id, brief_date, version, content_md,
		sections_json, source_refs_json, model, status, delivered_at, created_at, updated_at
		FROM agent_daily_brief WHERE owner_user_id = ?
		ORDER BY brief_date DESC, version DESC LIMIT ?`, ownerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]DailyBrief, 0)
	for rows.Next() {
		brief, err := scanDailyBrief(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *brief)
	}
	return out, rows.Err()
}

func (r *DailyBriefRepo) MarkDelivered(ctx context.Context, ownerID, id string, at time.Time) (bool, error) {
	res, err := r.q.ExecContext(ctx, `UPDATE agent_daily_brief SET status = 'delivered',
		delivered_at = ?, updated_at = ? WHERE owner_user_id = ? AND id = ? AND status = 'ready'`,
		at, at, ownerID, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func scanDailyBrief(row interface{ Scan(...any) error }) (*DailyBrief, error) {
	brief := &DailyBrief{}
	var sections, refs []byte
	var deliveredAt sql.NullTime
	if err := row.Scan(&brief.ID, &brief.OwnerUserID, &brief.BriefDate, &brief.Version,
		&brief.ContentMD, &sections, &refs, &brief.Model, &brief.Status, &deliveredAt,
		&brief.CreatedAt, &brief.UpdatedAt); err != nil {
		return nil, err
	}
	brief.Sections = sections
	brief.SourceRefs = refs
	if deliveredAt.Valid {
		brief.DeliveredAt = &deliveredAt.Time
	}
	return brief, nil
}
