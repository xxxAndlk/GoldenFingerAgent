package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// SharedSlot 记录某个共享记忆槽位当前使用的长期记忆来源。
//
// 槽位是一个稳定的位置（例如「2026-11-05 的简报」「张阿姨的备注」）。
// 同一槽位只保留最新一版：内容变了要换来源 ID（旧版本删掉），
// 两端先后修改时靠 SourceUpdatedAt 判断谁更新，避免旧请求覆盖新内容。
type SharedSlot struct {
	OwnerUserID string
	Slot        string
	SourceID    string
	// Revision 是云端/本机给这次更新分配的递增版本号。
	Revision int
	// SourceUpdatedAt 是写入方提供的时间（毫秒），用于判断新旧。
	SourceUpdatedAt int64
	CreatedAt       time.Time
	ModifiedAt      time.Time
}

// SharedMemorySlotRepo 维护共享记忆槽位的当前版本。
type SharedMemorySlotRepo struct{ q Querier }

func NewSharedMemorySlotRepo(q Querier) *SharedMemorySlotRepo {
	return &SharedMemorySlotRepo{q: q}
}

const sharedSlotColumns = `owner_user_id, slot, source_id, revision, source_updated_at,
	created_at, modified_at`

func scanSharedSlot(row interface{ Scan(...any) error }) (SharedSlot, error) {
	var item SharedSlot
	err := row.Scan(&item.OwnerUserID, &item.Slot, &item.SourceID, &item.Revision,
		&item.SourceUpdatedAt, &item.CreatedAt, &item.ModifiedAt)
	return item, err
}

// Current 返回槽位当前记录；没有记录时 found 为 false。
func (r *SharedMemorySlotRepo) Current(ctx context.Context, ownerUserID, slot string) (SharedSlot, bool, error) {
	item, err := scanSharedSlot(r.q.QueryRowContext(ctx, `
		SELECT `+sharedSlotColumns+` FROM shared_memory_slot
		WHERE owner_user_id = ? AND slot = ?`, ownerUserID, slot))
	if errors.Is(err, sql.ErrNoRows) {
		return SharedSlot{}, false, nil
	}
	if err != nil {
		return SharedSlot{}, false, err
	}
	return item, true, nil
}

// Apply 把槽位指向新的来源 ID，并返回上一版记录。
//
// incomingUpdatedAt 比已存版本更旧时返回 stale=true 且不做任何修改：
// 一个离线很久的旧请求恢复后不能把新内容覆盖掉。
// 同一时间戳视为同一版（重复提交），不算过期。
func (r *SharedMemorySlotRepo) Apply(ctx context.Context, ownerUserID, slot, sourceID string,
	incomingUpdatedAt int64, at time.Time) (SharedSlot, bool, error) {
	previous, found, err := r.Current(ctx, ownerUserID, slot)
	if err != nil {
		return SharedSlot{}, false, err
	}
	if found {
		if incomingUpdatedAt < previous.SourceUpdatedAt {
			return previous, true, nil
		}
		if incomingUpdatedAt == previous.SourceUpdatedAt && previous.SourceID == sourceID {
			// 同一版重复提交：不算更新，也不该再涨版本号。
			return previous, false, nil
		}
	}
	revision := 1
	if found {
		revision = previous.Revision + 1
	}
	if _, err := r.q.ExecContext(ctx, `
		INSERT INTO shared_memory_slot
			(owner_user_id, slot, source_id, revision, source_updated_at, created_at, modified_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(owner_user_id, slot) DO UPDATE SET
			source_id = excluded.source_id,
			revision = excluded.revision,
			source_updated_at = excluded.source_updated_at,
			modified_at = excluded.modified_at`,
		ownerUserID, slot, sourceID, revision, incomingUpdatedAt, at, at); err != nil {
		return SharedSlot{}, false, err
	}
	return previous, false, nil
}

// Delete 清掉槽位记录。
func (r *SharedMemorySlotRepo) Delete(ctx context.Context, ownerUserID, slot string) error {
	_, err := r.q.ExecContext(ctx, `
		DELETE FROM shared_memory_slot WHERE owner_user_id = ? AND slot = ?`, ownerUserID, slot)
	return err
}

// DeleteByPrefix 清掉某个前缀下的槽位记录。
//
// 遗忘一个位置时用：槽位的来源 ID 都以它开头，
// 一次删干净才能避免下次写入时又认回旧的版本。
func (r *SharedMemorySlotRepo) DeleteByPrefix(ctx context.Context, ownerUserID, prefix string) error {
	_, err := r.q.ExecContext(ctx, `
		DELETE FROM shared_memory_slot
		WHERE owner_user_id = ? AND slot LIKE ? ESCAPE '\'`,
		ownerUserID, escapeLike(prefix)+"%")
	return err
}
