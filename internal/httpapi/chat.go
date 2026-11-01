package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"goldenfinger/agent/internal/agent"
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
	// Context 是桌面端附带的本地工作轨迹数据；只进入本轮模型请求，不写入会话原文。
	Context string `json:"context,omitempty"`
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
// 具体执行抽到 agent.TurnService，HTTP 与 Worker 共用同一实现。
func (s *Server) runChatTurn(ctx context.Context, headerUserID string, req chatRequest, onEvent func(agent.Event)) (*chatResponse, error) {
	u, err := s.resolveUser(ctx, headerUserID)
	if err != nil {
		return nil, err
	}
	if u == nil {
		return nil, fmt.Errorf("unauthorized")
	}
	attachments := make([]agent.Attachment, 0, len(req.Attachments))
	for _, item := range req.Attachments {
		attachments = append(attachments, agent.Attachment{Name: item.Name, Kind: item.Kind, URL: item.URL})
	}
	svc := &agent.TurnService{
		Repos:        s.Repos,
		RuntimeFor:   s.effectiveRuntime,
		HistoryLimit: s.historyLimit,
		UploadDir:    s.UploadDir,
	}
	out, err := svc.Run(ctx, agent.TurnRequest{
		UserID:      u.ID,
		SessionID:   req.SessionID,
		Text:        req.Text,
		Attachments: attachments,
		Context:     req.Context,
		Internal:    req.Internal,
		OnEvent:     onEvent,
	})
	if err != nil {
		return nil, err
	}
	return &chatResponse{
		SessionID: out.SessionID,
		Reply:     out.Reply,
		Pending:   out.Pending,
		Cards:     out.Cards,
		Trace:     out.Trace,
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

type messageDTO struct {
	ID        string `json:"id"`
	Role      string `json:"role"`
	Content   string `json:"content"`
	At        string `json:"at"`
	CreatedAt string `json:"created_at"`
}

type sessionDTO struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Messages  int       `json:"messages"`
	Preview   string    `json:"preview"`
}

// handleMainSession 返回唯一主会话；客户端不需要选择或创建会话。
func (s *Server) handleMainSession(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	session, err := s.Repos.Sessions.GetOrCreateMain(r.Context(), u.ID)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"session": session})
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
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 200 {
		limit = 200
	}
	msgs, err := s.Repos.Sessions.MessagesBefore(r.Context(), id, strings.TrimSpace(r.URL.Query().Get("before")), limit)
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
		out = append(out, messageDTO{
			ID: m.ID, Role: m.Role, Content: m.Content,
			At: m.CreatedAt.Format("15:04"), CreatedAt: m.CreatedAt.Format(time.RFC3339Nano),
		})
	}
	writeJSON(w, 200, out)
}
