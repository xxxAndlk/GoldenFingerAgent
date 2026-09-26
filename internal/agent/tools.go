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

// ResultStatus 对工具执行结果进行分类。
type ResultStatus string

const (
	StatusOK      ResultStatus = "ok"
	StatusClarify ResultStatus = "clarify"
	StatusDiscard ResultStatus = "discarded"
	StatusError   ResultStatus = "error"
)

// ToolResult 以 DATA 形式（绝非指令）回馈给模型，也可能
// 用澄清问题短路本轮对话。
type ToolResult struct {
	Status   ResultStatus       `json:"status"`
	Data     any                `json:"data,omitempty"`
	Question string             `json:"question,omitempty"` // 面向用户的澄清（中文）
	Pending  *nlu.PendingAction `json:"-"`
	Cards    []Card             `json:"-"`
	Trace    string             `json:"trace,omitempty"`
}

// Tool 是一个可调用能力。参数以原始（UNTRUSTED）JSON 从模型到达，
// 必须在 Execute 内部校验。
type Tool interface {
	Spec() llm.ToolSpec
	Execute(ctx context.Context, args json.RawMessage, tc *ToolContext) (ToolResult, error)
}

// ToolContext 为工具提供领域服务与轮次状态访问。
type ToolContext struct {
	Session     *Session
	Runtime     *Runtime
	SourceMsgID *string
}

// Registry 把工具名映射到实现，并向模型暴露规格。
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

// Specs 返回聊天请求所需的工具声明。
func (r *Registry) Specs() []llm.ToolSpec {
	out := make([]llm.ToolSpec, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.tools[name].Spec())
	}
	return out
}

// Execute 派发一次工具调用。未知工具产生错误结果
// （作为数据回馈给模型）。
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
