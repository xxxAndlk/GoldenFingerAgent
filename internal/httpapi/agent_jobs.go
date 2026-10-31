package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"goldenfinger/agent/internal/store"
)

func (s *Server) requireAgentDispatcher(w http.ResponseWriter) bool {
	if s.AgentDispatcher == nil {
		writeError(w, http.StatusServiceUnavailable, "异步 Agent 任务未配置")
		return false
	}
	return true
}

func (s *Server) handleListAgentJobs(w http.ResponseWriter, r *http.Request) {
	if !s.requireAgentDispatcher(w) {
		return
	}
	user := s.currentUser(w, r)
	if user == nil {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	jobs, err := s.AgentDispatcher.List(r.Context(), user.ID, limit)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"tasks": jobs})
}

func (s *Server) handleGetAgentJob(w http.ResponseWriter, r *http.Request) {
	if !s.requireAgentDispatcher(w) {
		return
	}
	user := s.currentUser(w, r)
	if user == nil {
		return
	}
	job, events, err := s.AgentDispatcher.Get(r.Context(), user.ID, r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, 404, "agent task not found")
		return
	}
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"task": job, "events": events})
}

func (s *Server) handleCancelAgentJob(w http.ResponseWriter, r *http.Request) {
	if !s.requireAgentDispatcher(w) {
		return
	}
	user := s.currentUser(w, r)
	if user == nil {
		return
	}
	job, err := s.AgentDispatcher.Cancel(r.Context(), user.ID, r.PathValue("id"))
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, job)
}

func (s *Server) handleAgentNotifications(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(w, r)
	if user == nil {
		return
	}
	afterID, _ := strconv.ParseInt(r.URL.Query().Get("after_id"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := s.Repos.AgentNotifications.Unread(r.Context(), user.ID, afterID, limit)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"notifications": items})
}

func (s *Server) handleAckAgentNotifications(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(w, r)
	if user == nil {
		return
	}
	var req struct {
		IDs []int64 `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.IDs) == 0 {
		writeError(w, 400, "ids is required")
		return
	}
	if err := s.Repos.AgentNotifications.Ack(r.Context(), user.ID, req.IDs, s.Now()); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
