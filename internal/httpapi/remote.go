package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"goldenfinger/agent/internal/relay"
	"goldenfinger/agent/internal/store"
)

// RemoteControl 是手机远程控制电脑的能力集合。
//
// 由 relay.Phone 实现。电脑端未装配时相关端点返回 503，
// 不回落到本机数据冒充电脑状态。
type RemoteControl interface {
	WorkTaskCreate(ctx context.Context, idempotencyKey, title, goal, profile string) (relay.ResultPayload, error)
	WorkTaskList(ctx context.Context, status string, limit int) (relay.ResultPayload, error)
	WorkTaskGet(ctx context.Context, taskID string) (relay.ResultPayload, error)
	WorkTaskMessage(ctx context.Context, idempotencyKey, taskID, text string, expectedVersion *int) (relay.ResultPayload, error)
	WorkTaskAction(ctx context.Context, idempotencyKey, taskID, action string, expectedVersion *int) (relay.ResultPayload, error)
	ConversationList(ctx context.Context, kind string, limit int, deviceID string) (relay.ResultPayload, error)
	ConversationMessages(ctx context.Context, sessionID, before string, limit int) (relay.ResultPayload, error)
	ConversationRead(ctx context.Context, sessionID, deviceID string) (relay.ResultPayload, error)
	OutboxList(ctx context.Context, limit int) (relay.ResultPayload, error)
	OutboxDecide(ctx context.Context, idempotencyKey, outboxID string, approve bool, expectedVersion *int, reason string) (relay.ResultPayload, error)
	ArtifactGet(ctx context.Context, artifactID string) (relay.ResultPayload, error)
	ArtifactDecide(ctx context.Context, idempotencyKey, artifactID string, accept bool, expectedVersion *int, reason string) (relay.ResultPayload, error)
	ArtifactDeliver(ctx context.Context, idempotencyKey, artifactID string, version int) (relay.ResultPayload, error)
	BriefLatest(ctx context.Context, date string) (relay.ResultPayload, error)
	BriefList(ctx context.Context, limit int) (relay.ResultPayload, error)
	BriefGet(ctx context.Context, briefID string) (relay.ResultPayload, error)
	BriefGenerate(ctx context.Context, idempotencyKey, date string) (relay.ResultPayload, error)
	CollectionList(ctx context.Context) (relay.ResultPayload, error)
	CollectionConfigure(ctx context.Context, idempotencyKey, channel string, enabled bool, config map[string]any) (relay.ResultPayload, error)
	// RemoteStatus 报告远程通道是否可用（配对、在线状态）。
	RemoteStatus() map[string]any
}

// GET /api/remote/status
//
// 手机端据此决定读本机数据还是走电脑控制面；
// 电脑端（本机运行）返回 available=false，界面直接用本机接口。
func (s *Server) handleRemoteStatus(w http.ResponseWriter, r *http.Request) {
	if s.Remote == nil {
		writeJSON(w, http.StatusOK, map[string]any{"available": false})
		return
	}
	status := s.Remote.RemoteStatus()
	if status == nil {
		status = map[string]any{}
	}
	status["available"] = true
	writeJSON(w, http.StatusOK, status)
}

// 远程读的数据来源标记。
const (
	remoteSourceLive  = "live"
	remoteSourceCache = "cache"
)

// requireRemote 判断远程控制通道是否可用。
func (s *Server) requireRemote(w http.ResponseWriter) bool {
	if s.Remote == nil {
		writeError(w, http.StatusServiceUnavailable, "电脑远程未启用")
		return false
	}
	return true
}

