package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

type SessionRepo struct{ q Querier }

func NewSessionRepo(q Querier) *SessionRepo { return &SessionRepo{q: q} }

func (r *SessionRepo) Create(ctx context.Context, s *ChatSession) error {
	return r.q.QueryRow(ctx, `
		INSERT INTO chat_session (owner_user_id, state_jsonb)
		VALUES ($1, $2)
		RETURNING id, created_at, updated_at`,
		s.OwnerUserID, notNullJSON(s.State),
	).Scan(&s.ID, &s.CreatedAt, &s.UpdatedAt)
}

func (r *SessionRepo) Get(ctx context.Context, id string) (*ChatSession, error) {
	s := &ChatSession{}
	var state []byte
	err := r.q.QueryRow(ctx, `
		SELECT id, owner_user_id, state_jsonb, created_at, updated_at
		FROM chat_session WHERE id = $1`, id).
		Scan(&s.ID, &s.OwnerUserID, &state, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	s.State = state
	return s, nil
}

func (r *SessionRepo) SaveState(ctx context.Context, id string, state json.RawMessage) error {
	tag, err := r.q.Exec(ctx, `
		UPDATE chat_session SET state_jsonb = $2, updated_at = now() WHERE id = $1`,
		id, notNullJSON(state))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *SessionRepo) AppendMessage(ctx context.Context, m *ChatMessage) error {
	return r.q.QueryRow(ctx, `
		INSERT INTO chat_message (session_id, role, content, tool_calls_jsonb, tool_call_id, name)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, created_at`,
		m.SessionID, m.Role, m.Content, m.ToolCalls, m.ToolCallID, m.Name,
	).Scan(&m.ID, &m.CreatedAt)
}

// RecentMessages 按时间顺序返回最新的 n 条消息。
func (r *SessionRepo) RecentMessages(ctx context.Context, sessionID string, n int) ([]ChatMessage, error) {
	rows, err := r.q.Query(ctx, `
		SELECT id, session_id, role, content, tool_calls_jsonb, tool_call_id, name, created_at
		FROM chat_message WHERE session_id = $1
		ORDER BY created_at DESC LIMIT $2`, sessionID, n)
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
