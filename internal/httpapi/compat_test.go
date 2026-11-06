package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 这些测试固化旧接口的响应形状。新增统一会话投影后，
// 旧客户端（手机中继、桌面工作台、脚本）仍要能直接用，不能要求同步升级。

// 通知要能带着「点了去哪」一起下发：否则手机点任何通知都只打开首页。
func TestOutboxCarriesRoute(t *testing.T) {
	srv, repos := newTestServer(t)
	h := srv.Handler()
	u := ownerUser(t, repos, "主人")

	srv.Outbox.PushRoute(u.ID, "今日简报已生成", OutboxRouteBrief, fixedNow)
	srv.Outbox.Push(u.ID, "没有明确去处的提醒", fixedNow)

	req := httptest.NewRequest(http.MethodGet, "/api/outbox", nil)
	req.Header.Set("X-User-Id", u.ID)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	var items []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items=%v", items)
	}
	if items[0]["route"] != OutboxRouteBrief {
		t.Fatalf("brief route=%v", items[0]["route"])
	}
	if _, ok := items[1]["route"]; ok {
		t.Fatalf("没有去处的通知不应带 route: %v", items[1])
	}
}

func TestCompatLegacySessionsResponseShape(t *testing.T) {
	srv, repos := newTestServer(t)
	h := srv.Handler()
	ownerID := ownerUser(t, repos, "主人").ID
	sessionFor(t, repos, ownerID, "main", "第一条", "第二条")

	rec := getJSON(t, h, "/api/sessions")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	body := decodeMap(t, rec)
	sessions, ok := body["sessions"].([]any)
	if !ok {
		t.Fatalf("sessions key missing: %s", rec.Body)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions=%v", sessions)
	}
	entry := sessions[0].(map[string]any)
	for _, key := range []string{"id", "created_at", "updated_at", "messages", "preview"} {
		if _, ok := entry[key]; !ok {
			t.Fatalf("legacy session field %q missing: %s", key, rec.Body)
		}
	}
	// 新字段不能污染旧响应。
	for _, key := range []string{"kind", "task_id", "job_id", "session_id", "status", "unread_count"} {
		if _, ok := entry[key]; ok {
			t.Fatalf("legacy session must not carry %q: %s", key, rec.Body)
		}
	}
}

func TestCompatLegacyJobsResponseShape(t *testing.T) {
	h, _ := newJobServer(t)

	rec := postJSON(t, h, "/api/jobs", `{"agent_id":"cli","task":"跑一次构建","work_dir":"/tmp"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("submit status=%d body=%s", rec.Code, rec.Body)
	}
	submitBody := decodeMap(t, rec)
	if _, ok := submitBody["task"]; !ok {
		t.Fatalf("submit shape changed: %s", rec.Body)
	}

	rec = getJSON(t, h, "/api/jobs")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", rec.Code, rec.Body)
	}
	body := decodeMap(t, rec)
	tasks, ok := body["tasks"].([]any)
	if !ok || len(tasks) != 1 {
		t.Fatalf("tasks=%v body=%s", tasks, rec.Body)
	}
	entry := tasks[0].(map[string]any)
	for _, key := range []string{"id", "agent_id", "task", "status", "created_at", "updated_at"} {
		if _, ok := entry[key]; !ok {
			t.Fatalf("legacy job field %q missing: %s", key, rec.Body)
		}
	}

	rec = getJSON(t, h, "/api/jobs/job-does-not-exist")
	// 未知 id 返回 404，错误体形状保持不变。
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing job status=%d body=%s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "error") {
		t.Fatalf("missing job body=%s", rec.Body)
	}
}

func TestCompatWorkTaskEndpointsUnchanged(t *testing.T) {
	srv, _, _, _ := newWorkTaskServer(t)
	h := srv.Handler()

	rec := postJSON(t, h, "/api/work-tasks", `{"title":"写周报","goal":"整理本周工作","profile":"internal"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body)
	}
	taskBody := decodeTaskResponse(t, rec)["task"].(map[string]any)
	taskID := taskBody["id"].(string)
	version := int(taskBody["version"].(float64))

	rec = getJSON(t, h, "/api/work-tasks?limit=10")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", rec.Code, rec.Body)
	}
	if _, ok := decodeTaskResponse(t, rec)["tasks"]; !ok {
		t.Fatalf("work-tasks list shape changed: %s", rec.Body)
	}

	rec = postJSON(t, h, "/api/work-tasks/"+taskID+"/messages",
		`{"text":"先核对日期","expected_version":`+itoa(version)+`}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("message status=%d body=%s", rec.Code, rec.Body)
	}
	msg := decodeTaskResponse(t, rec)
	for _, key := range []string{"inbox", "delivery"} {
		if _, ok := msg[key]; !ok {
			t.Fatalf("work-task message field %q missing: %s", key, rec.Body)
		}
	}

	// 旧版本号必须冲突，不能静默接受。
	rec = postJSON(t, h, "/api/work-tasks/"+taskID+"/pause", `{"expected_version":999}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale pause status=%d body=%s", rec.Code, rec.Body)
	}
}