// remoteRead 执行一次远程读取。
//
// 电脑在线时返回实时结果并写入缓存；离线或失败时回落到缓存，
// 并明确标记 offline 与最后同步时间，不伪造实时数据。
func (s *Server) remoteRead(w http.ResponseWriter, r *http.Request, ownerID, cacheKey string,
	call func(ctx context.Context) (relay.ResultPayload, error)) {
	ctx := r.Context()
	result, err := call(ctx)
	if err == nil && result.OK {
		s.writeRemotePayload(w, r, ownerID, cacheKey, result.Data, remoteSourceLive, s.Now(), "")
		return
	}
	reason := ""
	if err != nil {
		reason = err.Error()
	} else if result.Error != "" {
		reason = result.Error
	}
	cached, synced, cacheErr := s.Repos.RemoteCache.Get(ctx, ownerID, cacheKey)
	if cacheErr != nil || len(cached) == 0 {
		// 没有缓存就不能装作有数据：如实报错。
		writeError(w, http.StatusBadGateway, remoteFailureReason(reason))
		return
	}
	s.writeRemotePayload(w, r, ownerID, cacheKey, cached, remoteSourceCache, synced, reason)
}

// remoteFailureReason 把底层错误翻译成用户能看懂的一句话。
func remoteFailureReason(reason string) string {
	if reason == "" {
		return "电脑当前离线，且本地没有缓存数据"
	}
	if strings.Contains(reason, relay.ErrDesktopOffline.Error()) {
		return "电脑当前离线，且本地没有缓存数据"
	}
	return reason
}

// writeRemotePayload 写出远程结果，并补上来源与同步时间。
//
// 保留电脑端原有的 JSON 形状（例如 {"brief": {...}}），
// 只额外附加 _source/_synced_at/_offline 三个字段，降低手机端改造成本。
func (s *Server) writeRemotePayload(w http.ResponseWriter, r *http.Request, ownerID, cacheKey string,
	data []byte, source string, syncedAt time.Time, reason string) {
	if source == remoteSourceLive {
		_ = s.Repos.RemoteCache.Put(r.Context(), ownerID, cacheKey, data, syncedAt)
	}
	body := map[string]any{}
	if len(data) > 0 {
		_ = json.Unmarshal(data, &body)
	}
	body["_source"] = source
	body["_offline"] = source == remoteSourceCache
	if !syncedAt.IsZero() {
		body["_synced_at"] = syncedAt.UTC().Format(time.RFC3339)
	}
	if reason != "" {
		body["_reason"] = reason
	}
	writeJSON(w, http.StatusOK, body)
}

