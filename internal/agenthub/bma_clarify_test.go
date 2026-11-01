package agenthub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWaitBMASessionReturnsClarifyPrompt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sessions/s1" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":     "s1",
			"status": "awaiting_clarify",
			"state": map[string]any{
				"pending_clarify": map[string]any{
					"id":       "ask-1",
					"question": "【计划确认】批准开工？",
					"detail":   "1. 创建测试文件",
					"options": []map[string]any{
						{"id": "approve", "label": "批准开工", "description": "按计划执行"},
						{"id": "revise", "label": "需要修改"},
					},
				},
			},
		})
	}))
	defer server.Close()

	manager := &Manager{HTTP: server.Client()}
	agent := Agent{ID: "bma", Name: "BMA", Kind: "bma", Endpoint: server.URL + "/api"}
	invocation, err := manager.waitBMASession(context.Background(), agent, server.URL+"/api", "s1", "running")
	if err != nil {
		t.Fatalf("waitBMASession: %v", err)
	}
	if invocation.State != "awaiting_clarify" {
		t.Fatalf("state = %q", invocation.State)
	}
	if invocation.TaskID != "s1" || invocation.QuestionID != "ask-1" {
		t.Fatalf("task/question = %q/%q", invocation.TaskID, invocation.QuestionID)
	}
	if len(invocation.Options) != 2 || invocation.Options[0].ID != "approve" {
		t.Fatalf("options = %+v", invocation.Options)
	}
	if !strings.Contains(invocation.Output, "批准开工") || !strings.Contains(invocation.Output, "s1") {
		t.Fatalf("output = %q", invocation.Output)
	}
}

func TestAnswerBMASendsClarifyAndReturnsResult(t *testing.T) {
	var got map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/health":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case r.URL.Path == "/api/sessions/s1/clarify" && r.Method == http.MethodPost:
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Errorf("decode clarify: %v", err)
			}
			_, _ = w.Write([]byte(`{"session_id":"s1","status":"running"}`))
		case r.URL.Path == "/api/sessions/s1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "s1", "status": "completed", "result": "已完成测试任务",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	manager := &Manager{
		BMAURL: server.URL,
		HTTP:   &http.Client{Timeout: 2 * time.Second},
	}
	invocation, err := manager.AnswerBMA(context.Background(), "s1", "approve", "ask-1")
	if err != nil {
		t.Fatalf("AnswerBMA: %v", err)
	}
	if got["answer"] != "approve" || got["question_id"] != "ask-1" {
		t.Fatalf("clarify body = %+v", got)
	}
	if invocation.State != "completed" || !strings.Contains(invocation.Output, "已完成测试任务") {
		t.Fatalf("invocation = %+v", invocation)
	}
}

func TestBMAPlanApprovalAnswer(t *testing.T) {
	plan := Invocation{
		State: "awaiting_clarify", Question: "【计划确认】执行前请确认",
		Options: []InvocationOption{
			{ID: "approve", Label: "批准开工"},
			{ID: "revise", Label: "需要修改"},
		},
	}
	if answer, ok := bmaPlanApprovalAnswer(plan); !ok || answer != "approve" {
		t.Fatalf("plan answer = %q/%v", answer, ok)
	}
	question := Invocation{
		State: "awaiting_clarify", Question: "请选择要处理的文件",
		Options: []InvocationOption{{ID: "approve", Label: "批准开工"}},
	}
	if _, ok := bmaPlanApprovalAnswer(question); ok {
		t.Fatal("ordinary ask_user must not be auto-approved")
	}
}

func TestBMANeedsInput(t *testing.T) {
	for _, status := range []string{"awaiting_clarify", "paused", "needs_input"} {
		if !bmaNeedsInput(status) {
			t.Errorf("bmaNeedsInput(%q) = false", status)
		}
	}
	for _, status := range []string{"running", "completed", "failed"} {
		if bmaNeedsInput(status) {
			t.Errorf("bmaNeedsInput(%q) = true", status)
		}
	}
}
