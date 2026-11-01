package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"goldenfinger/agent/internal/store"
	"goldenfinger/agent/internal/superagent"
)

// JobService 是持久化异步 Agent 任务队列（agenthub.Dispatcher 满足该接口）。
type JobService interface {
	Submit(ctx context.Context, ownerID, agentID, task, workDir, idempotencyKey string) (*store.AgentJob, error)
	Get(ctx context.Context, ownerID, id string) (*store.AgentJob, []store.AgentJobEvent, error)
	List(ctx context.Context, ownerID string, limit int) ([]store.AgentJob, error)
	Cancel(ctx context.Context, ownerID, id string) (*store.AgentJob, error)
}

func (s *Server) requireJobs(w http.ResponseWriter) bool {
	if s.Jobs == nil {
		writeError(w, http.StatusServiceUnavailable, "异步任务未启用")
		return false
	}
	return true
}

// POST /api/jobs —— 提交后台任务，立刻返回 task_id（不等执行完成）。
func (s *Server) handleJobSubmit(w http.ResponseWriter, r *http.Request) {
	if !s.requireJobs(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	var req struct {
		AgentID        string `json:"agent_id"`
		Task           string `json:"task"`
		WorkDir        string `json:"work_dir"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeError(w, 400, "bad request body")
		return
	}
	if strings.TrimSpace(req.AgentID) == "" {
		writeError(w, 400, "agent_id 不能为空")
		return
	}
	if req.AgentID == superagent.WorkerAgentID {
		writeError(w, 400, "agent_id 为 Super-agent 保留值")
		return
	}
	job, err := s.Jobs.Submit(r.Context(), u.ID, req.AgentID, req.Task, req.WorkDir, req.IdempotencyKey)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"task": job})
}

// GET /api/jobs?limit=
func (s *Server) handleJobList(w http.ResponseWriter, r *http.Request) {
	if !s.requireJobs(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	jobs, err := s.Jobs.List(r.Context(), u.ID, limit)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if jobs == nil {
		jobs = []store.AgentJob{}
	}
	writeJSON(w, 200, map[string]any{"tasks": jobs})
}

// GET /api/jobs/{id} —— 任务状态、结果与全部状态事件（重连后可用它补看进度）。
func (s *Server) handleJobGet(w http.ResponseWriter, r *http.Request) {
	if !s.requireJobs(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	job, events, err := s.Jobs.Get(r.Context(), u.ID, r.PathValue("id"))
	if err != nil {
		writeError(w, 404, "任务不存在")
		return
	}
	if events == nil {
		events = []store.AgentJobEvent{}
	}
	writeJSON(w, 200, map[string]any{"task": job, "events": events})
}

// DELETE /api/jobs/{id} —— 取消仍在排队或执行中的任务。
func (s *Server) handleJobCancel(w http.ResponseWriter, r *http.Request) {
	if !s.requireJobs(w) {
		return
	}
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	job, err := s.Jobs.Cancel(r.Context(), u.ID, r.PathValue("id"))
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"task": job})
}
