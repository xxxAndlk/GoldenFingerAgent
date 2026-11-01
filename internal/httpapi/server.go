// Package httpapi 是线上层（相当于 pi 的 packages/protocol）：
// JSON DTO + 覆盖 agent 运行时与领域服务的 HTTP 处理器。
package httpapi

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"
	"unicode/utf8"

	"goldenfinger/agent/internal/agent"
	"goldenfinger/agent/internal/agenthub"
	"goldenfinger/agent/internal/device"
	"goldenfinger/agent/internal/inbox"
	"goldenfinger/agent/internal/settings"
	"goldenfinger/agent/internal/skill"
	"goldenfinger/agent/internal/store"
	"goldenfinger/agent/internal/voice"
)

// Server 在运行时之上装配 HTTP 路由（无框架——标准库 mux）。
type Server struct {
	Repos     *store.Repos
	Runtime   *agent.Runtime
	WebDir    string
	UploadDir string
	Now       func() time.Time
	// Settings 保存可运行时编辑的模型设置（JSON 文件）；nil 表示禁用。
	Settings *settings.Store
	// FallbackLLM 描述 config.yaml 的对话模型，在没有运行时覆盖时
	// 显示在设置 UI 中。
	FallbackLLM settings.LLM
	// Outbox 收集 "app" 渠道的提醒投递，供 Web UI 轮询。
	Outbox *Outbox
	// Device 是设备操控通道（WS/屏幕缓存）；nil 时相关路由返回 503。
	Device *device.Hub
	// Inbox 是通知中心服务；nil 时 /api/inbox* 端点返回 503。
	Inbox *inbox.Service
	// Agents 是本机与 A2A Agent 的统一调度层。
	Agents *agenthub.Manager
	// AgentDispatcher 持久化执行异步 Agent 任务。
	AgentDispatcher *agenthub.Dispatcher
	// Relay 是手机→电脑的中继连接；nil 表示未启用（单机模式）。
	Relay RelayControl
	// DeviceGuard 是设备操作安全闸；nil 时急停端点返回 503。
	DeviceGuard *device.Guard
	// PhoneRelay 是手机 → 电脑的中继通道；nil 时手机远程端点返回 503。
	PhoneRelay PhoneRelay
	// Jobs 是持久化异步任务队列；nil 时 /api/jobs* 返回 503。
	Jobs JobService
	// Voice 是语音口令判定状态机；nil 时 /api/voice/text 返回 503。
	Voice *voice.Service
	// Skills 是本机 Markdown 技能池；nil 时 /api/skills 返回空列表。
	Skills *skill.Pool
}

// Outbox 是已投递提醒文本的小型内存队列（开发 MVP）。
type Outbox struct {
	items []OutboxItem
}

type OutboxItem struct {
	UserID string `json:"user_id"`
	Body   string `json:"body"`
	At     string `json:"at"`
}

func (o *Outbox) Push(userID, body string, at time.Time) {
	o.items = append(o.items, OutboxItem{UserID: userID, Body: body, At: at.Format("15:04")})
}

func (o *Outbox) Drain(userID string) []OutboxItem {
	var out []OutboxItem
	var rest []OutboxItem
	for _, it := range o.items {
		if it.UserID == userID {
			out = append(out, it)
		} else {
			rest = append(rest, it)
		}
	}
	o.items = rest
	return out
}

