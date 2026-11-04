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
	StatusLimit   ResultStatus = "limit" // 达到单任务步骤/预算上限（给人话）
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

// RiskLevel 是工具副作用分级；Profile 只决定上限，ActionGrant 再缩小范围。
type RiskLevel string

const (
	RiskRead          RiskLevel = "read"
	RiskPrepare       RiskLevel = "prepare"
	RiskExternalWrite RiskLevel = "external_write"
	RiskSensitive     RiskLevel = "sensitive"
)

type ToolDescriptor struct {
	Spec          llm.ToolSpec
	Risk          RiskLevel
	Environments  []string
	RequiresGrant bool
	WorkTaskOnly  bool
}

// Registry 把工具名映射到实现，并向模型暴露规格。
type Registry struct {
	tools       map[string]Tool
	descriptors map[string]ToolDescriptor
	order       []string
}

func NewRegistry(tools ...Tool) *Registry {
	r := &Registry{tools: make(map[string]Tool), descriptors: make(map[string]ToolDescriptor)}
	for _, t := range tools {
		desc := classifyTool(t)
		name := desc.Spec.Name
		if _, exists := r.tools[name]; !exists {
			r.order = append(r.order, name)
		}
		r.tools[name] = t
		r.descriptors[name] = desc
	}
	return r
}

func (r *Registry) RegisterDescriptor(desc ToolDescriptor, t Tool) {
	name := desc.Spec.Name
	if _, exists := r.tools[name]; !exists {
		r.order = append(r.order, name)
	}
	r.tools[name] = t
	r.descriptors[name] = desc
}

func (r *Registry) Descriptor(name string) (ToolDescriptor, bool) {
	desc, ok := r.descriptors[name]
	return desc, ok
}

// Specs 返回主聊天所需的工具声明；WorkTaskOnly 工具不暴露给普通对话。
func (r *Registry) Specs() []llm.ToolSpec {
	out := make([]llm.ToolSpec, 0, len(r.order))
	for _, name := range r.order {
		desc := r.descriptors[name]
		if desc.WorkTaskOnly {
			continue
		}
		out = append(out, desc.Spec)
	}
	return out
}

// SpecsFor 按 Profile 过滤工具声明；空 Profile 表示 interactive 主聊天。
func (r *Registry) SpecsFor(profile string) []llm.ToolSpec {
	if profile == "" {
		return r.Specs()
	}
	out := make([]llm.ToolSpec, 0, len(r.order))
	for _, name := range r.order {
		if profileAllows(profile, r.descriptors[name]) {
			out = append(out, r.descriptors[name].Spec)
		}
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
	desc := r.descriptors[call.Name]
	auth, denied := authorizeTool(ctx, call, tc, desc)
	if denied != nil {
		return *denied, nil
	}
	start := time.Now()
	res, err := t.Execute(ctx, call.Arguments, tc)
	if auth != nil {
		auth.finish(ctx, desc, res, err)
	}
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

func classifyTool(t Tool) ToolDescriptor {
	spec := t.Spec()
	risk, grant := classifyToolName(spec.Name)
	return ToolDescriptor{
		Spec: spec, Risk: risk, Environments: []string{"desktop"}, RequiresGrant: grant,
		WorkTaskOnly: spec.Name == "create_artifact",
	}
}

func classifyToolName(name string) (RiskLevel, bool) {
	switch name {
	case "get_datetime", "list_tasks", "search_memory", "query_person", "list_intents",
		"get_weather", "read", "web_search", "web_fetch", "screen_observe",
		"list_agents", "refresh_agents", "agent_provider_usage", "list_skills",
		"load_skill", "list_agent_tasks", "get_agent_task":
		return RiskRead, false
	case "create_task", "set_alarm", "save_note", "save_fact", "update_task",
		"create_intent", "cancel_intent", "wait", "tap", "input_text", "swipe",
		"back", "open_app", "open_agent", "cancel_agent_task", "create_artifact":
		return RiskPrepare, false
	case "write":
		return RiskPrepare, false
	case "bash", "delegate_agent":
		return RiskSensitive, true
	case "switch_claude_provider":
		return RiskExternalWrite, true
	default:
		// 未知工具在 WorkTask 中按敏感能力处理，必须显式授权；普通聊天不受影响。
		return RiskSensitive, true
	}
}

func profileAllows(profile string, desc ToolDescriptor) bool {
	switch profile {
	case "read":
		return desc.Risk == RiskRead
	case "internal":
		return desc.Risk != RiskExternalWrite
	case "external":
		return true
	default:
		return true
	}
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
