package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"goldenfinger/agent/internal/llm"
)

func TestChatBuildsRequestAndParsesToolCalls(t *testing.T) {
	var gotBody responsesRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("missing auth header")
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status": "completed",
			"output": [{
				"type": "function_call",
				"id": "fc_1",
				"call_id": "call_1",
				"name": "create_task",
				"arguments": "{\"title\":\"订票\"}"
			}],
			"usage": {"input_tokens": 10, "output_tokens": 5}
		}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "test-key", "deepseek-flash")
	resp, err := c.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: "system"},
			{Role: llm.RoleUser, Content: "我后天要去订票"},
		},
		Tools: []llm.ToolSpec{
			{Name: "create_task", Description: "create", Parameters: json.RawMessage(`{"type":"object"}`)},
		},
	})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if gotBody.Model != "deepseek-flash" {
		t.Errorf("model = %q", gotBody.Model)
	}
	if gotBody.Instructions != "system" {
		t.Errorf("instructions = %q", gotBody.Instructions)
	}
	if len(gotBody.Input) != 1 || gotBody.Input[0].Role != "user" || gotBody.Input[0].Content[0].Text != "我后天要去订票" {
		t.Errorf("input = %+v", gotBody.Input)
	}
	if len(gotBody.Tools) != 1 || gotBody.Tools[0].Type != "function" || gotBody.Tools[0].Name != "create_task" {
		t.Errorf("tools not serialized: %+v", gotBody.Tools)
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("want 1 tool call, got %+v", resp.Message)
	}
	tc := resp.Message.ToolCalls[0]
	if tc.ID != "call_1" || tc.Name != "create_task" || string(tc.Arguments) != `{"title":"订票"}` {
		t.Errorf("tool call = %+v", tc)
	}
	if resp.FinishReason != "stop" {
		t.Errorf("finish = %q", resp.FinishReason)
	}
	if resp.Usage.PromptTokens != 10 || resp.Usage.CompletionTokens != 5 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestHistorySerializesToolRoundTrip(t *testing.T) {
	var gotBody responsesRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"已创建"}]}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "", "deepseek-flash")
	_, err := c.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: "订票"},
			{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "call_1", Name: "create_task", Arguments: json.RawMessage(`{"title":"订票"}`)}}},
			{Role: llm.RoleTool, ToolCallID: "call_1", Name: "create_task", Content: `{"ok":true}`},
		},
	})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if len(gotBody.Input) != 3 {
		t.Fatalf("input items = %+v", gotBody.Input)
	}
	fc, fo := gotBody.Input[1], gotBody.Input[2]
	if fc.Type != "function_call" || fc.CallID != "call_1" || fc.Name != "create_task" || fc.Arguments != `{"title":"订票"}` {
		t.Errorf("function_call item = %+v", fc)
	}
	if fo.Type != "function_call_output" || fo.CallID != "call_1" || fo.Output != `{"ok":true}` {
		t.Errorf("function_call_output item = %+v", fo)
	}
}

func TestJSONModeSetsTextFormat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body responsesRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Text == nil || body.Text.Format.Type != "json_object" {
			t.Errorf("text.format = %+v", body.Text)
		}
		_, _ = w.Write([]byte(`{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"{}"}]}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "", "deepseek-flash")
	resp, err := c.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "extract"}},
		JSONMode: true,
	})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if resp.Message.Content != "{}" {
		t.Errorf("content = %q", resp.Message.Content)
	}
}

func TestStreamEmitsDeltasAndFinal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, line := range []string{
			`data: {"type":"response.output_text.delta","delta":"你好"}`,
			``,
			`data: {"type":"response.output_text.delta","delta":"呀"}`,
			``,
			`data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"你好呀"}]}],"usage":{"input_tokens":3,"output_tokens":2}}}`,
			``,
			`data: [DONE]`,
			``,
		} {
			fmt.Fprintln(w, line)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "", "deepseek-flash")
	var deltas strings.Builder
	resp, err := c.Stream(context.Background(), llm.ChatRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	}, func(s string) { deltas.WriteString(s) })
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if deltas.String() != "你好呀" {
		t.Errorf("deltas = %q", deltas.String())
	}
	if resp.Message.Content != "你好呀" || resp.Usage.PromptTokens != 3 {
		t.Errorf("final = %+v", resp)
	}
}

func TestEmbedParsesVectors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1,0.2,0.3]},{"embedding":[0.4,0.5,0.6]}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "", "bge-m3")
	c.EmbedModel = "bge-m3"
	vecs, err := c.Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	if len(vecs) != 2 || len(vecs[0]) != 3 {
		t.Fatalf("vecs = %+v", vecs)
	}
	if vecs[1][2] != 0.6 {
		t.Errorf("vec = %+v", vecs[1])
	}
}
