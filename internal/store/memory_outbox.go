package store

import (
	"context"
	"time"
)

// MemoryOutboxEntry 是等待上传到云端共享记忆的一段原始片段。
type MemoryOutboxEntry struct {
	OwnerUserID string
	SourceID    string
	PayloadJSON string
	ContentHash string
	Status      string
	Attempts    int
	NextRetryAt *time.Time
	LastError   string
	CreatedAt   time.Time
	SentAt      *time.Time
}

const (
	MemoryOutboxPending = "pending"
	MemoryOutboxSent    = "sent"
	MemoryOutboxFailed  = "failed"
	MemoryOutboxPaused  = "paused"
)

type MemoryOutboxRepo struct{ q Querier }

func NewMemoryOutboxRepo(q Querier) *MemoryOutboxRepo { return &MemoryOutboxRepo{q: q} }

// Enqueue 幂等写入待上传片段；同一 owner+source 已存在时保持原记录。
func (r *MemoryOutboxRepo) Enqueue(ctx context.Context, e *MemoryOutboxEntry) error {
	if e.Status == "" {
		e.Status = MemoryOutboxPending
	}
	now := nowForDB()
	e.CreatedAt = now
	_, err := r.q.ExecContext(ctx, `INSERT INTO memory_sync_outbox
		(owner_user_id, source_id, payload_json, content_hash, status, attempts,
		 next_retry_at, last_error, created_at)
		VALUES (?, ?, ?, ?, ?, 0, NULL, '', ?)
		ON CONFLICT(owner_user_id, source_id) DO NOTHING`,
		e.OwnerUserID, e.SourceID, e.PayloadJSON, e.ContentHash, e.Status, now)
	return err
}

// Due 返回现在应当尝试上传的条目（pending 且到达重试时间）。
func (r *MemoryOutboxRepo) Due(ctx context.Context, now time.Time, limit int) ([]MemoryOutboxEntry, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := r.q.QueryContext(ctx, `SELECT owner_user_id, source_id, payload_json,
		content_hash, status, attempts, next_retry_at, last_error, created_at, sent_at
		FROM memory_sync_outbox
		WHERE status = ? AND (next_retry_at IS NULL OR next_retry_at <= ?)
		ORDER BY created_at LIMIT ?`, MemoryOutboxPending, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MemoryOutboxEntry
	for rows.Next() {
		var e MemoryOutboxEntry
		if err := rows.Scan(&e.OwnerUserID, &e.SourceID, &e.PayloadJSON, &e.ContentHash,
			&e.Status, &e.Attempts, &e.NextRetryAt, &e.LastError, &e.CreatedAt, &e.SentAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r *MemoryOutboxRepo) MarkSent(ctx context.Context, ownerID, sourceID string, at time.Time) error {
	_, err := r.q.ExecContext(ctx, `UPDATE memory_sync_outbox
		SET status = ?, sent_at = ?, last_error = '', next_retry_at = NULL
		WHERE owner_user_id = ? AND source_id = ?`,
		MemoryOutboxSent, at, ownerID, sourceID)
	return err
}

func (r *MemoryOutboxRepo) MarkRetry(ctx context.Context, ownerID, sourceID, lastError string, next time.Time) error {
	_, err := r.q.ExecContext(ctx, `UPDATE memory_sync_outbox
		SET attempts = attempts + 1, last_error = ?, next_retry_at = ?
		WHERE owner_user_id = ? AND source_id = ?`,
		lastError, next, ownerID, sourceID)
	return err
}

func (r *MemoryOutboxRepo) MarkFailed(ctx context.Context, ownerID, sourceID, lastError string) error {
	_, err := r.q.ExecContext(ctx, `UPDATE memory_sync_outbox
		SET status = ?, attempts = attempts + 1, last_error = ?, next_retry_at = NULL
		WHERE owner_user_id = ? AND source_id = ?`,
		MemoryOutboxFailed, lastError, ownerID, sourceID)
	return err
}

func (r *MemoryOutboxRepo) MarkPaused(ctx context.Context, ownerID, sourceID, lastError string) error {
	_, err := r.q.ExecContext(ctx, `UPDATE memory_sync_outbox
		SET status = ?, last_error = ?, next_retry_at = NULL
		WHERE owner_user_id = ? AND source_id = ?`,
		MemoryOutboxPaused, lastError, ownerID, sourceID)
	return err
}

// ResumePaused 在重新授权后把暂停的条目恢复为待上传。
func (r *MemoryOutboxRepo) ResumePaused(ctx context.Context, ownerID string, at time.Time) (int64, error) {
	res, err := r.q.ExecContext(ctx, `UPDATE memory_sync_outbox
		SET status = ?, next_retry_at = ? WHERE owner_user_id = ? AND status = ?`,
		MemoryOutboxPending, at, ownerID, MemoryOutboxPaused)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteBySource 清除本机尚未发送的同一来源事件（遗忘时调用）。
func (r *MemoryOutboxRepo) DeleteBySource(ctx context.Context, ownerID, sourceID string) error {
	_, err := r.q.ExecContext(ctx, `DELETE FROM memory_sync_outbox
		WHERE owner_user_id = ? AND source_id = ? AND status <> ?`,
		ownerID, sourceID, MemoryOutboxSent)
	return err
}

// Counts 返回 pending/failed/paused 数量（健康状态展示）。
func (r *MemoryOutboxRepo) Counts(ctx context.Context, ownerID string) (pending, failed, paused int, err error) {
	rows, qerr := r.q.QueryContext(ctx, `SELECT status, count(*) FROM memory_sync_outbox
		WHERE owner_user_id = ? GROUP BY status`, ownerID)
	if qerr != nil {
		return 0, 0, 0, qerr
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return 0, 0, 0, err
		}
		switch status {
		case MemoryOutboxPending:
			pending = n
		case MemoryOutboxFailed:
			failed = n
		case MemoryOutboxPaused:
			paused = n
		}
	}
	return pending, failed, paused, rows.Err()
}
