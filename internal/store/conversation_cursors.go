package store

import (
	"context"
)

// ConversationCursorRepo 保存每个设备对每个会话的已读游标。
//
// 未读数按设备分开统计：手机读过一条任务后，电脑角标不能跟着消失。
// 游标用 chat_message.rowid 而不是时间戳：rowid 严格递增，
// 不会因为时区或精度把同一条消息算成未读。
type ConversationCursorRepo struct{ q Querier }

func NewConversationCursorRepo(q Querier) *ConversationCursorRepo {
	return &ConversationCursorRepo{q: q}
}

// MarkRead 记录该设备已经读到某个 rowid。
func (r *ConversationCursorRepo) MarkRead(ctx context.Context, ownerUserID, deviceID, conversationID string, rowid int64) error {
	if deviceID == "" {
		deviceID = "default"
	}
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO conversation_read_cursor (owner_user_id, device_id, conversation_id, read_rowid)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (owner_user_id, device_id, conversation_id)
		DO UPDATE SET read_rowid = MAX(read_rowid, excluded.read_rowid)`,
		ownerUserID, deviceID, conversationID, rowid)
	return err
}

// ReadMap 批量返回该设备在若干会话上的已读 rowid，便于一次算出未读数。
// 没有记录的会话返回 0。
func (r *ConversationCursorRepo) ReadMap(ctx context.Context, ownerUserID, deviceID string) (map[string]int64, error) {
	if deviceID == "" {
		deviceID = "default"
	}
	rows, err := r.q.QueryContext(ctx, `
		SELECT conversation_id, read_rowid FROM conversation_read_cursor
		WHERE owner_user_id = ? AND device_id = ?`, ownerUserID, deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var id string
		var rowid int64
		if err := rows.Scan(&id, &rowid); err != nil {
			return nil, err
		}
		out[id] = rowid
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