// Handler 构建包含全部路由的 http.Handler。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/chat", s.handleChat)
	mux.HandleFunc("POST /api/uploads", s.handleUpload)
	if s.UploadDir != "" {
		mux.Handle("GET /uploads/", http.StripPrefix("/uploads/", http.FileServer(http.Dir(s.UploadDir))))
	}
	mux.HandleFunc("GET /api/sessions", s.handleListSessions)
	mux.HandleFunc("GET /api/sessions/{id}/messages", s.handleSessionMessages)
	mux.HandleFunc("GET /api/skills", s.handleListSkills)
	mux.HandleFunc("GET /api/persons", s.handleListPersons)
	mux.HandleFunc("GET /api/persons/{id}", s.handleGetPerson)
	mux.HandleFunc("PUT /api/persons/{id}", s.handleUpdatePerson)
	mux.HandleFunc("DELETE /api/persons/{id}", s.handleForgetPerson)
	mux.HandleFunc("DELETE /api/facts/{id}", s.handleForgetFact)
	mux.HandleFunc("GET /api/tasks", s.handleListTasks)
	mux.HandleFunc("POST /api/tasks/{id}/{action}", s.handleTaskAction)
	mux.HandleFunc("GET /api/intents", s.handleListIntents)
	mux.HandleFunc("DELETE /api/intents/{id}", s.handleCancelIntent)
	mux.HandleFunc("GET /api/memory/export.md", s.handleMemoryExport)
	mux.HandleFunc("POST /api/memory/capture", s.handleCaptureEpisode)
	// 语音口令判定：唤醒词/指令窗口/片段合并都在 Go，手机只执行动作。
	mux.HandleFunc("POST /api/voice/text", s.handleVoiceText)
	mux.HandleFunc("GET /api/memory/search", s.handleSearchEpisodes)
	mux.HandleFunc("GET /api/reminders", s.handleReminders)
	mux.HandleFunc("GET /api/outbox", s.handleOutbox)
	mux.HandleFunc("GET /api/me", s.handleMe)
	mux.HandleFunc("POST /api/me/consent", s.handleConsent)
	mux.HandleFunc("GET /api/settings/llm", s.handleGetLLMSettings)
	mux.HandleFunc("GET /api/settings", s.handleGetSettings)
	mux.HandleFunc("PUT /api/settings/llm", s.handlePutLLMSettings)
	mux.HandleFunc("POST /api/settings/models", s.handleSaveModel)
	mux.HandleFunc("POST /api/settings/models/{id}/activate", s.handleActivateModel)
	mux.HandleFunc("DELETE /api/settings/models/{id}", s.handleDeleteModel)
	mux.HandleFunc("PUT /api/settings/search", s.handlePutSearch)
	mux.HandleFunc("PUT /api/settings/relay", s.handlePutRelay)
	mux.HandleFunc("POST /api/chat/stream", s.handleChatStream)
	mux.HandleFunc("POST /api/summarize", s.handleSummarize)
	mux.HandleFunc("GET /api/agents", s.handleListAgents)
	mux.HandleFunc("POST /api/agents/open", s.handleOpenAgent)
	mux.HandleFunc("POST /api/agents/delegate", s.handleDelegateAgent)
	mux.HandleFunc("POST /api/agents/clarify", s.handleClarifyAgent)
	mux.HandleFunc("GET /api/agent-jobs", s.handleListAgentJobs)
	mux.HandleFunc("GET /api/agent-jobs/{id}", s.handleGetAgentJob)
	mux.HandleFunc("POST /api/agent-jobs/{id}/cancel", s.handleCancelAgentJob)
	mux.HandleFunc("GET /api/agent-notifications", s.handleAgentNotifications)
	mux.HandleFunc("POST /api/agent-notifications/ack", s.handleAckAgentNotifications)
	mux.HandleFunc("POST /api/agents/a2a", s.handleRegisterA2A)
	mux.HandleFunc("GET /api/agents/providers", s.handleAgentProviders)
	mux.HandleFunc("POST /api/agents/switch", s.handleSwitchAgentProvider)
	mux.HandleFunc("POST /api/voice/transcribe", s.handleTranscribe)
	mux.HandleFunc("POST /api/voice/speak", s.handleSpeak)
	mux.HandleFunc("GET /ws/device", s.handleDeviceWS)
	mux.HandleFunc("POST /api/screen", s.handleScreen)
	mux.HandleFunc("POST /api/device/emergency", s.handleDeviceEmergency)

	// 手机连接：中继配对与通道管理（仅本机可访问；未启用时 503）。
	mux.HandleFunc("POST /api/relay/pair-code", s.handleRelayPairCode)
	mux.HandleFunc("GET /api/relay/status", s.handleRelayStatus)
	mux.HandleFunc("GET /api/relay/channels", s.handleRelayChannels)
	mux.HandleFunc("DELETE /api/relay/channels/{id}", s.handleRelayRevokeChannel)

	// 异步任务：提交/查询/取消（长任务不占用一次 HTTP 请求）。
	mux.HandleFunc("POST /api/jobs", s.handleJobSubmit)
	mux.HandleFunc("GET /api/jobs", s.handleJobList)
	mux.HandleFunc("GET /api/jobs/{id}", s.handleJobGet)
	mux.HandleFunc("DELETE /api/jobs/{id}", s.handleJobCancel)

	// 手机端：配对电脑、查看状态、发指令并取回结果（未启用时 503）。
	mux.HandleFunc("POST /api/relay/phone/pair", s.handlePhonePair)
	mux.HandleFunc("DELETE /api/relay/phone/pair", s.handlePhoneUnpair)
	mux.HandleFunc("GET /api/relay/phone/status", s.handlePhoneStatus)
	mux.HandleFunc("POST /api/relay/phone/send", s.handlePhoneSend)
	mux.HandleFunc("POST /api/relay/phone/job", s.handlePhoneJobSubmit)
	mux.HandleFunc("GET /api/relay/phone/job/{id}", s.handlePhoneJobStatus)
	mux.HandleFunc("DELETE /api/relay/phone/job/{id}", s.handlePhoneJobCancel)

	// 通知中心（Inbox nil 时端点 503）。
	mux.HandleFunc("POST /api/inbox/events", s.handleInboxIngest)
	mux.HandleFunc("GET /api/inbox/events", s.handleInboxEvents)
	mux.HandleFunc("GET /api/inbox/search", s.handleInboxSearch)
	mux.HandleFunc("POST /api/inbox/snooze", s.handleInboxCreateSnooze)
	mux.HandleFunc("GET /api/inbox/snooze", s.handleInboxListSnoozes)
	mux.HandleFunc("POST /api/inbox/watches", s.handleInboxCreateWatch)
	mux.HandleFunc("GET /api/inbox/watches", s.handleInboxListWatches)
	mux.HandleFunc("DELETE /api/inbox/watches/{id}", s.handleInboxCancelWatch)

	// 静态 Web UI。
	mux.Handle("GET /", http.FileServer(http.Dir(s.WebDir)))

	return s.withLogging(mux)
}

