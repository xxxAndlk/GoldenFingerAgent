package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"goldenfinger/agent/internal/llm"
	"goldenfinger/agent/internal/store"
)

const wechatDBPrefix = "wechat-db:"

type wechatPayload struct {
	SourceID     string `json:"source_id"`
	DisplayName  string `json:"display_name"`
	Sender       string `json:"sender"`
	ActorRole    string `json:"actor_role"`
	Direction    string `json:"direction"`
	MessageType  string `json:"message_type"`
	OccurredAt   string `json:"occurred_at"`
	ObservedAt   string `json:"observed_at"`
	Completeness string `json:"completeness"`
	SyncScope    string `json:"sync_scope"`
	MessageID    string `json:"message_id"`
	ChatType     string `json:"chat_type"`
}

type wechatMessageView struct {
	TriggerID    string `json:"trigger_id"`
	SourceID     string `json:"source_id"`
	DisplayName  string `json:"display_name"`
	MessageID    string `json:"message_id"`
	Sender       string `json:"sender"`
	ActorRole    string `json:"actor_role"`
	Direction    string `json:"direction"`
	MessageType  string `json:"message_type"`
	Text         string `json:"text"`
	OccurredAt   string `json:"occurred_at,omitempty"`
	ObservedAt   string `json:"observed_at,omitempty"`
	Completeness string `json:"completeness"`
	SyncScope    string `json:"sync_scope"`
}

func decodeWechatPayload(raw json.RawMessage) wechatPayload {
	var payload wechatPayload
	_ = json.Unmarshal(raw, &payload)
	return payload
}

func isWechatDBTrigger(trigger store.Trigger) bool {
	payload := decodeWechatPayload(trigger.Payload)
	return trigger.Channel == store.ChannelWeChatDesktop && strings.HasPrefix(payload.SourceID, wechatDBPrefix)
}

func wechatTriggers(ctx context.Context, repos *store.Repos, ownerID string, from, to time.Time, limit int) ([]store.Trigger, error) {
	rows, err := repos.Triggers.ListBetween(ctx, ownerID, store.ChannelWeChatDesktop, from, to, limit)
	if err != nil {
		return nil, err
	}
	out := make([]store.Trigger, 0, len(rows))
	for _, row := range rows {
		if isWechatDBTrigger(row) {
			out = append(out, row)
		}
	}
	return out, nil
}

func wechatAllowed(ctx context.Context, repos *store.Repos, ownerID, sourceID string) (bool, error) {
	if !strings.HasPrefix(sourceID, wechatDBPrefix) {
		return false, nil
	}
	return repos.ChannelPolicies.Allowed(ctx, ownerID, store.ChannelWeChatDesktop, sourceID)
}

func wechatMessageFromTrigger(trigger store.Trigger) wechatMessageView {
	payload := decodeWechatPayload(trigger.Payload)
	text := trigger.Body
	if index := strings.Index(text, "\n（当前可见内容"); index >= 0 {
		text = text[:index]
	}
	if len([]rune(text)) > 2000 {
		text = string([]rune(text)[:2000])
	}
	return wechatMessageView{
		TriggerID: trigger.ID, SourceID: payload.SourceID,
		DisplayName: firstNonEmpty(payload.DisplayName, trigger.Title),
		MessageID:   payload.MessageID, Sender: payload.Sender, ActorRole: payload.ActorRole,
		Direction: payload.Direction, MessageType: payload.MessageType, Text: text,
		OccurredAt: payload.OccurredAt, ObservedAt: payload.ObservedAt,
		Completeness: payload.Completeness, SyncScope: payload.SyncScope,
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func parseWechatTime(value string, fallback time.Time) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed
	}
	return fallback
}

// ---- get_connector_health ----

type getConnectorHealthTool struct{ repos *store.Repos }

