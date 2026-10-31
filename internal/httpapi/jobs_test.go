package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"goldenfinger/agent/internal/store"
)

// fakeJobs 用于验证 HTTP 契约（真实队列行为见 agenthub 的派发器测试）。
type fakeJobs struct {
	jobs   map[string]*store.AgentJob
	events map[string][]store.AgentJobEvent
	seq    int
}

func newFakeJobs() *fakeJobs {
	return &fakeJobs{jobs: map[string]*store.AgentJob{}, events: map[string][]store.AgentJobEvent{}}
}

func (f *fakeJobs) Submit(_ context.Context, ownerID, agentID, task, workDir, key string) (*store.AgentJob, error) {
	if task == "" {
		return nil, errors.New("任务不能为空")
	}
	for _, j := range f.jobs {
		if j.IdempotencyKey == key && key != "" {
			return j, nil
		}
	}
	f.seq++
	job := &store.AgentJob{
		ID: "job-" + string(rune('0'+f.seq)), OwnerUserID: ownerID, AgentID: agentID,
		Task: task, WorkDir: workDir, Status: store.AgentJobQueued, IdempotencyKey: key,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	f.jobs[job.ID] = job
	f.events[job.ID] = []store.AgentJobEvent{{ID: 1, JobID: job.ID, Type: "queued", Body: "任务已持久化"}}
	return job, nil
}

func (f *fakeJobs) Get(_ context.Context, ownerID, id string) (*store.AgentJob, []store.AgentJobEvent, error) {
	job, ok := f.jobs[id]
	if !ok || job.OwnerUserID != ownerID {
		return nil, nil, errors.New("not found")
	}
	return job, f.events[id], nil
}

func (f *fakeJobs) List(_ context.Context, ownerID string, _ int) ([]store.AgentJob, error) {
	out := []store.AgentJob{}
	for _, j := range f.jobs {
		if j.OwnerUserID == ownerID {
			out = append(out, *j)
		}
	}
	return out, nil
}

func (f *fakeJobs) Cancel(_ context.Context, ownerID, id string) (*store.AgentJob, error) {
	job, ok := f.jobs[id]
	if !ok || job.OwnerUserID != ownerID {
		return nil, errors.New("任务不存在")
	}
	if job.Status != store.AgentJobQueued && job.Status != store.AgentJobRunning {
		return nil, errors.New("任务已结束或不存在，不能取消")
	}
	job.Status = store.AgentJobCancelled
	return job, nil
}

func newJobServer(t *testing.T) (http.Handler, *fakeJobs) {
	t.Helper()
	srv, _ := newTestServer(t)
	jobs := newFakeJobs()
	srv.Jobs = jobs
	return srv.Handler(), jobs
}

func TestJobsEndpointsLifecycle(t *testing.T) {
	h, _ := newJobServer(t)

	rec := postJSON(t, h, "/api/jobs", `{"agent_id":"codex","task":"把下载目录按类型整理","work_dir":"/tmp"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("submit status=%d body=%s", rec.Code, rec.Body)
	}
	var submitted struct {
		Task store.AgentJob `json:"task"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &submitted); err != nil {
		t.Fatal(err)
	}
	id := submitted.Task.ID
	if id == "" || submitted.Task.Status != store.AgentJobQueued {
		t.Fatalf("bad submit response: %+v", submitted.Task)
	}

	// 查询：状态 + 事件（手机重连后靠它补看进度）。
	req := httptest.NewRequest(http.MethodGet, "/api/jobs/"+id, nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", rec.Code, rec.Body)
	}
	var got struct {
		Task struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"task"`
		Events []struct {
			Type string `json:"type"`
		} `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Task.ID != id || len(got.Events) == 0 {
		t.Fatalf("bad get response: %+v", got)
	}

	// 列表
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/jobs?limit=10", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", rec.Code, rec.Body)
	}
	var listed struct {
		Tasks []store.AgentJob `json:"tasks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Tasks) != 1 {
		t.Fatalf("tasks=%+v", listed.Tasks)
	}

	// 取消
	req = httptest.NewRequest(http.MethodDelete, "/api/jobs/"+id, nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel status=%d body=%s", rec.Code, rec.Body)
	}
	var cancelled struct {
		Task store.AgentJob `json:"task"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &cancelled); err != nil {
		t.Fatal(err)
	}
	if cancelled.Task.Status != store.AgentJobCancelled {
		t.Fatalf("status=%s", cancelled.Task.Status)
	}

	// 重复取消：409，而不是静默成功。
	req = httptest.NewRequest(http.MethodDelete, "/api/jobs/"+id, nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("double cancel status=%d", rec.Code)
	}
}

func TestJobsRejectsMissingAgentAndBadBody(t *testing.T) {
	h, _ := newJobServer(t)
	if rec := postJSON(t, h, "/api/jobs", `{"task":"x"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing agent_id status=%d", rec.Code)
	}
	if rec := postJSON(t, h, "/api/jobs", `not-json`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad body status=%d", rec.Code)
	}
	if rec := postJSON(t, h, "/api/jobs", `{"agent_id":"codex","task":""}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty task status=%d", rec.Code)
	}
}

func TestJobsUnknownIDIs404(t *testing.T) {
	h, _ := newJobServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/jobs/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestJobsDisabledReturns503(t *testing.T) {
	srv, _ := newTestServer(t)
	h := srv.Handler() // Jobs 未装配
	rec := postJSON(t, h, "/api/jobs", `{"agent_id":"codex","task":"x"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d", rec.Code)
	}
}
