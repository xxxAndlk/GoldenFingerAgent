package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// 统一会话投影的种类。
//
// 旧 /api/sessions 只暴露用户可见的普通会话，按约定把 worker/router 会话过滤掉
// （AGENT.md 的兼容约束）。新 /api/conversations 需要同时展示主对话、Worker
// 会话与委派执行记录，因此单独立一套投影，不改动旧接口语义。
const (
	ConversationMain      = "main"
	ConversationWorker    = "worker"
	ConversationDelegated = "delegated"
)

// ConversationRow 是统一会话投影的一行。
//
// SessionID 为空表示该条目没有可读消息的会话（例如没有绑定 Worker 会话的
// 委派执行记录）；此时 CanMessage 必须为 false，前端不得伪造聊天界面。
type ConversationRow struct {
	ID           string    `json:"id"`
	Kind         string    `json:"kind"`
	Title        string    `json:"title"`
	Preview      string    `json:"preview"`
	MessageCount int       `json:"message_count"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	SessionID    string    `json:"session_id"`
	TaskID       string    `json:"task_id"`
	JobID        string    `json:"job_id"`
	AgentID      string    `json:"agent_id"`
	Status       string    `json:"status"`
	WaitingKind  string    `json:"waiting_kind"`
	CanMessage   bool      `json:"can_message"`
}

// ConversationRepo 负责统一会话投影的只读查询。
type ConversationRepo struct{ q Querier }

func NewConversationRepo(q Querier) *ConversationRepo { return &ConversationRepo{q: q} }

// List 返回该用户的会话目录。kind 为空或 "all" 时返回全部类别；
// "main"/"worker"/"delegated" 只返回对应类别。router 等内部会话始终排除。
//
// Worker 会话即使一条消息都没有也照样出现：任务是真实存在的，
// 不能因为 messages=0 就从目录里消失。
func (r *ConversationRepo) List(ctx context.Context, ownerUserID, kind string, limit int) ([]ConversationRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	out := make([]ConversationRow, 0, limit)
	if kind == "" || kind == "all" || kind == ConversationMain || kind == ConversationWorker {
		sessions, err := r.listSessions(ctx, ownerUserID, kind, limit)
		if err != nil {
			return nil, err
		}
		out = append(out, sessions...)
	}
	if kind == "" || kind == "all" || kind == ConversationDelegated {
		jobs, err := r.listDelegated(ctx, ownerUserID, limit)
		if err != nil {
			return nil, err
		}
		out = append(out, jobs...)
	}
	sortConversations(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// listSessions 返回主对话与 Worker 会话。chat_session.kind='chat' 是
// GetOrCreateMain 升级前的老会话，同样归入「主对话」类别。
func (r *ConversationRepo) listSessions(ctx context.Context, ownerUserID, kind string, limit int) ([]ConversationRow, error) {
	query := `
		SELECT s.id, s.kind, s.created_at, s.updated_at,
			(SELECT MAX(m.created_at) FROM chat_message m WHERE m.session_id = s.id) AS last_message_at,
			(SELECT COUNT(*) FROM chat_message m WHERE m.session_id = s.id) AS messages,
			COALESCE((SELECT m.content FROM chat_message m
				WHERE m.session_id = s.id AND m.role = 'user'
				ORDER BY m.created_at ASC LIMIT 1), '') AS first_user,
			COALESCE((SELECT m.content FROM chat_message m
				WHERE m.session_id = s.id AND m.role = 'assistant'
				ORDER BY m.created_at DESC LIMIT 1), '') AS last_reply,
			COALESCE(t.id, ''), COALESCE(t.title, ''), COALESCE(t.status, ''),
			COALESCE(t.current_job_id, ''), COALESCE(ls.waiting_kind, ''), COALESCE(ls.stage, '')
		FROM chat_session s
		LEFT JOIN work_task t ON t.id = s.work_task_id
		LEFT JOIN work_task_live_state ls ON ls.task_id = s.work_task_id
		WHERE s.owner_user_id = ? AND s.kind IN ('main', 'chat', 'worker')`
	args := []any{ownerUserID}
	switch kind {
	case ConversationMain:
		query += ` AND s.kind IN ('main', 'chat')`
	case ConversationWorker:
		query += ` AND s.kind = 'worker'`
	}
	query += ` ORDER BY s.updated_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := r.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]ConversationRow, 0, limit)
	for rows.Next() {
		var (
			item         ConversationRow
			sessionID    string
			kindRaw      string
			sessionUpdAt time.Time
			lastMessage  sql.NullString
			firstUser    string
			lastReply    string
			taskID       string
			taskTitle    string
			taskState    string
			jobID        string
			waiting      string
			stage        string
		)
		if err := rows.Scan(&sessionID, &kindRaw, &item.CreatedAt, &sessionUpdAt, &lastMessage,
			&item.MessageCount, &firstUser, &lastReply,
			&taskID, &taskTitle, &taskState, &jobID, &waiting, &stage); err != nil {
			return nil, err
		}
		item.ID = sessionID
		item.SessionID = sessionID
		item.Kind = ConversationMain
		if kindRaw == "worker" {
			item.Kind = ConversationWorker
		}
		// 会话行本身不一定随消息更新，排序与已读游标都要以最后一条消息为准。
		item.UpdatedAt = sessionUpdAt
		if lastMessage.Valid {
			if at, perr := time.Parse(time.RFC3339Nano, lastMessage.String); perr == nil && at.After(sessionUpdAt) {
				item.UpdatedAt = at
			}
		}
		item.TaskID = taskID
		item.JobID = jobID
		item.WaitingKind = normalizeWaiting(waiting)
		item.Status = taskState
		if item.Status == "" {
			item.Status = WorkTaskActive
		}
		// 终态任务不能再追加指令：要么先重新打开，要么就到此为止。
		// 前端据此禁用输入框，后端也会按状态拒绝。
		item.CanMessage = item.Status != WorkTaskCompleted && item.Status != WorkTaskCancelled
		item.Title = taskTitle
		if item.Title == "" {
			item.Title = conversationTitle(firstUser, stage)
		}
		item.Preview = conversationPreview(firstUser, lastReply)
		out = append(out, item)
	}
	return out, rows.Err()
}

