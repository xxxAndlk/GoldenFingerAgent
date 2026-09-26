// Package llm 是统一的 LLM API（对应 pi 的 packages/ai）：
// 与供应商无关的请求/响应类型、一个 OpenAI 兼容客户端，
// 以及用于确定性测试的脚本化 mock。
package llm

import (
	"context"
	"encoding/json"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ToolCall 是模型请求的函数调用。参数是 UNTRUSTED
// 数据，必须在进入系统处（工具执行器）进行校验。
type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"` // 工具结果关联
	Name       string     `json:"name,omitempty"`         // role=tool 时的工具名
}

// ToolSpec 声明一个可调用工具（JSON Schema 参数）。
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

type ChatRequest struct {
	Model       string
	Messages    []Message
	Tools       []ToolSpec
	Temperature float64
	MaxTokens   int
	JSONMode    bool // 请求 JSON 对象输出（抽取回退）
}

type ChatResponse struct {
	Message      Message
	FinishReason string
	Usage        Usage
}

// Client 是聊天补全抽象。实现不得 panic；
// 失败以 error 返回（agent 循环会将其转成给用户的话术）。
type Client interface {
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
	// Stream 将文本增量实时推送给 onDelta，并返回最终消息。
	Stream(ctx context.Context, req ChatRequest, onDelta func(text string)) (*ChatResponse, error)
}

// Embedder 将文本转为向量用于记忆检索。
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}
