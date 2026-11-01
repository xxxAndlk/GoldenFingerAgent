package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"goldenfinger/agent/internal/store"
	"goldenfinger/agent/internal/superagent"
)

func getJSON(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeMap(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return out
}

func TestSuperAgentTriggerThreadAndEvents(t *testing.T) {
	srv, _ := newTestServer(t)
	h := srv.Handler()
	body := `{"channel":"wechat","external_id":"msg-1","thread_external_id":"chat-1","kind":"message","title":"项目群","body":"周五能交付吗"}`

	first := postJSON(t, h, "/api/triggers", body)
	if first.Code != http.StatusCreated {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body)
	}
	firstBody := decodeMap(t, first)
	trigger := firstBody["trigger"].(map[string]any)
	triggerID := trigger["id"].(string)
	thread := firstBody["thread"].(map[string]any)
	threadID := thread["id"].(string)
	if trigger["status"] != store.TriggerStatusQueued {
		t.Fatalf("trigger status=%v", trigger["status"])
	}

	second := postJSON(t, h, "/api/triggers", body)
	if second.Code != http.StatusOK {
		t.Fatalf("duplicate status=%d body=%s", second.Code, second.Body)
	}
	secondBody := decodeMap(t, second)
	if secondBody["created"].(bool) {
		t.Fatal("duplicate must not be created")
	}
	if id := secondBody["trigger"].(map[string]any)["id"].(string); id != triggerID {
		t.Fatalf("duplicate id=%s want=%s", id, triggerID)
	}

	list := getJSON(t, h, "/api/triggers")
	if list.Code != 200 {
		t.Fatalf("list triggers status=%d", list.Code)
	}
	triggers := decodeMap(t, list)["triggers"].([]any)
	if len(triggers) != 1 {
		t.Fatalf("triggers=%d", len(triggers))
	}

	threads := getJSON(t, h, "/api/threads")
	if threads.Code != 200 {
		t.Fatalf("list threads status=%d", threads.Code)
	}
	if len(decodeMap(t, threads)["threads"].([]any)) != 1 {
		t.Fatalf("threads=%s", threads.Body)
	}

	detail := getJSON(t, h, "/api/threads/"+threadID)
	if detail.Code != 200 {
		t.Fatalf("thread detail status=%d body=%s", detail.Code, detail.Body)
	}
	if len(decodeMap(t, detail)["triggers"].([]any)) != 1 {
		t.Fatalf("detail=%s", detail.Body)
	}

	events := getJSON(t, h, "/api/events?limit=10")
	if events.Code != 200 {
		t.Fatalf("events status=%d", events.Code)
	}
	eventBody := decodeMap(t, events)
	if len(eventBody["events"].([]any)) != 2 {
		t.Fatalf("events=%s", events.Body)
	}
	if eventBody["next_event_id"].(float64) == 0 {
		t.Fatal("next_event_id must advance")
	}

	health := getJSON(t, h, "/api/superagent/health")
	if health.Code != 200 {
		t.Fatalf("health status=%d body=%s", health.Code, health.Body)
	}
	healthBody := decodeMap(t, health)
	if healthBody["triggers"].(map[string]any)[store.TriggerStatusQueued].(float64) != 1 {
		t.Fatalf("health=%s", health.Body)
	}
}

func TestMainSessionEndpointAndDefaultChat(t *testing.T) {
	srv, _ := newTestServer(t)
	h := srv.Handler()

	mainRec := getJSON(t, h, "/api/sessions/main")
	if mainRec.Code != http.StatusOK {
		t.Fatalf("main session status=%d body=%s", mainRec.Code, mainRec.Body)
	}
	mainBody := decodeMap(t, mainRec)
	session := mainBody["session"].(map[string]any)
	mainID := session["id"].(string)
	if session["kind"] != "main" {
		t.Fatalf("main session=%v", session)
	}

	first := postJSON(t, h, "/api/chat", `{"text":"第一句"}`)
	if first.Code != http.StatusOK {
		t.Fatalf("chat status=%d body=%s", first.Code, first.Body)
	}
	if got := decodeMap(t, first)["session_id"]; got != mainID {
		t.Fatalf("default chat session=%v want main %s", got, mainID)
	}
	second := postJSON(t, h, "/api/chat", `{"text":"第二句"}`)
	if second.Code != http.StatusOK {
		t.Fatalf("second chat status=%d body=%s", second.Code, second.Body)
	}
	if got := decodeMap(t, second)["session_id"]; got != mainID {
		t.Fatalf("second default chat session=%v want main %s", got, mainID)
	}
	list := getJSON(t, h, "/api/sessions")
	if list.Code != http.StatusOK {
		t.Fatalf("sessions status=%d", list.Code)
	}
	if sessions := decodeMap(t, list)["sessions"].([]any); len(sessions) != 1 {
		t.Fatalf("sessions=%s", list.Body)
	}
}

func TestAgentJobsEndpointUsesDispatcher(t *testing.T) {
	srv, _ := newTestServer(t)
	h := srv.Handler()
	rec := getJSON(t, h, "/api/agent-jobs")
	if rec.Code != http.StatusOK {
		t.Fatalf("agent-jobs status=%d body=%s", rec.Code, rec.Body)
	}
	if tasks := decodeMap(t, rec)["tasks"].([]any); len(tasks) != 0 {
		t.Fatalf("tasks=%s", rec.Body)
	}
}

