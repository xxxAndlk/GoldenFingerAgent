package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"goldenfinger/agent/internal/store"
	"goldenfinger/agent/internal/worktask"
)

func newWorkTaskServer(t *testing.T) (*Server, *store.Repos, *worktask.Service, *worktask.ArtifactService) {
	t.Helper()
	srv, repos := newTestServer(t)
	tasks := worktask.New(repos.DB)
	artifacts := worktask.NewArtifactService(repos.DB, tasks, t.TempDir())
	srv.Tasks = tasks
	srv.Artifacts = artifacts
	return srv, repos, tasks, artifacts
}

func decodeTaskResponse(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v", rec.Body, err)
	}
	return body
}

func TestWorkTaskAPILifecycle(t *testing.T) {
	srv, repos, _, _ := newWorkTaskServer(t)
	h := srv.Handler()

	rec := postJSON(t, h, "/api/work-tasks", `{"title":"写周报","goal":"整理本周工作并形成周报","profile":"internal","priority":2}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body)
	}
	taskBody := decodeTaskResponse(t, rec)["task"].(map[string]any)
	taskID := taskBody["id"].(string)
	version := int(taskBody["version"].(float64))
	if taskID == "" || taskBody["status"] != store.WorkTaskQueued {
		t.Fatalf("task=%s", rec.Body)
	}

	rec = getJSON(t, h, "/api/work-tasks?limit=10")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), taskID) {
		t.Fatalf("list status=%d body=%s", rec.Code, rec.Body)
	}

	// 错误版本必须 409。
	rec = postJSON(t, h, "/api/work-tasks/"+taskID+"/pause", `{"expected_version":99}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale pause status=%d body=%s", rec.Code, rec.Body)
	}
	rec = postJSON(t, h, "/api/work-tasks/"+taskID+"/pause", `{"expected_version":`+itoa(version)+`}`)
	if rec.Code != 200 {
		t.Fatalf("pause status=%d body=%s", rec.Code, rec.Body)
	}
	paused := decodeTaskResponse(t, rec)["task"].(map[string]any)
	version = int(paused["version"].(float64))
	if paused["status"] != store.WorkTaskPaused {
		t.Fatalf("paused=%s", rec.Body)
	}

	rec = postJSON(t, h, "/api/work-tasks/"+taskID+"/messages", `{"text":"先核对日期","expected_version":`+itoa(version)+`}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("message status=%d body=%s", rec.Code, rec.Body)
	}
	msg := decodeTaskResponse(t, rec)
	if msg["inbox"] == nil || msg["delivery"] != "queued_for_resume" {
		t.Fatalf("message=%s", rec.Body)
	}

	rec = postJSON(t, h, "/api/work-tasks/"+taskID+"/resume", `{"expected_version":`+itoa(version)+`}`)
	if rec.Code != 200 {
		t.Fatalf("resume status=%d body=%s", rec.Code, rec.Body)
	}
	resumed := decodeTaskResponse(t, rec)["task"].(map[string]any)
	version = int(resumed["version"].(float64))
	if resumed["status"] != store.WorkTaskQueued {
		t.Fatalf("resumed=%s", rec.Body)
	}

	rec = postJSON(t, h, "/api/work-tasks/"+taskID+"/cancel", `{"expected_version":`+itoa(version)+`}`)
	if rec.Code != 200 {
		t.Fatalf("cancel status=%d body=%s", rec.Code, rec.Body)
	}
	cancelled := decodeTaskResponse(t, rec)["task"].(map[string]any)
	if cancelled["status"] != store.WorkTaskCancelled {
		t.Fatalf("cancelled=%s", rec.Body)
	}
	users, _ := repos.Users.ListAll(context.Background())
	job, err := repos.AgentJobs.ActivePrimaryForTask(context.Background(), users[0].ID, taskID)
	if err != nil || job != nil {
		t.Fatalf("no active job should remain: %+v err=%v", job, err)
	}
}

func TestLegacySteerRoutesToWorkTaskInbox(t *testing.T) {
	srv, repos, _, _ := newWorkTaskServer(t)
	h := srv.Handler()
	ctx := context.Background()
	rec := postJSON(t, h, "/api/work-tasks", `{"title":"旧接口","goal":"验证旧 steer","start":true}`)
	taskID := decodeTaskResponse(t, rec)["task"].(map[string]any)["id"].(string)
	users, _ := repos.Users.ListAll(ctx)
	job, err := repos.AgentJobs.ActivePrimaryForTask(ctx, users[0].ID, taskID)
	if err != nil || job == nil {
		t.Fatalf("job=%+v err=%v", job, err)
	}
	rec = postJSON(t, h, "/api/worker-tasks/"+job.ID+"/steer", `{"text":"先核对日期"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("steer status=%d body=%s", rec.Code, rec.Body)
	}
	body := decodeTaskResponse(t, rec)
	if body["queued"] != true || body["inbox_id"] == "" {
		t.Fatalf("steer=%s", rec.Body)
	}
	inbox, err := repos.WorkTaskInbox.ListByTask(ctx, users[0].ID, taskID, 10)
	if err != nil || len(inbox) != 1 || inbox[0].Body == "" {
		t.Fatalf("inbox=%+v err=%v", inbox, err)
	}
}

func TestArtifactAPIAcceptCompletesTask(t *testing.T) {
	srv, repos, tasks, artifacts := newWorkTaskServer(t)
	h := srv.Handler()
	ctx := context.Background()

	rec := postJSON(t, h, "/api/work-tasks", `{"title":"交付文档","goal":"生成一版说明","start":true}`)
	taskBody := decodeTaskResponse(t, rec)["task"].(map[string]any)
	taskID := taskBody["id"].(string)
	users, _ := repos.Users.ListAll(ctx)
	ownerID := users[0].ID
	job, err := repos.AgentJobs.ActivePrimaryForTask(ctx, ownerID, taskID)
	if err != nil || job == nil {
		t.Fatalf("job missing: %+v err=%v", job, err)
	}
	if _, err := tasks.SetActive(ctx, ownerID, taskID, job.ID, ""); err != nil {
		t.Fatal(err)
	}

	rec = postJSON(t, h, "/api/work-tasks/"+taskID+"/artifacts",
		`{"kind":"text","title":"说明草稿","content":"第一版说明"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("artifact create status=%d body=%s", rec.Code, rec.Body)
	}
	artifactBody := decodeTaskResponse(t, rec)["artifact"].(map[string]any)
	artifactID := artifactBody["id"].(string)
	artifactVersion := int(artifactBody["current_version"].(float64))
	if artifactVersion != 1 {
		t.Fatalf("artifact=%s", rec.Body)
	}
	if _, err := artifacts.SubmitForAcceptance(ctx, ownerID, artifactID, 1); err != nil {
		t.Fatal(err)
	}
	detail, err := tasks.Get(ctx, ownerID, taskID)
	if err != nil || detail.Task.Status != store.WorkTaskPendingAccept {
		t.Fatalf("task should be pending acceptance: %+v err=%v", detail, err)
	}

	rec = postJSON(t, h, "/api/artifacts/"+artifactID+"/accept", `{"expected_version":1}`)
	if rec.Code != 200 {
		t.Fatalf("accept status=%d body=%s", rec.Code, rec.Body)
	}
	detail, _ = tasks.Get(ctx, ownerID, taskID)
	if detail.Task.Status != store.WorkTaskCompleted {
		t.Fatalf("accepted artifact must complete task: %+v", detail.Task)
	}
	rec = getJSON(t, h, "/api/artifacts/"+artifactID)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "第一版说明") {
		t.Fatalf("artifact get status=%d body=%s", rec.Code, rec.Body)
	}
	rec = postJSON(t, h, "/api/artifacts/"+artifactID+"/accept", `{"expected_version":0}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale accept must conflict: %d %s", rec.Code, rec.Body)
	}
}

func itoa(value int) string {
	return strconv.Itoa(value)
}
