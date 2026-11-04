package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"goldenfinger/agent/internal/store"
)

func TestWeChatToolsReadAuthorizedDatabaseMessages(t *testing.T) {
	sess, rt, user := testHarness(t)
	ctx := context.Background()
	sourceID := "wechat-db:acct123:wxid_contact"
	if err := rt.Tools.Repos.ChannelPolicies.Set(ctx, user.ID, store.ChannelWeChatDesktop, sourceID, true, fixedNow); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{
		"schema_version": 2, "source_id": sourceID, "display_name": "张三",
		"sender": "张三", "sender_id": "wxid_contact", "actor_role": "contact", "direction": "other",
		"message_type": "text", "message_id": "msg-1", "occurred_at": "2026-03-05T09:00:00+08:00",
		"observed_at": "2026-03-05T09:00:02+08:00", "completeness": "complete", "sync_scope": "local_incremental",
	})
	if _, _, err := rt.Tools.Repos.Triggers.Create(ctx, &store.Trigger{
		OwnerUserID: user.ID, Channel: store.ChannelWeChatDesktop, ExternalID: "wechat:msg-1",
		Kind: "message", Title: "张三", Body: "周五下午三点评审", Payload: payload,
		ReceivedAt: fixedNow, DedupeKey: "wechat:msg-1",
	}); err != nil {
		t.Fatal(err)
	}

	tc := &ToolContext{Session: sess, Runtime: rt}
	tools := map[string]Tool{}
	for _, tool := range WeChatTools(rt.Tools.Repos) {
		tools[tool.Spec().Name] = tool
	}

	result, err := tools["list_wechat_chats"].Execute(ctx, json.RawMessage(`{"query":"张","limit":10}`), tc)
	if err != nil || result.Status != StatusOK {
		t.Fatalf("list=%v err=%v", result, err)
	}
	var listData struct {
		Chats []struct {
			SourceID string `json:"source_id"`
		} `json:"chats"`
	}
	raw, _ := json.Marshal(result.Data)
	if err := json.Unmarshal(raw, &listData); err != nil || len(listData.Chats) != 1 {
		t.Fatalf("chats=%s err=%v", raw, err)
	}

	result, err = tools["read_wechat_messages"].Execute(ctx, json.RawMessage(`{"source_id":"`+sourceID+`","limit":30}`), tc)
	if err != nil || result.Status != StatusOK {
		t.Fatalf("read=%v err=%v", result, err)
	}
	var readData struct {
		Messages []struct {
			Text       string `json:"text"`
			OccurredAt string `json:"occurred_at"`
		} `json:"messages"`
	}
	raw, _ = json.Marshal(result.Data)
	if err := json.Unmarshal(raw, &readData); err != nil || len(readData.Messages) != 1 ||
		readData.Messages[0].Text != "周五下午三点评审" || readData.Messages[0].OccurredAt == "" {
		t.Fatalf("messages=%s err=%v", raw, err)
	}

	result, err = tools["search_wechat_messages"].Execute(ctx, json.RawMessage(`{"query":"评审"}`), tc)
	if err != nil || result.Status != StatusOK {
		t.Fatalf("search=%v err=%v", result, err)
	}
	var searchData struct {
		Messages []json.RawMessage `json:"messages"`
	}
	raw, _ = json.Marshal(result.Data)
	_ = json.Unmarshal(raw, &searchData)
	if len(searchData.Messages) != 1 {
		t.Fatalf("search=%s", raw)
	}

	if err := rt.Tools.Repos.ChannelPolicies.Set(ctx, user.ID, store.ChannelWeChatDesktop, sourceID, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	result, err = tools["read_wechat_messages"].Execute(ctx, json.RawMessage(`{"source_id":"`+sourceID+`"}`), tc)
	if err != nil || result.Status != StatusError {
		t.Fatalf("revoked read=%v err=%v", result, err)
	}
}

