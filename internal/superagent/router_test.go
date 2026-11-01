package superagent_test

import (
	"context"
	"testing"

	"goldenfinger/agent/internal/llm/mock"
	"goldenfinger/agent/internal/store"
	"goldenfinger/agent/internal/superagent"
)

func TestLLMRouterParsesAndValidatesDecisions(t *testing.T) {
	triggers := []store.Trigger{
		{ID: "t1", Channel: "email", Kind: "email", ThreadID: "th1", Title: "周五能交付吗", Body: "请确认"},
		{ID: "t2", Channel: "wechat", Kind: "message", Title: "闲聊", Body: "哈哈"},
	}
	content := "```json\n" + `{"decisions":[
		{"trigger_id":"t1","action":"create_worker","goal":"起草交付确认回复","profile":"internal","priority":2,"reason":"明确询问交付"},
		{"trigger_id":"t2","action":"ignore","reason":"闲聊"},
		{"trigger_id":"unknown","action":"notify"},
		{"trigger_id":"t1","action":"notify"}
	]}` + "\n```"
	client := mock.New(mock.TextResponse(content))
	router := &superagent.LLMRouter{Client: client, Model: "router-test"}
	decisions, err := router.Decide(context.Background(), "u1", triggers)
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 2 {
		t.Fatalf("decisions=%+v", decisions)
	}
	if decisions[0].Action != superagent.ActionCreateWorker || decisions[0].Goal == "" {
		t.Fatalf("first=%+v", decisions[0])
	}
	if decisions[1].Action != superagent.ActionIgnore {
		t.Fatalf("second=%+v", decisions[1])
	}
	if len(client.Calls) != 1 {
		t.Fatalf("llm calls=%d", len(client.Calls))
	}
	if !client.Calls[0].JSONMode || len(client.Calls[0].Tools) != 0 {
		t.Fatalf("router request must be no-tools JSON: %+v", client.Calls[0])
	}
}

func TestLLMRouterRejectsMalformedJSON(t *testing.T) {
	router := &superagent.LLMRouter{
		Client: mock.New(mock.TextResponse("not json")),
		Model:  "router-test",
	}
	if _, err := router.Decide(context.Background(), "u1", []store.Trigger{{ID: "t1"}}); err == nil {
		t.Fatal("malformed JSON must fail so Service can fall back")
	}
}