func TestSuperAgentRouteEndpointDeterministic(t *testing.T) {
	srv, _ := newTestServer(t)
	h := srv.Handler()
	ingested := postJSON(t, h, "/api/triggers", `{
		"channel":"notification","external_id":"route-1","kind":"notification","title":"提醒"
	}`)
	if ingested.Code != http.StatusCreated {
		t.Fatalf("ingest status=%d body=%s", ingested.Code, ingested.Body)
	}
	routed := postJSON(t, h, "/api/superagent/route?mode=deterministic", `{}`)
	if routed.Code != 200 {
		t.Fatalf("route status=%d body=%s", routed.Code, routed.Body)
	}
	results := decodeMap(t, routed)["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("results=%s", routed.Body)
	}
	if action := results[0].(map[string]any)["action"]; action != string(superagent.ActionNotify) {
		t.Fatalf("action=%v", action)
	}
	list := getJSON(t, h, "/api/triggers?status="+store.TriggerStatusRouted)
	if list.Code != 200 {
		t.Fatalf("list status=%d", list.Code)
	}
	if len(decodeMap(t, list)["triggers"].([]any)) != 1 {
		t.Fatalf("routed triggers=%s", list.Body)
	}
}

func TestSuperAgentActionOutboxVersionConflict(t *testing.T) {
	srv, _ := newTestServer(t)
	h := srv.Handler()

	created := postJSON(t, h, "/api/action-outbox", `{
		"channel":"email","kind":"reply","body":"草稿","idempotency_key":"draft-1"
	}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body)
	}
	item := decodeMap(t, created)["outbox"].(map[string]any)
	id := item["id"].(string)
	if item["status"] != store.OutboxPendingApproval {
		t.Fatalf("status=%v", item["status"])
	}

	stale := postJSON(t, h, "/api/action-outbox/"+id+"/approve", `{"version":99}`)
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale approve status=%d body=%s", stale.Code, stale.Body)
	}
	ok := postJSON(t, h, "/api/action-outbox/"+id+"/approve", `{"version":0}`)
	if ok.Code != 200 {
		t.Fatalf("approve status=%d body=%s", ok.Code, ok.Body)
	}
	approved := decodeMap(t, ok)["outbox"].(map[string]any)
	if approved["status"] != store.OutboxApproved || approved["version"].(float64) != 1 {
		t.Fatalf("approved=%v", approved)
	}
	replay := postJSON(t, h, "/api/action-outbox/"+id+"/approve", `{"version":0}`)
	if replay.Code != http.StatusConflict {
		t.Fatalf("replay approve status=%d body=%s", replay.Code, replay.Body)
	}
}

func TestSuperAgentWorkerTasksSteerAndCancel(t *testing.T) {
	srv, repos := newTestServer(t)
	h := srv.Handler()
	ingested := postJSON(t, h, "/api/triggers", `{
		"channel":"email","external_id":"mail-2","thread_external_id":"mail-thread-2","kind":"email","title":"delivery"
	}`)
	if ingested.Code != http.StatusCreated {
		t.Fatalf("ingest status=%d body=%s", ingested.Code, ingested.Body)
	}
	ingestBody := decodeMap(t, ingested)
	triggerID := ingestBody["trigger"].(map[string]any)["id"].(string)
	threadID := ingestBody["thread"].(map[string]any)["id"].(string)

	u, err := repos.Users.ListAll(t.Context())
	if err != nil || len(u) == 0 {
		t.Fatalf("list users: %v", err)
	}
	now := time.Now()
	job := &store.AgentJob{
		ID: "worker-http-1", OwnerUserID: u[0].ID, AgentID: "superagent", Task: "draft reply",
		Status: store.AgentJobQueued, IdempotencyKey: "worker-http-1",
		ThreadID: threadID, ParentTriggerID: triggerID, CreatedAt: now, UpdatedAt: now,
	}
	if err := repos.AgentJobs.Create(t.Context(), job); err != nil {
		t.Fatal(err)
	}

	tasks := getJSON(t, h, "/api/worker-tasks?thread_id="+threadID)
	if tasks.Code != 200 {
		t.Fatalf("worker tasks status=%d body=%s", tasks.Code, tasks.Body)
	}
	if len(decodeMap(t, tasks)["tasks"].([]any)) != 1 {
		t.Fatalf("tasks=%s", tasks.Body)
	}

	steer := postJSON(t, h, "/api/worker-tasks/"+job.ID+"/steer", `{"text":"先核对日期"}`)
	if steer.Code != http.StatusAccepted {
		t.Fatalf("steer status=%d body=%s", steer.Code, steer.Body)
	}
	cancel := postJSON(t, h, "/api/worker-tasks/"+job.ID+"/cancel", `{}`)
	if cancel.Code != 200 {
		t.Fatalf("cancel status=%d body=%s", cancel.Code, cancel.Body)
	}
	updated, err := repos.AgentJobs.Get(t.Context(), u[0].ID, job.ID)
	if err != nil || updated.Status != store.AgentJobCancelled {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	events, err := repos.AgentJobs.Events(t.Context(), u[0].ID, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundSteer := false
	for _, event := range events {
		if event.Type == "steer" {
			foundSteer = true
		}
	}
	if !foundSteer {
		t.Fatalf("steer event missing: %+v", events)
	}
}

func TestSuperAgentRequiresService(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.SuperAgent = nil
	h := srv.Handler()
	for _, path := range []string{"/api/triggers", "/api/threads", "/api/worker-tasks", "/api/action-outbox", "/api/events", "/api/superagent/health"} {
		req := httptest.NewRequest(http.MethodGet, path, bytes.NewReader(nil))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s status=%d body=%s", path, rec.Code, rec.Body)
		}
	}
}