func (t getConnectorHealthTool) Spec() llm.ToolSpec {
	return toolSpec("get_connector_health", "查询微信数据库连接器的授权会话数、最近入库时间和采集覆盖范围。用户问“为什么看不到微信/怎么没有新消息”时先调用它，区分未授权、未采集和确实没有新消息。",
		map[string]any{"channel": map[string]any{"type": "string", "description": "目前只支持 wechat_desktop"}})
}

func (t getConnectorHealthTool) Execute(ctx context.Context, args json.RawMessage, tc *ToolContext) (ToolResult, error) {
	if tc == nil || tc.Session == nil || t.repos == nil {
		return systemError(fmt.Errorf("微信工具未装配")), nil
	}
	policies, err := t.repos.ChannelPolicies.List(ctx, tc.Session.UserID, store.ChannelWeChatDesktop)
	if err != nil {
		return systemError(err), nil
	}
	allowed := 0
	for _, policy := range policies {
		if policy.Allowed && strings.HasPrefix(policy.Source, wechatDBPrefix) {
			allowed++
		}
	}
	now := time.Now()
	rows, _ := wechatTriggers(ctx, t.repos, tc.Session.UserID, now.Add(-365*24*time.Hour), now.Add(time.Minute), 2000)
	lastEvent := ""
	if len(rows) > 0 {
		last := rows[len(rows)-1]
		payload := decodeWechatPayload(last.Payload)
		lastEvent = firstNonEmpty(payload.ObservedAt, last.ReceivedAt.Format(time.RFC3339))
	}
	state := "waiting_authorization"
	if allowed > 0 {
		state = "observing"
		if lastEvent == "" {
			state = "unknown"
		}
	}
	return ToolResult{Status: StatusOK, Data: map[string]any{
		"channel": store.ChannelWeChatDesktop, "state": state,
		"authorized_chats": allowed, "last_event_at": lastEvent,
		"sync_scope":    "local_incremental",
		"coverage_note": "只代表本机已授权、已入库的微信数据库记录，不代表微信服务器完整历史。",
	}}, nil
}

// ---- list_wechat_chats ----

type listWeChatChatsTool struct{ repos *store.Repos }

func (t listWeChatChatsTool) Spec() llm.ToolSpec {
	return toolSpec("list_wechat_chats", "列出当前已授权且有入库消息的微信会话，返回稳定 source_id、显示名、最近消息时间和方向。同名联系人是不同条目；用户只给称呼且匹配多个时让用户选择。",
		map[string]any{
			"query": map[string]any{"type": "string", "description": "可选：按显示名过滤"},
			"limit": map[string]any{"type": "integer", "description": "默认 20，最大 50"},
		})
}

func (t listWeChatChatsTool) Execute(ctx context.Context, args json.RawMessage, tc *ToolContext) (ToolResult, error) {
	if tc == nil || tc.Session == nil || t.repos == nil {
		return systemError(fmt.Errorf("微信工具未装配")), nil
	}
	var input struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	_ = json.Unmarshal(args, &input)
	if input.Limit <= 0 || input.Limit > 50 {
		input.Limit = 20
	}
	policies, err := t.repos.ChannelPolicies.List(ctx, tc.Session.UserID, store.ChannelWeChatDesktop)
	if err != nil {
		return systemError(err), nil
	}
	allowed := map[string]bool{}
	for _, policy := range policies {
		if policy.Allowed && strings.HasPrefix(policy.Source, wechatDBPrefix) {
			allowed[policy.Source] = true
		}
	}
	now := time.Now()
	rows, _ := wechatTriggers(ctx, t.repos, tc.Session.UserID, now.Add(-365*24*time.Hour), now.Add(time.Minute), 2000)
	latest := map[string]wechatMessageView{}
	for _, row := range rows {
		message := wechatMessageFromTrigger(row)
		if allowed[message.SourceID] {
			latest[message.SourceID] = message
		}
	}
	type chat struct {
		SourceID      string `json:"source_id"`
		DisplayName   string `json:"display_name"`
		ChatType      string `json:"chat_type"`
		LastMessageAt string `json:"last_message_at"`
		LastDirection string `json:"last_direction"`
	}
	out := []chat{}
	needle := strings.ToLower(strings.TrimSpace(input.Query))
	for sourceID, message := range latest {
		if needle != "" && !strings.Contains(strings.ToLower(message.DisplayName), needle) && !strings.Contains(strings.ToLower(sourceID), needle) {
			continue
		}
		out = append(out, chat{SourceID: sourceID, DisplayName: message.DisplayName,
			ChatType: chatTypeFromSource(sourceID), LastMessageAt: message.OccurredAt, LastDirection: message.Direction})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastMessageAt > out[j].LastMessageAt })
	truncated := len(out) > input.Limit
	if truncated {
		out = out[:input.Limit]
	}
	return ToolResult{Status: StatusOK, Data: map[string]any{"chats": out, "truncated": truncated}}, nil
}

