package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

type ReminderRepo struct{ q Querier }

func NewReminderRepo(q Querier) *ReminderRepo { return &ReminderRepo{q: q} }

// InsertIdempotent 插入一条提醒，除非其 dedupe_key 已存在。
// 冲突时返回 created=false（幂等重触发 / 双重扫描）。
func (r *ReminderRepo) InsertIdempotent(ctx context.Context, rem *Reminder) (bool, error) {
	rem.ID = uuid.NewString()
	var created bool
	err := r.q.QueryRowContext(ctx, `
		INSERT INTO reminder (id, task_id, fire_at, channel, level, state, dedupe_key, created_at)
		VALUES (?, ?, ?, ?, ?, 'pending', ?, ?)
		ON CONFLICT (dedupe_key) DO NOTHING
		RETURNING created_at`, rem.ID, rem.TaskID, rem.FireAt, rem.Channel, rem.Level, rem.DedupeKey, nowForDB()).
		Scan(&rem.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	created = true
	return created, nil
}

// Due 返回在 cutoff 之前到期的 pending 提醒（单进程下无需行锁）。
func (r *ReminderRepo) Due(ctx context.Context, cutoff time.Time, limit int) ([]Reminder, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT id, task_id, fire_at, channel, level, state, dedupe_key, created_at
		FROM reminder
		WHERE state = 'pending' AND fire_at <= ?
		ORDER BY fire_at
		LIMIT ?`, cutoff, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanReminders(rows)
}

func scanReminders(rows *sql.Rows) ([]Reminder, error) {
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
	_, err := r.q.ExecContext(ctx, `UPDATE reminder SET state = ? WHERE id = ?`, state, id)
	return err
}

// Defer 重写 pending 提醒的 fire_at（DND 延后）。dedupe_key
// 有意保持不变：一次延后并不是一条新提醒。
func (r *ReminderRepo) Defer(ctx context.Context, id string, newFireAt time.Time) error {
	_, err := r.q.ExecContext(ctx,
		`UPDATE reminder SET fire_at = ? WHERE id = ? AND state = 'pending'`, newFireAt, id)
	return err
}

// CancelPendingByTask 取消某任务未投递的提醒（完成/稍后/取消后的收尾）。
func (r *ReminderRepo) CancelPendingByTask(ctx context.Context, taskID string) error {
	_, err := r.q.ExecContext(ctx, `
		DELETE FROM reminder WHERE task_id = ? AND state = 'pending'`, taskID)
	return err
}

// CountSentSince 统计自某个时间点以来在指定渠道集合内已投递的提醒数
// （用于每日推送预算）。渠道集合展开为 IN (?,...?)。
func (r *ReminderRepo) CountSentSince(ctx context.Context, ownerID string, since time.Time, channels []string, maxLevel int) (int, error) {
	if len(channels) == 0 {
		return 0, nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(channels)), ",")
	args := make([]any, 0, len(channels)+4)
	args = append(args, ownerID, since)
	for _, c := range channels {
		args = append(args, c)
	}
	args = append(args, maxLevel)
	var n int
	err := r.q.QueryRowContext(ctx, `
		SELECT count(*) FROM reminder r
		JOIN task t ON t.id = r.task_id
		WHERE t.owner_user_id = ?
		  AND r.created_at >= ?
		  AND r.channel IN (`+ph+`)
		  AND r.level <= ?
		  AND r.state IN ('sent', 'acked')`, args...).Scan(&n)
	return n, err
}

// ListByTask 返回某任务的全部提醒（供状态检查 / 测试）。
func (r *ReminderRepo) ListByTask(ctx context.Context, taskID string) ([]Reminder, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT id, task_id, fire_at, channel, level, state, dedupe_key, created_at
		FROM reminder WHERE task_id = ? ORDER BY fire_at`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanReminders(rows)
}

// SentSince 统计自某个时间点以来发送给某用户的提醒数（摘要/推送预算）。
func (r *ReminderRepo) SentSince(ctx context.Context, ownerID string, since time.Time) (int, error) {
	var n int
	err := r.q.QueryRowContext(ctx, `
		SELECT count(*) FROM reminder r JOIN task t ON t.id = r.task_id
		WHERE t.owner_user_id = ? AND r.state = 'sent' AND r.created_at >= ?`,
		ownerID, since).Scan(&n)
	return n, err
}
