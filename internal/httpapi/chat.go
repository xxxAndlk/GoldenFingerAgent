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

// handleChat runs one conversation turn through the agent loop.
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

	// Load or create the chat session.
	sess := &agent.Session{UserID: u.ID}
	if req.SessionID != "" {
		cs, err := s.Repos.Sessions.Get(ctx, req.SessionID)
		if err != nil {
			writeError(w, 404, "session not found")
			return
		}
		sess.ID = cs.ID
		// Restore pending action from persisted session state.
		sess.Pending = pendingFromState(cs.State)
		// Reload recent transcript so the loop has conversation context.
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

	// Append the user message to persistence before the turn.
	userMsg := &store.ChatMessage{SessionID: sess.ID, Role: "user", Content: req.Text}
	_ = s.Repos.Sessions.AppendMessage(ctx, userMsg)

	// Run the agent turn (uses the shared runtime; session transcript in memory).
	rt := s.effectiveRuntime()
	result, err := agent.Run(ctx, sess, req.Text, rt)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}

	// Persist assistant reply + session state (pending action).
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

// pendingFromState restores the clarify round-trip state from session JSON.
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
