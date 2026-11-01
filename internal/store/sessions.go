package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

type SessionRepo struct{ q Querier }

func NewSessionRepo(q Querier) *SessionRepo { return &SessionRepo{q: q} }

// SessionSummary 是会话列表项：会话名由创建时间生成，预览取第一条用户消息。
type SessionSummary struct {
	ID          string    `json:"id"`
	OwnerUserID string    `json:"owner_user_id"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	Messages    int       `json:"messages"`
	Preview     string    `json:"preview"`
}

func (r *SessionRepo) Create(ctx context.Context, s *ChatSession) error {
	s.ID = uuid.NewString()
	return r.q.QueryRowContext(ctx, `
		INSERT INTO chat_session (id, owner_user_id, state_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		RETURNING created_at, updated_at`,
		s.ID, s.OwnerUserID, notNullJSON(s.State), nowForDB(), nowForDB(),
	).Scan(&s.CreatedAt, &s.UpdatedAt)
}

func (r *SessionRepo) Get(ctx context.Context, id string) (*ChatSession, error) {
	s := &ChatSession{}
	var state []byte
	err := r.q.QueryRowContext(ctx, `
		SELECT id, owner_user_id, state_json, created_at, updated_at
		FROM chat_session WHERE id = ?`, id).
		Scan(&s.ID, &s.OwnerUserID, &state, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	s.State = state
	return s, nil
}

func (r *SessionRepo) SaveState(ctx context.Context, id string, state json.RawMessage) error {
	res, err := r.q.ExecContext(ctx, `
		UPDATE chat_session SET state_json = ?, updated_at = ? WHERE id = ?`,
		notNullJSON(state), nowForDB(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *SessionRepo) AppendMessage(ctx context.Context, m *ChatMessage) error {
	m.ID = uuid.NewString()
	return r.q.QueryRowContext(ctx, `
		INSERT INTO chat_message (id, session_id, role, content, tool_calls_json, tool_call_id, name, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING created_at`,
		m.ID, m.SessionID, m.Role, m.Content, m.ToolCalls, m.ToolCallID, m.Name, nowForDB(),
	).Scan(&m.CreatedAt)
}

// ListByOwner 返回该用户最近的会话，附带首条用户消息预览与消息数。
func (r *SessionRepo) ListByOwner(ctx context.Context, ownerUserID string, limit int) ([]SessionSummary, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.q.QueryContext(ctx, `
		SELECT s.id, s.owner_user_id, s.created_at, s.updated_at,
			(SELECT COUNT(*) FROM chat_message m WHERE m.session_id = s.id) AS messages,
			COALESCE((SELECT m.content FROM chat_message m
				WHERE m.session_id = s.id AND m.role = 'user'
				ORDER BY m.created_at ASC LIMIT 1), '') AS preview
		FROM chat_session s
		WHERE s.owner_user_id = ?
		ORDER BY s.created_at DESC
		LIMIT ?`, ownerUserID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionSummary
	for rows.Next() {
		var item SessionSummary
		if err := rows.Scan(&item.ID, &item.OwnerUserID, &item.CreatedAt, &item.UpdatedAt, &item.Messages, &item.Preview); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// RecentMessages 按时间顺序返回最新的 n 条消息。
func (r *SessionRepo) RecentMessages(ctx context.Context, sessionID string, n int) ([]ChatMessage, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT id, session_id, role, content, tool_calls_json, tool_call_id, name, created_at
		FROM chat_message WHERE session_id = ?
		ORDER BY rowid DESC LIMIT ?`, sessionID, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChatMessage
	for rows.Next() {
		var m ChatMessage
		var toolCalls []byte
		if err := rows.Scan(&m.ID, &m.SessionID, &m.Role, &m.Content, &toolCalls, &m.ToolCallID, &m.Name, &m.CreatedAt); err != nil {
			return nil, err
		}
		m.ToolCalls = toolCalls
		out = append(out, m)
	}
	// 反转为时间正序。
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

// MessagesBefore 返回 beforeID 之前的一页消息（不含 beforeID），时间正序。
// beforeID 为空时取最新一页；游标不存在时返回空页，让客户端停止继续加载。
func (r *SessionRepo) MessagesBefore(ctx context.Context, sessionID, beforeID string, limit int) ([]ChatMessage, error) {
	if limit <= 0 {
		limit = 50
	}
	const columns = `id, session_id, role, content, tool_calls_json, tool_call_id, name, created_at`
	var (
		rows *sql.Rows
		err  error
	)
	if beforeID == "" {
		rows, err = r.q.QueryContext(ctx, `
			SELECT `+columns+` FROM chat_message WHERE session_id = ?
			ORDER BY rowid DESC LIMIT ?`, sessionID, limit)
	} else {
		var beforeRowID int64
		err = r.q.QueryRowContext(ctx, `
			SELECT rowid FROM chat_message WHERE id = ? AND session_id = ?`,
			beforeID, sessionID).Scan(&beforeRowID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		rows, err = r.q.QueryContext(ctx, `
			SELECT `+columns+` FROM chat_message
			WHERE session_id = ? AND rowid < ?
			ORDER BY rowid DESC LIMIT ?`,
			sessionID, beforeRowID, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChatMessage
	for rows.Next() {
		var m ChatMessage
		var toolCalls []byte
		if err := rows.Scan(&m.ID, &m.SessionID, &m.Role, &m.Content, &toolCalls, &m.ToolCallID, &m.Name, &m.CreatedAt); err != nil {
			return nil, err
		}
		m.ToolCalls = toolCalls
		out = append(out, m)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}
