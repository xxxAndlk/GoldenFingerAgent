package httpapi

import (
	"encoding/json"
	"log"
	"net/http"

	"goldenfinger/agent/internal/agent"
	"goldenfinger/agent/internal/llm"
	"goldenfinger/agent/internal/nlu"
	"goldenfinger/agent/internal/store"
)

type chatRequest struct {
	SessionID string `json:"session_id"`
	Text      string `json:"text"`
}

type chatResponse struct {
	SessionID string       `json:"session_id"`
	Reply     string       `json:"reply"`
	Pending   any          `json:"pending,omitempty"`
	Cards     []agent.Card `json:"cards,omitempty"`
	Trace     []string     `json:"trace,omitempty"`
}

// handleChat 通过 agent 循环运行一轮对话。
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "bad request body")
		return
	}
	if req.Text == "" {
		writeError(w, 400, "text is required")
		return
	}
	ctx := r.Context()
	u := s.currentUser(w, r)
	if u == nil {
		return
	}

	// 加载或创建聊天会话。
	sess := &agent.Session{UserID: u.ID}
	if req.SessionID != "" {
		cs, err := s.Repos.Sessions.Get(ctx, req.SessionID)
		if err != nil {
			writeError(w, 404, "session not found")
			return
		}
		sess.ID = cs.ID
		// 从持久化的会话状态恢复待处理动作。
		sess.Pending = pendingFromState(cs.State)
		// 重载最近的对话记录，使循环具备对话上下文。
		if msgs, err := s.Repos.Sessions.RecentMessages(ctx, cs.ID, 20); err == nil {
			for _, m := range msgs {
				sess.Append(llm.Message{
					Role:    llm.Role(m.Role),
					Content: m.Content,
				})
			}
		}
	} else {
		cs := &store.ChatSession{OwnerUserID: u.ID}
		if err := s.Repos.Sessions.Create(ctx, cs); err != nil {
			writeError(w, 500, err.Error())
			return
		}
		sess.ID = cs.ID
	}

	rid := reqID(ctx)
	log.Printf("[chat] %s user=%s(%s) session=%s text=%q", rid, u.Name, shortID(u.ID), shortID(sess.ID), truncate(req.Text, 80))

	// 在轮次开始前把用户消息写入持久化。
	userMsg := &store.ChatMessage{SessionID: sess.ID, Role: "user", Content: req.Text}
	_ = s.Repos.Sessions.AppendMessage(ctx, userMsg)

	// 运行 agent 轮次（使用共享 runtime；会话记录保存在内存中）。
	rt := s.effectiveRuntime()
	result, err := agent.Run(ctx, sess, req.Text, rt)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}

	// 持久化助手回复 + 会话状态（待处理动作）。
	log.Printf("[chat] %s done: reply=%q cards=%d pending=%v trace=%d", rid, truncate(result.Reply, 80), len(result.Cards), result.Pending != nil, len(result.Trace))
	assistantMsg := &store.ChatMessage{SessionID: sess.ID, Role: "assistant", Content: result.Reply}
	_ = s.Repos.Sessions.AppendMessage(ctx, assistantMsg)
	state, _ := json.Marshal(map[string]any{"pending_action": sess.Pending})
	_ = s.Repos.Sessions.SaveState(ctx, sess.ID, state)

	writeJSON(w, 200, chatResponse{
		SessionID: sess.ID,
		Reply:     result.Reply,
		Pending:   result.Pending,
		Cards:     result.Cards,
		Trace:     result.Trace,
	})
}

// pendingFromState 从会话 JSON 中恢复澄清往返状态。
func pendingFromState(state json.RawMessage) *nlu.PendingAction {
	if len(state) == 0 {
		return nil
	}
	var s struct {
		PendingAction *nlu.PendingAction `json:"pending_action"`
	}
	if err := json.Unmarshal(state, &s); err != nil {
		return nil
	}
	return s.PendingAction
}

type messageDTO struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	At      string `json:"at"`
}

func (s *Server) handleSessionMessages(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	msgs, err := s.Repos.Sessions.RecentMessages(r.Context(), id, 100)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	out := make([]messageDTO, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, messageDTO{Role: m.Role, Content: m.Content, At: m.CreatedAt.Format("15:04")})
	}
	writeJSON(w, 200, out)
}