func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id := newReqID()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r.WithContext(context.WithValue(r.Context(), reqIDKey, id)))
		log.Printf("[http] %s %s %s → %d (%s)", id, r.Method, r.URL.Path, sw.status, time.Since(start).Round(time.Millisecond))
	})
}

// statusWriter 为访问日志捕获响应状态码。
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Flush 让 SSE 处理器能通过 http.Flusher 实时推送；否则 /api/chat/stream
// 会因类型断言失败直接返回 500。
func (w *statusWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Unwrap 让 http.ResponseController 能取到底层 ResponseWriter。
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Hijack 支持 WebSocket 升级（把握手交给底层连接）。
func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("http: response does not implement http.Hijacker")
	}
	return hj.Hijack()
}

// reqID 关联同一 HTTP 请求内产生的所有日志行。
type ctxKey string

const reqIDKey ctxKey = "reqid"

func newReqID() string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func reqID(ctx context.Context) string {
	if v, ok := ctx.Value(reqIDKey).(string); ok {
		return v
	}
	return "------"
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// ---- 辅助函数 ----

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// currentUser 从 X-User-Id 请求头解析演示用户，否则回退到第一个
// （自动预置的）用户。MVP 中认证是桩。
func (s *Server) currentUser(w http.ResponseWriter, r *http.Request) *store.User {
	u, err := s.resolveUser(r.Context(), r.Header.Get("X-User-Id"))
	if err != nil {
		writeError(w, 500, err.Error())
		return nil
	}
	return u
}

// resolveUser 是不写响应的用户解析，供流式接口等无法中途改状态码的路径使用。
func (s *Server) resolveUser(ctx context.Context, headerUserID string) (*store.User, error) {
	if headerUserID != "" {
		if u, err := s.Repos.Users.Get(ctx, headerUserID); err == nil {
			return u, nil
		}
	}
	users, err := s.Repos.Users.ListAll(ctx)
	if err == nil && len(users) > 0 {
		return &users[0], nil
	}
	u := &store.User{UserType: store.UserGeneral, Name: "演示用户", TZ: "Asia/Shanghai"}
	if err := s.Repos.Users.Create(ctx, u); err != nil {
		return nil, err
	}
	return u, nil
}
