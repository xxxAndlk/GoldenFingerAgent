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
	// TaskID/Action/Task/Conversation 是 Coordinator 命中任务指令时的结构化结果。
	// 普通聊天不填这些字段，响应形状保持不变。
	TaskID       string               `json:"task_id,omitempty"`
	Action       string               `json:"action,omitempty"`
	Task         *chatTaskRef         `json:"task,omitempty"`
	Conversation *chatConversationRef `json:"conversation,omitempty"`
}

// chatTaskRef 是聊天里可点击的任务卡引用。
type chatTaskRef struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

// chatConversationRef 指向可以继续对话的会话（Worker 会话优先）。
//
// id 与 session_id 都给出：id 是会话目录里的条目 ID，session_id 是
// 读消息用的会话 ID。前端两端都按 session_id 跳转，缺失时任务卡就点不动。
type chatConversationRef struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	Kind      string `json:"kind"`
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
	if !req.Internal && s.Coordinator != nil {
		result, handled, err := s.Coordinator.Handle(ctx, u.ID, req.Text)
		if err != nil {
			return nil, err
		}
		if handled {
			sessionID := req.SessionID
			var session *store.ChatSession
			if sessionID != "" {
				loaded, getErr := s.Repos.Sessions.Get(ctx, sessionID)
				if getErr != nil || loaded.OwnerUserID != u.ID {
					return nil, fmt.Errorf("session not found")
				}
				session = loaded
			} else {
				main, getErr := s.Repos.Sessions.GetOrCreateMain(ctx, u.ID)
				if getErr != nil {
					return nil, getErr
				}
				session = main
			}
			if session != nil {
				_ = s.Repos.Sessions.AppendMessage(ctx, &store.ChatMessage{
					SessionID: session.ID, Role: "user", Content: req.Text,
				})
				_ = s.Repos.Sessions.AppendMessage(ctx, &store.ChatMessage{
					SessionID: session.ID, Role: "assistant", Content: result.Reply,
				})
				sessionID = session.ID
			}
			return &chatResponse{
				SessionID:    sessionID,
				Reply:        result.Reply,
				TaskID:       result.TaskID,
				Action:       result.Action,
				Task:         s.taskRef(ctx, u.ID, result.TaskID),
				Conversation: s.taskConversationRef(ctx, u.ID, result.TaskID),
			}, nil
		}
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

// taskRef 取出任务卡需要的最小信息；拿不到就返回 nil，前端不显示卡片。
func (s *Server) taskRef(ctx context.Context, ownerID, taskID string) *chatTaskRef {
	if taskID == "" || s.Coordinator == nil || s.Coordinator.Tasks == nil {
		return nil
	}
	detail, err := s.Coordinator.Tasks.Get(ctx, ownerID, taskID)
	if err != nil || detail == nil || detail.Task == nil {
		return nil
	}
	return &chatTaskRef{ID: detail.Task.ID, Title: detail.Task.Title, Status: detail.Task.Status}
}

// taskConversationRef 找出该任务的 Worker 会话，让聊天里的任务卡能跳进对话。
// 没有 Worker 会话时返回 nil：不能伪造一个可对话的入口。
func (s *Server) taskConversationRef(ctx context.Context, ownerID, taskID string) *chatConversationRef {
	if taskID == "" || s.Coordinator == nil || s.Coordinator.Tasks == nil {
		return nil
	}
	detail, err := s.Coordinator.Tasks.Get(ctx, ownerID, taskID)
	if err != nil || detail == nil || detail.Task == nil {
		return nil
	}
	if detail.Task.CurrentSessionID != "" {
		return &chatConversationRef{
			ID: detail.Task.CurrentSessionID, SessionID: detail.Task.CurrentSessionID,
			Kind: store.ConversationWorker,
		}
	}
	sessionID, err := s.Repos.Conversations.WorkerSessionID(ctx, ownerID, taskID)
	if err != nil || sessionID == "" {
		return nil
	}
	return &chatConversationRef{ID: sessionID, SessionID: sessionID, Kind: store.ConversationWorker}
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
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	id := r.PathValue("id")
	// 会话归属必须先校验：光有会话 ID 不足以读到消息。
	// 跨 owner 与不存在都返回 404，不泄露会话是否存在。
	session, err := s.Repos.Sessions.Get(r.Context(), id)
	if err != nil || session.OwnerUserID != u.ID {
		writeError(w, 404, "会话不存在")
		return
	}
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
