package store

import (
	"context"
	"time"
)

// MemoryOutboxEntry 是等待上传到云端共享记忆的一段原始片段或一条删除指令。
type MemoryOutboxEntry struct {
	OwnerUserID string
	SourceID    string
	PayloadJSON string
	ContentHash string
	Status      string
	// Op 是这条待发记录要做的事：upsert（写入/更新）或 delete（遗忘）。
	Op          string
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

const (
	// MemoryOutboxUpsert 是写入/更新一段记忆。
	MemoryOutboxUpsert = "upsert"
	// MemoryOutboxDelete 是从云端删除一段记忆。
	MemoryOutboxDelete = "delete"
)

type MemoryOutboxRepo struct{ q Querier }

func NewMemoryOutboxRepo(q Querier) *MemoryOutboxRepo { return &MemoryOutboxRepo{q: q} }

// Enqueue 幂等写入待上传片段；同一 owner+source 已存在时保持原记录。
//
// 相同来源但内容变了要重新排队：来源 ID 按内容寻址，
// 内容变了就是新的一条记忆，必须送到云端。
func (r *MemoryOutboxRepo) Enqueue(ctx context.Context, e *MemoryOutboxEntry) error {
	if e.Status == "" {
		e.Status = MemoryOutboxPending
	}
	if e.Op == "" {
		e.Op = MemoryOutboxUpsert
	}
	now := nowForDB()
	e.CreatedAt = now
	_, err := r.q.ExecContext(ctx, `INSERT INTO memory_sync_outbox
		(owner_user_id, source_id, payload_json, content_hash, status, attempts,
		 next_retry_at, last_error, created_at, op)
		VALUES (?, ?, ?, ?, ?, 0, NULL, '', ?, ?)
		ON CONFLICT(owner_user_id, source_id) DO UPDATE SET
			payload_json = excluded.payload_json,
			content_hash = excluded.content_hash,
			status = excluded.status,
			attempts = 0,
			next_retry_at = NULL,
			last_error = '',
			op = excluded.op
		WHERE memory_sync_outbox.content_hash <> excluded.content_hash
		   OR memory_sync_outbox.status <> ?`,
		e.OwnerUserID, e.SourceID, e.PayloadJSON, e.ContentHash, e.Status, now, e.Op,
		MemoryOutboxSent)
	return err
}

// EnqueueDelete 排队一条删除指令。
//
// 删除必须和写入一样进队列：只在联网时直接删，
// 断网时删除会丢，云端旧内容就永久留着、另一端仍能召回。
func (r *MemoryOutboxRepo) EnqueueDelete(ctx context.Context, e *MemoryOutboxEntry) error {
	if e.Status == "" {
		e.Status = MemoryOutboxPending
	}
	e.Op = MemoryOutboxDelete
	now := nowForDB()
	e.CreatedAt = now
	_, err := r.q.ExecContext(ctx, `INSERT INTO memory_sync_outbox
		(owner_user_id, source_id, payload_json, content_hash, status, attempts,
		 next_retry_at, last_error, created_at, op)
		VALUES (?, ?, ?, ?, ?, 0, NULL, '', ?, ?)
		ON CONFLICT(owner_user_id, source_id) DO UPDATE SET
			payload_json = excluded.payload_json,
			content_hash = excluded.content_hash,
			status = excluded.status,
			attempts = 0,
			next_retry_at = NULL,
			last_error = '',
			op = excluded.op
		WHERE memory_sync_outbox.status <> ?`,
		e.OwnerUserID, e.SourceID, e.PayloadJSON, e.ContentHash, e.Status, now, e.Op,
		MemoryOutboxSent)
	return err
}

// Due 返回现在应当尝试上传的条目（pending 且到达重试时间）。
func (r *MemoryOutboxRepo) Due(ctx context.Context, now time.Time, limit int) ([]MemoryOutboxEntry, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := r.q.QueryContext(ctx, `SELECT owner_user_id, source_id, payload_json,
		content_hash, status, attempts, next_retry_at, last_error, created_at, sent_at, op
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
			&e.Status, &e.Attempts, &e.NextRetryAt, &e.LastError, &e.CreatedAt, &e.SentAt, &e.Op); err != nil {
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

// PurgeBySource 清掉同一来源的全部待发记录（含已发送）。
//
// 遗忘之后要能重新写回同样的内容：来源 ID 按内容寻址，
// 同样的内容会得到同一个 ID，旧记录必须先清掉才能重新排队。
func (r *MemoryOutboxRepo) PurgeBySource(ctx context.Context, ownerID, sourceID string) error {
	_, err := r.q.ExecContext(ctx, `DELETE FROM memory_sync_outbox
		WHERE owner_user_id = ? AND source_id = ?`, ownerID, sourceID)
	return err
}

// PurgeBySourcePrefix 清掉同一前缀下的全部待发记录（含已发送）。
func (r *MemoryOutboxRepo) PurgeBySourcePrefix(ctx context.Context, ownerID, prefix string) error {
	_, err := r.q.ExecContext(ctx, `DELETE FROM memory_sync_outbox
		WHERE owner_user_id = ? AND source_id LIKE ? ESCAPE '\'`,
		ownerID, escapeLike(prefix)+"%")
	return err
}

// RetryFailed 把失败条目重新放回待发队列（用户在界面上点「重试」）。
func (r *MemoryOutboxRepo) RetryFailed(ctx context.Context, ownerID string) (int64, error) {
	res, err := r.q.ExecContext(ctx, `UPDATE memory_sync_outbox
		SET status = ?, attempts = 0, next_retry_at = NULL, last_error = ''
		WHERE owner_user_id = ? AND status = ?`,
		MemoryOutboxPending, ownerID, MemoryOutboxFailed)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteBySourcePrefix 清除本机尚未发送的同一前缀事件（按前缀遗忘时调用）。
func (r *MemoryOutboxRepo) DeleteBySourcePrefix(ctx context.Context, ownerID, prefix string) error {
	_, err := r.q.ExecContext(ctx, `DELETE FROM memory_sync_outbox
		WHERE owner_user_id = ? AND source_id LIKE ? ESCAPE '\' AND status <> ?`,
		ownerID, escapeLike(prefix)+"%", MemoryOutboxSent)
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
