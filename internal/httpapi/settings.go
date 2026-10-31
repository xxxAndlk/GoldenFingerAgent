package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"goldenfinger/agent/internal/agent"
	"goldenfinger/agent/internal/extsvc/deepseek"
	"goldenfinger/agent/internal/llm/openai"
	"goldenfinger/agent/internal/settings"
)

// llmSettingsDTO 是「一套模型方案」的可传输形态。
// GET 时 api_key 打码；PUT 传空或掩码表示保留原密钥。
type llmSettingsDTO struct {
	BaseURL         string  `json:"base_url"`
	Model           string  `json:"model"`
	APIKey          string  `json:"api_key"`
	HasKey          bool    `json:"has_key"`
	Protocol        string  `json:"protocol"`
	ReasoningEffort string  `json:"reasoning_effort"`
	MaxContext      int     `json:"max_context"`
	MaxOutput       int     `json:"max_output"`
	Temperature     float64 `json:"temperature"`
	SettingsPath    string  `json:"settings_path"`
}

// modelDTO 是保存的模型方案（带 id 与显示名）。
type modelDTO struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	BaseURL         string  `json:"base_url"`
	Model           string  `json:"model"`
	APIKey          string  `json:"api_key"`
	HasKey          bool    `json:"has_key"`
	Protocol        string  `json:"protocol"`
	ReasoningEffort string  `json:"reasoning_effort"`
	MaxContext      int     `json:"max_context"`
	MaxOutput       int     `json:"max_output"`
	Temperature     float64 `json:"temperature"`
	Active          bool    `json:"active"`
}

type searchDTO struct {
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
	HasKey  bool   `json:"has_key"`
}

type relayDTO struct {
	URL string `json:"url"`
}

// settingsEnvelope 一次性返回界面需要的全部设置，减少往返。
type settingsEnvelope struct {
	LLM     llmSettingsDTO `json:"llm"`
	Models  []modelDTO     `json:"models"`
	Search  searchDTO      `json:"search"`
	Relay   relayDTO       `json:"relay"`
	Efforts []string       `json:"efforts"`
}

// currentLLM 将 JSON 运行时设置与旧配置逐项合并，避免首次保存时丢失原密钥。
func (s *Server) currentLLM() settings.LLM {
	ls := s.FallbackLLM
	if s.Settings != nil {
		cur := s.Settings.LLM()
		if cur.BaseURL != "" {
			ls.BaseURL = cur.BaseURL
		}
		if cur.APIKey != "" {
			ls.APIKey = cur.APIKey
		}
		if cur.Model != "" {
			ls.Model = cur.Model
		}
		if cur.Protocol != "" {
			ls.Protocol = cur.Protocol
		}
		if cur.ReasoningEffort != "" {
			ls.ReasoningEffort = cur.ReasoningEffort
		}
		if cur.MaxContext > 0 {
			ls.MaxContext = cur.MaxContext
		}
		if cur.MaxOutput > 0 {
			ls.MaxOutput = cur.MaxOutput
		}
		if cur.Temperature > 0 {
			ls.Temperature = cur.Temperature
		}
	}
	ls.Protocol = openai.NormalizeProtocol(ls.Protocol, ls.BaseURL)
	return ls
}

// effectiveRuntime 每次调用都读取 JSON，因此界面保存和外部编辑均无需重启。
func (s *Server) effectiveRuntime() *agent.Runtime {
	rt := s.Runtime
	if s.Settings == nil {
		return rt
	}
	ls := s.currentLLM()
	if ls.BaseURL == "" || ls.Model == "" {
		return rt
	}
	cp := *rt
	cp.LLM = openai.NewWithProtocol(ls.BaseURL, ls.APIKey, ls.Model, ls.Protocol)
	cp.Model = ls.Model
	cp.Temperature = ls.Temperature
	cp.MaxOutputTokens = ls.MaxOutput
	cp.ReasoningEffort = ls.ReasoningEffort
	if rt.Tools != nil {
		tools := *rt.Tools
		// 联网检索优先使用独立密钥；未单独配置时才复用对话模型的 Key。
		search := s.Settings.Search()
		base, key := search.BaseURL, search.APIKey
		if base == "" {
			base = ls.BaseURL
		}
		if key == "" {
			key = ls.APIKey
		}
		webClient := deepseek.New(base, key, 8)
		tools.Search, tools.Fetch = webClient, webClient
		cp.Tools = &tools
	}
	return &cp
}

