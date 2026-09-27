package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

// 常驻意图的生命周期状态（事件触发的前瞻记忆）。
const (
	IntentPending   = "pending"
	IntentArmed     = "armed"
	IntentFired     = "fired"
	IntentDone      = "done"
	IntentCancelled = "cancelled"
	IntentExpired   = "expired"
)

// StandingIntent 是事件触发的提醒（“当……时提醒我”）。它在匹配的消息
// 出现时触发，而非按时钟触发。TriggerGroups 是 OR-of-ANDs：一条消息只要
// 至少一个分组中的每个词都出现，就算命中。
type StandingIntent struct {
	ID              string     `json:"id"`
	OwnerUserID     string     `json:"owner_user_id"`
	Description     string     `json:"description"`
	TriggerGroups   [][]string `json:"trigger_groups"`
	Status          string     `json:"status"`
	FireCount       int        `json:"fire_count"`
	MaxFires        int        `json:"max_fires"`
	CooldownSeconds int        `json:"cooldown_seconds"`
	LastFiredAt     *time.Time `json:"last_fired_at,omitempty"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
}

type IntentRepo struct{ q Querier }

func NewIntentRepo(q Querier) *IntentRepo { return &IntentRepo{q: q} }

const intentCols = `id, owner_user_id, description, trigger_groups, status,
	fire_count, max_fires, cooldown_seconds, last_fired_at, expires_at, created_at`

func scanIntent(row Row) (*StandingIntent, error) {
	it := &StandingIntent{}
	var groups []byte
	err := row.Scan(&it.ID, &it.OwnerUserID, &it.Description, &groups, &it.Status,
		&it.FireCount, &it.MaxFires, &it.CooldownSeconds, &it.LastFiredAt, &it.ExpiresAt, &it.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if len(groups) > 0 {
		_ = json.Unmarshal(groups, &it.TriggerGroups)
	}
	return it, nil
}

func scanIntents(rows *sql.Rows) ([]StandingIntent, error) {
	var out []StandingIntent
	for rows.Next() {
		it := StandingIntent{}
		var groups []byte
		if err := rows.Scan(&it.ID, &it.OwnerUserID, &it.Description, &groups, &it.Status,
			&it.FireCount, &it.MaxFires, &it.CooldownSeconds, &it.LastFiredAt, &it.ExpiresAt, &it.CreatedAt); err != nil {
			return nil, err
		}
		if len(groups) > 0 {
			_ = json.Unmarshal(groups, &it.TriggerGroups)
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (r *IntentRepo) Insert(ctx context.Context, it *StandingIntent) error {
	it.ID = uuid.NewString()
	groups, _ := json.Marshal(it.TriggerGroups)
	return r.q.QueryRowContext(ctx, `
		INSERT INTO standing_intent (id, owner_user_id, description, trigger_groups,
			status, max_fires, cooldown_seconds, expires_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING created_at`,
		it.ID, it.OwnerUserID, it.Description, groups, it.Status,
		it.MaxFires, it.CooldownSeconds, it.ExpiresAt, nowForDB(), nowForDB(),
	).Scan(&it.CreatedAt)
}

func (r *IntentRepo) ListByOwner(ctx context.Context, ownerID string) ([]StandingIntent, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT `+intentCols+` FROM standing_intent
		WHERE owner_user_id = ? AND deleted_at IS NULL
		ORDER BY created_at DESC`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIntents(rows)
}

func (r *IntentRepo) Get(ctx context.Context, ownerID, id string) (*StandingIntent, error) {
	return scanIntent(r.q.QueryRowContext(ctx, `
		SELECT `+intentCols+` FROM standing_intent
		WHERE id = ? AND owner_user_id = ? AND deleted_at IS NULL`, id, ownerID))
}

// Cancel 始终是显式操作（意图永远不会被推断取消）。
func (r *IntentRepo) Cancel(ctx context.Context, ownerID, id string) (*StandingIntent, error) {
	return scanIntent(r.q.QueryRowContext(ctx, `
		UPDATE standing_intent SET status = '`+IntentCancelled+`', updated_at = ?
		WHERE id = ? AND owner_user_id = ? AND status IN ('pending','armed','fired')
		RETURNING `+intentCols, nowForDB(), id, ownerID))
}

// MatchCandidates 返回当前可触发的 armed 意图（未过期）。
// 冷却期过滤在 Go 侧完成（SQLite 的 datetime() 无法解析
// modernc 存储的带时区时间文本，故不做 SQL 侧 cooldown 运算）。
func (r *IntentRepo) MatchCandidates(ctx context.Context, ownerID string, now time.Time) ([]StandingIntent, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT `+intentCols+` FROM standing_intent
		WHERE owner_user_id = ? AND deleted_at IS NULL
		  AND (expires_at IS NULL OR expires_at > ?)
		  AND status IN ('armed', 'fired')
		ORDER BY created_at ASC
		LIMIT 64`, ownerID, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIntents(rows)
}

// Fire 原子地标记一次命中：触发预算耗尽 → done，否则 fired。
// 并发安全依赖 MaxOpenConns(1) 单连接串行化；CAS 谓词只做状态约束。
func (r *IntentRepo) Fire(ctx context.Context, id string, now time.Time) (*StandingIntent, error) {
	return scanIntent(r.q.QueryRowContext(ctx, `
		UPDATE standing_intent SET
			fire_count = fire_count + 1,
			last_fired_at = ?,
			status = CASE WHEN fire_count + 1 >= max_fires THEN 'done' ELSE 'fired' END,
			updated_at = ?
		WHERE id = ? AND deleted_at IS NULL
		  AND status IN ('armed', 'fired')
		RETURNING `+intentCols, now, nowForDB(), id))
}

// MarkExpired 是维护操作：过期检测搭在聊天/检查路径上
// （OpenClaw 同样如此——不引入额外的定时器子系统）。
func (r *IntentRepo) MarkExpired(ctx context.Context, now time.Time) error {
	_, err := r.q.ExecContext(ctx, `
		UPDATE standing_intent SET status = '`+IntentExpired+`', updated_at = ?
		WHERE status IN ('pending','armed','fired') AND expires_at IS NOT NULL AND expires_at <= ?`,
		nowForDB(), now)
	return err
}
