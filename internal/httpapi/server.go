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
	"sync"
	"time"
	"unicode/utf8"

	"goldenfinger/agent/internal/agent"
	"goldenfinger/agent/internal/agenthub"
	"goldenfinger/agent/internal/collection"
	"goldenfinger/agent/internal/deliveryclient"
	"goldenfinger/agent/internal/device"
	"goldenfinger/agent/internal/inbox"
	"goldenfinger/agent/internal/memory"
	"goldenfinger/agent/internal/settings"
	"goldenfinger/agent/internal/skill"
	"goldenfinger/agent/internal/store"
	"goldenfinger/agent/internal/superagent"
	"goldenfinger/agent/internal/voice"
	"goldenfinger/agent/internal/worktask"
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
	// RemoteMemory 在云端共享模式下非空，用于连接测试与同步状态展示。
	RemoteMemory *memory.Remote
	// Memory 是记忆业务层；nil 时涉及共享长期记忆的写入会退化为只写本机。
	Memory *memory.Service
	// Delivery 是云端文件交付客户端；nil 时 /api/delivery* 返回 503。
	Delivery *deliveryclient.Client
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
	// Remote 是手机远程控制电脑的控制面；nil 时 /api/remote/* 返回 503。
	Remote RemoteControl
	// Jobs 是持久化异步任务队列；nil 时 /api/jobs* 返回 503。
	Jobs JobService
	// Voice 是语音口令判定状态机；nil 时 /api/voice/text 返回 503。
	Voice *voice.Service
	// Skills 是本机 Markdown 技能池；nil 时 /api/skills 返回空列表。
	Skills *skill.Pool
	// SuperAgent 是 Trigger/Thread/EventLog 事件层；nil 时相关端点返回 503。
	SuperAgent *superagent.Service
	// SuperAgentWorkers 执行 Thread Worker；nil 时 create_worker 会降级为通知。
	SuperAgentWorkers *superagent.WorkerManager
	// Coordinator 负责明确的 WorkTask 指令分流；普通闲聊仍走主会话快速路径。
	Coordinator *superagent.Coordinator
	// Briefs 生成跨渠道每日简报；nil 时生成端点返回 503。
	Briefs *superagent.BriefGenerator
	// Tasks 是跨执行段的长期工作控制面；nil 时同前缀端点返回 503。
	Tasks *worktask.Service
	// Artifacts 管理不可变交付物版本与验收；nil 时相关端点返回 503。
	Artifacts *worktask.ArtifactService
	// Collection 协调聊天配置工具与桌面真实采集开关。
	Collection *collection.Manager
	// DeviceID 是本机设备标识；会话已读游标按设备分别记录时用它兜底。
	DeviceID string
}

// Outbox 是已投递提醒文本的小型内存队列（开发 MVP）。
// 调度器、Inbox 与 Router 后台循环会并发写入，因此需要互斥。
type Outbox struct {
	mu    sync.Mutex
	items []OutboxItem
}

type OutboxItem struct {
	UserID string `json:"user_id"`
	Body   string `json:"body"`
	At     string `json:"at"`
	// Route 告诉手机点通知时该打开哪个面板：brief / tasks / inbox。
	// 没有明确去处时留空，手机只打开应用、不猜页面。
	Route string `json:"route,omitempty"`
}

// 通知的去处。手机端按这些值决定点通知后打开的面板。
const (
	OutboxRouteBrief = "brief"
	OutboxRouteTasks = "tasks"
	OutboxRouteInbox = "inbox"
)

func (o *Outbox) Push(userID, body string, at time.Time) {
	o.PushRoute(userID, body, "", at)
}

// PushRoute 在提醒正文之外带上「点了该去哪」，避免所有通知都只打开首页。
func (o *Outbox) PushRoute(userID, body, route string, at time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.items = append(o.items, OutboxItem{UserID: userID, Body: body, At: at.Format("15:04"), Route: route})
}