func maskKey(k string) string {
	if len(k) <= 4 {
		return "****"
	}
	return "****" + k[len(k)-4:]
}

func isMaskedOrEmpty(v, current string) bool {
	return v == "" || v == maskKey(current)
}

func (s *Server) settingsPath() string {
	if s.Settings == nil {
		return ""
	}
	return s.Settings.Path()
}

func (s *Server) llmSettingsResponse(ls settings.LLM) llmSettingsDTO {
	return llmSettingsDTO{
		BaseURL: ls.BaseURL, Model: ls.Model, APIKey: maskKey(ls.APIKey), HasKey: ls.APIKey != "",
		Protocol:        openai.NormalizeProtocol(ls.Protocol, ls.BaseURL),
		ReasoningEffort: ls.ReasoningEffort, MaxContext: ls.MaxContext, MaxOutput: ls.MaxOutput,
		Temperature: ls.Temperature, SettingsPath: s.settingsPath(),
	}
}

// activeModelID 找出与当前生效设置一致的那套方案（用于列表高亮）。
func (s *Server) activeModelID(ls settings.LLM) string {
	if s.Settings == nil {
		return ""
	}
	for _, m := range s.Settings.Models() {
		if m.BaseURL == ls.BaseURL && m.Model == ls.Model && m.Protocol == ls.Protocol {
			return m.ID
		}
	}
	return ""
}

// modelsResponse 列出已保存的模型方案；一套都没存过时，用当前生效的模型占位，
// 让界面至少有一行可看、可编辑（保存后就会变成真正的方案）。
func (s *Server) modelsResponse(ls settings.LLM) []modelDTO {
	out := []modelDTO{}
	if s.Settings == nil {
		return out
	}
	activeID := s.activeModelID(ls)
	for _, m := range s.Settings.Models() {
		out = append(out, modelDTO{
			ID: m.ID, Name: m.Name, BaseURL: m.BaseURL, Model: m.Model,
			APIKey: maskKey(m.APIKey), HasKey: m.APIKey != "",
			Protocol:        openai.NormalizeProtocol(m.Protocol, m.BaseURL),
			ReasoningEffort: m.ReasoningEffort, MaxContext: m.MaxContext, MaxOutput: m.MaxOutput,
			Temperature: m.Temperature, Active: m.ID != "" && m.ID == activeID,
		})
	}
	if len(out) == 0 && (ls.BaseURL != "" || ls.Model != "") {
		out = append(out, modelDTO{
			ID: "", Name: "当前生效", BaseURL: ls.BaseURL, Model: ls.Model,
			APIKey: maskKey(ls.APIKey), HasKey: ls.APIKey != "",
			Protocol:        openai.NormalizeProtocol(ls.Protocol, ls.BaseURL),
			ReasoningEffort: ls.ReasoningEffort, MaxContext: ls.MaxContext, MaxOutput: ls.MaxOutput,
			Temperature: ls.Temperature, Active: true,
		})
	}
	return out
}

func (s *Server) envelope() settingsEnvelope {
	ls := s.currentLLM()
	env := settingsEnvelope{LLM: s.llmSettingsResponse(ls), Models: s.modelsResponse(ls), Efforts: openai.EffortLevels}
	if s.Settings != nil {
		search := s.Settings.Search()
		env.Search = searchDTO{BaseURL: search.BaseURL, APIKey: maskKey(search.APIKey), HasKey: search.APIKey != ""}
		env.Relay = relayDTO{URL: s.Settings.Relay().URL}
	}
	return env
}

// handleGetLLMSettings 返回「当前生效模型」的扁平结构（旧客户端契约，保持兼容）。
func (s *Server) handleGetLLMSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.llmSettingsResponse(s.currentLLM()))
}

