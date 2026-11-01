package httpapi

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"goldenfinger/agent/internal/llm"
)

type summarizeRequest struct {
	Context string `json:"context"`
	Focus   string `json:"focus,omitempty"`
}

type summarizeResponse struct {
	Reply string `json:"reply"`
}

const summarizeSystemPrompt = "你是工作轨迹总结子代理。只根据提供的工作轨迹数据，用中文简洁陈述事实：做了什么、有什么进展或结果、还有什么在继续。不要调用工具，不要向用户提问，不要编造数据之外的内容，不要输出建议。"

// handleSummarize 是一次独立的无工具子代理调用：主对话只接收压缩后的短总结，
// 不把当天轨迹和大量阶段总结塞进主 Agent 上下文。
func (s *Server) handleSummarize(w http.ResponseWriter, r *http.Request) {
	var req summarizeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request body")
		return
	}
	contextText := strings.TrimSpace(req.Context)
	if contextText == "" {
		writeError(w, http.StatusBadRequest, "context is required")
		return
	}
	if len(contextText) > 24000 {
		contextText = truncate(contextText, 24000)
	}
	focus := truncate(strings.TrimSpace(req.Focus), 2000)
	userText := "工作轨迹数据：\n" + contextText
	if focus != "" {
		userText = "用户问题：" + focus + "\n\n" + userText
	}

	rt := s.effectiveRuntime()
	resp, err := rt.LLM.Chat(r.Context(), llm.ChatRequest{
		Model:           rt.Model,
		Messages:        []llm.Message{{Role: llm.RoleSystem, Content: summarizeSystemPrompt}, {Role: llm.RoleUser, Content: userText}},
		Temperature:     0.2,
		MaxTokens:       2048,
		ReasoningEffort: rt.ReasoningEffort,
	})
	if err != nil {
		log.Printf("[summarize] sub-agent failed: %v", err)
		writeError(w, http.StatusBadGateway, "总结子代理暂时不可用")
		return
	}
	reply := strings.TrimSpace(resp.Message.Content)
	if reply == "" {
		writeError(w, http.StatusBadGateway, "总结子代理没有返回内容")
		return
	}
	writeJSON(w, http.StatusOK, summarizeResponse{Reply: reply})
}
