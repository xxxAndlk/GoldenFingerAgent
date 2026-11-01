package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"goldenfinger/agent/internal/store"
	"goldenfinger/agent/internal/superagent"
)

func (s *Server) requireSuperAgent(w http.ResponseWriter) bool {
	if s.SuperAgent == nil {
		writeError(w, http.StatusServiceUnavailable, "Super-agent 未启用")
		return false
	}
	return true
}

func (s *Server) handleCreateTrigger(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAgent(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	var req superagent.TriggerInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10)).Decode(&req); err != nil {
		writeError(w, 400, "bad request body")
		return
	}
	result, err := s.SuperAgent.Ingest(r.Context(), u.ID, req)
	if err != nil {
		if errors.Is(err, superagent.ErrInvalid) {
			writeError(w, 400, err.Error())
			return
		}
		writeError(w, 500, err.Error())
		return
	}
	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	writeJSON(w, status, result)
}

func (s *Server) handleListTriggers(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAgent(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	items, err := s.SuperAgent.ListTriggers(r.Context(), u.ID,
		strings.TrimSpace(r.URL.Query().Get("status")),
		strings.TrimSpace(r.URL.Query().Get("thread_id")),
		parseLimit(r.URL.Query().Get("limit"), 50))
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if items == nil {
		items = []store.Trigger{}
	}
	writeJSON(w, 200, map[string]any{"triggers": items})
}

func (s *Server) handleListThreads(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAgent(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	items, err := s.SuperAgent.ListThreads(r.Context(), u.ID,
		strings.TrimSpace(r.URL.Query().Get("channel")),
		parseLimit(r.URL.Query().Get("limit"), 50))
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if items == nil {
		items = []store.Thread{}
	}
	writeJSON(w, 200, map[string]any{"threads": items})
}

func (s *Server) handleGetThread(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAgent(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	thread, triggers, links, err := s.SuperAgent.GetThread(r.Context(), u.ID, r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, 404, "Thread 不存在")
		return
	}
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if triggers == nil {
		triggers = []store.Trigger{}
	}
	if links == nil {
		links = []store.ThreadLink{}
	}
	writeJSON(w, 200, map[string]any{"thread": thread, "triggers": triggers, "links": links})
}

var allowedThreadRelations = map[string]bool{
	"same_people": true, "same_project": true, "calendar": true, "manual": true,
}

func (s *Server) handleLinkThread(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAgent(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	threadID := r.PathValue("id")
	if _, err := s.Repos.Threads.Get(r.Context(), u.ID, threadID); err != nil {
		writeError(w, 404, "Thread 不存在")
		return
	}
	var req struct {
		LinkedThreadID string  `json:"linked_thread_id"`
		Relation       string  `json:"relation"`
		Confidence     float64 `json:"confidence,omitempty"`
		Reason         string  `json:"reason,omitempty"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&req); err != nil {
		writeError(w, 400, "bad request body")
		return
	}
	req.LinkedThreadID = strings.TrimSpace(req.LinkedThreadID)
	req.Relation = strings.TrimSpace(req.Relation)
	if req.Relation == "" {
		req.Relation = "manual"
	}
	if !allowedThreadRelations[req.Relation] {
		writeError(w, 400, "relation 不支持")
		return
	}
	if req.LinkedThreadID == "" || req.LinkedThreadID == threadID {
		writeError(w, 400, "linked_thread_id 无效")
		return
	}
	if _, err := s.Repos.Threads.Get(r.Context(), u.ID, req.LinkedThreadID); err != nil {
		writeError(w, 404, "关联 Thread 不存在")
		return
	}
	link := &store.ThreadLink{
		OwnerUserID: u.ID, ThreadID: threadID, LinkedThreadID: req.LinkedThreadID,
		Relation: req.Relation, Confidence: req.Confidence, Reason: req.Reason,
	}
	if err := s.Repos.ThreadLinks.Add(r.Context(), link); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"link": link})
}

func (s *Server) handleUnlinkThread(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAgent(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	relation := strings.TrimSpace(r.URL.Query().Get("relation"))
	if relation == "" {
		relation = "manual"
	}
	removed, err := s.Repos.ThreadLinks.Remove(r.Context(), u.ID, r.PathValue("id"), r.PathValue("linkedID"), relation)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if !removed {
		writeError(w, 404, "关联不存在")
		return
	}
	writeJSON(w, 200, map[string]any{"removed": true})
}

func (s *Server) handleListWorkerTasks(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAgent(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	jobs, err := s.Repos.AgentJobs.ListWorkerTasks(r.Context(), u.ID,
		strings.TrimSpace(r.URL.Query().Get("thread_id")),
		parseLimit(r.URL.Query().Get("limit"), 50))
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if jobs == nil {
		jobs = []store.AgentJob{}
	}
	writeJSON(w, 200, map[string]any{"tasks": jobs})
}

func (s *Server) handleCancelWorkerTask(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAgent(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	job, err := s.Repos.AgentJobs.Get(r.Context(), u.ID, r.PathValue("id"))
	if err != nil {
		writeError(w, 404, "任务不存在")
		return
	}
	if job.ThreadID == "" && job.ParentTriggerID == "" {
		writeError(w, 404, "不是 Super-agent Worker 任务")
		return
	}
	if job.AgentID == superagent.WorkerAgentID && s.SuperAgentWorkers != nil {
		updated, err := s.SuperAgentWorkers.Cancel(r.Context(), u.ID, job.ID)
		if err != nil {
			writeError(w, 409, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"task": updated})
		return
	}
	if s.Jobs != nil {
		updated, err := s.Jobs.Cancel(r.Context(), u.ID, job.ID)
		if err != nil {
			writeError(w, 409, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"task": updated})
		return
	}
	changed, err := s.Repos.AgentJobs.Cancel(r.Context(), u.ID, job.ID, time.Now())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if !changed {
		writeError(w, 409, "任务已结束，不能取消")
		return
	}
	writeJSON(w, 200, map[string]any{"cancelled": true})
}

func (s *Server) handleSteerWorkerTask(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAgent(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	job, err := s.Repos.AgentJobs.Get(r.Context(), u.ID, r.PathValue("id"))
	if err != nil {
		writeError(w, 404, "任务不存在")
		return
	}
	if job.ThreadID == "" && job.ParentTriggerID == "" {
		writeError(w, 404, "不是 Super-agent Worker 任务")
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeError(w, 400, "bad request body")
		return
	}
	req.Text = strings.TrimSpace(req.Text)
	if req.Text == "" || len(req.Text) > 4000 {
		writeError(w, 400, "追加指令不能为空且不能超过 4000 字符")
		return
	}
	if err := s.Repos.AgentJobs.AddEvent(r.Context(), job.ID, "steer", req.Text, time.Now()); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 202, map[string]any{"accepted": true})
}

func (s *Server) handleListActionOutbox(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAgent(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	items, err := s.Repos.ActionOutbox.List(r.Context(), u.ID,
		strings.TrimSpace(r.URL.Query().Get("status")),
		parseLimit(r.URL.Query().Get("limit"), 50))
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if items == nil {
		items = []store.ActionOutbox{}
	}
	writeJSON(w, 200, map[string]any{"outbox": items})
}

type actionOutboxRequest struct {
	Channel          string          `json:"channel"`
	ThreadID         string          `json:"thread_id,omitempty"`
	WorkerTaskID     string          `json:"worker_task_id,omitempty"`
	Kind             string          `json:"kind"`
	Target           json.RawMessage `json:"target,omitempty"`
	Body             string          `json:"body"`
	Payload          json.RawMessage `json:"payload,omitempty"`
	RequiresApproval *bool           `json:"requires_approval,omitempty"`
	IdempotencyKey   string          `json:"idempotency_key,omitempty"`
}

func (s *Server) handleCreateActionOutbox(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAgent(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	var req actionOutboxRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10)).Decode(&req); err != nil {
		writeError(w, 400, "bad request body")
		return
	}
	req.Channel = strings.TrimSpace(req.Channel)
	req.Kind = strings.TrimSpace(req.Kind)
	req.Body = strings.TrimSpace(req.Body)
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	if req.Channel == "" || req.Kind == "" || req.Body == "" {
		writeError(w, 400, "channel、kind、body 不能为空")
		return
	}
	if req.IdempotencyKey == "" {
		req.IdempotencyKey = uuid.NewString()
	}
	requiresApproval := true
	if req.RequiresApproval != nil {
		requiresApproval = *req.RequiresApproval
	}
	item := &store.ActionOutbox{
		OwnerUserID: u.ID, Channel: req.Channel, ThreadID: req.ThreadID,
		WorkerTaskID: req.WorkerTaskID, Kind: req.Kind, Target: req.Target, Body: req.Body,
		Payload: req.Payload, RequiresApproval: requiresApproval, IdempotencyKey: req.IdempotencyKey,
	}
	if err := s.Repos.ActionOutbox.Create(r.Context(), item); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, 409, "幂等键已存在")
			return
		}
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"outbox": item})
}

func (s *Server) handleGetActionOutbox(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAgent(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	item, err := s.Repos.ActionOutbox.Get(r.Context(), u.ID, r.PathValue("id"))
	if err != nil {
		writeError(w, 404, "草稿不存在")
		return
	}
	writeJSON(w, 200, map[string]any{"outbox": item})
}

func (s *Server) handleUpdateActionOutbox(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAgent(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	var req struct {
		Version int             `json:"version"`
		Body    string          `json:"body"`
		Target  json.RawMessage `json:"target,omitempty"`
		Payload json.RawMessage `json:"payload,omitempty"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10)).Decode(&req); err != nil {
		writeError(w, 400, "bad request body")
		return
	}
	changed, err := s.Repos.ActionOutbox.UpdateDraft(r.Context(), u.ID, r.PathValue("id"),
		req.Version, strings.TrimSpace(req.Body), req.Target, req.Payload, time.Now())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if !changed {
		writeError(w, 409, "草稿已被修改或已进入发送流程，请刷新后重试")
		return
	}
	s.writeActionOutbox(w, r, u.ID)
}

func (s *Server) handleApproveActionOutbox(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAgent(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	var req struct {
		Version int `json:"version"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeError(w, 400, "bad request body")
		return
	}
	changed, err := s.Repos.ActionOutbox.Approve(r.Context(), u.ID, r.PathValue("id"),
		req.Version, u.ID, time.Now())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if !changed {
		writeError(w, 409, "版本已变化或已处理，请刷新后重试")
		return
	}
	s.writeActionOutbox(w, r, u.ID)
}

func (s *Server) handleRejectActionOutbox(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAgent(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	var req struct {
		Version int    `json:"version"`
		Reason  string `json:"reason,omitempty"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeError(w, 400, "bad request body")
		return
	}
	changed, err := s.Repos.ActionOutbox.Reject(r.Context(), u.ID, r.PathValue("id"),
		req.Version, u.ID, strings.TrimSpace(req.Reason), time.Now())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if !changed {
		writeError(w, 409, "版本已变化或已处理，请刷新后重试")
		return
	}
	s.writeActionOutbox(w, r, u.ID)
}

func (s *Server) writeActionOutbox(w http.ResponseWriter, r *http.Request, ownerID string) {
	item, err := s.Repos.ActionOutbox.Get(r.Context(), ownerID, r.PathValue("id"))
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"outbox": item})
}

// POST /api/superagent/route?mode=deterministic|llm —— 手动触发一批 queued Trigger 的路由。
func (s *Server) handleRouteTriggers(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAgent(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	var router superagent.Router = superagent.DeterministicRouter{}
	if r.URL.Query().Get("mode") == "llm" && s.Runtime != nil {
		rt := s.effectiveRuntime()
		if rt != nil && rt.LLM != nil {
			router = &superagent.LLMRouter{Client: rt.LLM, Model: rt.Model}
		}
	}
	results, err := s.SuperAgent.RoutePending(r.Context(), u.ID, router,
		parseLimit(r.URL.Query().Get("limit"), 20))
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if results == nil {
		results = []superagent.RouteResult{}
	}
	writeJSON(w, 200, map[string]any{"results": results})
}

func (s *Server) handleAgentEvents(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAgent(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	after := parseEventID(r.Header.Get("Last-Event-ID"))
	if after == 0 {
		after = parseEventID(r.URL.Query().Get("after"))
	}
	limit := parseLimit(r.URL.Query().Get("limit"), 100)
	if r.URL.Query().Get("stream") == "1" {
		s.streamAgentEvents(w, r, u.ID, after)
		return
	}
	events, err := s.SuperAgent.ListEvents(r.Context(), u.ID, after, limit)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	next := after
	for _, event := range events {
		next = event.ID
	}
	if events == nil {
		events = []store.AgentEvent{}
	}
	writeJSON(w, 200, map[string]any{
		"events":        events,
		"next_event_id": next,
		"has_more":      len(events) == limit,
	})
}

func (s *Server) streamAgentEvents(w http.ResponseWriter, r *http.Request, ownerID string, after int64) {
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

	lastID := after
	send := func(events []store.AgentEvent) bool {
		for _, event := range events {
			raw, err := json.Marshal(event)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "id: %d\nevent: agent\ndata: %s\n\n", event.ID, raw); err != nil {
				return false
			}
			lastID = event.ID
		}
		flusher.Flush()
		return true
	}
	events, err := s.SuperAgent.ListEvents(r.Context(), ownerID, lastID, 200)
	if err != nil {
		return
	}
	if !send(events) {
		return
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-ticker.C:
			events, err := s.SuperAgent.ListEvents(r.Context(), ownerID, lastID, 200)
			if err != nil {
				return
			}
			if !send(events) {
				return
			}
		}
	}
}

func (s *Server) handleSuperAgentHealth(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAgent(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	health, err := s.SuperAgent.Health(r.Context(), u.ID)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, health)
}

func parseLimit(raw string, fallback int) int {
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return fallback
	}
	if n > 200 {
		return 200
	}
	return n
}

func parseEventID(raw string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}