// handleGetSettings 返回界面所需的完整设置：当前模型 + 模型方案 + 搜索 + 中继。
func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.envelope())
}

// handlePutLLMSettings 更新「当前生效」的模型与生成参数。
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
	req.BaseURL = strings.TrimSpace(req.BaseURL)
	req.Model = strings.TrimSpace(req.Model)
	if req.BaseURL == "" || req.Model == "" {
		writeError(w, 400, "base_url and model are required")
		return
	}
	cur := s.currentLLM()
	next := settings.LLM{
		BaseURL: req.BaseURL, Model: req.Model, APIKey: req.APIKey,
		Protocol:        openai.NormalizeProtocol(req.Protocol, req.BaseURL),
		ReasoningEffort: normalizeEffortInput(req.ReasoningEffort),
		MaxContext:      clampInt(req.MaxContext, 0, 2000000),
		MaxOutput:       clampInt(req.MaxOutput, 0, 200000),
		Temperature:     clampFloat(req.Temperature, 0, 2),
	}
	if isMaskedOrEmpty(next.APIKey, cur.APIKey) {
		next.APIKey = cur.APIKey
	}
	if err := s.Settings.SaveLLM(next); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	log.Printf("[settings] active model updated: file=%s base=%s model=%s effort=%s max_out=%d key=%s",
		s.Settings.Path(), next.BaseURL, next.Model, next.ReasoningEffort, next.MaxOutput, maskKey(next.APIKey))
	writeJSON(w, 200, s.envelope())
}

// handleSaveModel 新建或更新一套模型方案；activate=true 时立即切为当前生效。
func (s *Server) handleSaveModel(w http.ResponseWriter, r *http.Request) {
	if s.Settings == nil {
		writeError(w, 500, "settings store not configured")
		return
	}
	var req modelDTO
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "bad request body")
		return
	}
	req.BaseURL = strings.TrimSpace(req.BaseURL)
	req.Model = strings.TrimSpace(req.Model)
	req.Name = strings.TrimSpace(req.Name)
	if req.BaseURL == "" || req.Model == "" {
		writeError(w, 400, "base_url and model are required")
		return
	}
	if req.Name == "" {
		req.Name = req.Model
	}
	var saved settings.Model
	currentKey := s.currentLLM().APIKey
	err := s.Settings.Save(func(f *settings.Shape) {
		idx := -1
		for i := range f.Models {
			if f.Models[i].ID == req.ID && req.ID != "" {
				idx = i
				break
			}
		}
		model := settings.Model{
			ID: req.ID, Name: req.Name, BaseURL: req.BaseURL, Model: req.Model,
			Protocol:        openai.NormalizeProtocol(req.Protocol, req.BaseURL),
			ReasoningEffort: normalizeEffortInput(req.ReasoningEffort),
			MaxContext:      clampInt(req.MaxContext, 0, 2000000),
			MaxOutput:       clampInt(req.MaxOutput, 0, 200000),
			Temperature:     clampFloat(req.Temperature, 0, 2),
			APIKey:          req.APIKey,
		}
		if model.ID == "" {
			model.ID = newModelID()
		}
		if idx >= 0 {
			// 沿用原密钥（界面回传的是掩码）。
			if isMaskedOrEmpty(model.APIKey, f.Models[idx].APIKey) {
				model.APIKey = f.Models[idx].APIKey
			}
			f.Models[idx] = model
		} else {
			// 新建方案时未填密钥 = 沿用当前生效的密钥，避免存出一套用不了的方案。
			if model.APIKey == maskKey("") {
				model.APIKey = ""
			}
			if model.APIKey == "" {
				model.APIKey = currentKey
			}
			f.Models = append(f.Models, model)
		}
		saved = model
		if req.Active {
			f.LLM = settings.LLM{
				BaseURL: model.BaseURL, Model: model.Model, APIKey: model.APIKey, Protocol: model.Protocol,
				ReasoningEffort: model.ReasoningEffort, MaxContext: model.MaxContext,
				MaxOutput: model.MaxOutput, Temperature: model.Temperature,
			}
		}
	})
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	log.Printf("[settings] model saved: id=%s name=%s model=%s active=%v", saved.ID, saved.Name, saved.Model, req.Active)
	writeJSON(w, 200, s.envelope())
}