// listDelegated 返回没有绑定 WorkTask 的委派 Agent 执行记录。
// 这类执行通常没有聊天会话，只展示 Job 输出与状态，不伪造对话消息。
func (r *ConversationRepo) listDelegated(ctx context.Context, ownerUserID string, limit int) ([]ConversationRow, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT j.id, j.agent_id, j.task, j.status, j.waiting_kind, j.output, j.error,
			j.session_id, j.work_task_id, j.created_at, j.updated_at
		FROM agent_job j
		WHERE j.owner_user_id = ? AND j.work_task_id = ''
		ORDER BY j.created_at DESC LIMIT ?`, ownerUserID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]ConversationRow, 0, limit)
	for rows.Next() {
		var (
			item    ConversationRow
			agentID string
			task    string
			waiting string
			output  string
			failure string
		)
		if err := rows.Scan(&item.ID, &agentID, &task, &item.Status, &waiting, &output, &failure,
			&item.SessionID, &item.TaskID, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		item.Kind = ConversationDelegated
		item.AgentID = agentID
		item.WaitingKind = normalizeWaiting(waiting)
		item.Title = conversationTitle(task, "")
		item.Preview = conversationPreview(task, output)
		if item.Preview == "" {
			item.Preview = failure
		}
		// 委派执行记录没有可写的 Worker 会话时，禁止前端显示输入框。
		item.CanMessage = item.SessionID != ""
		out = append(out, item)
	}
	return out, rows.Err()
}

// SessionIDFor 按会话/执行记录 ID 找到可读消息的会话 ID。
//
// 之前用「取前 500 条再反查」的写法，会话多了以后合法 ID 会被截断成 404。
// 这里按 ID + owner 直接查：先当作会话 ID，再当作委派执行记录。
func (r *ConversationRepo) SessionIDFor(ctx context.Context, ownerUserID, conversationID string) (string, bool, error) {
	if conversationID == "" {
		return "", false, nil
	}
	var sessionID string
	err := r.q.QueryRowContext(ctx, `
		SELECT id FROM chat_session
		WHERE id = ? AND owner_user_id = ? AND kind IN ('main', 'chat', 'worker')`,
		conversationID, ownerUserID).Scan(&sessionID)
	if err == nil {
		return sessionID, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", false, err
	}
	var jobSession sql.NullString
	err = r.q.QueryRowContext(ctx, `
		SELECT session_id FROM agent_job
		WHERE id = ? AND owner_user_id = ? AND work_task_id = ''`,
		conversationID, ownerUserID).Scan(&jobSession)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if !jobSession.Valid {
		return "", false, nil
	}
	return jobSession.String, true, nil
}

// LastRowid 返回某个会话最后一条消息的 rowid；没有消息时返回 0。
func (r *ConversationRepo) LastRowid(ctx context.Context, sessionID string) (int64, error) {
	if sessionID == "" {
		return 0, nil
	}
	var rowid sql.NullInt64
	if err := r.q.QueryRowContext(ctx,
		`SELECT MAX(rowid) FROM chat_message WHERE session_id = ?`, sessionID).Scan(&rowid); err != nil {
		return 0, err
	}
	if !rowid.Valid {
		return 0, nil
	}
	return rowid.Int64, nil
}

// WorkerSessionID 找出绑定到某个 WorkTask 的 Worker 会话。
// 没有 Worker 会话时返回空字符串：不能凭空造一个可对话的入口。
func (r *ConversationRepo) WorkerSessionID(ctx context.Context, ownerUserID, taskID string) (string, error) {
	if taskID == "" {
		return "", nil
	}
	var sessionID string
	err := r.q.QueryRowContext(ctx, `
		SELECT id FROM chat_session
		WHERE owner_user_id = ? AND work_task_id = ? AND kind = 'worker'
		ORDER BY updated_at DESC LIMIT 1`, ownerUserID, taskID).Scan(&sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return sessionID, nil
}

// UnreadCounts 统计各会话在设备游标之后新增的消息条数，返回 session_id → 条数。
//
// 没有游标的会话不计入：未读角标由「待你处理」的任务状态驱动，
// 首次使用不该满屏红点。委派执行记录没有会话消息，也不计入。
func (r *ConversationRepo) UnreadCounts(ctx context.Context, rows []ConversationRow, cursors map[string]int64) (map[string]int, error) {
	out := map[string]int{}
	args := make([]any, 0, len(rows)*2)
	marked := 0
	for _, row := range rows {
		cursor, ok := cursors[row.ID]
		if !ok || row.SessionID == "" {
			continue
		}
		args = append(args, row.SessionID, cursor)
		marked++
	}
	if marked == 0 {
		return out, nil
	}
	query := `SELECT session_id, COUNT(*) FROM chat_message WHERE (`
	for i := 0; i < marked; i++ {
		if i > 0 {
			query += ` OR `
		}
		query += `(session_id = ? AND rowid > ?)`
	}
	query += `) GROUP BY session_id`
	result, err := r.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer result.Close()
	for result.Next() {
		var sessionID string
		var count int
		if err := result.Scan(&sessionID, &count); err != nil {
			return nil, err
		}
		out[sessionID] = count
	}
	return out, result.Err()
}

// LastRowids 返回各会话最后一条消息的 rowid；没有消息的会话返回 0。
// 客户端标记已读时用它作为游标。
func (r *ConversationRepo) LastRowids(ctx context.Context, rows []ConversationRow) (map[string]int64, error) {
	out := map[string]int64{}
	args := make([]any, 0, len(rows))
	for _, row := range rows {
		if row.SessionID == "" {
			continue
		}
		args = append(args, row.SessionID)
	}
	if len(args) == 0 {
		return out, nil
	}
	query := `SELECT session_id, MAX(rowid) FROM chat_message WHERE session_id IN (?`
	for i := 1; i < len(args); i++ {
		query += `, ?`
	}
	query += `) GROUP BY session_id`
	result, err := r.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer result.Close()
	for result.Next() {
		var sessionID string
		var rowid int64
		if err := result.Scan(&sessionID, &rowid); err != nil {
			return nil, err
		}
		out[sessionID] = rowid
	}
	return out, result.Err()
}

// normalizeWaiting 把空的等待原因归一成 none，避免前端到处判空。
func normalizeWaiting(kind string) string {
	if kind == "" {
		return WaitingNone
	}
	return kind
}

// conversationTitle 用首条用户消息或任务阶段生成一句可读标题。
func conversationTitle(text, stage string) string {
	if stage != "" {
		return stage
	}
	text = collapseSpace(text)
	if text == "" {
		return "未命名对话"
	}
	runes := []rune(text)
	if len(runes) > 40 {
		return string(runes[:40]) + "…"
	}
	return text
}

// conversationPreview 取最近一条有效内容作为预览。
func conversationPreview(firstUser, lastReply string) string {
	text := collapseSpace(lastReply)
	if text == "" {
		text = collapseSpace(firstUser)
	}
	runes := []rune(text)
	if len(runes) > 120 {
		return string(runes[:120]) + "…"
	}
	return text
}

func collapseSpace(text string) string {
	out := make([]rune, 0, len(text))
	space := false
	for _, r := range text {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			space = true
			continue
		}
		if space && len(out) > 0 {
			out = append(out, ' ')
		}
		space = false
		out = append(out, r)
	}
	return string(out)
}

// sortConversations 按更新时间倒序；时间相同时用 ID 保证稳定。
func sortConversations(items []ConversationRow) {
	for i := 1; i < len(items); i++ {
		for j := i; j > 0; j-- {
			if !conversationBefore(items[j], items[j-1]) {
				break
			}
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
}

func conversationBefore(a, b ConversationRow) bool {
	if a.UpdatedAt.Equal(b.UpdatedAt) {
		return a.ID < b.ID
	}
	return a.UpdatedAt.After(b.UpdatedAt)
}