// 撤销授权后，全局搜索不能把历史正文再读出来。
func TestWeChatSearchHidesRevokedSource(t *testing.T) {
	sess, rt, user := testHarness(t)
	ctx := context.Background()
	sourceID := "wechat-db:acct123:wxid_contact"
	if err := rt.Tools.Repos.ChannelPolicies.Set(ctx, user.ID, store.ChannelWeChatDesktop, sourceID, true, fixedNow); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{
		"schema_version": 2, "source_id": sourceID, "display_name": "张三",
		"sender": "张三", "actor_role": "contact", "direction": "other",
		"message_type": "text", "message_id": "msg-secret", "occurred_at": "2026-03-05T09:00:00+08:00",
		"completeness": "complete", "sync_scope": "local_incremental",
	})
	if _, _, err := rt.Tools.Repos.Triggers.Create(ctx, &store.Trigger{
		OwnerUserID: user.ID, Channel: store.ChannelWeChatDesktop, ExternalID: "wechat:msg-secret",
		Kind: "message", Title: "张三", Body: "内部机密内容", Payload: payload,
		ReceivedAt: fixedNow, DedupeKey: "wechat:msg-secret",
	}); err != nil {
		t.Fatal(err)
	}
	tc := &ToolContext{Session: sess, Runtime: rt}
	tools := map[string]Tool{}
	for _, tool := range WeChatTools(rt.Tools.Repos) {
		tools[tool.Spec().Name] = tool
	}
	result, err := tools["search_wechat_messages"].Execute(ctx, json.RawMessage(`{"query":"机密"}`), tc)
	if err != nil || result.Status != StatusOK {
		t.Fatalf("search=%v err=%v", result, err)
	}
	raw, _ := json.Marshal(result.Data)
	if !strings.Contains(string(raw), "内部机密内容") {
		t.Fatalf("authorized search missing message: %s", raw)
	}

	if err := rt.Tools.Repos.ChannelPolicies.Set(ctx, user.ID, store.ChannelWeChatDesktop, sourceID, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	result, err = tools["search_wechat_messages"].Execute(ctx, json.RawMessage(`{"query":"机密"}`), tc)
	if err != nil || result.Status != StatusOK {
		t.Fatalf("revoked search=%v err=%v", result, err)
	}
	raw, _ = json.Marshal(result.Data)
	if strings.Contains(string(raw), "内部机密内容") {
		t.Fatalf("revoked search leaked message: %s", raw)
	}
}

// 健康状态必须来自桌面端上报，历史上有没有消息不算依据。
func TestWeChatHealthUsesReportedConnectorState(t *testing.T) {
	sess, rt, user := testHarness(t)
	ctx := context.Background()
	tc := &ToolContext{Session: sess, Runtime: rt}
	tools := map[string]Tool{}
	for _, tool := range WeChatTools(rt.Tools.Repos) {
		tools[tool.Spec().Name] = tool
	}
	result, err := tools["get_connector_health"].Execute(ctx, json.RawMessage(`{}`), tc)
	if err != nil || result.Status != StatusOK {
		t.Fatalf("health=%v err=%v", result, err)
	}
	raw, _ := json.Marshal(result.Data)
	if !strings.Contains(string(raw), `"state":"waiting_authorization"`) {
		t.Fatalf("health=%s", raw)
	}

	payload, _ := json.Marshal(map[string]any{"pending": 3, "last_observed_at": "2026-03-05T09:00:00+08:00"})
	if err := rt.Tools.Repos.ConnectorStatuses.Save(ctx, user.ID, store.ChannelWeChatDesktop,
		"observing", payload, time.Now()); err != nil {
		t.Fatal(err)
	}
	result, err = tools["get_connector_health"].Execute(ctx, json.RawMessage(`{}`), tc)
	if err != nil || result.Status != StatusOK {
		t.Fatalf("health=%v err=%v", result, err)
	}
	raw, _ = json.Marshal(result.Data)
	if !strings.Contains(string(raw), `"state":"observing"`) || !strings.Contains(string(raw), `"pending":3`) {
		t.Fatalf("health=%s", raw)
	}
	// 服务端没有授权来源却报告在采集，必须标出这个矛盾。
	if !strings.Contains(string(raw), `"authorization_mismatch":true`) {
		t.Fatalf("health=%s", raw)
	}

	// 心跳过期后不能再报告正在观察。
	if err := rt.Tools.Repos.ConnectorStatuses.Save(ctx, user.ID, store.ChannelWeChatDesktop,
		"observing", payload, time.Now().Add(-10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	result, err = tools["get_connector_health"].Execute(ctx, json.RawMessage(`{}`), tc)
	if err != nil || result.Status != StatusOK {
		t.Fatalf("health=%v err=%v", result, err)
	}
	raw, _ = json.Marshal(result.Data)
	if !strings.Contains(string(raw), `"state":"stale"`) {
		t.Fatalf("health=%s", raw)
	}
}
