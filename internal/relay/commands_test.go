package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeControlAPI 模拟电脑端的控制面接口，记录手机发来的请求。
func fakeControlAPI(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	record := func(entry string) {
		seen = append(seen, entry)
	}
	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/work-tasks", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		record("create:" + body["title"].(string) + ":" + body["idempotency_key"].(string))
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"task": map[string]any{"id": "wt-1", "title": "整理邮件", "status": "queued"}})
	})
	mux.HandleFunc("GET /api/work-tasks", func(w http.ResponseWriter, r *http.Request) {
		record("list:" + r.URL.Query().Get("status"))
		_ = json.NewEncoder(w).Encode(map[string]any{"tasks": []map[string]any{{"id": "wt-1"}}})
	})
	mux.HandleFunc("GET /api/work-tasks/{id}", func(w http.ResponseWriter, r *http.Request) {
		record("get:" + r.PathValue("id"))
		_ = json.NewEncoder(w).Encode(map[string]any{"task": map[string]any{"id": r.PathValue("id"), "status": "active"}})
	})
	mux.HandleFunc("POST /api/work-tasks/{id}/messages", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		record("message:" + r.PathValue("id") + ":" + body["text"].(string))
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{"inbox": map[string]any{"id": "ib-1"}, "delivery": "queued_for_resume"})
	})
	mux.HandleFunc("POST /api/work-tasks/{id}/{action}", func(w http.ResponseWriter, r *http.Request) {
		record("action:" + r.PathValue("id") + ":" + r.PathValue("action"))
		_ = json.NewEncoder(w).Encode(map[string]any{"task": map[string]any{"id": r.PathValue("id"), "status": "paused"}})
	})
	mux.HandleFunc("GET /api/conversations", func(w http.ResponseWriter, r *http.Request) {
		record("conversations:" + r.URL.Query().Get("kind"))
		_ = json.NewEncoder(w).Encode(map[string]any{"conversations": []map[string]any{{"id": "s-1", "kind": "worker"}}})
	})
	mux.HandleFunc("GET /api/conversations/{id}/messages", func(w http.ResponseWriter, r *http.Request) {
		record("messages:" + r.PathValue("id") + ":" + r.URL.Query().Get("before"))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"messages": []map[string]any{{"id": "m-1", "role": "user", "content": "你好"}},
		})
	})
	mux.HandleFunc("GET /api/action-outbox", func(w http.ResponseWriter, r *http.Request) {
		record("outbox")
		_ = json.NewEncoder(w).Encode(map[string]any{"actions": []map[string]any{{"id": "ao-1"}}})
	})
	mux.HandleFunc("POST /api/action-outbox/{id}/approve", func(w http.ResponseWriter, r *http.Request) {
		record("approve:" + r.PathValue("id"))
		_ = json.NewEncoder(w).Encode(map[string]any{"action": map[string]any{"id": r.PathValue("id"), "status": "approved"}})
	})
	mux.HandleFunc("POST /api/artifacts/{id}/accept", func(w http.ResponseWriter, r *http.Request) {
		record("accept:" + r.PathValue("id"))
		_ = json.NewEncoder(w).Encode(map[string]any{"artifact": map[string]any{"id": r.PathValue("id"), "status": "accepted"}})
	})
	mux.HandleFunc("POST /api/artifacts/{id}/deliver", func(w http.ResponseWriter, r *http.Request) {
		record("deliver:" + r.PathValue("id"))
		_ = json.NewEncoder(w).Encode(map[string]any{"artifact": map[string]any{"id": r.PathValue("id")}, "name": "报告.pdf"})
	})
	mux.HandleFunc("GET /api/briefs/latest", func(w http.ResponseWriter, r *http.Request) {
		record("brief-latest:" + r.URL.Query().Get("date"))
		_ = json.NewEncoder(w).Encode(map[string]any{"brief": map[string]any{"id": "b-1", "content_md": "# 今日"}})
	})
	mux.HandleFunc("POST /api/briefs/generate", func(w http.ResponseWriter, r *http.Request) {
		record("brief-generate")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"brief": map[string]any{"id": "b-2"}})
	})
	mux.HandleFunc("GET /api/collection/channels", func(w http.ResponseWriter, r *http.Request) {
		record("collection-list")
		_ = json.NewEncoder(w).Encode(map[string]any{"channels": []map[string]any{{"channel": "wechat_desktop", "availability": "desktop"}}})
	})
	mux.HandleFunc("POST /api/collection/channels", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		record("collection-configure:" + body["channel"].(string))
		_ = json.NewEncoder(w).Encode(map[string]any{"applied": true})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &seen
}

