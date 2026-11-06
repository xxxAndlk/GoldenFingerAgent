package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"goldenfinger/agent/internal/store"
)

// conversationDTO 是统一会话投影对外的形状。
//
// 与旧的 /api/sessions 相比多了任务、执行段、等待原因与未读信息；
// 旧接口保持不变，继续只返回用户可见的普通会话。
type conversationDTO struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	Title        string `json:"title"`
	Preview      string `json:"preview"`
	MessageCount int    `json:"message_count"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
	SessionID    string `json:"session_id"`
	TaskID       string `json:"task_id"`
	JobID        string `json:"job_id"`
	AgentID      string `json:"agent_id"`
	Status       string `json:"status"`
	WaitingKind  string `json:"waiting_kind"`
	UnreadCount  int    `json:"unread_count"`
	CanMessage   bool   `json:"can_message"`
	SourceDevice string `json:"source_device"`
}

// GET /api/conversations?kind=all|main|worker|delegated&limit=
//
// 返回双端共用的会话目录：主对话、Worker 会话与委派执行记录。
// router 等内部会话不出现；Worker 会话即使还没有消息也照样列出。
func (s *Server) handleListConversations(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	kind := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("kind")))
	switch kind {
	case "", "all", store.ConversationMain, store.ConversationWorker, store.ConversationDelegated:
	default:
		writeError(w, http.StatusBadRequest, "kind 必须是 all/main/worker/delegated")
		return
	}
	limit := parseLimit(r.URL.Query().Get("limit"), 100)
	rows, err := s.Repos.Conversations.List(r.Context(), u.ID, kind, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// 未读数按「发起请求的这台设备」统计。
	deviceID := s.requestDeviceID(r)
	cursors, err := s.Repos.ConversationCursors.ReadMap(r.Context(), u.ID, deviceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// 会话事实源是运行这份数据的电脑，不是正在读它的设备。
	sourceDevice := s.DeviceID
	if sourceDevice == "" {
		sourceDevice = "desktop"
	}
	unread, err := s.unreadCounts(r.Context(), rows, cursors)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]conversationDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, conversationDTO{
			ID: row.ID, Kind: row.Kind, Title: row.Title, Preview: row.Preview,
			MessageCount: row.MessageCount,
			CreatedAt:    row.CreatedAt.UTC().Format(time.RFC3339),
			UpdatedAt:    row.UpdatedAt.UTC().Format(time.RFC3339),
			SessionID:    row.SessionID, TaskID: row.TaskID, JobID: row.JobID,
			AgentID: row.AgentID, Status: row.Status, WaitingKind: row.WaitingKind,
			UnreadCount: unread[row.ID], CanMessage: row.CanMessage,
			SourceDevice: sourceDevice,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversations": out})
}

// GET /api/conversations/{id}/messages?before=&limit=
//
// 统一投影里的会话消息。返回 {"messages": [...]} 包装，
// 便于手机端与桌面端共用同一形状（旧的 /api/sessions/{id}/messages 仍返回裸数组）。
func (s *Server) handleConversationMessages(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	id := r.PathValue("id")
	session, err := s.Repos.Sessions.Get(r.Context(), id)
	if err != nil || session.OwnerUserID != u.ID {
		writeError(w, http.StatusNotFound, "会话不存在")
		return
	}
	limit := parseLimit(r.URL.Query().Get("limit"), 50)
	if limit > 200 {
		limit = 200
	}
	msgs, err := s.Repos.Sessions.MessagesBefore(r.Context(), id,
		strings.TrimSpace(r.URL.Query().Get("before")), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]messageDTO, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == "user" && strings.HasPrefix(m.Content, "以下是用户授权协作模式采集到的页面") {
			continue
		}
		out = append(out, messageDTO{
			ID: m.ID, Role: m.Role, Content: m.Content,
			At: m.CreatedAt.Format("15:04"), CreatedAt: m.CreatedAt.Format(time.RFC3339Nano),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": out})
}

// POST /api/conversations/{id}/read
//
// 按设备记录已读游标：手机读过之后电脑角标不清零。
func (s *Server) handleMarkConversationRead(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	id := r.PathValue("id")
	// 按 ID + owner 直接查，不依赖前 500 条的列表：
	// 会话多了之后列表截断会把合法会话误报成 404。
	sessionID, found, err := s.Repos.Conversations.SessionIDFor(r.Context(), u.ID, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "会话不存在")
		return
	}
	rowid, err := s.Repos.Conversations.LastRowid(r.Context(), sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	deviceID := s.requestDeviceID(r)
	if err := s.Repos.ConversationCursors.MarkRead(r.Context(), u.ID, deviceID, id, rowid); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"read": true, "conversation_id": id, "device_id": deviceID, "read_rowid": rowid,
	})
}

// unreadCounts 统计每个会话在设备游标之后新增的消息条数。
// 没有游标的会话按 0 计算：未读角标由「待你处理」的任务状态驱动，
// 不让首次使用就满屏红点。
func (s *Server) unreadCounts(ctx context.Context, rows []store.ConversationRow, cursors map[string]int64) (map[string]int, error) {
	out := map[string]int{}
	bySession, err := s.Repos.Conversations.UnreadCounts(ctx, rows, cursors)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.ID] = bySession[row.SessionID]
	}
	return out, nil
}

// requestDeviceID 取出请求方的设备标识。
// 请求头没有时退回本机设备标识（本机运行的界面），再退回 "default"。
func (s *Server) requestDeviceID(r *http.Request) string {
	id := strings.TrimSpace(r.Header.Get("X-Device-Id"))
	if id == "" {
		id = strings.TrimSpace(r.URL.Query().Get("device_id"))
	}
	if id == "" {
		id = strings.TrimSpace(s.DeviceID)
	}
	if id == "" {
		return "default"
	}
	return id
}