// handleActivateModel 切换当前生效的模型方案。
func (s *Server) handleActivateModel(w http.ResponseWriter, r *http.Request) {
	if s.Settings == nil {
		writeError(w, 500, "settings store not configured")
		return
	}
	id := r.PathValue("id")
	var target settings.Model
	found := false
	for _, m := range s.Settings.Models() {
		if m.ID == id {
			target, found = m, true
			break
		}
	}
	if !found {
		writeError(w, 404, "model not found")
		return
	}
	err := s.Settings.Save(func(f *settings.Shape) {
		f.LLM = settings.LLM{
			BaseURL: target.BaseURL, Model: target.Model, APIKey: target.APIKey, Protocol: target.Protocol,
			ReasoningEffort: target.ReasoningEffort, MaxContext: target.MaxContext,
			MaxOutput: target.MaxOutput, Temperature: target.Temperature,
		}
	})
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	log.Printf("[settings] model activated: id=%s name=%s model=%s", target.ID, target.Name, target.Model)
	writeJSON(w, 200, s.envelope())
}

// handleDeleteModel 删除一套方案；删除当前生效的方案时不清空生效设置。
func (s *Server) handleDeleteModel(w http.ResponseWriter, r *http.Request) {
	if s.Settings == nil {
		writeError(w, 500, "settings store not configured")
		return
	}
	id := r.PathValue("id")
	err := s.Settings.Save(func(f *settings.Shape) {
		out := f.Models[:0]
		for _, m := range f.Models {
			if m.ID != id {
				out = append(out, m)
			}
		}
		f.Models = out
	})
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, s.envelope())
}

// handlePutSearch 保存联网检索的独立凭据。
func (s *Server) handlePutSearch(w http.ResponseWriter, r *http.Request) {
	if s.Settings == nil {
		writeError(w, 500, "settings store not configured")
		return
	}
	var req searchDTO
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "bad request body")
		return
	}
	cur := s.Settings.Search()
	next := settings.Search{BaseURL: strings.TrimSpace(req.BaseURL), APIKey: req.APIKey}
	if next.BaseURL == "" {
		next.BaseURL = "https://api.deepseek.com"
	}
	if isMaskedOrEmpty(next.APIKey, cur.APIKey) {
		next.APIKey = cur.APIKey
	}
	if err := s.Settings.SaveSearch(next); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	log.Printf("[settings] search credentials updated: base=%s key=%s", next.BaseURL, maskKey(next.APIKey))
	writeJSON(w, 200, s.envelope())
}

// handlePutRelay 保存中继地址（桌面端中继客户端在启动时读取 GFA_RELAY_URL，
// 这里保存的值供界面展示与下次启动生效）。
func (s *Server) handlePutRelay(w http.ResponseWriter, r *http.Request) {
	if s.Settings == nil {
		writeError(w, 500, "settings store not configured")
		return
	}
	var req relayDTO
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "bad request body")
		return
	}
	url := strings.TrimSpace(req.URL)
	if url != "" && !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		writeError(w, 400, "中继地址必须以 http:// 或 https:// 开头")
		return
	}
	if err := s.Settings.SaveRelay(settings.Relay{URL: url}); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, s.envelope())
}

func newModelID() string {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return "m-" + strings.ToLower(hex.EncodeToString([]byte{0, 1, 2, 3, 4, 5}))
	}
	return "m-" + hex.EncodeToString(buf)
}

func normalizeEffortInput(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "low", "低":
		return "low"
	case "high", "高":
		return "high"
	case "max", "最高":
		return "max"
	case "medium", "中":
		// 兼容旧配置：界面只保留 low/high/max，旧的 medium 按 high 处理。
		return "high"
	default:
		return ""
	}
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
