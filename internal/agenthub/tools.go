package agenthub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"goldenfinger/agent/internal/agent"
	"goldenfinger/agent/internal/llm"
	"goldenfinger/agent/internal/worktask"
)

type hubTool struct {
	manager                   *Manager
	name, description, schema string
	run                       func(context.Context, json.RawMessage) (any, error)
	// runTC 需要访问当前用户/WorkTask（例如把外部文件入库）。
	runTC func(context.Context, json.RawMessage, *agent.ToolContext) (any, error)
}

func (t hubTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{Name: t.name, Description: t.description, Parameters: json.RawMessage(t.schema)}
}

func (t hubTool) Execute(ctx context.Context, args json.RawMessage, tc *agent.ToolContext) (agent.ToolResult, error) {
	var data any
	var err error
	if t.runTC != nil {
		data, err = t.runTC(ctx, args, tc)
	} else {
		data, err = t.run(ctx, args)
	}
	if err != nil {
		return agent.ToolResult{Status: agent.StatusError, Data: map[string]string{"error": err.Error()}}, nil
	}
	return agent.ToolResult{Status: agent.StatusOK, Data: data}, nil
}

// attachArtifacts 只导入已经随响应提供正文、并落到本地的文件。
// 外部 URL 是不可信输入，不能继承 delegate_agent 的授权自动访问；模型必须再调用
// download_agent_artifact，由工具门禁校验独立 Grant 后才会下载。
func (manager *Manager) attachArtifacts(ctx context.Context, tc *agent.ToolContext, invocation *Invocation) {
	if manager == nil || invocation == nil || len(invocation.Artifacts) == 0 {
		return
	}
	var ownerID, taskID, jobID string
	var artifacts *worktask.ArtifactService
	if tc != nil {
		if tc.Session != nil {
			ownerID = tc.Session.UserID
		}
		if tc.Runtime != nil {
			taskID, jobID = tc.Runtime.WorkTaskID, tc.Runtime.JobID
			if tc.Runtime.Tools != nil {
				artifacts = tc.Runtime.Tools.Artifacts
			}
		}
	}
	if artifacts != nil && ownerID != "" && taskID != "" {
		imported, err := manager.ImportArtifacts(ctx, artifacts, ownerID, taskID, jobID,
			invocation.AgentID, invocation.Artifacts)
		if len(imported) > 0 {
			names := make([]string, 0, len(imported))
			for _, item := range imported {
				names = append(names, fmt.Sprintf("%s（v%d，ID %s）", item.Name, item.Version, item.ArtifactID))
			}
			invocation.Output += "\n\n已转为 WorkTask 交付物：" + strings.Join(names, "、")
		}
		if err != nil {
			invocation.Output += "\n\n文件产物待处理：" + err.Error()
		}
		return
	}
	paths := make([]string, 0, len(invocation.Artifacts))
	pending := make([]string, 0, len(invocation.Artifacts))
	for _, item := range invocation.Artifacts {
		if item.LocalPath != "" {
			paths = append(paths, item.Name+" -> "+item.LocalPath)
		} else if item.ExternalURI != "" {
			pending = append(pending, item.Name)
		}
	}
	if len(paths) > 0 {
		invocation.Output += "\n\n内联文件已保存到本地（当前不在 WorkTask 中，未生成交付物）：\n" + strings.Join(paths, "\n")
	}
	if len(pending) > 0 {
		invocation.Output += "\n\n外部 URL 文件尚未下载，需要在 WorkTask 中通过 download_agent_artifact 独立授权：" + strings.Join(pending, "、")
	}
}

