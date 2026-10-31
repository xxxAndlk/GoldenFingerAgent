package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"goldenfinger/agent/internal/agent"
	"goldenfinger/agent/internal/llm"
	"goldenfinger/agent/internal/nlu"
	"goldenfinger/agent/internal/store"
)

type chatAttachment struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	URL  string `json:"url"`
}

type chatRequest struct {
	SessionID   string           `json:"session_id"`
	Text        string           `json:"text"`
	Attachments []chatAttachment `json:"attachments,omitempty"`
	// Internal 用于协作总结等后台调用：不把内部提示词写成用户消息，也不新建历史会话。
	Internal bool `json:"internal,omitempty"`
}

type chatResponse struct {
	SessionID string       `json:"session_id"`
	Reply     string       `json:"reply"`
	Pending   any          `json:"pending,omitempty"`
	Cards     []agent.Card `json:"cards,omitempty"`
	Trace     []string     `json:"trace,omitempty"`
}

// handleChat 通过 agent 循环运行一轮对话（非流式，兼容旧客户端）。
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "bad request body")
		return
	}
	resp, err := s.runChatTurn(r.Context(), r.Header.Get("X-User-Id"), req, nil)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, resp)
}

// handleChatStream 以 SSE 推送本轮进度（思考 / 调用工具 / 澄清），
// 最后一帧发送完整结果。工具名与步号来自 agent.Runtime 的进度回调。
func (s *Server) handleChatStream(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "bad request body")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, 500, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	emit := func(event string, payload any) {
		raw, err := json.Marshal(payload)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, raw)
		flusher.Flush()
	}

	onEvent := func(ev agent.Event) {
		emit("status", map[string]any{
			"kind": ev.Kind, "name": ev.Name, "step": ev.Step, "detail": ev.Detail,
		})
	}

	resp, err := s.runChatTurn(r.Context(), r.Header.Get("X-User-Id"), req, onEvent)
	if err != nil {
		emit("error", map[string]any{"error": err.Error()})
		return
	}
	emit("done", resp)
}

// runChatTurn 是一轮对话的公共流程：会话准备 → 运行 agent → 持久化。
// onEvent 为 nil 时不派发进度（非流式调用）。
func (s *Server) runChatTurn(ctx context.Context, headerUserID string, req chatRequest, onEvent func(agent.Event)) (*chatResponse, error) {
	if req.Text == "" && len(req.Attachments) == 0 {
		return nil, fmt.Errorf("text is required")
	}
	u, err := s.resolveUser(ctx, headerUserID)
	if err != nil {
		return nil, err
	}
	if u == nil {
		return nil, fmt.Errorf("unauthorized")
	}

	sess := &agent.Session{UserID: u.ID}
	persistedSession := false
	if req.SessionID != "" {
		cs, err := s.Repos.Sessions.Get(ctx, req.SessionID)
		if err != nil {
			return nil, fmt.Errorf("session not found")
		}
		sess.ID = cs.ID
		persistedSession = true
		sess.Pending = pendingFromState(cs.State)
		// 上下文条数上限由运行时设置决定（max_context 越小，回看的轮次越少）。
		limit := s.historyLimit()
		if msgs, err := s.Repos.Sessions.RecentMessages(ctx, cs.ID, limit); err == nil {
			for _, m := range msgs {
				sess.Append(llm.Message{Role: llm.Role(m.Role), Content: m.Content})
			}
		}
	} else if req.Internal {
		// 后台总结没有可写入的历史会话：用一次性会话跑本轮，不落库也不创建历史。
		sess.ID = "internal-" + shortID(u.ID)
	} else {
		cs := &store.ChatSession{OwnerUserID: u.ID}
		if err := s.Repos.Sessions.Create(ctx, cs); err != nil {
			return nil, err
		}
		sess.ID = cs.ID
		persistedSession = true
	}

	rid := reqID(ctx)
	log.Printf("[chat] %s user=%s(%s) session=%s text=%q", rid, u.Name, shortID(u.ID), shortID(sess.ID), truncate(req.Text, 80))

	// 附件不交给多模态协议，而是把本地路径附在提示词末尾；agent 可用文件/命令工具处理。
	agentText := req.Text
	if len(req.Attachments) > 0 && s.UploadDir != "" {
		lines := make([]string, 0, len(req.Attachments))
		for _, attachment := range req.Attachments {
			if !strings.HasPrefix(attachment.URL, "/uploads/") {
				continue
			}
			localPath := filepath.Join(s.UploadDir, filepath.Base(attachment.URL))
			info, err := os.Stat(localPath)
			if err != nil || info.IsDir() {
				continue
			}
			kind := "文件"
			if attachment.Kind == "image" {
				kind = "图片"
			} else if attachment.Kind == "video" {
				kind = "视频"
			}
			name := strings.TrimSpace(attachment.Name)
			if name == "" {
				name = filepath.Base(localPath)
			}
			lines = append(lines, fmt.Sprintf("- %s：%s（本地文件：%s）", kind, name, localPath))
		}
		if len(lines) > 0 {
			agentText += "\n\n[用户附带了本地附件]\n" + strings.Join(lines, "\n")
		}
	}

	if !req.Internal {
		userMsg := &store.ChatMessage{SessionID: sess.ID, Role: "user", Content: req.Text}
		if err := s.Repos.Sessions.AppendMessage(ctx, userMsg); err != nil {
			return nil, err
		}
		if s.Runtime != nil && s.Runtime.Tools != nil && s.Runtime.Tools.Memory != nil {
			s.Runtime.Tools.Memory.MirrorChat(ctx, u.ID, userMsg.ID, req.Text, userMsg.CreatedAt)
		}
	}

	rt := s.effectiveRuntime()
	// 只在本轮副本上挂回调，避免并发请求互相覆盖。
	cp := *rt
	cp.OnEvent = onEvent
	result, err := agent.Run(ctx, sess, agentText, &cp)
	if err != nil {
		return nil, err
	}

	log.Printf("[chat] %s done: reply=%q cards=%d pending=%v trace=%d", rid, truncate(result.Reply, 80), len(result.Cards), result.Pending != nil, len(result.Trace))
	if persistedSession {
		assistantMsg := &store.ChatMessage{SessionID: sess.ID, Role: "assistant", Content: result.Reply}
		_ = s.Repos.Sessions.AppendMessage(ctx, assistantMsg)
		state, _ := json.Marshal(map[string]any{"pending_action": sess.Pending})
		_ = s.Repos.Sessions.SaveState(ctx, sess.ID, state)
	}

	responseSessionID := ""
	if persistedSession {
		responseSessionID = sess.ID
	}
	return &chatResponse{
		SessionID: responseSessionID,
		Reply:     result.Reply,
		Pending:   result.Pending,
		Cards:     result.Cards,
		Trace:     result.Trace,
	}, nil
}

