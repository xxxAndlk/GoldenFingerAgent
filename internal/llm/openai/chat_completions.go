package openai

import (
	"context"
	"encoding/json"
	"fmt"

	"goldenfinger/agent/internal/llm"
)

type completionMessage struct {
	Role       llm.Role `json:"role"`
	Content    string   `json:"content,omitempty"`
	ToolCallID string   `json:"tool_call_id,omitempty"`
	Name       string   `json:"name,omitempty"`
	ToolCalls  []struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls,omitempty"`
}

type completionTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type completionRequest struct {
	Model           string              `json:"model"`
	Messages        []completionMessage `json:"messages"`
	Tools           []completionTool    `json:"tools,omitempty"`
	Temperature     float64             `json:"temperature,omitempty"`
	MaxTokens       int                 `json:"max_tokens,omitempty"`
	ReasoningEffort string              `json:"reasoning_effort,omitempty"`
	ResponseFormat  *struct {
		Type string `json:"type"`
	} `json:"response_format,omitempty"`
}

type completionResponse struct {
	Choices []struct {
		Message      completionMessage `json:"message"`
		FinishReason string            `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens          int  `json:"prompt_tokens"`
		CompletionTokens      int  `json:"completion_tokens"`
		PromptCacheHitTokens  *int `json:"prompt_cache_hit_tokens"`
		PromptCacheMissTokens *int `json:"prompt_cache_miss_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (c *Client) buildCompletionRequest(req llm.ChatRequest) completionRequest {
	model := req.Model
	if model == "" {
		model = c.Model
	}
	out := completionRequest{Model: model, Temperature: req.Temperature, MaxTokens: req.MaxTokens}
	out.ReasoningEffort = normalizeEffort(req.ReasoningEffort)
	for _, m := range req.Messages {
		cm := completionMessage{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID, Name: m.Name}
		for _, tc := range m.ToolCalls {
			call := struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			}{ID: tc.ID, Type: "function"}
			call.Function.Name = tc.Name
			call.Function.Arguments = string(tc.Arguments)
			cm.ToolCalls = append(cm.ToolCalls, call)
		}
		out.Messages = append(out.Messages, cm)
	}
	for _, t := range req.Tools {
		tool := completionTool{Type: "function"}
		tool.Function.Name = t.Name
		tool.Function.Description = t.Description
		tool.Function.Parameters = t.Parameters
		out.Tools = append(out.Tools, tool)
	}
	if req.JSONMode {
		out.ResponseFormat = &struct {
			Type string `json:"type"`
		}{Type: "json_object"}
	}
	return out
}

func (c *Client) chatCompletions(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	raw, err := c.doJSON(ctx, "/chat/completions", c.buildCompletionRequest(req))
	if err != nil {
		return nil, err
	}
	var cr completionResponse
	if err := json.Unmarshal(raw, &cr); err != nil {
		return nil, fmt.Errorf("parse chat completions reply: %w", err)
	}
	if cr.Error != nil {
		return nil, fmt.Errorf("llm error: %s", cr.Error.Message)
	}
	if len(cr.Choices) == 0 {
		return nil, fmt.Errorf("llm: empty chat completions choices")
	}
	choice := cr.Choices[0]
	msg := llm.Message{Role: llm.RoleAssistant, Content: choice.Message.Content}
	for _, tc := range choice.Message.ToolCalls {
		msg.ToolCalls = append(msg.ToolCalls, llm.ToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: json.RawMessage(tc.Function.Arguments)})
	}
	usage := llm.Usage{PromptTokens: cr.Usage.PromptTokens, CompletionTokens: cr.Usage.CompletionTokens}
	if cr.Usage.PromptCacheHitTokens != nil && cr.Usage.PromptCacheMissTokens != nil {
		usage.PromptCacheHitTokens = *cr.Usage.PromptCacheHitTokens
		usage.PromptCacheMissTokens = *cr.Usage.PromptCacheMissTokens
		usage.CacheReported = true
	}
	return &llm.ChatResponse{Message: msg, FinishReason: choice.FinishReason, Usage: usage}, nil
}
