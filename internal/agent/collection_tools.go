package agent

import (
	"context"
	"encoding/json"
	"errors"

	"goldenfinger/agent/internal/collection"
	"goldenfinger/agent/internal/llm"
)

// CollectionTools 仅在电脑主服务注册；Android 的 DefaultTools 不包含这些能力。
func CollectionTools(manager *collection.Manager) []Tool {
	return []Tool{listCollectionChannelsTool{manager: manager}, configureCollectionChannelTool{manager: manager}}
}

type listCollectionChannelsTool struct{ manager *collection.Manager }

func (t listCollectionChannelsTool) Spec() llm.ToolSpec {
	return toolSpec("list_collection_channels",
		"查看机主当前真实可配置的采集渠道：微信联系人/群、授权网页，以及桌面工作轨迹总开关。返回稳定 source_id、site_id 和开关状态，不返回消息正文或凭据。",
		map[string]any{})
}

func (t listCollectionChannelsTool) Execute(ctx context.Context, _ json.RawMessage, tc *ToolContext) (ToolResult, error) {
	if tc == nil || tc.Session == nil || t.manager == nil {
		return systemError(errors.New("采集渠道工具未装配")), nil
	}
	data, err := t.manager.List(ctx, tc.Session.UserID)
	if err != nil {
		return systemError(err), nil
	}
	return ToolResult{Status: StatusOK, Data: data}, nil
}

type configureCollectionChannelTool struct{ manager *collection.Manager }

func (t configureCollectionChannelTool) Spec() llm.ToolSpec {
	return toolSpec("configure_collection_channel",
		"按当前主对话机主的明确要求新增、移除、启用或停用采集渠道。微信同名候选必须让机主选择；网页默认只采当前 URL，只有机主明确说整站才能 scope=site；activity 是整个桌面工作轨迹/协作模式总开关，不是单应用白名单。",
		map[string]any{
			"channel":          map[string]any{"type": "string", "enum": []string{"wechat_desktop", "web", "activity"}},
			"action":           map[string]any{"type": "string", "enum": []string{"add", "remove", "enable", "disable"}},
			"query":            map[string]any{"type": "string", "description": "微信 add：联系人或群名称"},
			"source_id":        map[string]any{"type": "string", "description": "微信候选的稳定 source_id；remove 必填"},
			"url":              map[string]any{"type": "string", "description": "网页 add 的 http(s) URL，或用于定位已有配置"},
			"site_id":          map[string]any{"type": "string", "description": "list_collection_channels 返回的网页 site_id"},
			"scope":            map[string]any{"type": "string", "enum": []string{"current_page", "site"}, "description": "默认 current_page；site 必须由机主明确授权整站"},
			"interval_seconds": map[string]any{"type": "integer", "description": "网页采集间隔，60–86400 秒，默认 1800"},
		}, "channel", "action")
}

func (t configureCollectionChannelTool) Execute(ctx context.Context, args json.RawMessage, tc *ToolContext) (ToolResult, error) {
	if tc == nil || tc.Session == nil || t.manager == nil {
		return systemError(errors.New("采集渠道工具未装配")), nil
	}
	var input struct {
		Channel         string `json:"channel"`
		Action          string `json:"action"`
		Query           string `json:"query"`
		SourceID        string `json:"source_id"`
		URL             string `json:"url"`
		SiteID          string `json:"site_id"`
		Scope           string `json:"scope"`
		IntervalSeconds int    `json:"interval_seconds"`
	}
	if err := decodeArgs(args, &input); err != nil {
		return systemError(err), nil
	}
	outcome, err := t.manager.Configure(ctx, tc.Session.UserID, collection.ConfigureInput{
		Channel: input.Channel, Action: input.Action, Query: input.Query, SourceID: input.SourceID,
		URL: input.URL, SiteID: input.SiteID, Scope: input.Scope, IntervalSeconds: input.IntervalSeconds,
	})
	if err != nil {
		return systemError(err), nil
	}
	if outcome.Question != "" {
		return ToolResult{Status: StatusClarify, Data: outcome.Data, Question: outcome.Question}, nil
	}
	return ToolResult{Status: StatusOK, Data: outcome.Data}, nil
}