// historyLimit 把 max_context（token）折算成回看的消息条数；
// 未配置时沿用原来的 20 条。
func (s *Server) historyLimit() int {
	ls := s.currentLLM()
	if ls.MaxContext <= 0 {
		return 20
	}
	// 以每条约 120 token 粗估，限制在 6..60 条之间。
	n := ls.MaxContext / 120
	if n < 6 {
		n = 6
	}
	if n > 60 {
		n = 60
	}
	return n
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

type sessionDTO struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Messages  int       `json:"messages"`
	Preview   string    `json:"preview"`
}

// handleListSessions 返回当前用户的会话列表；date=YYYY-MM-DD 时只返回该天创建的会话。
// 会话名由创建时间生成，这里只暴露时间与首条用户消息预览。
func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	limit := 200
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 500 {
		limit = 500
	}
	date := strings.TrimSpace(r.URL.Query().Get("date"))
	var day string
	if date != "" {
		t, err := time.ParseInLocation("2006-01-02", date, time.Local)
		if err != nil {
			writeError(w, 400, "date must be YYYY-MM-DD")
			return
		}
		day = t.Format("2006-01-02")
	}
	items, err := s.Repos.Sessions.ListByOwner(r.Context(), u.ID, 500)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	out := make([]sessionDTO, 0, len(items))
	for _, item := range items {
		if item.Messages == 0 {
			continue
		}
		if day != "" && item.CreatedAt.In(time.Local).Format("2006-01-02") != day {
			continue
		}
		// 只有自动总结本身（一问一答）不算对话；用户在该会话里继续聊过就保留。
		if item.Messages <= 2 && strings.HasPrefix(item.Preview, "以下是用户授权协作模式采集到的页面") {
			continue
		}
		out = append(out, sessionDTO{
			ID:        item.ID,
			CreatedAt: item.CreatedAt,
			UpdatedAt: item.UpdatedAt,
			Messages:  item.Messages,
			Preview:   sessionPreview(item.Preview),
		})
		if len(out) >= limit {
			break
		}
	}
	writeJSON(w, 200, map[string]any{"sessions": out})
}

func sessionPreview(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) > 80 {
		return string(runes[:80]) + "…"
	}
	return text
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
		// 旧版本协作总结把内部提示词写成了用户消息；恢复历史时不再展示。
		if m.Role == "user" && strings.HasPrefix(m.Content, "以下是用户授权协作模式采集到的页面") {
			continue
		}
		out = append(out, messageDTO{Role: m.Role, Content: m.Content, At: m.CreatedAt.Format("15:04")})
	}
	writeJSON(w, 200, out)
}