func chatTypeFromSource(sourceID string) string {
	if strings.Contains(sourceID, "@chatroom") {
		return "group"
	}
	return "private"
}

// ---- read_wechat_messages ----

type readWeChatMessagesTool struct{ repos *store.Repos }

func (t readWeChatMessagesTool) Spec() llm.ToolSpec {
	return toolSpec("read_wechat_messages", "按稳定 source_id 读取已授权微信会话的消息。按发生时间正序返回；before 是上一页最早消息的 trigger_id。结果只覆盖本机已采集范围。",
		map[string]any{
			"source_id": map[string]any{"type": "string", "description": "list_wechat_chats 返回的稳定 source_id"},
			"before":    map[string]any{"type": "string", "description": "可选：上一页最早消息的 trigger_id"},
			"from":      map[string]any{"type": "string", "description": "可选：RFC3339 起始时间"},
			"to":        map[string]any{"type": "string", "description": "可选：RFC3339 结束时间"},
			"limit":     map[string]any{"type": "integer", "description": "默认 30，最大 100"},
		}, "source_id")
}

func (t readWeChatMessagesTool) Execute(ctx context.Context, args json.RawMessage, tc *ToolContext) (ToolResult, error) {
	if tc == nil || tc.Session == nil || t.repos == nil {
		return systemError(fmt.Errorf("微信工具未装配")), nil
	}
	var input struct {
		SourceID string `json:"source_id"`
		Before   string `json:"before"`
		From     string `json:"from"`
		To       string `json:"to"`
		Limit    int    `json:"limit"`
	}
	if err := decodeArgs(args, &input); err != nil {
		return systemError(err), nil
	}
	if input.Limit <= 0 || input.Limit > 100 {
		input.Limit = 30
	}
	allowed, err := wechatAllowed(ctx, t.repos, tc.Session.UserID, input.SourceID)
	if err != nil {
		return systemError(err), nil
	}
	if !allowed {
		return systemError(fmt.Errorf("该微信会话未授权或不存在")), nil
	}
	now := time.Now()
	from := parseWechatTime(input.From, now.Add(-365*24*time.Hour))
	to := parseWechatTime(input.To, now.Add(time.Minute))
	if input.Before != "" {
		if before, err := t.repos.Triggers.Get(ctx, tc.Session.UserID, input.Before); err == nil {
			to = before.ReceivedAt
		}
	}
	rows, err := wechatTriggers(ctx, t.repos, tc.Session.UserID, from, to, 2000)
	if err != nil {
		return systemError(err), nil
	}
	messages := []wechatMessageView{}
	for _, row := range rows {
		message := wechatMessageFromTrigger(row)
		if message.SourceID == input.SourceID {
			messages = append(messages, message)
		}
	}
	sort.SliceStable(messages, func(i, j int) bool { return messages[i].OccurredAt < messages[j].OccurredAt })
	truncated := len(messages) > input.Limit
	nextBefore := ""
	if truncated {
		messages = messages[len(messages)-input.Limit:]
		nextBefore = messages[0].TriggerID
	}
	return ToolResult{Status: StatusOK, Data: map[string]any{
		"source_id": input.SourceID, "messages": messages,
		"next_before": nextBefore, "truncated": truncated,
		"coverage_note": "仅包含本机已授权、已采集范围，不代表微信服务器全部历史。",
	}}, nil
}

