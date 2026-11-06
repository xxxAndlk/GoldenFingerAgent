package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// forwardCommand 把手机发来的白名单控制面指令转发到本机 HTTP 接口。
//
// 返回值第二个是 handled=false 表示这条 kind 不在白名单里，
// 由调用方回退到普通聊天/委派路径。
//
// 结果分类必须如实：
//   - 2xx → executed（真的执行了）
//   - 409 → conflict（版本冲突，需要重新读取）
//   - 4xx → rejected（电脑明确拒绝）
//   - 5xx → unknown（结果未知，不要自动重试）
func forwardCommand(ctx context.Context, client *http.Client, base string, p localRequest) (ResultPayload, bool) {
	route, ok := commandRoutes[p.Kind]
	if !ok {
		return ResultPayload{}, false
	}
	body, query, err := route.build(p)
	if err != nil {
		return ResultPayload{OK: false, Error: err.Error(), Command: CommandRejected}, true
	}
	target := base + substitutePath(route.path, p)
	if query != "" {
		target += "?" + query
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, route.method, target, reader)
	if err != nil {
		return ResultPayload{OK: false, Error: err.Error(), Command: CommandUnknown}, true
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if route.headers != nil {
		for key, value := range route.headers(p) {
			req.Header.Set(key, value)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return ResultPayload{OK: false, Error: err.Error(), Command: CommandUnknown}, true
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return ResultPayload{OK: false, Error: err.Error(), Command: CommandUnknown}, true
	}
	result := ResultPayload{
		Command:       classifyStatus(resp.StatusCode),
		SchemaVersion: CommandSchemaVersion,
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var decoded struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &decoded)
		result.OK = false
		result.Error = strings.TrimSpace(decoded.Error)
		if result.Error == "" {
			result.Error = resp.Status
		}
		return result, true
	}
	result.OK = true
	if len(bytes.TrimSpace(raw)) > 0 {
		result.Data = json.RawMessage(raw)
	}
	result.Reply = route.reply(p, raw)
	if route.workTaskID != nil {
		result.WorkTaskID = route.workTaskID(p, raw)
	}
	if route.jobID != nil {
		result.JobID = route.jobID(p, raw)
	}
	return result, true
}

// classifyStatus 把 HTTP 状态码映射成指令结果分类。
func classifyStatus(status int) string {
	switch {
	case status >= 200 && status < 300:
		return CommandExecuted
	case status == http.StatusConflict:
		return CommandConflict
	case status >= 400 && status < 500:
		return CommandRejected
	default:
		return CommandUnknown
	}
}

// commandRoute 描述一条白名单指令如何映射到本机 HTTP 接口。
type commandRoute struct {
	method string
	// path 里的 {id} 会被转义后的 ID 替换。
	path string
	// build 生成请求体与查询串；body 为 nil 表示没有请求体。
	build func(p localRequest) ([]byte, string, error)
	// reply 从响应里提取一句给用户看的话。
	reply func(p localRequest, raw []byte) string
	// workTaskID 从响应里提取关联的 WorkTask ID。
	workTaskID func(p localRequest, raw []byte) string
	// jobID 从响应里提取关联的 AgentJob ID。
	jobID func(p localRequest, raw []byte) string
	// headers 是这条指令额外要带的请求头（例如按设备记录的已读游标）。
	headers func(p localRequest) map[string]string
}

// commandRoutes 是手机 → 电脑的白名单控制面指令表。
// 新增指令必须同时写清楚重试语义与结果分类，不能开任意 URL 转发。
var commandRoutes = map[string]commandRoute{
	CmdWorkTaskCreate: {
		method: http.MethodPost,
		path:   "/api/work-tasks",
		build: func(p localRequest) ([]byte, string, error) {
			return bodyOf(map[string]any{
				"title": p.Title, "goal": p.Goal, "profile": p.Profile,
				"idempotency_key": p.IdempotencyKey, "start": true,
			})
		},
		reply:      func(p localRequest, raw []byte) string { return "已在电脑创建长期任务" },
		workTaskID: func(p localRequest, raw []byte) string { return pickString(raw, "task", "id") },
	},
	CmdWorkTaskList: {
		method: http.MethodGet,
		path:   "/api/work-tasks",
		build: func(p localRequest) ([]byte, string, error) {
			q := url.Values{}
			if p.Status != "" {
				q.Set("status", p.Status)
			}
			if p.Limit > 0 {
				q.Set("limit", fmt.Sprint(p.Limit))
			}
			return nil, q.Encode(), nil
		},
		reply: func(p localRequest, raw []byte) string { return "已取回长期任务列表" },
	},
	CmdWorkTaskGet: {
		method: http.MethodGet,
		path:   "/api/work-tasks/{id}",
		build: func(p localRequest) ([]byte, string, error) {
			return nil, "", requireID(p.TaskID, "task_id")
		},
		reply:      func(p localRequest, raw []byte) string { return "已取回任务详情" },
		workTaskID: func(p localRequest, raw []byte) string { return pickString(raw, "task", "id") },
	},
	CmdWorkTaskMsg: {
		method: http.MethodPost,
		path:   "/api/work-tasks/{id}/messages",
		build: func(p localRequest) ([]byte, string, error) {
			if p.TaskID == "" {
				return nil, "", fmt.Errorf("缺少 task_id")
			}
			body := map[string]any{"text": p.Text, "idempotency_key": p.IdempotencyKey}
			if p.ExpectedVersion != nil {
				body["expected_version"] = *p.ExpectedVersion
			}
			return bodyOf(body)
		},
		reply:      func(p localRequest, raw []byte) string { return "指令已进入任务 Inbox" },
		workTaskID: func(p localRequest, raw []byte) string { return pickString(raw, "task", "id") },
	},
	CmdWorkTaskAction: {
		method: http.MethodPost,
		path:   "/api/work-tasks/{id}/{action}",
		build: func(p localRequest) ([]byte, string, error) {
			if p.TaskID == "" {
				return nil, "", fmt.Errorf("缺少 task_id")
			}
			action := strings.ToLower(strings.TrimSpace(p.Action))
			switch action {
			case "pause", "resume", "cancel", "recover", "reopen", "complete":
			default:
				return nil, "", fmt.Errorf("未知任务动作: %s", p.Action)
			}
			body := map[string]any{}
			if p.ExpectedVersion != nil {
				body["expected_version"] = *p.ExpectedVersion
			}
			return bodyOf(body)
		},
		reply:      func(p localRequest, raw []byte) string { return "任务状态已更新" },
		workTaskID: func(p localRequest, raw []byte) string { return pickString(raw, "task", "id") },
	},
	CmdConversationList: {
		method: http.MethodGet,
		path:   "/api/conversations",
		build: func(p localRequest) ([]byte, string, error) {
			q := url.Values{}
			if p.ConversationKind != "" {
				q.Set("kind", p.ConversationKind)
			}
			if p.Limit > 0 {
				q.Set("limit", fmt.Sprint(p.Limit))
			}
			return nil, q.Encode(), nil
		},
		// 未读数按设备统计，必须带上发起方的设备标识，
		// 与 conversation_read 用同一个键。
		headers: func(p localRequest) map[string]string {
			if p.DeviceID == "" {
				return nil
			}
			return map[string]string{"X-Device-Id": p.DeviceID}
		},
		reply: func(p localRequest, raw []byte) string { return "已取回会话目录" },
	},
	CmdConversationMessages: {
		method: http.MethodGet,
		path:   "/api/conversations/{id}/messages",
		build: func(p localRequest) ([]byte, string, error) {
			if p.SessionID == "" {
				return nil, "", fmt.Errorf("缺少 session_id")
			}
			q := url.Values{}
			if p.Before != "" {
				q.Set("before", p.Before)
			}
			if p.Limit > 0 {
				q.Set("limit", fmt.Sprint(p.Limit))
			}
			return nil, q.Encode(), nil
		},
		reply: func(p localRequest, raw []byte) string { return "已取回会话消息" },
	},
	CmdConversationRead: {
		method: http.MethodPost,
		path:   "/api/conversations/{id}/read",
		build: func(p localRequest) ([]byte, string, error) {
			if p.SessionID == "" {
				return nil, "", fmt.Errorf("缺少 session_id")
			}
			return bodyOf(map[string]any{})
		},
		// 已读游标必须记在会话真正所在的电脑上，并带上手机自己的设备标识，
		// 否则手机读过的会话会把电脑的角标一起清掉。
		headers: func(p localRequest) map[string]string {
			if p.DeviceID == "" {
				return nil
			}
			return map[string]string{"X-Device-Id": p.DeviceID}
		},
		reply: func(p localRequest, raw []byte) string { return "已记录已读位置" },
	},
	CmdOutboxList: {
		method: http.MethodGet,
		path:   "/api/action-outbox",
		build: func(p localRequest) ([]byte, string, error) {
			q := url.Values{}
			if p.Limit > 0 {
				q.Set("limit", fmt.Sprint(p.Limit))
			}
			return nil, q.Encode(), nil
		},
		reply: func(p localRequest, raw []byte) string { return "已取回待审批动作" },
	},
	CmdOutboxApprove: {
		method: http.MethodPost,
		path:   "/api/action-outbox/{id}/approve",
		build: func(p localRequest) ([]byte, string, error) {
			if p.OutboxID == "" {
				return nil, "", fmt.Errorf("缺少 outbox_id")
			}
			body := map[string]any{}
			if p.ExpectedVersion != nil {
				body["version"] = *p.ExpectedVersion
			}
			return bodyOf(body)
		},
		reply: func(p localRequest, raw []byte) string { return "已批准该动作" },
	},
	CmdOutboxReject: {
		method: http.MethodPost,
		path:   "/api/action-outbox/{id}/reject",
		build: func(p localRequest) ([]byte, string, error) {
			if p.OutboxID == "" {
				return nil, "", fmt.Errorf("缺少 outbox_id")
			}
			body := map[string]any{"reason": p.Reason}
			if p.ExpectedVersion != nil {
				body["version"] = *p.ExpectedVersion
			}
			return bodyOf(body)
		},
		reply: func(p localRequest, raw []byte) string { return "已驳回该动作" },
	},
	CmdArtifactGet: {
		method: http.MethodGet,
		path:   "/api/artifacts/{id}",
		build: func(p localRequest) ([]byte, string, error) {
			return nil, "", requireID(p.ArtifactID, "artifact_id")
		},
		reply: func(p localRequest, raw []byte) string { return "已取回交付物" },
	},
	CmdArtifactAccept: {
		method: http.MethodPost,
		path:   "/api/artifacts/{id}/accept",
		build: func(p localRequest) ([]byte, string, error) {
			if p.ArtifactID == "" {
				return nil, "", fmt.Errorf("缺少 artifact_id")
			}
			body := map[string]any{}
			if p.ExpectedVersion != nil {
				body["expected_version"] = *p.ExpectedVersion
			}
			return bodyOf(body)
		},
		reply: func(p localRequest, raw []byte) string { return "已验收该交付物" },
	},
	CmdArtifactReject: {
		method: http.MethodPost,
		path:   "/api/artifacts/{id}/reject",
		build: func(p localRequest) ([]byte, string, error) {
			if p.ArtifactID == "" {
				return nil, "", fmt.Errorf("缺少 artifact_id")
			}
			body := map[string]any{"reason": p.Reason}
			if p.ExpectedVersion != nil {
				body["expected_version"] = *p.ExpectedVersion
			}
			return bodyOf(body)
		},
		reply: func(p localRequest, raw []byte) string { return "已要求修改该交付物" },
	},
	CmdArtifactDeliver: {
		method: http.MethodPost,
		path:   "/api/artifacts/{id}/deliver",
		build: func(p localRequest) ([]byte, string, error) {
			if p.ArtifactID == "" {
				return nil, "", fmt.Errorf("缺少 artifact_id")
			}
			body := map[string]any{}
			if p.ExpectedVersion != nil {
				body["expected_version"] = *p.ExpectedVersion
			}
			if p.Version > 0 {
				body["version"] = p.Version
			}
			return bodyOf(body)
		},
		reply: func(p localRequest, raw []byte) string { return "已请求交付该文件" },
	},
	CmdBriefLatest: {
		method: http.MethodGet,
		path:   "/api/briefs/latest",
		build: func(p localRequest) ([]byte, string, error) {
			q := url.Values{}
			if p.Date != "" {
				q.Set("date", p.Date)
			}
			return nil, q.Encode(), nil
		},
		reply: func(p localRequest, raw []byte) string { return "已取回每日简报" },
	},
	CmdBriefList: {
		method: http.MethodGet,
		path:   "/api/briefs",
		build: func(p localRequest) ([]byte, string, error) {
			q := url.Values{}
			if p.Limit > 0 {
				q.Set("limit", fmt.Sprint(p.Limit))
			}
			return nil, q.Encode(), nil
		},
		reply: func(p localRequest, raw []byte) string { return "已取回简报历史" },
	},
	CmdBriefGet: {
		method: http.MethodGet,
		path:   "/api/briefs/{id}",
		build: func(p localRequest) ([]byte, string, error) {
			return nil, "", requireID(p.BriefID, "brief_id")
		},
		reply: func(p localRequest, raw []byte) string { return "已取回指定简报" },
	},
	CmdBriefGenerate: {
		method: http.MethodPost,
		path:   "/api/briefs/generate",
		build: func(p localRequest) ([]byte, string, error) {
			q := url.Values{}
			if p.Date != "" {
				q.Set("date", p.Date)
			}
			return nil, q.Encode(), nil
		},
		reply: func(p localRequest, raw []byte) string { return "电脑已生成新的每日简报" },
	},
	CmdCollectionList: {
		method: http.MethodGet,
		path:   "/api/collection/channels",
		build: func(p localRequest) ([]byte, string, error) {
			return nil, "", nil
		},
		reply: func(p localRequest, raw []byte) string { return "已取回桌面采集渠道状态" },
	},
	CmdCollectionConfigure: {
		method: http.MethodPost,
		path:   "/api/collection/channels",
		build: func(p localRequest) ([]byte, string, error) {
			if p.Channel == "" {
				return nil, "", fmt.Errorf("缺少 channel")
			}
			body := map[string]any{"channel": p.Channel, "enabled": p.Enabled}
			if p.Action != "" {
				body["action"] = p.Action
			}
			if p.Query != "" {
				body["query"] = p.Query
			}
			if p.SourceID != "" {
				body["source_id"] = p.SourceID
			}
			if p.URL != "" {
				body["url"] = p.URL
			}
			if p.SiteID != "" {
				body["site_id"] = p.SiteID
			}
			if p.Scope != "" {
				body["scope"] = p.Scope
			}
			if p.IntervalSeconds > 0 {
				body["interval_seconds"] = p.IntervalSeconds
			}
			for key, value := range p.Config {
				body[key] = value
			}
			return bodyOf(body)
		},
		reply: func(p localRequest, raw []byte) string { return "桌面已回执采集配置" },
	},
}

// requireID 校验必填 ID 参数。
func requireID(value, field string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("缺少 %s", field)
	}
	return nil
}

// substitutePath 把路由里的 {id} / {action} 占位符换成转义后的真实值。
func substitutePath(path string, p localRequest) string {
	replacer := strings.NewReplacer(
		"{id}", url.PathEscape(firstNonEmpty(p.TaskID, p.SessionID, p.OutboxID, p.ArtifactID, p.BriefID)),
		"{action}", url.PathEscape(strings.ToLower(strings.TrimSpace(p.Action))),
	)
	return replacer.Replace(path)
}

// firstNonEmpty 返回第一个非空字符串。
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// bodyOf 把 map 序列化成 JSON 请求体，并套上路由要求的返回形状。
func bodyOf(body map[string]any) ([]byte, string, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, "", err
	}
	return raw, "", nil
}

// pickString 从响应里取某个对象的某个字符串字段。
func pickString(raw []byte, object, field string) string {
	var decoded map[string]map[string]any
	if json.Unmarshal(raw, &decoded) != nil {
		return ""
	}
	item, ok := decoded[object]
	if !ok {
		return ""
	}
	value, _ := item[field].(string)
	return value
}
