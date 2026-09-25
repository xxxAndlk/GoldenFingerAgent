// Package llm is the unified LLM API (pi's packages/ai analog):
// provider-agnostic request/response types, one OpenAI-compatible client,
// and a scripted mock for deterministic tests.
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

// ToolCall is a model-requested function invocation. Arguments are UNTRUSTED
// data and must be validated where they enter the system (tool executors).
type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"` // tool result correlation
	Name       string     `json:"name,omitempty"`         // tool name on role=tool
}

// ToolSpec declares a callable tool (JSON Schema parameters).
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
	JSONMode    bool // request JSON object output (extraction fallback)
}

type ChatResponse struct {
	Message      Message
	FinishReason string
	Usage        Usage
}

// Client is the chat-completion abstraction. Implementations must not panic;
// failures are returned as errors (the agent loop turns them into user copy).
type Client interface {
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
	// Stream emits text deltas to onDelta as they arrive and returns the final message.
	Stream(ctx context.Context, req ChatRequest, onDelta func(text string)) (*ChatResponse, error)
}

// Embedder turns text into vectors for memory retrieval.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}