func TestForwardCommandRoutesControlPlane(t *testing.T) {
	api, seen := fakeControlAPI(t)
	handle := ExecuteLocal(api.URL, nil)
	ctx := context.Background()

	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{"create", `{"kind":"work_task_create","title":"整理邮件","goal":"g","profile":"read","idempotency_key":"k1"}`, "create:整理邮件:k1"},
		{"list", `{"kind":"work_task_list","status":"queued"}`, "list:queued"},
		{"get", `{"kind":"work_task_get","task_id":"wt-1"}`, "get:wt-1"},
		{"message", `{"kind":"work_task_message","task_id":"wt-1","text":"先核对日期"}`, "message:wt-1:先核对日期"},
		{"action", `{"kind":"work_task_action","task_id":"wt-1","action":"pause"}`, "action:wt-1:pause"},
		{"conversations", `{"kind":"conversation_list","conversation_kind":"worker"}`, "conversations:worker"},
		{"messages", `{"kind":"conversation_messages","session_id":"s-9","before":"m-1"}`, "messages:s-9:m-1"},
		{"outbox", `{"kind":"outbox_list"}`, "outbox"},
		{"approve", `{"kind":"outbox_approve","outbox_id":"ao-1"}`, "approve:ao-1"},
		{"accept", `{"kind":"artifact_accept","artifact_id":"ar-1"}`, "accept:ar-1"},
		{"deliver", `{"kind":"artifact_deliver","artifact_id":"ar-1","version":2}`, "deliver:ar-1"},
		{"brief-latest", `{"kind":"brief_latest","date":"2026-11-05"}`, "brief-latest:2026-11-05"},
		{"brief-generate", `{"kind":"brief_generate","date":"2026-11-05"}`, "brief-generate"},
		{"collection-list", `{"kind":"collection_list"}`, "collection-list"},
		{"collection-configure", `{"kind":"collection_configure","channel":"wechat_desktop","enabled":true}`, "collection-configure:wechat_desktop"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := handle(ctx, json.RawMessage(tc.payload))
			if err != nil {
				t.Fatal(err)
			}
			if !result.OK {
				t.Fatalf("result=%+v", result)
			}
			if result.Command != CommandExecuted {
				t.Fatalf("command=%q result=%+v", result.Command, result)
			}
		})
	}
	want := []string{
		"create:整理邮件:k1", "list:queued", "get:wt-1", "message:wt-1:先核对日期",
		"action:wt-1:pause", "conversations:worker", "messages:s-9:m-1", "outbox",
		"approve:ao-1", "accept:ar-1", "deliver:ar-1", "brief-latest:2026-11-05",
		"brief-generate", "collection-list", "collection-configure:wechat_desktop",
	}
	if len(*seen) != len(want) {
		t.Fatalf("calls=%v", *seen)
	}
	for i := range want {
		if (*seen)[i] != want[i] {
			t.Fatalf("calls=%v", *seen)
		}
	}
}

func TestForwardCommandClassifiesFailures(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/work-tasks/{id}/pause", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "版本过期"})
	})
	mux.HandleFunc("POST /api/work-tasks/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "任务已结束"})
	})
	mux.HandleFunc("GET /api/work-tasks/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "数据库炸了"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	handle := ExecuteLocal(srv.URL, nil)
	ctx := context.Background()

	conflict, err := handle(ctx, json.RawMessage(`{"kind":"work_task_action","task_id":"wt-1","action":"pause"}`))
	if err != nil {
		t.Fatal(err)
	}
	if conflict.OK || conflict.Command != CommandConflict {
		t.Fatalf("conflict=%+v", conflict)
	}
	if conflict.Error != "版本过期" {
		t.Fatalf("conflict error=%q", conflict.Error)
	}

	rejected, err := handle(ctx, json.RawMessage(`{"kind":"work_task_action","task_id":"wt-1","action":"cancel"}`))
	if err != nil {
		t.Fatal(err)
	}
	if rejected.OK || rejected.Command != CommandRejected {
		t.Fatalf("rejected=%+v", rejected)
	}

	unknown, err := handle(ctx, json.RawMessage(`{"kind":"work_task_get","task_id":"wt-1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if unknown.OK || unknown.Command != CommandUnknown {
		t.Fatalf("unknown=%+v", unknown)
	}
}

func TestForwardCommandRejectsBadInput(t *testing.T) {
	api, _ := fakeControlAPI(t)
	handle := ExecuteLocal(api.URL, nil)
	ctx := context.Background()

	// 缺少 task_id：明确拒绝，不能打到电脑。
	result, err := handle(ctx, json.RawMessage(`{"kind":"work_task_get"}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.OK || result.Command != CommandRejected {
		t.Fatalf("result=%+v", result)
	}

	// 未知动作：拒绝。
	result, err = handle(ctx, json.RawMessage(`{"kind":"work_task_action","task_id":"wt-1","action":"explode"}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.OK || result.Command != CommandRejected {
		t.Fatalf("result=%+v", result)
	}
}

func TestForwardCommandLeavesUnknownKindToChatPath(t *testing.T) {
	_, seen := fakeControlAPI(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/chat", func(w http.ResponseWriter, r *http.Request) {
		*seen = append(*seen, "chat")
		_ = json.NewEncoder(w).Encode(map[string]string{"reply": "你好"})
	})
	chatSrv := httptest.NewServer(mux)
	defer chatSrv.Close()

	handle := ExecuteLocal(chatSrv.URL, nil)
	result, err := handle(context.Background(), json.RawMessage(`{"kind":"something_else","text":"你好"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.Reply != "你好" {
		t.Fatalf("result=%+v", result)
	}
}
