package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"goldenfinger/agent/internal/store"
	"goldenfinger/agent/internal/superagent"
)

func (s *Server) requireBriefs(w http.ResponseWriter) bool {
	if s.Briefs == nil {
		writeError(w, http.StatusServiceUnavailable, "每日简报未启用")
		return false
	}
	return true
}

func parseBriefDate(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Now().In(time.Local), nil
	}
	return time.ParseInLocation("2006-01-02", raw, time.Local)
}

func (s *Server) handleListBriefs(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	items, err := s.Repos.DailyBriefs.List(r.Context(), u.ID, parseLimit(r.URL.Query().Get("limit"), 30))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"briefs": items})
}

func (s *Server) handleLatestBrief(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	day, err := parseBriefDate(r.URL.Query().Get("date"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "date must be YYYY-MM-DD")
		return
	}
	brief, err := s.Repos.DailyBriefs.Latest(r.Context(), u.ID, day.Format("2006-01-02"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "今日简报尚未生成")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"brief": brief})
}

func (s *Server) handleGetBrief(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	brief, err := s.Repos.DailyBriefs.Get(r.Context(), u.ID, r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "简报不存在")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"brief": brief})
}

func (s *Server) handleGenerateBrief(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil || !s.requireBriefs(w) {
		return
	}
	day, err := parseBriefDate(r.URL.Query().Get("date"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "date must be YYYY-MM-DD")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	brief, err := s.Briefs.Generate(ctx, u.ID, day)
	var sharedSyncErr *superagent.BriefSharedSyncError
	if errors.As(err, &sharedSyncErr) {
		writeJSON(w, http.StatusCreated, map[string]any{
			"brief": brief, "shared_sync_error": sharedSyncErr.Error(),
		})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"brief": brief})
}

func (s *Server) handleBriefDelivered(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	changed, err := s.Repos.DailyBriefs.MarkDelivered(r.Context(), u.ID, r.PathValue("id"), s.Now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !changed {
		writeError(w, http.StatusConflict, "简报已交付或不存在")
		return
	}
	s.writeBrief(w, r, u.ID, r.PathValue("id"))
}

func (s *Server) writeBrief(w http.ResponseWriter, r *http.Request, ownerID, id string) {
	brief, err := s.Repos.DailyBriefs.Get(r.Context(), ownerID, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"brief": brief})
}
