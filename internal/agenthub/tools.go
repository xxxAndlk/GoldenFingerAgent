package agenthub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"goldenfinger/agent/internal/agent"
	"goldenfinger/agent/internal/llm"
)

type hubTool struct {
	manager                   *Manager
	name, description, schema string
	run                       func(context.Context, json.RawMessage) (any, error)
}

func (t hubTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{Name: t.name, Description: t.description, Parameters: json.RawMessage(t.schema)}
}

func (t hubTool) Execute(ctx context.Context, args json.RawMessage, _ *agent.ToolContext) (agent.ToolResult, error) {
	data, err := t.run(ctx, args)
	if err != nil {
		return agent.ToolResult{Status: agent.StatusError, Data: map[string]string{"error": err.Error()}}, nil
	}
	return agent.ToolResult{Status: agent.StatusOK, Data: data}, nil
}

func Tools(manager *Manager) []agent.Tool {
	return []agent.Tool{
		hubTool{manager: manager, name: "list_agents", description: "扫描本机、A2A、桌面和 BMA Agent，返回真实安装、运行和可调用状态", schema: `{"type":"object","properties":{},"additionalProperties":false}`, run: func(ctx context.Context, _ json.RawMessage) (any, error) {
			return manager.Scan(ctx), nil
		}},
		hubTool{manager: manager, name: "open_agent", description: "打开或唤起已安装的桌面 Agent；只适用于 list_agents 中具有 open 能力的桌面软件", schema: `{"type":"object","properties":{"agent_id":{"type":"string","description":"list_agents 返回的桌面 Agent ID"}},"required":["agent_id"],"additionalProperties":false}`, run: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var args struct {
				AgentID string `json:"agent_id"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return nil, err
			}
			return manager.Open(ctx, args.AgentID)
		}},
		hubTool{manager: manager, name: "delegate_agent", description: "Agent 调用工具：把具体任务交给另一个 Agent 并等待结果。agent_id 可省略，省略时按权重自动选（BMA 优先级最高）；支持工作目录、A2A、CLI、运行中的桌面 Agent 和 BMA", schema: `{"type":"object","properties":{"agent_id":{"type":"string","description":"可选：list_agents 返回的 ID，省略则自动选择"},"task":{"type":"string","description":"完整任务与验收要求"},"work_dir":{"type":"string","description":"可选的项目文件夹绝对路径；默认当前项目"}},"required":["task"],"additionalProperties":false}`, run: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var args struct {
				AgentID string `json:"agent_id"`
				Task    string `json:"task"`
				WorkDir string `json:"work_dir"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return nil, err
			}
			if strings.TrimSpace(args.AgentID) == "" {
				return manager.DelegateAuto(ctx, args.Task, args.WorkDir)
			}
			return manager.DelegateAt(ctx, args.AgentID, args.Task, args.WorkDir)
		}},
		hubTool{manager: manager, name: "agent_provider_usage", description: "读取 CC Switch 的供应商、今日/本月消耗和每日/每月额度，不返回密钥", schema: `{"type":"object","properties":{"app_type":{"type":"string","description":"可选：claude、codex、hermes 等"}},"additionalProperties":false}`, run: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var args struct {
				AppType string `json:"app_type"`
			}
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &args); err != nil {
					return nil, err
				}
			}
			if manager.CCSwitch == nil {
				return nil, errors.New("CC Switch 未配置")
			}
			return manager.CCSwitch.Providers(ctx, args.AppType)
		}},
		hubTool{manager: manager, name: "switch_claude_provider", description: "通过 CC Switch 已有供应商切换 Claude Code 密钥和接口；会备份原配置，只有用户明确要求切换时才将 confirmed 设为 true", schema: `{"type":"object","properties":{"provider_id":{"type":"string"},"confirmed":{"type":"boolean"}},"required":["provider_id","confirmed"],"additionalProperties":false}`, run: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var args struct {
				ProviderID string `json:"provider_id"`
				Confirmed  bool   `json:"confirmed"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return nil, err
			}
			if !args.Confirmed {
				return map[string]string{"status": "confirmation_required", "message": "请用户明确确认切换到该供应商后再执行"}, nil
			}
			if manager.CCSwitch == nil {
				return nil, errors.New("CC Switch 未配置")
			}
			provider, err := manager.CCSwitch.SwitchClaude(ctx, args.ProviderID)
			if err != nil {
				return nil, err
			}
			return map[string]any{"provider": provider, "message": fmt.Sprintf("已切换到 %s；新开的 Claude Code 会话将使用新配置", provider.Name)}, nil
		}},
	}
}