func (o *Outbox) Drain(userID string) []OutboxItem {
	o.mu.Lock()
	defer o.mu.Unlock()
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
	mux.HandleFunc("GET /api/sessions/main", s.handleMainSession)
	mux.HandleFunc("GET /api/sessions/{id}/messages", s.handleSessionMessages)
	mux.HandleFunc("GET /api/conversations", s.handleListConversations)
	mux.HandleFunc("GET /api/conversations/{id}/messages", s.handleConversationMessages)
	mux.HandleFunc("POST /api/conversations/{id}/read", s.handleMarkConversationRead)
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
	mux.HandleFunc("POST /api/memory/shared", s.handleSaveSharedMemory)
	mux.HandleFunc("GET /api/memory/longterm", s.handleListLongTermEdges)
	mux.HandleFunc("POST /api/memory/longterm/{id}/confirm", s.handleConfirmLongTermEdge)
	mux.HandleFunc("POST /api/memory/longterm/{id}/conflict", s.handleConflictLongTermEdge)
	mux.HandleFunc("GET /api/personal-policies", s.handleListPersonalPolicies)
	mux.HandleFunc("POST /api/personal-policies", s.handleCreatePersonalPolicy)
	mux.HandleFunc("POST /api/personal-policies/{id}/status", s.handlePersonalPolicyStatus)
	mux.HandleFunc("GET /api/devices", s.handleListDevices)
	mux.HandleFunc("POST /api/devices", s.handleUpsertDevice)
	mux.HandleFunc("POST /api/devices/{id}/revoke", s.handleRevokeDevice)
	mux.HandleFunc("GET /api/devices/{id}/capabilities", s.handleListDeviceCapabilities)
	mux.HandleFunc("POST /api/devices/{id}/capabilities", s.handleAddDeviceCapability)
	// 语音口令判定：唤醒词/指令窗口/片段合并都在 Go，手机只执行动作。
	mux.HandleFunc("POST /api/voice/text", s.handleVoiceText)
	mux.HandleFunc("GET /api/memory/search", s.handleSearchEpisodes)
	mux.HandleFunc("GET /api/reminders", s.handleReminders)
	mux.HandleFunc("GET /api/outbox", s.handleOutbox)
	mux.HandleFunc("GET /api/me", s.handleMe)
	mux.HandleFunc("POST /api/me/consent", s.handleConsent)
	mux.HandleFunc("GET /api/settings/llm", s.handleGetLLMSettings)
	mux.HandleFunc("GET /api/settings/channels", s.handleChannelSettings)
	mux.HandleFunc("PUT /api/settings/channels", s.handleUpdateChannelSettings)
	mux.HandleFunc("PUT /api/settings/channels/config/{channel}", s.handleUpdateChannelConfig)
	mux.HandleFunc("PUT /api/settings/channels/global", s.handleUpdateGlobalConfig)
	mux.HandleFunc("GET /api/settings/email", s.handleGetEmailSettings)
	mux.HandleFunc("PUT /api/settings/email", s.handlePutEmailSettings)
	mux.HandleFunc("POST /api/channels/activity/episodes", s.handleActivityEpisode)
	mux.HandleFunc("POST /api/channels/email/messages", s.handleEmailMessage)
	mux.HandleFunc("POST /api/channels/wechat/messages", s.handleWeChatMessage)
	mux.HandleFunc("POST /api/channels/connector/status", s.handleConnectorStatus)
	mux.HandleFunc("GET /api/channels/connector/status", s.handleGetConnectorStatus)
	mux.HandleFunc("GET /api/channels/connector/requests", s.handleConnectorRequests)
	mux.HandleFunc("POST /api/channels/connector/requests/{id}/complete", s.handleCompleteConnectorRequest)
	mux.HandleFunc("GET /api/collection/channels", s.handleListCollectionChannels)
	mux.HandleFunc("POST /api/collection/channels", s.handleConfigureCollectionChannel)
	mux.HandleFunc("POST /api/channels/calendar/events", s.handleCalendarEvent)
	mux.HandleFunc("POST /api/channels/web/items", s.handleWebItem)
	mux.HandleFunc("GET /api/settings/web/sites", s.handleWebSites)
	mux.HandleFunc("PUT /api/settings/web/sites/{id}", s.handlePutWebSite)
	mux.HandleFunc("DELETE /api/settings/web/sites/{id}", s.handleDeleteWebSite)
	mux.HandleFunc("GET /api/settings", s.handleGetSettings)
	mux.HandleFunc("PUT /api/settings/llm", s.handlePutLLMSettings)
	mux.HandleFunc("POST /api/settings/models", s.handleSaveModel)
	mux.HandleFunc("POST /api/settings/models/{id}/activate", s.handleActivateModel)
	mux.HandleFunc("DELETE /api/settings/models/{id}", s.handleDeleteModel)
	mux.HandleFunc("PUT /api/settings/search", s.handlePutSearch)
	mux.HandleFunc("PUT /api/settings/relay", s.handlePutRelay)
	mux.HandleFunc("GET /api/settings/cloud-memory", s.handleGetCloudMemory)
	mux.HandleFunc("PUT /api/settings/cloud-memory", s.handlePutCloudMemory)
	mux.HandleFunc("POST /api/cloud-memory/test", s.handleTestCloudMemory)
	mux.HandleFunc("GET /api/cloud-memory/status", s.handleCloudMemoryStatus)
	mux.HandleFunc("POST /api/cloud-memory/retry", s.handleRetryCloudMemory)
	mux.HandleFunc("GET /api/delivery/inbox", s.handleDeliveryInbox)
	mux.HandleFunc("GET /api/delivery/status", s.handleDeliveryStatus)
	mux.HandleFunc("GET /api/delivery/{id}/content", s.handleDeliveryContent)
	mux.HandleFunc("POST /api/delivery/{id}/ack", s.handleDeliveryAck)
	mux.HandleFunc("DELETE /api/delivery/{id}", s.handleDeliveryDelete)
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

	// 远程控制面：手机经中继读写电脑的 WorkTask、Worker 会话、审批、
	// 交付物与每日简报。查询只读缓存，变更离线排队。
	mux.HandleFunc("GET /api/remote/conversations", s.handleRemoteConversations)
	mux.HandleFunc("GET /api/remote/conversations/{id}/messages", s.handleRemoteConversationMessages)
	mux.HandleFunc("POST /api/remote/conversations/{id}/read", s.handleRemoteConversationRead)
	mux.HandleFunc("GET /api/remote/work-tasks", s.handleRemoteWorkTasks)
	mux.HandleFunc("POST /api/remote/work-tasks", s.handleRemoteCreateWorkTask)
	mux.HandleFunc("GET /api/remote/work-tasks/{id}", s.handleRemoteWorkTask)
	mux.HandleFunc("POST /api/remote/work-tasks/{id}/messages", s.handleRemoteWorkTaskMessage)
	mux.HandleFunc("POST /api/remote/work-tasks/{id}/{action}", s.handleRemoteWorkTaskAction)
	mux.HandleFunc("GET /api/remote/action-outbox", s.handleRemoteOutbox)
	mux.HandleFunc("POST /api/remote/action-outbox/{id}/{action}", s.handleRemoteOutboxDecision)
	mux.HandleFunc("GET /api/remote/artifacts/{id}", s.handleRemoteArtifact)
	mux.HandleFunc("POST /api/remote/artifacts/{id}/{action}", s.handleRemoteArtifactDecision)
	mux.HandleFunc("GET /api/remote/briefs/latest", s.handleRemoteLatestBrief)
	mux.HandleFunc("GET /api/remote/briefs", s.handleRemoteBriefs)
	mux.HandleFunc("GET /api/remote/briefs/{id}", s.handleRemoteBrief)
	mux.HandleFunc("POST /api/remote/briefs/generate", s.handleRemoteGenerateBrief)
	mux.HandleFunc("GET /api/remote/collection/channels", s.handleRemoteCollectionChannels)
	mux.HandleFunc("POST /api/remote/collection/channels", s.handleRemoteConfigureCollection)
	mux.HandleFunc("GET /api/remote/pending", s.handleRemotePending)
	mux.HandleFunc("POST /api/remote/flush", s.handleRemoteFlush)
	mux.HandleFunc("GET /api/remote/status", s.handleRemoteStatus)

	// 通知中心（Inbox nil 时端点 503）。
	mux.HandleFunc("POST /api/inbox/events", s.handleInboxIngest)
	mux.HandleFunc("GET /api/inbox/events", s.handleInboxEvents)
	mux.HandleFunc("GET /api/inbox/search", s.handleInboxSearch)
	mux.HandleFunc("POST /api/inbox/snooze", s.handleInboxCreateSnooze)
	mux.HandleFunc("GET /api/inbox/snooze", s.handleInboxListSnoozes)
	mux.HandleFunc("POST /api/inbox/watches", s.handleInboxCreateWatch)
	mux.HandleFunc("GET /api/inbox/watches", s.handleInboxListWatches)
	mux.HandleFunc("DELETE /api/inbox/watches/{id}", s.handleInboxCancelWatch)

	// Super-agent：Trigger/Thread/Worker/审批与持久事件流（未装配时 503）。
	mux.HandleFunc("POST /api/triggers", s.handleCreateTrigger)
	mux.HandleFunc("GET /api/triggers", s.handleListTriggers)
	mux.HandleFunc("GET /api/threads", s.handleListThreads)
	mux.HandleFunc("GET /api/threads/{id}", s.handleGetThread)
	mux.HandleFunc("POST /api/threads/{id}/link", s.handleLinkThread)
	mux.HandleFunc("DELETE /api/threads/{id}/link/{linkedID}", s.handleUnlinkThread)
	mux.HandleFunc("GET /api/worker-tasks", s.handleListWorkerTasks)
	mux.HandleFunc("POST /api/worker-tasks/{id}/cancel", s.handleCancelWorkerTask)
	mux.HandleFunc("POST /api/worker-tasks/{id}/steer", s.handleSteerWorkerTask)
	mux.HandleFunc("GET /api/action-outbox", s.handleListActionOutbox)
	mux.HandleFunc("POST /api/action-outbox", s.handleCreateActionOutbox)
	mux.HandleFunc("GET /api/action-outbox/{id}", s.handleGetActionOutbox)
	mux.HandleFunc("PUT /api/action-outbox/{id}", s.handleUpdateActionOutbox)
	mux.HandleFunc("POST /api/action-outbox/{id}/approve", s.handleApproveActionOutbox)
	mux.HandleFunc("POST /api/action-outbox/{id}/reject", s.handleRejectActionOutbox)
	mux.HandleFunc("POST /api/superagent/route", s.handleRouteTriggers)
	mux.HandleFunc("GET /api/events", s.handleAgentEvents)
	mux.HandleFunc("GET /api/superagent/health", s.handleSuperAgentHealth)
	mux.HandleFunc("GET /api/briefs", s.handleListBriefs)
	mux.HandleFunc("GET /api/briefs/latest", s.handleLatestBrief)
	mux.HandleFunc("POST /api/briefs/generate", s.handleGenerateBrief)
	mux.HandleFunc("GET /api/briefs/{id}", s.handleGetBrief)
	mux.HandleFunc("POST /api/briefs/{id}/delivered", s.handleBriefDelivered)

	// WorkTask：跨执行段的长期任务、控制 Inbox、Artifact 与 Grant。
	mux.HandleFunc("POST /api/work-tasks", s.handleCreateWorkTask)
	mux.HandleFunc("GET /api/work-tasks", s.handleListWorkTasks)
	mux.HandleFunc("GET /api/work-tasks/{id}", s.handleGetWorkTask)
	mux.HandleFunc("POST /api/work-tasks/{id}/messages", s.handleWorkTaskMessage)
	mux.HandleFunc("POST /api/work-tasks/{id}/pause", s.handlePauseWorkTask)
	mux.HandleFunc("POST /api/work-tasks/{id}/resume", s.handleResumeWorkTask)
	mux.HandleFunc("POST /api/work-tasks/{id}/cancel", s.handleCancelWorkTask)
	mux.HandleFunc("POST /api/work-tasks/{id}/recover", s.handleRecoverWorkTask)
	mux.HandleFunc("POST /api/work-tasks/{id}/reopen", s.handleReopenWorkTask)
	mux.HandleFunc("POST /api/work-tasks/{id}/complete", s.handleCompleteWorkTask)
	mux.HandleFunc("GET /api/work-tasks/{id}/events", s.handleWorkTaskEvents)
	mux.HandleFunc("POST /api/work-tasks/{id}/threads/{thread_id}", s.handleAttachWorkTaskThread)
	mux.HandleFunc("DELETE /api/work-tasks/{id}/threads/{thread_id}", s.handleDetachWorkTaskThread)
	mux.HandleFunc("GET /api/work-tasks/{id}/artifacts", s.handleListTaskArtifacts)
	mux.HandleFunc("POST /api/work-tasks/{id}/artifacts", s.handleCreateArtifact)
	mux.HandleFunc("GET /api/work-tasks/{id}/grants", s.handleListTaskGrants)
	mux.HandleFunc("POST /api/work-tasks/{id}/grants", s.handleCreateTaskGrant)
	mux.HandleFunc("POST /api/work-tasks/{id}/devices/{device_id}", s.handleBindTaskDevice)
	mux.HandleFunc("GET /api/artifacts/{id}", s.handleGetArtifact)
	mux.HandleFunc("GET /api/artifacts/{id}/versions/{version}/download", s.handleDownloadArtifact)
	mux.HandleFunc("GET /api/artifacts/{id}/diff", s.handleDiffArtifact)
	mux.HandleFunc("POST /api/artifacts/{id}/submit", s.handleSubmitArtifact)
	mux.HandleFunc("POST /api/artifacts/{id}/accept", s.handleAcceptArtifact)
	mux.HandleFunc("POST /api/artifacts/{id}/reject", s.handleRejectArtifact)
	mux.HandleFunc("POST /api/artifacts/{id}/deliver", s.handleDeliverArtifact)
	mux.HandleFunc("DELETE /api/grants/{id}", s.handleRevokeGrant)

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
