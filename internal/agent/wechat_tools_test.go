package agent

import (
	"context"
	"encoding/json"
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
