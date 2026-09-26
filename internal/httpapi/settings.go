package httpapi

import (
	"encoding/json"
	"log"
	"net/http"

	"goldenfinger/agent/internal/agent"
	"goldenfinger/agent/internal/llm/openai"
	"goldenfinger/agent/internal/settings"
)

type llmSettingsDTO struct {
	BaseURL string `json:"base_url"`
	Model   string `json:"model"`
	APIKey  string `json:"api_key"` // GET 时打码；PUT 传空表示保留已存密钥
	HasKey  bool   `json:"has_key"`
}

// effectiveRuntime 返回一个 LLM 客户端取自 JSON 设置文件的运行时；
// 设置文件配置了模型时用它，否则回退到 config.yaml。
func (s *Server) effectiveRuntime() *agent.Runtime {
	rt := s.Runtime
	if s.Settings == nil {
		return rt
	}
	ls := s.Settings.LLM()
	if ls.BaseURL == "" || ls.APIKey == "" || ls.Model == "" {
		return rt
	}
	cp := *rt
	cp.LLM = openai.New(ls.BaseURL, ls.APIKey, ls.Model)
	cp.Model = ls.Model
	return &cp
}

func maskKey(k string) string {
	if len(k) <= 4 {
		return "****"
	}
	return "****" + k[len(k)-4:]
}

func (s *Server) handleGetLLMSettings(w http.ResponseWriter, r *http.Request) {
	ls := s.FallbackLLM
	if s.Settings != nil {
		if cur := s.Settings.LLM(); cur.BaseURL != "" || cur.APIKey != "" || cur.Model != "" {
			ls = cur
		}
	}
	writeJSON(w, 200, llmSettingsDTO{
		BaseURL: ls.BaseURL,
		Model:   ls.Model,
		APIKey:  maskKey(ls.APIKey),
		HasKey:  ls.APIKey != "",
	})
}

func (s *Server) handlePutLLMSettings(w http.ResponseWriter, r *http.Request) {
	if s.Settings == nil {
		writeError(w, 500, "settings store not configured")
		return
	}
	var req llmSettingsDTO
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "bad request body")
		return
	}
	if req.BaseURL == "" || req.Model == "" {
		writeError(w, 400, "base_url and model are required")
		return
	}
	cur := s.Settings.LLM()
	next := settings.LLM{BaseURL: req.BaseURL, Model: req.Model, APIKey: req.APIKey}
	if next.APIKey == "" || next.APIKey == maskKey(cur.APIKey) {
		next.APIKey = cur.APIKey // 未变更
	}
	if err := s.Settings.SaveLLM(next); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	log.Printf("[settings] llm updated: base=%s model=%s key=%s", next.BaseURL, next.Model, maskKey(next.APIKey))
	writeJSON(w, 200, llmSettingsDTO{BaseURL: next.BaseURL, Model: next.Model, APIKey: maskKey(next.APIKey), HasKey: next.APIKey != ""})
}
