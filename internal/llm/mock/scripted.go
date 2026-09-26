// Package mock 提供用于测试的确定性脚本化 LLM 实现。
package mock

import (
	"context"
	"fmt"
	"sync"

	"goldenfinger/agent/internal/llm"
)

// Scripted 按顺序回放一串预置的 ChatResponses。请求会被
// 记录下来，便于测试断言 agent 发送了什么。
type Scripted struct {
	mu        sync.Mutex
	Responses []llm.ChatResponse
	Calls     []llm.ChatRequest
	// Matcher 可选：拒绝请求（返回 error）而非回放。
	Matcher func(req llm.ChatRequest) error
}

func New(responses ...llm.ChatResponse) *Scripted {
	return &Scripted{Responses: responses}
}

func (s *Scripted) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Calls = append(s.Calls, req)
	if s.Matcher != nil {
		if err := s.Matcher(req); err != nil {
			return nil, err
		}
	}
	if len(s.Responses) == 0 {
		return nil, fmt.Errorf("mock llm: script exhausted")
	}
	resp := s.Responses[0]
	s.Responses = s.Responses[1:]
	return &resp, nil
}

func (s *Scripted) Stream(ctx context.Context, req llm.ChatRequest, onDelta func(string)) (*llm.ChatResponse, error) {
	resp, err := s.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	if onDelta != nil && resp.Message.Content != "" {
		onDelta(resp.Message.Content)
	}
	return resp, nil
}

// FixedEmbedder 对每段文本返回固定长度的确定性向量。
type FixedEmbedder struct {
	Dim int
	// Vec 可选：按文本覆盖生成的向量。
	Vec map[string][]float32
}

func (f *FixedEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	dim := f.Dim
	if dim == 0 {
		dim = 3
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		if v, ok := f.Vec[t]; ok {
			out[i] = v
			continue
		}
		v := make([]float32, dim)
		// 由文本字节派生的确定性伪向量。
		var sum float32
		for _, b := range t {
			sum += float32(b)
		}
		v[0] = sum
		if dim > 1 {
			v[1] = float32(len(t))
		}
		out[i] = v
	}
	return out, nil
}

// ---- test helpers ----

// TextResponse 构造一个纯文本的助手回复。
func TextResponse(content string) llm.ChatResponse {
	return llm.ChatResponse{
		Message:      llm.Message{Role: llm.RoleAssistant, Content: content},
		FinishReason: "stop",
	}
}

// ToolResponse 构造一个调用单个工具的助手回复。
func ToolResponse(id, name, argsJSON string) llm.ChatResponse {
	return llm.ChatResponse{
		Message: llm.Message{
			Role: llm.RoleAssistant,
			ToolCalls: []llm.ToolCall{
				{ID: id, Name: name, Arguments: []byte(argsJSON)},
			},
		},
		FinishReason: "tool_calls",
	}
}