// remoteMutate 执行一次远程变更。
//
// 电脑在线时直接执行；明确离线时把指令连同持久化幂等键排队，
// 返回 queued 而不是谎称已批准。客户端重试必须复用同一个幂等键。
//
// 结果未知（超时、5xx、连接中断）时既不排队也不自动重放：
// 电脑可能已经执行成功、只是回执丢了，盲目重放会执行第二遍。
// 这种情况下如实告诉用户「结果未知，请先核对」。
//
// queueable=false 用于审批、验收这类高风险动作：
// 它们必须等电脑在线并返回真实版本结果，不能把「已排队」显示成「已批准」。
func (s *Server) remoteMutate(w http.ResponseWriter, r *http.Request, ownerID, kind, idempotencyKey string,
	payload map[string]any, call func(ctx context.Context, key string) (relay.ResultPayload, error), queueable bool) {
	ctx := r.Context()
	if idempotencyKey == "" {
		idempotencyKey = newCommandID()
	}
	result, err := call(ctx, idempotencyKey)
	if err == nil && result.OK {
		body := map[string]any{}
		if len(result.Data) > 0 {
			_ = json.Unmarshal(result.Data, &body)
		}
		body["_source"] = remoteSourceLive
		body["_command"] = result.Command
		body["_idempotency_key"] = idempotencyKey
		if result.Reply != "" {
			body["_reply"] = result.Reply
		}
		writeJSON(w, http.StatusOK, body)
		return
	}
	reason := ""
	if err != nil {
		reason = err.Error()
	} else if result.Error != "" {
		reason = result.Error
	}
	// 电脑明确拒绝或版本冲突时不要排队：这些必须让用户看到真实原因。
	if result.Command == relay.CommandRejected || result.Command == relay.CommandConflict {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": reason, "_command": result.Command, "_queued": false,
		})
		return
	}
	// 未配对是配置问题，不是「结果未知」：让用户去配对，不要暗示可能已经执行。
	if errors.Is(err, relay.ErrNotPaired) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": "还没有和电脑配对，请先在设置里完成配对。", "_command": result.Command, "_queued": false,
		})
		return
	}
	// 只有明确的「电脑离线」才允许排队。其余失败一律按结果未知处理：
	// 电脑可能已经执行成功，只是回执没回来，重放会执行第二遍。
	if !remoteOffline(err, result) {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error":    remoteUnknownReason(reason),
			"_command": relay.CommandUnknown, "_queued": false,
		})
		return
	}
	if !queueable {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error":    "这个操作需要电脑在线才能确认，请等电脑上线后重试。",
			"_command": result.Command, "_queued": false,
		})
		return
	}
	// 明确离线：排队，等电脑上线后最多执行一次。
	raw, _ := json.Marshal(payload)
	pending := store.RemotePending{
		IdempotencyKey: idempotencyKey, OwnerUserID: ownerID, Kind: kind,
		Payload: raw, Status: "pending", CreatedAt: s.Now(), UpdatedAt: s.Now(),
	}
	existing, inserted, err := s.Repos.RemotePending.Enqueue(ctx, pending)
	if errors.Is(err, store.ErrConflict) {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":    "这个幂等键已经对应了另一条指令（内容、操作类型或归属不同），请换一个键再试。",
			"_command": relay.CommandRejected, "_queued": false,
		})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !inserted {
		// 同键记录已经存在：按它真实的状态回执，不能再一律说「已排队」。
		switch existing.Status {
		case "done":
			body := map[string]any{}
			if len(existing.Result) > 0 {
				_ = json.Unmarshal(existing.Result, &body)
			}
			body["_source"] = "replay"
			body["_replayed"] = true
			body["_command"] = relay.CommandExecuted
			body["_idempotency_key"] = idempotencyKey
			writeJSON(w, http.StatusOK, body)
			return
		case "failed":
			reason := existing.LastError
			if reason == "" {
				reason = "上次执行结果未知"
			}
			writeJSON(w, http.StatusBadGateway, map[string]any{
				"error":    "这条指令上次执行失败或结果未知：" + reason + "。请先在电脑上核对，再决定是否重试。",
				"_command": relay.CommandUnknown, "_queued": false, "_idempotency_key": idempotencyKey,
			})
			return
		default:
			writeJSON(w, http.StatusAccepted, map[string]any{
				"queued": true, "_command": relay.CommandQueued, "_idempotency_key": idempotencyKey,
				"reply": "电脑当前离线，这条指令已经在队列中，等电脑上线后执行",
			})
			return
		}
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"queued": true, "_command": relay.CommandQueued, "_idempotency_key": idempotencyKey,
		"reply": "电脑当前离线，指令已排队，等电脑上线后执行",
	})
}

// remoteOffline 判断这次失败是不是「电脑明确离线」。
//
// 只有明确的离线才允许排队重放；超时、5xx、连接中断都属于结果未知。
func remoteOffline(err error, result relay.ResultPayload) bool {
	if err == nil {
		return result.Command == relay.CommandQueued
	}
	if errors.Is(err, relay.ErrDesktopOffline) {
		return true
	}
	return strings.Contains(err.Error(), relay.ErrDesktopOffline.Error())
}

// remoteUnknownReason 把「结果未知」翻译成用户能看懂的一句话。
func remoteUnknownReason(reason string) string {
	if reason == "" {
		return "结果未知：电脑可能已经执行成功，但回执没有送达。请先在电脑上核对，再决定是否重试。"
	}
	return "结果未知：" + reason + "。电脑可能已经执行成功，请先核对再决定是否重试。"
}

