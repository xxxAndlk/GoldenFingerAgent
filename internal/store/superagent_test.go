package store_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"goldenfinger/agent/internal/store"
)

func superAgentUser(t *testing.T, repos *store.Repos, name string) *store.User {
	t.Helper()
	u := &store.User{UserType: store.UserGeneral, Name: name, TZ: "Asia/Shanghai"}
	if err := repos.Users.Create(context.Background(), u); err != nil {
		t.Fatalf("create user: %v", err)
	}
	return u
}

func TestTriggerDedupeAndStateCAS(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	repos := store.NewRepos(db)
	u := superAgentUser(t, repos, "trigger-user")

	thread := &store.Thread{OwnerUserID: u.ID, Channel: "wechat", ExternalID: "chat-1", Title: "项目群"}
	if err := repos.Threads.Create(ctx, thread); err != nil {
		t.Fatal(err)
	}
	trigger := &store.Trigger{
		OwnerUserID: u.ID, Channel: "wechat", ExternalID: "msg-1", ThreadID: thread.ID,
		Kind: "message", Title: "hello", Body: "hello", DedupeKey: "wechat:msg-1",
	}
	created, got, err := repos.Triggers.Create(ctx, trigger)
	if err != nil || !created {
		t.Fatalf("first create: created=%v err=%v", created, err)
	}
	again := *trigger
	again.ID = "another-id"
	created, gotAgain, err := repos.Triggers.Create(ctx, &again)
	if err != nil {
		t.Fatal(err)
	}
	if created || gotAgain.ID != got.ID {
		t.Fatalf("duplicate must return original: created=%v id=%s want=%s", created, gotAgain.ID, got.ID)
	}

	changed, err := repos.Triggers.Transition(ctx, u.ID, got.ID,
		[]string{store.TriggerStatusNew}, store.TriggerStatusQueued, nil, "", time.Now())
	if err != nil || !changed {
		t.Fatalf("new->queued: changed=%v err=%v", changed, err)
	}
	changed, err = repos.Triggers.Transition(ctx, u.ID, got.ID,
		[]string{store.TriggerStatusNew}, store.TriggerStatusIgnored, nil, "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("stale transition must not win")
	}

	for i := 0; i < 3; i++ {
		if _, err := repos.AgentEvents.Append(ctx, &store.AgentEvent{
			OwnerUserID: u.ID, Kind: "test", EntityType: "trigger", EntityID: got.ID,
			Payload: json.RawMessage(`{"n":1}`),
		}); err != nil {
			t.Fatal(err)
		}
	}
	events, err := repos.AgentEvents.After(ctx, u.ID, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("page size=%d want 2", len(events))
	}
	rest, err := repos.AgentEvents.After(ctx, u.ID, events[len(events)-1].ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 1 {
		t.Fatalf("remaining=%d want 1", len(rest))
	}
}

func TestMainSessionAdoptsLatestChatAndHidesWorkers(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	repos := store.NewRepos(db)
	u := superAgentUser(t, repos, "main-session-user")

	older := &store.ChatSession{OwnerUserID: u.ID, Kind: "chat"}
	if err := repos.Sessions.Create(ctx, older); err != nil {
		t.Fatal(err)
	}
	newer := &store.ChatSession{OwnerUserID: u.ID, Kind: "chat"}
	if err := repos.Sessions.Create(ctx, newer); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE chat_session SET updated_at = ? WHERE id = ?`,
		time.Now().Add(-time.Hour), older.ID); err != nil {
		t.Fatal(err)
	}
	worker := &store.ChatSession{OwnerUserID: u.ID, Kind: "worker"}
	if err := repos.Sessions.Create(ctx, worker); err != nil {
		t.Fatal(err)
	}

	main, err := repos.Sessions.GetOrCreateMain(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if main.ID != newer.ID || main.Kind != "main" {
		t.Fatalf("main=%+v want latest chat %s", main, newer.ID)
	}
	again, err := repos.Sessions.GetOrCreateMain(ctx, u.ID)
	if err != nil || again.ID != main.ID {
		t.Fatalf("main must be stable: %+v err=%v", again, err)
	}

	items, err := repos.Sessions.ListByOwner(ctx, u.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Kind == "worker" {
			t.Fatalf("worker session must stay hidden: %+v", items)
		}
	}
	if len(items) != 2 {
		t.Fatalf("visible sessions=%d items=%+v", len(items), items)
	}

	duplicate := &store.ChatSession{OwnerUserID: u.ID, Kind: "main"}
	if err := repos.Sessions.Create(ctx, duplicate); err == nil {
		t.Fatal("second main session must violate the unique constraint")
	}
}

func TestThreadSummaryAndSubscription(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	repos := store.NewRepos(db)
	u := superAgentUser(t, repos, "thread-user")

	thread := &store.Thread{OwnerUserID: u.ID, Channel: "email", ExternalID: "mail-1"}
	if err := repos.Threads.Create(ctx, thread); err != nil {
		t.Fatal(err)
	}
	changed, err := repos.Threads.UpdateSummary(ctx, u.ID, thread.ID, 10, "new", time.Now())
	if err != nil || !changed {
		t.Fatalf("summary update: changed=%v err=%v", changed, err)
	}
	changed, err = repos.Threads.UpdateSummary(ctx, u.ID, thread.ID, 5, "stale", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("older summary cursor must not overwrite newer summary")
	}

	job := &store.AgentJob{
		ID: "worker-1", OwnerUserID: u.ID, AgentID: "superagent", Task: "do",
		WorkDir: "", Status: store.AgentJobQueued, IdempotencyKey: "worker-1",
		ThreadID: thread.ID, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := repos.AgentJobs.Create(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err := repos.ThreadSubscriptions.Add(ctx, u.ID, thread.ID, job.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := repos.ThreadSubscriptions.Add(ctx, u.ID, thread.ID, job.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	workers, err := repos.ThreadSubscriptions.WorkersForThread(ctx, u.ID, thread.ID)
	if err != nil || len(workers) != 1 {
		t.Fatalf("workers=%v err=%v", workers, err)
	}
	tasks, err := repos.AgentJobs.ListWorkerTasks(ctx, u.ID, thread.ID, 10)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("worker tasks=%v err=%v", tasks, err)
	}
}

func TestInterruptRunningScope(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	repos := store.NewRepos(db)
	u := superAgentUser(t, repos, "restart-user")
	now := time.Now()

	makeRunning := func(id, agentID string) {
		job := &store.AgentJob{
			ID: id, OwnerUserID: u.ID, AgentID: agentID, Task: "task",
			Status: store.AgentJobQueued, IdempotencyKey: id, CreatedAt: now, UpdatedAt: now,
		}
		if err := repos.AgentJobs.Create(ctx, job); err != nil {
			t.Fatal(err)
		}
		if changed, err := repos.AgentJobs.MarkRunning(ctx, id, now); err != nil || !changed {
			t.Fatalf("mark %s running: changed=%v err=%v", id, changed, err)
		}
	}
	makeRunning("generic-1", "codex")
	makeRunning("worker-1", store.AgentIDSuperAgent)
	makeRunning("worker-waiting", store.AgentIDSuperAgent)
	if changed, err := repos.AgentJobs.SetWaiting(ctx, u.ID, "worker-waiting",
		store.AgentJobWaitingClarify, json.RawMessage(`{"question":"which?"}`), now); err != nil || !changed {
		t.Fatalf("set waiting: changed=%v err=%v", changed, err)
	}

	interrupted, err := repos.AgentJobs.InterruptRunningExcludingAgent(ctx, store.AgentIDSuperAgent, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(interrupted) != 1 || interrupted[0].ID != "generic-1" {
		t.Fatalf("generic interrupted=%+v", interrupted)
	}
	worker, _ := repos.AgentJobs.Get(ctx, u.ID, "worker-1")
	if worker.Status != store.AgentJobRunning {
		t.Fatalf("superagent must be left for WorkerManager: %s", worker.Status)
	}

	interrupted, err = repos.AgentJobs.InterruptRunningForAgent(ctx, store.AgentIDSuperAgent, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(interrupted) != 1 || interrupted[0].ID != "worker-1" {
		t.Fatalf("worker interrupted=%+v", interrupted)
	}
	worker, _ = repos.AgentJobs.Get(ctx, u.ID, "worker-1")
	if worker.Status != store.AgentJobInterrupted {
		t.Fatalf("worker status=%s", worker.Status)
	}
	waiting, _ := repos.AgentJobs.Get(ctx, u.ID, "worker-waiting")
	if waiting.Status != store.AgentJobRunning || waiting.WaitingKind != store.AgentJobWaitingClarify {
		t.Fatalf("waiting must survive restart: %+v", waiting)
	}
}

func TestActionOutboxVersionCAS(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	repos := store.NewRepos(db)
	u := superAgentUser(t, repos, "outbox-user")

	item := &store.ActionOutbox{
		OwnerUserID: u.ID, Channel: "email", Kind: "reply", Body: "draft",
		RequiresApproval: true, IdempotencyKey: "outbox-1",
	}
	if err := repos.ActionOutbox.Create(ctx, item); err != nil {
		t.Fatal(err)
	}
	if item.Status != store.OutboxPendingApproval {
		t.Fatalf("status=%s", item.Status)
	}
	changed, err := repos.ActionOutbox.UpdateDraft(ctx, u.ID, item.ID, 0, "edited", nil, nil, time.Now())
	if err != nil || !changed {
		t.Fatalf("update draft: changed=%v err=%v", changed, err)
	}
	changed, err = repos.ActionOutbox.UpdateDraft(ctx, u.ID, item.ID, 0, "stale edit", nil, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("stale edit must fail")
	}
	changed, err = repos.ActionOutbox.Approve(ctx, u.ID, item.ID, 1, u.ID, time.Now())
	if err != nil || !changed {
		t.Fatalf("approve: changed=%v err=%v", changed, err)
	}
	changed, err = repos.ActionOutbox.Approve(ctx, u.ID, item.ID, 1, u.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("duplicate approve must not change state twice")
	}
}