// ---- search_wechat_messages ----

type searchWeChatMessagesTool struct{ repos *store.Repos }

func (t searchWeChatMessagesTool) Spec() llm.ToolSpec {
	return toolSpec("search_wechat_messages", "在已授权、已入库的微信消息中按关键词、发送者、方向和会话检索。无结果只表示当前已采集范围没有命中，不表示微信里不存在。",
		map[string]any{
			"query":     map[string]any{"type": "string", "description": "关键词，最长 200 字"},
			"source_id": map[string]any{"type": "string", "description": "可选：限定会话"},
			"sender":    map[string]any{"type": "string", "description": "可选：发送者显示名"},
			"direction": map[string]any{"type": "string", "description": "可选：self|other|system|unknown"},
			"from":      map[string]any{"type": "string", "description": "可选：RFC3339"},
			"to":        map[string]any{"type": "string", "description": "可选：RFC3339"},
			"limit":     map[string]any{"type": "integer", "description": "默认 20，最大 50"},
		}, "query")
}

func (t searchWeChatMessagesTool) Execute(ctx context.Context, args json.RawMessage, tc *ToolContext) (ToolResult, error) {
	if tc == nil || tc.Session == nil || t.repos == nil {
		return systemError(fmt.Errorf("微信工具未装配")), nil
	}
	var input struct {
		Query     string `json:"query"`
		SourceID  string `json:"source_id"`
		Sender    string `json:"sender"`
		Direction string `json:"direction"`
		From      string `json:"from"`
		To        string `json:"to"`
		Limit     int    `json:"limit"`
	}
	if err := decodeArgs(args, &input); err != nil {
		return systemError(err), nil
	}
	input.Query = strings.TrimSpace(input.Query)
	if input.Query == "" {
		return systemError(fmt.Errorf("query 不能为空")), nil
	}
	if len([]rune(input.Query)) > 200 {
		input.Query = string([]rune(input.Query)[:200])
	}
	if input.Limit <= 0 || input.Limit > 50 {
		input.Limit = 20
	}
	if input.SourceID != "" {
		allowed, err := wechatAllowed(ctx, t.repos, tc.Session.UserID, input.SourceID)
		if err != nil {
			return systemError(err), nil
		}
		if !allowed {
			return systemError(fmt.Errorf("该微信会话未授权或不存在")), nil
		}
	}
	now := time.Now()
	from := parseWechatTime(input.From, now.Add(-365*24*time.Hour))
	to := parseWechatTime(input.To, now.Add(time.Minute))
	rows, err := wechatTriggers(ctx, t.repos, tc.Session.UserID, from, to, 2000)
	if err != nil {
		return systemError(err), nil
	}
	needle := strings.ToLower(input.Query)
	results := []wechatMessageView{}
	for index := len(rows) - 1; index >= 0; index-- {
		message := wechatMessageFromTrigger(rows[index])
		if input.SourceID != "" && message.SourceID != input.SourceID {
			continue
		}
		if input.Sender != "" && !strings.Contains(strings.ToLower(message.Sender), strings.ToLower(input.Sender)) {
			continue
		}
		if input.Direction != "" && message.Direction != input.Direction {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(message.Text), needle) {
			continue
		}
		results = append(results, message)
		if len(results) >= input.Limit {
			break
		}
	}
	return ToolResult{Status: StatusOK, Data: map[string]any{
		"messages":      results,
		"coverage_note": "仅搜索当前已授权、已入库的微信消息；无结果不代表微信服务器中没有。",
	}}, nil
}

func WeChatTools(repos *store.Repos) []Tool {
	return []Tool{
		getConnectorHealthTool{repos: repos},
		listWeChatChatsTool{repos: repos},
		readWeChatMessagesTool{repos: repos},
		searchWeChatMessagesTool{repos: repos},
	}
}
