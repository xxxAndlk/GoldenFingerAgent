package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"goldenfinger/agent/internal/inbox"
	"goldenfinger/agent/internal/store"
)

// 通知中心 /api/inbox 端点（JSON 字段与路径逐字对齐跨域契约）。
// Inbox 为 nil 时一律 503。

type inboxEventDTO struct {
	ID        string `json:"id"`
	SourcePkg string `json:"source_pkg"`
	AppName   string `json:"app_name"`
	Title     string `json:"title"`
	Text      string `json:"text"`
	Kind      string `json:"kind"`
	ClueType  string `json:"clue_type"`
	ClueValue string `json:"clue_value"`
	PostedAt  string `json:"posted_at"`
}

func toInboxEventDTO(e store.InboxEvent) inboxEventDTO {
	return inboxEventDTO{
		ID: e.ID, SourcePkg: e.SourcePkg, AppName: e.AppName,
		Title: e.Title, Text: e.Text, Kind: e.Kind,
		ClueType: e.ClueType, ClueValue: e.ClueValue,
		PostedAt: e.PostedAt.Format(time.RFC3339),
	}
}

// inboxWatchDTO 字段与契约逐字对齐（不含 owner_user_id 内部字段）。
type inboxWatchDTO struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Keyword   string `json:"keyword"`
	SourcePkg string `json:"source_pkg"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
}

func toInboxWatchDTO(w store.InboxWatch) inboxWatchDTO {
	return inboxWatchDTO{
		ID: w.ID, Title: w.Title, Keyword: w.Keyword, SourcePkg: w.SourcePkg,
		Status: w.Status, CreatedAt: w.CreatedAt.Format(time.RFC3339),
	}
}

func (s *Server) requireInbox(w http.ResponseWriter) *inbox.Service {
	if s.Inbox == nil {
		writeError(w, http.StatusServiceUnavailable, "inbox service unavailable")
		return nil
	}
	return s.Inbox
}

// POST /api/inbox/events — 批量上报，返回实际入库条数（重复去重）。
func (s *Server) handleInboxIngest(w http.ResponseWriter, r *http.Request) {
	svc := s.requireInbox(w)
	if svc == nil {
		return
	}
	user := s.currentUser(w, r)
	if user == nil {
		return
	}
	var req struct {
		Events []inbox.Input `json:"events"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	n, err := svc.Ingest(r.Context(), user.ID, req.Events)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"ingested": n})
}

// GET /api/inbox/events?kind=normal|clue|all&limit=50
func (s *Server) handleInboxEvents(w http.ResponseWriter, r *http.Request) {
	svc := s.requireInbox(w)
	if svc == nil {
		return
	}
	user := s.currentUser(w, r)
	if user == nil {
		return
	}
	q := r.URL.Query()
	limit := 50
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 {
		limit = n
	}
	events, err := svc.Repos.InboxEvents.List(r.Context(), user.ID, q.Get("kind"), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	dtos := make([]inboxEventDTO, 0, len(events))
	for _, e := range events {
		dtos = append(dtos, toInboxEventDTO(e))
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": dtos})
}

// GET /api/inbox/search?q=… — mode 如实回传 vector|keyword。
func (s *Server) handleInboxSearch(w http.ResponseWriter, r *http.Request) {
	svc := s.requireInbox(w)
	if svc == nil {
		return
	}
	user := s.currentUser(w, r)
	if user == nil {
		return
	}
	limit := 20
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 {
		limit = n
	}
	out, err := svc.Search(r.Context(), user.ID, r.URL.Query().Get("q"), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type hitDTO struct {
		ID       string  `json:"id"`
		AppName  string  `json:"app_name"`
		Title    string  `json:"title"`
		Text     string  `json:"text"`
		PostedAt string  `json:"posted_at"`
		Score    float64 `json:"score"`
	}
	hits := make([]hitDTO, 0, len(out.Events))
	for _, e := range out.Events {
		hits = append(hits, hitDTO{
			ID: e.ID, AppName: e.AppName, Title: e.Title, Text: e.Text,
			PostedAt: e.PostedAt.Format(time.RFC3339), Score: e.Score,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"mode": out.Mode, "results": hits})
}

// POST /api/inbox/snooze — {"event_id","minutes"} 优先，也接受 "fire_at":"RFC3339"。
func (s *Server) handleInboxCreateSnooze(w http.ResponseWriter, r *http.Request) {
	svc := s.requireInbox(w)
	if svc == nil {
		return
	}
	user := s.currentUser(w, r)
	if user == nil {
		return
	}
	var req struct {
		EventID string `json:"event_id"`
		Minutes int    `json:"minutes"`
		FireAt  string `json:"fire_at"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	var fireAt *time.Time
	if req.FireAt != "" {
		t, err := time.Parse(time.RFC3339, req.FireAt)
		if err != nil {
			writeError(w, http.StatusBadRequest, "fire_at must be RFC3339")
			return
		}
		fireAt = &t
	}
	sn, err := svc.CreateSnooze(r.Context(), user.ID, req.EventID, req.Minutes, fireAt)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "event not found")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"id": sn.ID, "fire_at": sn.FireAt.Format(time.RFC3339),
	})
}

// GET /api/inbox/snooze
func (s *Server) handleInboxListSnoozes(w http.ResponseWriter, r *http.Request) {
	svc := s.requireInbox(w)
	if svc == nil {
		return
	}
	user := s.currentUser(w, r)
	if user == nil {
		return
	}
	views, err := svc.Repos.InboxSnoozes.ListWithEvent(r.Context(), user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	dtos := make([]store.InboxSnoozeView, 0, len(views))
	for _, v := range views {
		v.FireAt = v.FireAt.UTC() // 保持零值安全；传输层统一 RFC3339 由 json 编码
		dtos = append(dtos, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"snoozes": dtos})
}

// POST /api/inbox/watches — {"title","keyword","source_pkg"}。
func (s *Server) handleInboxCreateWatch(w http.ResponseWriter, r *http.Request) {
	svc := s.requireInbox(w)
	if svc == nil {
		return
	}
	user := s.currentUser(w, r)
	if user == nil {
		return
	}
	var req struct {
		Title     string `json:"title"`
		Keyword   string `json:"keyword"`
		SourcePkg string `json:"source_pkg"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	watch, err := svc.CreateWatch(r.Context(), user.ID, req.Title, req.Keyword, req.SourcePkg)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toInboxWatchDTO(*watch))
}

// GET /api/inbox/watches
func (s *Server) handleInboxListWatches(w http.ResponseWriter, r *http.Request) {
	svc := s.requireInbox(w)
	if svc == nil {
		return
	}
	user := s.currentUser(w, r)
	if user == nil {
		return
	}
	watches, err := svc.Repos.InboxWatches.List(r.Context(), user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	dtos := make([]inboxWatchDTO, 0, len(watches))
	for _, wt := range watches {
		dtos = append(dtos, toInboxWatchDTO(wt))
	}
	writeJSON(w, http.StatusOK, map[string]any{"watches": dtos})
}

// DELETE /api/inbox/watches/{id}
func (s *Server) handleInboxCancelWatch(w http.ResponseWriter, r *http.Request) {
	svc := s.requireInbox(w)
	if svc == nil {
		return
	}
	user := s.currentUser(w, r)
	if user == nil {
		return
	}
	ok, err := svc.CancelWatch(r.Context(), user.ID, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "watch not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
