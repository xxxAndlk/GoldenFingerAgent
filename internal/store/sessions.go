package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
)

type SessionRepo struct{ q Querier }

func NewSessionRepo(q Querier) *SessionRepo { return &SessionRepo{q: q} }

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

// RecentMessages 按时间顺序返回最新的 n 条消息。
func (r *SessionRepo) RecentMessages(ctx context.Context, sessionID string, n int) ([]ChatMessage, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT id, session_id, role, content, tool_calls_json, tool_call_id, name, created_at
		FROM chat_message WHERE session_id = ?
		ORDER BY created_at DESC LIMIT ?`, sessionID, n)
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
