package agenthub

import (
	"context"
	"encoding/json"
	"errors"

	"goldenfinger/agent/internal/agent"
	"goldenfinger/agent/internal/llm"
)

type asyncTool struct {
	dispatcher *Dispatcher
	spec       llm.ToolSpec
	run        func(context.Context, json.RawMessage, string) (any, error)
}

func (t asyncTool) Spec() llm.ToolSpec { return t.spec }

func (t asyncTool) Execute(ctx context.Context, raw json.RawMessage, tc *agent.ToolContext) (agent.ToolResult, error) {
	ownerID := ""
	if tc != nil && tc.Session != nil {
		ownerID = tc.Session.UserID
	}
	if ownerID == "" {
		return agent.ToolResult{Status: agent.StatusError, Data: map[string]string{"error": "异步任务缺少用户身份"}}, nil
	}
	data, err := t.run(ctx, raw, ownerID)
	if err != nil {
		return agent.ToolResult{Status: agent.StatusError, Data: map[string]string{"error": err.Error()}}, nil
	}
	return agent.ToolResult{Status: agent.StatusOK, Data: data}, nil
}

func asyncSpec(name, description, schema string) llm.ToolSpec {
	return llm.ToolSpec{Name: name, Description: description, Parameters: json.RawMessage(schema)}
}

// AsyncTools 用持久化异步派发覆盖同名 delegate_agent，并提供查询和取消工具。
func AsyncTools(dispatcher *Dispatcher) []agent.Tool {
	return []agent.Tool{
		asyncTool{dispatcher: dispatcher, spec: asyncSpec("delegate_agent", "异步派发 Agent 任务：持久化后立即返回 task_id，不等待执行完成。结果会保存并发送可靠通知；之后用 get_agent_task 查询", `{"type":"object","properties":{"agent_id":{"type":"string"},"task":{"type":"string"},"work_dir":{"type":"string"},"idempotency_key":{"type":"string","description":"可选：客户端幂等键，重复提交返回原任务"}},"required":["agent_id","task"],"additionalProperties":false}`), run: func(ctx context.Context, raw json.RawMessage, ownerID string) (any, error) {
			var args struct {
				AgentID        string `json:"agent_id"`
				Task           string `json:"task"`
				WorkDir        string `json:"work_dir"`
				IdempotencyKey string `json:"idempotency_key"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return nil, err
			}
			return dispatcher.Submit(ctx, ownerID, args.AgentID, args.Task, args.WorkDir, args.IdempotencyKey)
		}},
		asyncTool{dispatcher: dispatcher, spec: asyncSpec("list_agent_tasks", "列出当前用户的异步 Agent 任务及状态，不返回其他用户任务", `{"type":"object","properties":{"limit":{"type":"integer","description":"可选，默认 50，最大 200"}},"additionalProperties":false}`), run: func(ctx context.Context, raw json.RawMessage, ownerID string) (any, error) {
			var args struct {
				Limit int `json:"limit"`
			}
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &args); err != nil {
					return nil, err
				}
			}
			jobs, err := dispatcher.List(ctx, ownerID, args.Limit)
			return map[string]any{"tasks": jobs}, err
		}},
		asyncTool{dispatcher: dispatcher, spec: asyncSpec("get_agent_task", "按 task_id 查询异步 Agent 任务、执行结果和状态事件", `{"type":"object","properties":{"task_id":{"type":"string"}},"required":["task_id"],"additionalProperties":false}`), run: func(ctx context.Context, raw json.RawMessage, ownerID string) (any, error) {
			var args struct {
				TaskID string `json:"task_id"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return nil, err
			}
			job, events, err := dispatcher.Get(ctx, ownerID, args.TaskID)
			if err != nil {
				return nil, err
			}
			return map[string]any{"task": job, "events": events}, nil
		}},
		asyncTool{dispatcher: dispatcher, spec: asyncSpec("cancel_agent_task", "取消仍在 queued 或 running 的异步 Agent 任务；已结束任务不能取消", `{"type":"object","properties":{"task_id":{"type":"string"}},"required":["task_id"],"additionalProperties":false}`), run: func(ctx context.Context, raw json.RawMessage, ownerID string) (any, error) {
			var args struct {
				TaskID string `json:"task_id"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return nil, err
			}
			if args.TaskID == "" {
				return nil, errors.New("task_id 不能为空")
			}
			return dispatcher.Cancel(ctx, ownerID, args.TaskID)
		}},
	}
}
