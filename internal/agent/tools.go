package agent

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	"goldenfinger/agent/internal/llm"
	"goldenfinger/agent/internal/nlu"
)

// ResultStatus classifies a tool execution outcome.
type ResultStatus string

const (
	StatusOK      ResultStatus = "ok"
	StatusClarify ResultStatus = "clarify"
	StatusDiscard ResultStatus = "discarded"
	StatusError   ResultStatus = "error"
)

// ToolResult feeds back to the model as DATA (never instructions) and may
// short-circuit the turn with a clarify question.
type ToolResult struct {
	Status   ResultStatus       `json:"status"`
	Data     any                `json:"data,omitempty"`
	Question string             `json:"question,omitempty"` // user-facing clarify (Chinese)
	Pending  *nlu.PendingAction `json:"-"`
	Cards    []Card             `json:"-"`
	Trace    string             `json:"trace,omitempty"`
}

// Tool is one callable capability. Args arrive as raw (UNTRUSTED) JSON from
// the model and must be validated inside Execute.
type Tool interface {
	Spec() llm.ToolSpec
	Execute(ctx context.Context, args json.RawMessage, tc *ToolContext) (ToolResult, error)
}

// ToolContext gives tools access to domain services and turn state.
// ToolContext gives tools access to domain services and turn state.
type ToolContext struct {
	Session     *Session
	Runtime     *Runtime
	SourceMsgID *string
}

// Registry maps tool names to implementations and exposes specs to the model.
type Registry struct {
	tools map[string]Tool
	order []string
}

func NewRegistry(tools ...Tool) *Registry {
	r := &Registry{tools: make(map[string]Tool)}
	for _, t := range tools {
		name := t.Spec().Name
		if _, exists := r.tools[name]; !exists {
			r.order = append(r.order, name)
		}
		r.tools[name] = t
	}
	return r
}

// Specs returns tool declarations for the chat request.
func (r *Registry) Specs() []llm.ToolSpec {
	out := make([]llm.ToolSpec, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.tools[name].Spec())
	}
	return out
}

// Execute dispatches one tool call. Unknown tools yield an error result
// (fed back to the model as data).
func (r *Registry) Execute(ctx context.Context, call llm.ToolCall, tc *ToolContext) (ToolResult, error) {
	t, ok := r.tools[call.Name]
	if !ok {
		return ToolResult{
			Status: StatusError,
			Data:   map[string]string{"error": "unknown tool: " + call.Name},
			Trace:  call.Name + " → unknown",
		}, nil
	}
	start := time.Now()
	res, err := t.Execute(ctx, call.Arguments, tc)
	if err != nil {
		log.Printf("[tool] %s → error: %v (%s)", call.Name, err, time.Since(start).Round(time.Millisecond))
		return ToolResult{
			Status: StatusError,
			Data:   map[string]string{"error": err.Error()},
			Trace:  call.Name + " → error",
		}, nil
	}
	log.Printf("[tool] %s → %s (%s)", call.Name, res.Status, time.Since(start).Round(time.Millisecond))
	if res.Trace == "" {
		res.Trace = call.Name + " → " + string(res.Status)
	}
	return res, nil
}

func toolNames(calls []llm.ToolCall) string {
	names := make([]string, 0, len(calls))
	for _, c := range calls {
		names = append(names, c.Name)
	}
	return "[" + strings.Join(names, ", ") + "]"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
