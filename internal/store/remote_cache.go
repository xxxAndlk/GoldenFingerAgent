package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"time"
)

// RemoteCacheRepo 保存手机端最近一次成功读到的电脑端结果。
//
// 只保存展示需要的数据与同步时间，不塞进长期记忆：
// 缓存不是事实源，电脑重新上线后要能被真实结果覆盖。
type RemoteCacheRepo struct{ q Querier }

func NewRemoteCacheRepo(q Querier) *RemoteCacheRepo { return &RemoteCacheRepo{q: q} }

// Put 覆盖某个缓存键的内容。
func (r *RemoteCacheRepo) Put(ctx context.Context, ownerUserID, key string, payload []byte, at time.Time) error {
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO remote_cache (owner_user_id, cache_key, payload_json, synced_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (owner_user_id, cache_key)
		DO UPDATE SET payload_json = excluded.payload_json, synced_at = excluded.synced_at`,
		ownerUserID, key, string(notNullJSON(payload)), at)
	return err
}

// Get 返回缓存内容与同步时间；没有记录时返回 ErrNotFound。
func (r *RemoteCacheRepo) Get(ctx context.Context, ownerUserID, key string) ([]byte, time.Time, error) {
	var (
		payload []byte
		synced  time.Time
	)
	err := r.q.QueryRowContext(ctx, `
		SELECT payload_json, synced_at FROM remote_cache
		WHERE owner_user_id = ? AND cache_key = ?`, ownerUserID, key).
		Scan(&payload, &synced)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, time.Time{}, ErrNotFound
	}
	if err != nil {
		return nil, time.Time{}, err
	}
	return payload, synced, nil
}

// RemotePending 是一条排队的远程变更指令。
type RemotePending struct {
	IdempotencyKey string          `json:"idempotency_key"`
	OwnerUserID    string          `json:"owner_user_id"`
	Kind           string          `json:"kind"`
	Payload        json.RawMessage `json:"payload"`
	Status         string          `json:"status"`
	Attempts       int             `json:"attempts"`
	LastError      string          `json:"last_error"`
	Result         json.RawMessage `json:"result,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

// RemotePendingRepo 保存电脑离线时排队的变更指令。
//
// 幂等键持久化：重连后重放复用同一个键，电脑端据此保证最多执行一次。
type RemotePendingRepo struct{ q Querier }

func NewRemotePendingRepo(q Querier) *RemotePendingRepo { return &RemotePendingRepo{q: q} }

// Enqueue 把一条变更指令排进队列。
//
// 同一个幂等键只会有一条记录，并如实返回它的状态：
//   - 不存在 → 插入，inserted=true；
//   - 已存在且 payload 相同 → inserted=false，返回原记录（调用方按 done/failed/pending 处理）；
//   - 已存在但 payload 不同 → ErrConflict，不能把同键悄悄换成另一条指令。
func (r *RemotePendingRepo) Enqueue(ctx context.Context, item RemotePending) (RemotePending, bool, error) {
	existing, found, err := r.Get(ctx, item.IdempotencyKey)
	if err != nil {
		return RemotePending{}, false, err
	}
	if found {
		return matchExisting(existing, item)
	}
	res, err := r.q.ExecContext(ctx, `
		INSERT INTO remote_pending_command
			(idempotency_key, owner_user_id, kind, payload_json, status, attempts,
			 last_error, result_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'pending', 0, '', '{}', ?, ?)
		ON CONFLICT (idempotency_key) DO NOTHING`,
		item.IdempotencyKey, item.OwnerUserID, item.Kind, string(notNullJSON(item.Payload)),
		item.CreatedAt, item.UpdatedAt)
	if err != nil {
		return RemotePending{}, false, err
	}
	if rows, err := res.RowsAffected(); err == nil && rows == 0 {
		// 并发入队：另一条请求先插入了，按已存在记录处理。
		existing, found, err = r.Get(ctx, item.IdempotencyKey)
		if err != nil {
			return RemotePending{}, false, err
		}
		if !found {
			return RemotePending{}, false, ErrNotFound
		}
		return matchExisting(existing, item)
	}
	return item, true, nil
}

// matchExisting 判断同键请求是不是同一条指令。
//
// 必须同时比对 owner、指令类型和 payload：
//   - 只比 payload，另一个 owner 的同键请求会读到别人已完成的结果或失败原因；
//   - 不比 kind，同一个 owner 用同一个键换一种操作会被误判成同一条指令。
//
// 三者任一不同都按冲突处理：宁可让调用方换键，也不能悄悄替换或复用。
func matchExisting(existing, incoming RemotePending) (RemotePending, bool, error) {
	if existing.OwnerUserID != incoming.OwnerUserID ||
		existing.Kind != incoming.Kind ||
		!sameJSON(existing.Payload, incoming.Payload) {
		return RemotePending{}, false, ErrConflict
	}
	return existing, false, nil
}

// Get 按幂等键取一条排队记录。
func (r *RemotePendingRepo) Get(ctx context.Context, key string) (RemotePending, bool, error) {
	var (
		item    RemotePending
		payload []byte
		result  []byte
	)
	err := r.q.QueryRowContext(ctx, `
		SELECT idempotency_key, owner_user_id, kind, payload_json, status, attempts,
			last_error, result_json, created_at, updated_at
		FROM remote_pending_command WHERE idempotency_key = ?`, key).
		Scan(&item.IdempotencyKey, &item.OwnerUserID, &item.Kind, &payload,
			&item.Status, &item.Attempts, &item.LastError, &result,
			&item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return RemotePending{}, false, nil
	}
	if err != nil {
		return RemotePending{}, false, err
	}
	item.Payload = payload
	item.Result = result
	return item, true, nil
}

// sameJSON 判断两段 JSON 是否等价（忽略空白）。
func sameJSON(a, b json.RawMessage) bool {
	var left, right any
	if json.Unmarshal(a, &left) != nil {
		return string(a) == string(b)
	}
	if json.Unmarshal(b, &right) != nil {
		return string(a) == string(b)
	}
	return reflect.DeepEqual(left, right)
}

// ListPending 按创建顺序返回待执行的指令。
func (r *RemotePendingRepo) ListPending(ctx context.Context, ownerUserID string, limit int) ([]RemotePending, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := r.q.QueryContext(ctx, `
		SELECT idempotency_key, owner_user_id, kind, payload_json, status, attempts,
			last_error, result_json, created_at, updated_at
		FROM remote_pending_command
		WHERE owner_user_id = ? AND status = 'pending'
		ORDER BY created_at ASC LIMIT ?`, ownerUserID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]RemotePending, 0, limit)
	for rows.Next() {
		var (
			item    RemotePending
			payload []byte
			result  []byte
		)
		if err := rows.Scan(&item.IdempotencyKey, &item.OwnerUserID, &item.Kind, &payload,
			&item.Status, &item.Attempts, &item.LastError, &result,
			&item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		item.Payload = payload
		item.Result = result
		out = append(out, item)
	}
	return out, rows.Err()
}

// MarkDone 标记指令已执行并保存结果。
func (r *RemotePendingRepo) MarkDone(ctx context.Context, key string, result []byte, at time.Time) error {
	_, err := r.q.ExecContext(ctx, `
		UPDATE remote_pending_command
		SET status = 'done', result_json = ?, last_error = '', updated_at = ?
		WHERE idempotency_key = ?`, string(notNullJSON(result)), at, key)
	return err
}

// MarkFailed 标记指令执行失败。失败的指令不自动重试：
// 结果未知时让用户核对，不能盲目重放。
func (r *RemotePendingRepo) MarkFailed(ctx context.Context, key, reason string, at time.Time) error {
	_, err := r.q.ExecContext(ctx, `
		UPDATE remote_pending_command
		SET status = 'failed', last_error = ?, attempts = attempts + 1, updated_at = ?
		WHERE idempotency_key = ?`, reason, at, key)
	return err
}

// CountPending 返回该用户还有多少条排队指令。
func (r *RemotePendingRepo) CountPending(ctx context.Context, ownerUserID string) (int, error) {
	var count int
	err := r.q.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM remote_pending_command
		WHERE owner_user_id = ? AND status = 'pending'`, ownerUserID).Scan(&count)
	return count, err
}
