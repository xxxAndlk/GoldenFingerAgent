package superagent_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"goldenfinger/agent/internal/store"
	"goldenfinger/agent/internal/superagent"
)

type fakeWorkerRunner struct {
	block   bool
	started chan string
}

func (f *fakeWorkerRunner) RunWorker(ctx context.Context, req superagent.WorkerRequest) (*superagent.WorkerResult, error) {
	if f.started != nil {
		select {
		case f.started <- req.JobID:
		default:
		}
	}
	if f.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return &superagent.WorkerResult{
		SessionID: "sess-" + req.JobID,
		Reply:     "已完成：" + req.Goal,
		Summary:   "Worker 摘要",
	}, nil
}

func waitJobStatus(t *testing.T, db *store.DB, ownerID, id, want string) *store.AgentJob {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		job, err := db.AgentJobs().Get(context.Background(), ownerID, id)
		if err == nil && job.Status == want {
			return job
		}
		time.Sleep(20 * time.Millisecond)
	}
	job, _ := db.AgentJobs().Get(context.Background(), ownerID, id)
	t.Fatalf("job %s did not reach %s: %+v", id, want, job)
	return nil
}

func agentEventKinds(t *testing.T, db *store.DB, ownerID string) map[string]bool {
	t.Helper()
	events, err := db.AgentEvents().After(context.Background(), ownerID, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	kinds := make(map[string]bool, len(events))
	for _, event := range events {
		kinds[event.Kind] = true
	}
	return kinds
}

func newWorkerFixture(t *testing.T) (*superagent.Service, *superagent.WorkerManager, *store.DB, *store.User, *store.Trigger) {
	t.Helper()
	svc, db, u := newTestService(t)
	result, err := svc.Ingest(context.Background(), u.ID, superagent.TriggerInput{
		Channel: "email", ExternalID: "worker-mail", ThreadExternalID: "worker-thread",
		Kind: "email", Title: "交付时间", Body: "周五能交付吗",
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeWorkerRunner{}
	manager := superagent.NewWorkerManager(db, runner)
	manager.MaxWorkers = 1
	manager.Timeout = 2 * time.Second
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := manager.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return svc, manager, db, u, result.Trigger
}

func TestWorkerManagerCompletesAndIsIdempotent(t *testing.T) {
	_, manager, db, u, trigger := newWorkerFixture(t)
	results := make(chan string, 1)
	manager.OnResult = func(_ context.Context, _ *store.AgentJob, summary string) error {
		results <- summary
		return nil
	}
	decision := superagent.Decision{Action: superagent.ActionCreateWorker, Goal: "起草回复", Profile: "read", Priority: 2}
	id, err := manager.CreateWorker(context.Background(), u.ID, trigger, decision)
	if err != nil {
		t.Fatal(err)
	}
	again, err := manager.CreateWorker(context.Background(), u.ID, trigger, decision)
	if err != nil {
		t.Fatal(err)
	}
	if again != id {
		t.Fatalf("idempotent create: %s vs %s", again, id)
	}
	job := waitJobStatus(t, db, u.ID, id, store.AgentJobCompleted)
	if !strings.Contains(job.Output, "起草回复") || job.ResultSummary == "" || job.SessionID == "" {
		t.Fatalf("job = %+v", job)
	}
	if job.ThreadID != trigger.ThreadID || job.ParentTriggerID != trigger.ID {
		t.Fatalf("thread/trigger missing: %+v", job)
	}
	workers, err := db.ThreadSubscriptions().WorkersForThread(context.Background(), u.ID, trigger.ThreadID)
	if err != nil || len(workers) != 1 || workers[0] != id {
		t.Fatalf("subscriptions=%v err=%v", workers, err)
	}
	tasks, err := db.AgentJobs().ListWorkerTasks(context.Background(), u.ID, trigger.ThreadID, 10)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("worker tasks=%d err=%v", len(tasks), err)
	}
	notifications, err := db.AgentNotifications().Unread(context.Background(), u.ID, 0, 10)
	if err != nil || len(notifications) == 0 {
		t.Fatalf("notifications=%v err=%v", notifications, err)
	}
	events, err := db.AgentJobs().Events(context.Background(), u.ID, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 3 {
		t.Fatalf("events=%d", len(events))
	}
	kinds := agentEventKinds(t, db, u.ID)
	for _, want := range []string{"worker.created", "worker.running", "worker.completed"} {
		if !kinds[want] {
			t.Fatalf("missing %s event: %v", want, kinds)
		}
	}
	select {
	case summary := <-results:
		if strings.TrimSpace(summary) != "Worker 摘要" {
			t.Fatalf("OnResult summary=%q", summary)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OnResult was not called")
	}
	thread, err := db.Threads().Get(context.Background(), u.ID, trigger.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if thread.Summary == "" || thread.SummaryEventID == 0 {
		t.Fatalf("thread summary not updated: %+v", thread)
	}
}

func TestWorkerManagerCancelStopsRunningWorker(t *testing.T) {
	svc, db, u := newTestService(t)
	ingested, err := svc.Ingest(context.Background(), u.ID, superagent.TriggerInput{
		Channel: "email", ExternalID: "worker-cancel", Kind: "email", Title: "cancel",
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeWorkerRunner{block: true, started: make(chan string, 1)}
	manager := superagent.NewWorkerManager(db, runner)
	manager.MaxWorkers = 1
	manager.Timeout = 5 * time.Second
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := manager.Start(ctx); err != nil {
		t.Fatal(err)
	}
	id, err := manager.CreateWorker(context.Background(), u.ID, ingested.Trigger,
		superagent.Decision{Action: superagent.ActionCreateWorker, Goal: "wait"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not start")
	}
	cancelled, err := manager.Cancel(context.Background(), u.ID, id)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != store.AgentJobCancelled {
		t.Fatalf("status=%s", cancelled.Status)
	}
	if kinds := agentEventKinds(t, db, u.ID); !kinds["worker.cancelled"] {
		t.Fatalf("missing worker.cancelled event: %v", kinds)
	}
}

type countingRunner struct{ calls atomic.Int32 }

func (r *countingRunner) RunWorker(context.Context, superagent.WorkerRequest) (*superagent.WorkerResult, error) {
	r.calls.Add(1)
	return &superagent.WorkerResult{Reply: "unexpected"}, nil
}

func createWorkerJob(t *testing.T, db *store.DB, ownerID, id string) *store.AgentJob {
	t.Helper()
	now := time.Now()
	job := &store.AgentJob{
		ID: id, OwnerUserID: ownerID, AgentID: superagent.WorkerAgentID, Task: "task",
		Status: store.AgentJobQueued, IdempotencyKey: id, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.AgentJobs().Create(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	return job
}

func TestWorkerManagerRestartMarksRunningInterrupted(t *testing.T) {
	_, db, u := newTestService(t)
	job := createWorkerJob(t, db, u.ID, "restart-running")
	if changed, err := db.AgentJobs().MarkRunning(context.Background(), job.ID, time.Now()); err != nil || !changed {
		t.Fatalf("mark running: changed=%v err=%v", changed, err)
	}
	runner := &countingRunner{}
	manager := superagent.NewWorkerManager(db, runner)
	manager.MaxWorkers = 1
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := manager.Start(ctx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	final, err := db.AgentJobs().Get(context.Background(), u.ID, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != store.AgentJobInterrupted {
		t.Fatalf("status=%s", final.Status)
	}
	if runner.calls.Load() != 0 {
		t.Fatal("interrupted worker must not be rerun")
	}
	notes, err := db.AgentNotifications().Unread(context.Background(), u.ID, 0, 10)
	if err != nil || len(notes) == 0 {
		t.Fatalf("interrupted notification missing: %+v err=%v", notes, err)
	}
	if kinds := agentEventKinds(t, db, u.ID); !kinds["worker.interrupted"] {
		t.Fatalf("missing worker.interrupted event: %v", kinds)
	}
}

func TestWorkerManagerLeavesWaitingWorkerPaused(t *testing.T) {
	_, db, u := newTestService(t)
	job := createWorkerJob(t, db, u.ID, "waiting-worker")
	if changed, err := db.AgentJobs().MarkRunning(context.Background(), job.ID, time.Now()); err != nil || !changed {
		t.Fatalf("mark running: changed=%v err=%v", changed, err)
	}
	if changed, err := db.AgentJobs().SetWaiting(context.Background(), u.ID, job.ID,
		store.AgentJobWaitingClarify, json.RawMessage(`{"question":"which?"}`), time.Now()); err != nil || !changed {
		t.Fatalf("set waiting: changed=%v err=%v", changed, err)
	}
	runner := &countingRunner{}
	manager := superagent.NewWorkerManager(db, runner)
	manager.MaxWorkers = 1
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := manager.Start(ctx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	final, err := db.AgentJobs().Get(context.Background(), u.ID, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != store.AgentJobRunning || final.WaitingKind != store.AgentJobWaitingClarify {
		t.Fatalf("waiting job changed: %+v", final)
	}
	if runner.calls.Load() != 0 {
		t.Fatal("waiting job must not be claimed")
	}
}

func TestCreateWorkerRequiresGoal(t *testing.T) {
	_, manager, _, u, trigger := newWorkerFixture(t)
	if _, err := manager.CreateWorker(context.Background(), u.ID, trigger,
		superagent.Decision{Action: superagent.ActionCreateWorker}); err == nil {
		t.Fatal("empty goal must fail")
	}
	if _, err := manager.CreateWorker(context.Background(), u.ID, nil,
		superagent.Decision{Action: superagent.ActionCreateWorker, Goal: "x"}); err == nil {
		t.Fatal("nil trigger must fail")
	}
}
