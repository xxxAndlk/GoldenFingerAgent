// Package mock provides deterministic scripted LLM implementations for tests.
package mock

import (
	"context"
	"fmt"
	"sync"

	"goldenfinger/agent/internal/llm"
)

// Scripted replays a queue of canned ChatResponses in order. Requests are
// recorded so tests can assert on what the agent sent.
type Scripted struct {
	mu        sync.Mutex
	Responses []llm.ChatResponse
	Calls     []llm.ChatRequest
	// Matcher optionally rejects a request (returns error) instead of replaying.
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

// FixedEmbedder returns a constant-size deterministic vector per text.
type FixedEmbedder struct {
	Dim int
	// Vec optionally overrides generated vectors by text.
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
		// Deterministic pseudo-vector derived from the text bytes.
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

// TextResponse builds a plain text assistant reply.
func TextResponse(content string) llm.ChatResponse {
	return llm.ChatResponse{
		Message:      llm.Message{Role: llm.RoleAssistant, Content: content},
		FinishReason: "stop",
	}
}

// ToolResponse builds an assistant reply that calls one tool.
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
