package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"goldenfinger/agent/internal/agenthub"
)

func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	if s.Agents == nil {
		writeError(w, 503, "Agent 调度未配置")
		return
	}
	refresh := r.URL.Query().Get("refresh")
	if refresh == "1" || strings.EqualFold(refresh, "true") {
		writeJSON(w, 200, map[string]any{"agents": s.Agents.Refresh(r.Context())})
		return
	}
	writeJSON(w, 200, map[string]any{"agents": s.Agents.Scan(r.Context())})
}

func (s *Server) handleOpenAgent(w http.ResponseWriter, r *http.Request) {
	if s.Agents == nil {
		writeError(w, 503, "Agent 调度未配置")
		return
	}
	var req struct {
		AgentID string `json:"agent_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "bad request body")
		return
	}
	result, err := s.Agents.Open(r.Context(), req.AgentID)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, result)
}

func (s *Server) handleDelegateAgent(w http.ResponseWriter, r *http.Request) {
	if s.Agents == nil {
		writeError(w, 503, "Agent 调度未配置")
		return
	}
	var req struct {
		AgentID string `json:"agent_id"`
		Task    string `json:"task"`
		WorkDir string `json:"work_dir"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "bad request body")
		return
	}
	result, err := s.Agents.DelegateAt(r.Context(), req.AgentID, req.Task, req.WorkDir)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, result)
}

// handleClarifyAgent 把用户对 BMA 计划确认/提问的答复发回原会话。
func (s *Server) handleClarifyAgent(w http.ResponseWriter, r *http.Request) {
	if s.Agents == nil {
		writeError(w, 503, "Agent 调度未配置")
		return
	}
	var req struct {
		SessionID  string `json:"session_id"`
		Answer     string `json:"answer"`
		QuestionID string `json:"question_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "bad request body")
		return
	}
	result, err := s.Agents.AnswerBMA(r.Context(), req.SessionID, req.Answer, req.QuestionID)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, result)
}

func (s *Server) handleRegisterA2A(w http.ResponseWriter, r *http.Request) {
	if s.Agents == nil {
		writeError(w, 503, "Agent 调度未配置")
		return
	}
	var req agenthub.Endpoint
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "bad request body")
		return
	}
	agent, err := s.Agents.RegisterA2A(req)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, agent)
}

func (s *Server) handleAgentProviders(w http.ResponseWriter, r *http.Request) {
	if s.Agents == nil || s.Agents.CCSwitch == nil {
		writeError(w, 503, "CC Switch 未配置")
		return
	}
	providers, err := s.Agents.CCSwitch.Providers(r.Context(), r.URL.Query().Get("app_type"))
	if err != nil {
		writeError(w, 404, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"providers": providers})
}

func (s *Server) handleSwitchAgentProvider(w http.ResponseWriter, r *http.Request) {
	if s.Agents == nil || s.Agents.CCSwitch == nil {
		writeError(w, 503, "CC Switch 未配置")
		return
	}
	var req struct {
		AppType    string `json:"app_type"`
		ProviderID string `json:"provider_id"`
		Confirmed  bool   `json:"confirmed"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "bad request body")
		return
	}
	if !req.Confirmed {
		writeError(w, 409, "切换供应商需要明确确认")
		return
	}
	if req.AppType != "claude" {
		writeError(w, 409, "当前仅支持直接切换 Claude Code；其他 Agent 请通过 CC Switch 界面切换")
		return
	}
	provider, err := s.Agents.CCSwitch.SwitchClaude(r.Context(), req.ProviderID)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, provider)
}
