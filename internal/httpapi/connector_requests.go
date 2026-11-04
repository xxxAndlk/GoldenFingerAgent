package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"goldenfinger/agent/internal/collection"
)

// handleConnectorRequests 让本机桌面端轮询短命配置命令。
func (s *Server) handleConnectorRequests(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	if s.Collection == nil {
		writeError(w, http.StatusServiceUnavailable, "采集渠道管理未装配")
		return
	}
	channel := strings.TrimSpace(r.URL.Query().Get("channel"))
	writeJSON(w, http.StatusOK, map[string]any{"requests": s.Collection.Pending(u.ID, channel)})
}

// handleCompleteConnectorRequest 接收桌面真实执行结果；owner 必须一致。
func (s *Server) handleCompleteConnectorRequest(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(w, r)
	if u == nil {
		return
	}
	if s.Collection == nil {
		writeError(w, http.StatusServiceUnavailable, "采集渠道管理未装配")
		return
	}
	var result collection.ConnectorResult
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10)).Decode(&result); err != nil {
		writeError(w, http.StatusBadRequest, "bad request body")
		return
	}
	if !s.Collection.Complete(u.ID, r.PathValue("id"), result) {
		writeError(w, http.StatusNotFound, "请求不存在、已完成或不属于当前用户")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"completed": true})
}
