package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"goldenfinger/agent/internal/llm"
)

func TestChatCompletionsRequestAndResponse(t *testing.T) {
	var body completionRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path=%s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"已完成","tool_calls":[{"id":"call_1","type":"function","function":{"name":"save","arguments":"{\"ok\":true}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":8,"completion_tokens":3,"prompt_cache_hit_tokens":6,"prompt_cache_miss_tokens":2}}`))
	}))
	defer srv.Close()
	c := NewWithProtocol(srv.URL, "key", "model", ProtocolChatCompletions)
	resp, err := c.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.Message{{Role: llm.RoleSystem, Content: "system"}, {Role: llm.RoleUser, Content: "hello"}},
		Tools:    []llm.ToolSpec{{Name: "save", Parameters: json.RawMessage(`{"type":"object"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(body.Messages) != 2 || len(body.Tools) != 1 || body.Tools[0].Function.Name != "save" {
		t.Fatalf("body=%+v", body)
	}
	if resp.Message.Content != "已完成" || len(resp.Message.ToolCalls) != 1 || resp.Message.ToolCalls[0].Name != "save" {
		t.Fatalf("resp=%+v", resp)
	}
	if rate, ok := resp.Usage.CacheHitRate(); !ok || rate != 0.75 {
		t.Fatalf("cache usage=%+v rate=%v reported=%v", resp.Usage, rate, ok)
	}
	var deltas strings.Builder
	_, err = c.Stream(context.Background(), llm.ChatRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}}}, func(s string) { deltas.WriteString(s) })
	if err != nil || deltas.String() != "已完成" {
		t.Fatalf("stream deltas=%q err=%v", deltas.String(), err)
	}
}

func TestDeepSeekDefaultsToResponses(t *testing.T) {
	c := NewWithProtocol("https://api.deepseek.com", "", "deepseek-flash", "")
	if c.Protocol != ProtocolResponses {
		t.Fatalf("protocol=%q", c.Protocol)
	}
}