// downloadAgentArtifact 只由 Registry 在独立 Grant 校验通过后调用。
// 不要从 delegate_agent 或异步 Worker 直接调用，以免把委派授权扩大成任意外部下载授权。
func (manager *Manager) downloadAgentArtifact(ctx context.Context, raw json.RawMessage, tc *agent.ToolContext) (any, error) {
	var args struct {
		AgentID     string `json:"agent_id"`
		ArtifactID  string `json:"artifact_id,omitempty"`
		Name        string `json:"name"`
		MediaType   string `json:"media_type,omitempty"`
		SHA256      string `json:"sha256,omitempty"`
		ExternalURI string `json:"external_uri"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, err
	}
	if tc == nil || tc.Session == nil || tc.Runtime == nil || tc.Runtime.Tools == nil ||
		tc.Runtime.Tools.Artifacts == nil || tc.Runtime.WorkTaskID == "" {
		return nil, errors.New("download_agent_artifact 只能在具有 Artifact 服务的 WorkTask 中使用")
	}
	item := AgentArtifact{
		ID: args.ArtifactID, Name: args.Name, MediaType: args.MediaType,
		SHA256: args.SHA256, ExternalURI: strings.TrimSpace(args.ExternalURI),
	}
	if item.Name == "" {
		item.Name = "artifact"
	}
	if item.ExternalURI == "" {
		return nil, errors.New("external_uri 不能为空")
	}
	updated, err := manager.DownloadArtifact(ctx, item, "")
	if err != nil {
		return nil, err
	}
	imported, err := manager.ImportArtifacts(ctx, tc.Runtime.Tools.Artifacts, tc.Session.UserID,
		tc.Runtime.WorkTaskID, tc.Runtime.JobID, args.AgentID, []AgentArtifact{updated})
	if err != nil {
		return nil, err
	}
	if len(imported) != 1 {
		return nil, errors.New("外部文件未生成交付物")
	}
	return map[string]any{
		"status": "imported", "artifact": imported[0],
		"reply": "外部文件已在独立授权下下载并转为 WorkTask 交付物。",
	}, nil
}

func Tools(manager *Manager) []agent.Tool {
	return []agent.Tool{
		hubTool{manager: manager, name: "list_agents", description: "读取已保存的本机、A2A、桌面和 BMA Agent 列表（默认命中缓存，不会重新扫描）", schema: `{"type":"object","properties":{},"additionalProperties":false}`, run: func(ctx context.Context, _ json.RawMessage) (any, error) {
			return manager.Scan(ctx), nil
		}},
		hubTool{manager: manager, name: "refresh_agents", description: "重新扫描本机、A2A、桌面和 BMA Agent；仅在用户明确要求或安装/启动新工具后使用", schema: `{"type":"object","properties":{},"additionalProperties":false}`, run: func(ctx context.Context, _ json.RawMessage) (any, error) {
			return manager.Refresh(ctx), nil
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
		hubTool{manager: manager, name: "delegate_agent", description: "Agent 调用工具：把具体任务交给另一个 Agent 并等待结果。agent_id 可省略，省略时按权重自动选（BMA 优先级最高）；支持工作目录、A2A、CLI、运行中的桌面 Agent 和 BMA", schema: `{"type":"object","properties":{"agent_id":{"type":"string","description":"可选：list_agents 返回的 ID，省略则自动选择"},"task":{"type":"string","description":"完整任务与验收要求"},"work_dir":{"type":"string","description":"可选的项目文件夹绝对路径；默认当前项目"}},"required":["task"],"additionalProperties":false}`, runTC: func(ctx context.Context, raw json.RawMessage, tc *agent.ToolContext) (any, error) {
			var args struct {
				AgentID string `json:"agent_id"`
				Task    string `json:"task"`
				WorkDir string `json:"work_dir"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return nil, err
			}
			var invocation Invocation
			var err error
			if strings.TrimSpace(args.AgentID) == "" {
				invocation, err = manager.DelegateAuto(ctx, args.Task, args.WorkDir)
			} else {
				invocation, err = manager.DelegateAt(ctx, args.AgentID, args.Task, args.WorkDir)
			}
			if err != nil {
				return nil, err
			}
			manager.attachArtifacts(ctx, tc, &invocation)
			return invocation, nil
		}},
		hubTool{manager: manager, name: "download_agent_artifact", description: "在独立 ActionGrant 授权后下载一个外部 Agent 返回的 HTTPS 文件，并转为当前 WorkTask 的交付物。delegate_agent 的授权不覆盖此工具；每次 URL、文件名和哈希变化都需要匹配的授权", schema: `{"type":"object","properties":{"agent_id":{"type":"string","description":"返回文件的外部 Agent ID"},"artifact_id":{"type":"string","description":"外部 Artifact ID"},"name":{"type":"string","description":"外部文件名"},"media_type":{"type":"string"},"sha256":{"type":"string","description":"外部 Agent 声明的 SHA-256；下载后会重新计算校验"},"external_uri":{"type":"string","description":"外部 Agent 返回的 HTTPS URL"}},"required":["agent_id","name","external_uri"],"additionalProperties":false}`, runTC: manager.downloadAgentArtifact},
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
