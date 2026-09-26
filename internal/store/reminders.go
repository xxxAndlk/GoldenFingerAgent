package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type ReminderRepo struct{ q Querier }

func NewReminderRepo(q Querier) *ReminderRepo { return &ReminderRepo{q: q} }

// InsertIdempotent 插入一条提醒，除非其 dedupe_key 已存在。
// 冲突时返回 created=false（幂等重触发 / 双重扫描）。
func (r *ReminderRepo) InsertIdempotent(ctx context.Context, rem *Reminder) (bool, error) {
	var created bool
	err := r.q.QueryRow(ctx, `
		INSERT INTO reminder (task_id, fire_at, channel, level, state, dedupe_key)
		VALUES ($1, $2, $3, $4, 'pending', $5)
		ON CONFLICT (dedupe_key) DO NOTHING
		RETURNING id, created_at`, rem.TaskID, rem.FireAt, rem.Channel, rem.Level, rem.DedupeKey).
		Scan(&rem.ID, &rem.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	created = true
	return created, nil
}

// Due 返回在 cutoff 之前到期的 pending 提醒（FOR UPDATE SKIP LOCKED）。
func (r *ReminderRepo) Due(ctx context.Context, cutoff time.Time, limit int) ([]Reminder, error) {
	rows, err := r.q.Query(ctx, `
		SELECT id, task_id, fire_at, channel, level, state, dedupe_key, created_at
		FROM reminder
		WHERE state = 'pending' AND fire_at <= $1
		ORDER BY fire_at
		LIMIT $2
		FOR UPDATE SKIP LOCKED`, cutoff, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanReminders(rows)
}

func scanReminders(rows pgx.Rows) ([]Reminder, error) {
	var out []Reminder
	for rows.Next() {
		var r Reminder
		if err := rows.Scan(&r.ID, &r.TaskID, &r.FireAt, &r.Channel, &r.Level, &r.State, &r.DedupeKey, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (r *ReminderRepo) Mark(ctx context.Context, id, state string) error {
	_, err := r.q.Exec(ctx, `UPDATE reminder SET state = $2 WHERE id = $1`, id, state)
	return err
}

// Defer 重写 pending 提醒的 fire_at（DND 延后）。dedupe_key
// 有意保持不变：一次延后并不是一条新提醒。
func (r *ReminderRepo) Defer(ctx context.Context, id string, newFireAt time.Time) error {
	_, err := r.q.Exec(ctx,
		`UPDATE reminder SET fire_at = $2 WHERE id = $1 AND state = 'pending'`, id, newFireAt)
	return err
}

// CancelPendingByTask 取消某任务未投递的提醒（完成/稍后/取消后的收尾）。
func (r *ReminderRepo) CancelPendingByTask(ctx context.Context, taskID string) error {
	_, err := r.q.Exec(ctx, `
		DELETE FROM reminder WHERE task_id = $1 AND state = 'pending'`, taskID)
	return err
}

// CountSentSince 统计自某个时间点以来在指定渠道集合内已投递的提醒数
// （用于每日推送预算）。
func (r *ReminderRepo) CountSentSince(ctx context.Context, ownerID string, since time.Time, channels []string, maxLevel int) (int, error) {
	var n int
	err := r.q.QueryRow(ctx, `
		SELECT count(*) FROM reminder r
		JOIN task t ON t.id = r.task_id
		WHERE t.owner_user_id = $1
		  AND r.created_at >= $2
		  AND r.channel = ANY($3)
		  AND r.level <= $4
		  AND r.state IN ('sent', 'acked')`, ownerID, since, channels, maxLevel).Scan(&n)
	return n, err
}

// ListByTask 返回某任务的全部提醒（供状态检查 / 测试）。
func (r *ReminderRepo) ListByTask(ctx context.Context, taskID string) ([]Reminder, error) {
	rows, err := r.q.Query(ctx, `
		SELECT id, task_id, fire_at, channel, level, state, dedupe_key, created_at
		FROM reminder WHERE task_id = $1 ORDER BY fire_at`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanReminders(rows)
}

// SentSince 统计自某个时间点以来发送给某用户的提醒数（摘要/推送预算）。
func (r *ReminderRepo) SentSince(ctx context.Context, ownerID string, since time.Time) (int, error) {
	var n int
	err := r.q.QueryRow(ctx, `
		SELECT count(*) FROM reminder r JOIN task t ON t.id = r.task_id
		WHERE t.owner_user_id = $1 AND r.state = 'sent' AND r.created_at >= $2`,
		ownerID, since).Scan(&n)
	return n, err
}