// GET /api/remote/conversations?kind=&limit=
func (s *Server) handleRemoteConversations(w http.ResponseWriter, r *http.Request) {
	if !s.requireRemote(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	limit := parseLimit(r.URL.Query().Get("limit"), 50)
	cacheKey := "conversations:" + kind
	// 未读数按设备统计，必须用这台设备的标识去问电脑。
	deviceID := s.requestDeviceID(r)
	s.remoteRead(w, r, u.ID, cacheKey, func(ctx context.Context) (relay.ResultPayload, error) {
		return s.Remote.ConversationList(ctx, kind, limit, deviceID)
	})
}

// GET /api/remote/conversations/{id}/messages?before=&limit=
func (s *Server) handleRemoteConversationMessages(w http.ResponseWriter, r *http.Request) {
	if !s.requireRemote(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	sessionID := r.PathValue("id")
	before := strings.TrimSpace(r.URL.Query().Get("before"))
	limit := parseLimit(r.URL.Query().Get("limit"), 50)
	cacheKey := "messages:" + sessionID + ":" + before
	s.remoteRead(w, r, u.ID, cacheKey, func(ctx context.Context) (relay.ResultPayload, error) {
		return s.Remote.ConversationMessages(ctx, sessionID, before, limit)
	})
}

// POST /api/remote/conversations/{id}/read
//
// 记录这台设备读到某个会话的位置。游标记在电脑端（会话真正所在的地方），
// 手机读过不会把电脑的角标一起清掉。
func (s *Server) handleRemoteConversationRead(w http.ResponseWriter, r *http.Request) {
	if !s.requireRemote(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	sessionID := r.PathValue("id")
	deviceID := s.requestDeviceID(r)
	result, err := s.Remote.ConversationRead(r.Context(), sessionID, deviceID)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if !result.OK {
		writeError(w, http.StatusBadGateway, result.Error)
		return
	}
	body := map[string]any{"read": true, "conversation_id": sessionID, "device_id": deviceID}
	if len(result.Data) > 0 {
		_ = json.Unmarshal(result.Data, &body)
	}
	writeJSON(w, http.StatusOK, body)
}

// POST /api/remote/work-tasks
//
// 手机端建长期任务：交给电脑创建，电脑是唯一事实源。
// 幂等键随请求带过去，重试不会建出两个任务。
func (s *Server) handleRemoteCreateWorkTask(w http.ResponseWriter, r *http.Request) {
	if !s.requireRemote(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	var req struct {
		Title          string `json:"title"`
		Goal           string `json:"goal"`
		Profile        string `json:"profile"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
	}
	if strings.TrimSpace(req.Title) == "" {
		writeError(w, http.StatusBadRequest, "任务标题不能为空")
		return
	}
	key := req.IdempotencyKey
	if key == "" {
		key = newCommandID()
	}
	s.remoteMutate(w, r, u.ID, relay.CmdWorkTaskCreate, key,
		map[string]any{"title": req.Title, "goal": req.Goal, "profile": req.Profile},
		func(ctx context.Context, idempotencyKey string) (relay.ResultPayload, error) {
			return s.Remote.WorkTaskCreate(ctx, idempotencyKey, req.Title, req.Goal, req.Profile)
		}, true)
}

// GET /api/remote/work-tasks?status=&limit=
func (s *Server) handleRemoteWorkTasks(w http.ResponseWriter, r *http.Request) {
	if !s.requireRemote(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	limit := parseLimit(r.URL.Query().Get("limit"), 50)
	cacheKey := "work-tasks:" + status
	s.remoteRead(w, r, u.ID, cacheKey, func(ctx context.Context) (relay.ResultPayload, error) {
		return s.Remote.WorkTaskList(ctx, status, limit)
	})
}

// GET /api/remote/work-tasks/{id}
func (s *Server) handleRemoteWorkTask(w http.ResponseWriter, r *http.Request) {
	if !s.requireRemote(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	taskID := r.PathValue("id")
	s.remoteRead(w, r, u.ID, "work-task:"+taskID, func(ctx context.Context) (relay.ResultPayload, error) {
		return s.Remote.WorkTaskGet(ctx, taskID)
	})
}

// POST /api/remote/work-tasks/{id}/messages
//
// 追加指令走 WorkTask Inbox，不直接往聊天表插消息。
func (s *Server) handleRemoteWorkTaskMessage(w http.ResponseWriter, r *http.Request) {
	if !s.requireRemote(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	var req struct {
		Text            string `json:"text"`
		IdempotencyKey  string `json:"idempotency_key"`
		ExpectedVersion *int   `json:"expected_version"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request body")
		return
	}
	taskID := r.PathValue("id")
	key := req.IdempotencyKey
	if key == "" {
		key = newCommandID()
	}
	s.remoteMutate(w, r, u.ID, relay.CmdWorkTaskMsg, key,
		map[string]any{"task_id": taskID, "text": req.Text, "expected_version": req.ExpectedVersion},
		func(ctx context.Context, idempotencyKey string) (relay.ResultPayload, error) {
			return s.Remote.WorkTaskMessage(ctx, idempotencyKey, taskID, req.Text, req.ExpectedVersion)
		}, true)
}

// POST /api/remote/work-tasks/{id}/{action}
func (s *Server) handleRemoteWorkTaskAction(w http.ResponseWriter, r *http.Request) {
	if !s.requireRemote(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	action := strings.ToLower(strings.TrimSpace(r.PathValue("action")))
	switch action {
	case "pause", "resume", "cancel", "recover", "reopen", "complete":
	default:
		writeError(w, http.StatusBadRequest, "未知任务动作")
		return
	}
	var req struct {
		IdempotencyKey  string `json:"idempotency_key"`
		ExpectedVersion *int   `json:"expected_version"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
	}
	taskID := r.PathValue("id")
	key := req.IdempotencyKey
	if key == "" {
		key = newCommandID()
	}
	s.remoteMutate(w, r, u.ID, relay.CmdWorkTaskAction, key,
		map[string]any{"task_id": taskID, "action": action, "expected_version": req.ExpectedVersion},
		func(ctx context.Context, idempotencyKey string) (relay.ResultPayload, error) {
			return s.Remote.WorkTaskAction(ctx, idempotencyKey, taskID, action, req.ExpectedVersion)
		}, true)
}

// GET /api/remote/outbox?limit=
func (s *Server) handleRemoteOutbox(w http.ResponseWriter, r *http.Request) {
	if !s.requireRemote(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	limit := parseLimit(r.URL.Query().Get("limit"), 50)
	s.remoteRead(w, r, u.ID, "outbox", func(ctx context.Context) (relay.ResultPayload, error) {
		return s.Remote.OutboxList(ctx, limit)
	})
}

// POST /api/remote/outbox/{id}/approve
// POST /api/remote/outbox/{id}/reject
func (s *Server) handleRemoteOutboxDecision(w http.ResponseWriter, r *http.Request) {
	if !s.requireRemote(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	// 只接受精确的 approve/reject：任何拼错的动作都不能被当成 disapproval
	// 执行，否则一个 typo 就会产生真实的驳回副作用。
	action := strings.ToLower(strings.TrimSpace(r.PathValue("action")))
	if action != "approve" && action != "reject" {
		writeError(w, http.StatusBadRequest, "未知审批动作，只支持 approve/reject")
		return
	}
	approve := action == "approve"
	var req struct {
		Reason          string `json:"reason"`
		IdempotencyKey  string `json:"idempotency_key"`
		ExpectedVersion *int   `json:"expected_version"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
	}
	outboxID := r.PathValue("id")
	key := req.IdempotencyKey
	if key == "" {
		key = newCommandID()
	}
	kind := relay.CmdOutboxReject
	if approve {
		kind = relay.CmdOutboxApprove
	}
	s.remoteMutate(w, r, u.ID, kind, key,
		map[string]any{"outbox_id": outboxID, "reason": req.Reason},
		func(ctx context.Context, idempotencyKey string) (relay.ResultPayload, error) {
			return s.Remote.OutboxDecide(ctx, idempotencyKey, outboxID, approve, req.ExpectedVersion, req.Reason)
		}, false)
}

// GET /api/remote/artifacts/{id}
func (s *Server) handleRemoteArtifact(w http.ResponseWriter, r *http.Request) {
	if !s.requireRemote(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	artifactID := r.PathValue("id")
	s.remoteRead(w, r, u.ID, "artifact:"+artifactID, func(ctx context.Context) (relay.ResultPayload, error) {
		return s.Remote.ArtifactGet(ctx, artifactID)
	})
}

// POST /api/remote/artifacts/{id}/accept
// POST /api/remote/artifacts/{id}/reject
// POST /api/remote/artifacts/{id}/deliver
//
// 「交付」与「验收」是两种不同动作，不能互相顶替。
func (s *Server) handleRemoteArtifactDecision(w http.ResponseWriter, r *http.Request) {
	if !s.requireRemote(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	action := strings.ToLower(strings.TrimSpace(r.PathValue("action")))
	var req struct {
		Reason          string `json:"reason"`
		Version         int    `json:"version"`
		IdempotencyKey  string `json:"idempotency_key"`
		ExpectedVersion *int   `json:"expected_version"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
	}
	artifactID := r.PathValue("id")
	key := req.IdempotencyKey
	if key == "" {
		key = newCommandID()
	}
	switch action {
	case "accept", "reject":
		accept := action == "accept"
		kind := relay.CmdArtifactReject
		if accept {
			kind = relay.CmdArtifactAccept
		}
		s.remoteMutate(w, r, u.ID, kind, key,
			map[string]any{"artifact_id": artifactID, "reason": req.Reason},
			func(ctx context.Context, idempotencyKey string) (relay.ResultPayload, error) {
				return s.Remote.ArtifactDecide(ctx, idempotencyKey, artifactID, accept, req.ExpectedVersion, req.Reason)
			}, false)
	case "deliver":
		s.remoteMutate(w, r, u.ID, relay.CmdArtifactDeliver, key,
			map[string]any{"artifact_id": artifactID, "version": req.Version},
			func(ctx context.Context, idempotencyKey string) (relay.ResultPayload, error) {
				return s.Remote.ArtifactDeliver(ctx, idempotencyKey, artifactID, req.Version)
			}, true)
	default:
		writeError(w, http.StatusBadRequest, "未知交付物动作")
	}
}

// GET /api/remote/briefs/latest?date=
func (s *Server) handleRemoteLatestBrief(w http.ResponseWriter, r *http.Request) {
	if !s.requireRemote(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	date := strings.TrimSpace(r.URL.Query().Get("date"))
	cacheKey := "brief:latest:" + date
	s.remoteRead(w, r, u.ID, cacheKey, func(ctx context.Context) (relay.ResultPayload, error) {
		return s.Remote.BriefLatest(ctx, date)
	})
}

// GET /api/remote/briefs?limit=
func (s *Server) handleRemoteBriefs(w http.ResponseWriter, r *http.Request) {
	if !s.requireRemote(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	limit := parseLimit(r.URL.Query().Get("limit"), 30)
	s.remoteRead(w, r, u.ID, "briefs", func(ctx context.Context) (relay.ResultPayload, error) {
		return s.Remote.BriefList(ctx, limit)
	})
}

// GET /api/remote/briefs/{id}
func (s *Server) handleRemoteBrief(w http.ResponseWriter, r *http.Request) {
	if !s.requireRemote(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	briefID := r.PathValue("id")
	s.remoteRead(w, r, u.ID, "brief:"+briefID, func(ctx context.Context) (relay.ResultPayload, error) {
		return s.Remote.BriefGet(ctx, briefID)
	})
}

// POST /api/remote/briefs/generate
//
// 电脑离线时不伪造新版本，只如实排队。
func (s *Server) handleRemoteGenerateBrief(w http.ResponseWriter, r *http.Request) {
	if !s.requireRemote(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	var req struct {
		Date           string `json:"date"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
	}
	key := req.IdempotencyKey
	if key == "" {
		key = newCommandID()
	}
	s.remoteMutate(w, r, u.ID, relay.CmdBriefGenerate, key,
		map[string]any{"date": req.Date},
		func(ctx context.Context, idempotencyKey string) (relay.ResultPayload, error) {
			return s.Remote.BriefGenerate(ctx, idempotencyKey, req.Date)
		}, false)
}

// GET /api/remote/collection/channels
func (s *Server) handleRemoteCollectionChannels(w http.ResponseWriter, r *http.Request) {
	if !s.requireRemote(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	s.remoteRead(w, r, u.ID, "collection:channels", func(ctx context.Context) (relay.ResultPayload, error) {
		return s.Remote.CollectionList(ctx)
	})
}

// POST /api/remote/collection/channels
//
// 远程配置必须等电脑真实回执，失败时保留原状态。
func (s *Server) handleRemoteConfigureCollection(w http.ResponseWriter, r *http.Request) {
	if !s.requireRemote(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	var req struct {
		Channel         string         `json:"channel"`
		Enabled         bool           `json:"enabled"`
		Action          string         `json:"action"`
		Query           string         `json:"query"`
		SourceID        string         `json:"source_id"`
		URL             string         `json:"url"`
		SiteID          string         `json:"site_id"`
		Scope           string         `json:"scope"`
		IntervalSeconds int            `json:"interval_seconds"`
		IdempotencyKey  string         `json:"idempotency_key"`
		Config          map[string]any `json:"config"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request body")
		return
	}
	if strings.TrimSpace(req.Channel) == "" {
		writeError(w, http.StatusBadRequest, "channel 不能为空")
		return
	}
	key := req.IdempotencyKey
	if key == "" {
		key = newCommandID()
	}
	// 其余参数原样透传给电脑端，语义与电脑端工具一致。
	config := map[string]any{}
	for field, value := range req.Config {
		config[field] = value
	}
	for field, value := range map[string]string{
		"action": req.Action, "query": req.Query, "source_id": req.SourceID,
		"url": req.URL, "site_id": req.SiteID, "scope": req.Scope,
	} {
		if value != "" {
			config[field] = value
		}
	}
	if req.IntervalSeconds > 0 {
		config["interval_seconds"] = req.IntervalSeconds
	}
	s.remoteMutate(w, r, u.ID, relay.CmdCollectionConfigure, key,
		map[string]any{"channel": req.Channel, "enabled": req.Enabled, "config": config},
		func(ctx context.Context, idempotencyKey string) (relay.ResultPayload, error) {
			return s.Remote.CollectionConfigure(ctx, idempotencyKey, req.Channel, req.Enabled, config)
		}, true)
}

// GET /api/remote/pending —— 排队的离线指令列表与计数。
func (s *Server) handleRemotePending(w http.ResponseWriter, r *http.Request) {
	if !s.requireRemote(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	items, err := s.Repos.RemotePending.ListPending(r.Context(), u.ID, parseLimit(r.URL.Query().Get("limit"), 20))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	count, err := s.Repos.RemotePending.CountPending(r.Context(), u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pending": items, "count": count})
}

// POST /api/remote/flush —— 电脑恢复在线后重放排队的指令。
//
// 每条指令复用原来的幂等键，电脑端据此保证最多执行一次。
func (s *Server) handleRemoteFlush(w http.ResponseWriter, r *http.Request) {
	if !s.requireRemote(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	replayed, failed, err := s.FlushRemote(r.Context(), u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"replayed": replayed, "failed": failed})
}

// FlushRemote 重放该用户排队中的离线指令，返回成功与失败条数。
//
// 失败的指令不自动重试：结果未知时先让用户核对。
func (s *Server) FlushRemote(ctx context.Context, ownerID string) (int, int, error) {
	if s.Remote == nil {
		return 0, 0, nil
	}
	items, err := s.Repos.RemotePending.ListPending(ctx, ownerID, 20)
	if err != nil {
		return 0, 0, err
	}
	replayed, failed := 0, 0
	for _, item := range items {
		result, replayErr := s.replayPending(ctx, item)
		if replayErr != nil {
			// 电脑又离线了：保留 pending，等它下次上线再试。
			if remoteOffline(replayErr, result) {
				continue
			}
			// 超时、连接中断等结果未知：停止自动重放，让用户先核对。
			// 一直保持 pending 会每 60 秒重试一次，可能重复执行。
			failed++
			_ = s.Repos.RemotePending.MarkFailed(ctx, item.IdempotencyKey, replayErr.Error(), s.Now())
			continue
		}
		if result.OK {
			replayed++
			_ = s.Repos.RemotePending.MarkDone(ctx, item.IdempotencyKey, result.Data, s.Now())
			continue
		}
		// 电脑明确拒绝、版本冲突或结果未知，都不再自动重试。
		failed++
		_ = s.Repos.RemotePending.MarkFailed(ctx, item.IdempotencyKey, result.Error, s.Now())
	}
	return replayed, failed, nil
}

// replayPending 重放一条排队指令。
//
// 排队时保存的 expected_version 必须原样带回去：
// 丢掉版本号就绕过了客户端要求的 CAS 检查，可能基于过期视图执行操作。
func (s *Server) replayPending(ctx context.Context, item store.RemotePending) (relay.ResultPayload, error) {
	var payload struct {
		TaskID          string         `json:"task_id"`
		Text            string         `json:"text"`
		Action          string         `json:"action"`
		OutboxID        string         `json:"outbox_id"`
		ArtifactID      string         `json:"artifact_id"`
		Reason          string         `json:"reason"`
		Version         int            `json:"version"`
		Date            string         `json:"date"`
		Channel         string         `json:"channel"`
		Enabled         bool           `json:"enabled"`
		Config          map[string]any `json:"config"`
		ExpectedVersion *int           `json:"expected_version"`
		Title           string         `json:"title"`
		Goal            string         `json:"goal"`
		Profile         string         `json:"profile"`
	}
	if len(item.Payload) > 0 {
		_ = json.Unmarshal(item.Payload, &payload)
	}
	switch item.Kind {
	case relay.CmdWorkTaskCreate:
		return s.Remote.WorkTaskCreate(ctx, item.IdempotencyKey, payload.Title, payload.Goal, payload.Profile)
	case relay.CmdWorkTaskMsg:
		return s.Remote.WorkTaskMessage(ctx, item.IdempotencyKey, payload.TaskID, payload.Text, payload.ExpectedVersion)
	case relay.CmdWorkTaskAction:
		return s.Remote.WorkTaskAction(ctx, item.IdempotencyKey, payload.TaskID, payload.Action, payload.ExpectedVersion)
	case relay.CmdOutboxApprove:
		return s.Remote.OutboxDecide(ctx, item.IdempotencyKey, payload.OutboxID, true, payload.ExpectedVersion, payload.Reason)
	case relay.CmdOutboxReject:
		return s.Remote.OutboxDecide(ctx, item.IdempotencyKey, payload.OutboxID, false, payload.ExpectedVersion, payload.Reason)
	case relay.CmdArtifactAccept:
		return s.Remote.ArtifactDecide(ctx, item.IdempotencyKey, payload.ArtifactID, true, payload.ExpectedVersion, payload.Reason)
	case relay.CmdArtifactReject:
		return s.Remote.ArtifactDecide(ctx, item.IdempotencyKey, payload.ArtifactID, false, payload.ExpectedVersion, payload.Reason)
	case relay.CmdArtifactDeliver:
		return s.Remote.ArtifactDeliver(ctx, item.IdempotencyKey, payload.ArtifactID, payload.Version)
	case relay.CmdBriefGenerate:
		return s.Remote.BriefGenerate(ctx, item.IdempotencyKey, payload.Date)
	case relay.CmdCollectionConfigure:
		return s.Remote.CollectionConfigure(ctx, item.IdempotencyKey, payload.Channel, payload.Enabled, payload.Config)
	default:
		return relay.ResultPayload{OK: false, Error: "未知排队指令: " + item.Kind}, errors.New("unknown command")
	}
}
