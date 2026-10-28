package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"goldenfinger/agent/internal/memory"
)

type captureEpisodeRequest struct {
	Text          string    `json:"text"`
	ContextBefore string    `json:"context_before"`
	ContextAfter  string    `json:"context_after"`
	Source        string    `json:"source"`
	CapturedAt    time.Time `json:"captured_at"`
}

// handleCaptureEpisode 接收安卓持续听写的最终文本。音频不经过此端点，
// 只把原话和附近的转写上下文保存到本机 SQLite。
func (s *Server) handleCaptureEpisode(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	if s.Runtime == nil || s.Runtime.Tools == nil || s.Runtime.Tools.Memory == nil {
		writeError(w, 503, "memory service unavailable")
		return
	}
	var req captureEpisodeRequest
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "bad request body")
		return
	}
	result, err := s.Runtime.Tools.Memory.CaptureEpisode(r.Context(), memory.CaptureInput{
		OwnerUserID: u.ID, Text: req.Text,
		ContextBefore: req.ContextBefore, ContextAfter: req.ContextAfter,
		Source: req.Source, CapturedAt: req.CapturedAt,
	})
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, result)
}

// handleSearchEpisodes 是记忆召回的调试/客户端入口；对话中的 search_memory
// 工具走同一个 Service.Search，因此两条链路拥有完全一致的关键词降级行为。
func (s *Server) handleSearchEpisodes(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	if s.Runtime == nil || s.Runtime.Tools == nil || s.Runtime.Tools.Memory == nil {
		writeError(w, 503, "memory service unavailable")
		return
	}
	query := r.URL.Query().Get("q")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	results, err := s.Runtime.Tools.Memory.Search(r.Context(), u.ID, query, limit)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"results": results})
}
