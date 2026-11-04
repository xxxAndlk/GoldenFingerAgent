package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"goldenfinger/agent/internal/store"
)

func TestWorkTaskUpgradeBackfillsLegacyJobs(t *testing.T) {
	ctx := context.Background()
	db, err := store.Connect(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	applyLegacySchema(t, db,
		"0001_init.sql", "0002_indexes.sql", "0003_intents.sql", "0004_inbox.sql",
		"0005_agent_jobs.sql", "0006_longterm_memory.sql", "0007_superagent.sql",
		"0008_single_main_session.sql", "0009_channels_briefs.sql", "0010_channel_configs.sql")

	now := time.Date(2026, 3, 5, 10, 0, 0, 0, time.UTC)
	for _, stmt := range []string{
		`INSERT INTO app_user (id, user_type, name, tz, notif_prefs_json, created_at)
		 VALUES ('u1', 'general', 'legacy', 'Asia/Shanghai', '{}', '2026-03-05 10:00:00')`,
		`INSERT INTO chat_session (id, owner_user_id, kind, thread_id, summary, state_json, created_at, updated_at)
		 VALUES ('s1', 'u1', 'worker', 'th1', '', '{}', '2026-03-05 10:00:00', '2026-03-05 10:00:00')`,
		`INSERT INTO chat_session (id, owner_user_id, kind, thread_id, summary, state_json, created_at, updated_at)
		 VALUES ('s2', 'u1', 'chat', '', '', '{}', '2026-03-05 10:00:00', '2026-03-05 10:00:00')`,
		`INSERT INTO agent_thread (id, owner_user_id, channel, external_id, title, participants_json,
		 summary, summary_event_id, status, retention_class, created_at, updated_at)
		 VALUES ('th1', 'u1', 'email', 'thread-1', '旧邮件', '[]', '', 0, 'active', 'default',
		 '2026-03-05 10:00:00', '2026-03-05 10:00:00')`,
		`INSERT INTO agent_job (id, owner_user_id, agent_id, task, work_dir, status, idempotency_key,
		 attempt, thread_id, session_id, parent_trigger_id, profile, priority, result_summary,
		 waiting_kind, waiting_payload_json, version, created_at, updated_at, finished_at)
		 VALUES ('job-old', 'u1', 'superagent', '整理旧邮件', '', 'completed', 'legacy-job-old',
		 1, 'th1', 's1', '', 'internal', 3, '完成',
		 'none', '{}', 2, '2026-03-05 10:00:00', '2026-03-05 11:00:00', '2026-03-05 11:00:00')`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	applied, err := db.Migrate(ctx)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if len(applied) != 5 {
		t.Fatalf("applied=%v", applied)
	}
	repos := store.NewRepos(db)
	task, err := repos.WorkTasks.Get(ctx, "u1", "legacy-job-old")
	if err != nil {
		t.Fatalf("backfilled task: %v", err)
	}
	if task.Status != store.WorkTaskCompleted || task.Goal != "整理旧邮件" || task.Profile != "internal" ||
		task.CurrentSessionID != "s1" || task.Priority != 3 {
		t.Fatalf("task=%+v", task)
	}
	job, err := repos.AgentJobs.Get(ctx, "u1", "job-old")
	if err != nil {
		t.Fatal(err)
	}
	if job.WorkTaskID != task.ID || job.JobRole != store.AgentJobRolePrimary {
		t.Fatalf("job=%+v", job)
	}
	links, err := repos.WorkTaskThreads.ListByTask(ctx, "u1", task.ID)
	if err != nil || len(links) != 1 || links[0].ThreadID != "th1" {
		t.Fatalf("links=%+v err=%v", links, err)
	}
	session, err := repos.Sessions.Get(ctx, "s1")
	if err != nil || session.WorkTaskID != task.ID {
		t.Fatalf("session=%+v err=%v", session, err)
	}
	plain, err := repos.Sessions.Get(ctx, "s2")
	if err != nil || plain.WorkTaskID != "" {
		t.Fatalf("session without job must keep empty work_task_id: %+v err=%v", plain, err)
	}
	if again, err := db.Migrate(ctx); err != nil || len(again) != 0 {
		t.Fatalf("second migrate applied=%v err=%v", again, err)
	}
	_ = now
}

func TestWorkTaskCASAndOwnerIsolation(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	repos := store.NewRepos(db)
	u := superAgentUser(t, repos, "task-cas")
	other := superAgentUser(t, repos, "task-other")

	task := &store.WorkTask{
		OwnerUserID: u.ID, Title: "写报告", Goal: "整理并交付报告",
		Status: store.WorkTaskDraft, IdempotencyKey: "task-cas-1",
	}
	if err := repos.WorkTasks.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	if got, err := repos.WorkTasks.Get(ctx, other.ID, task.ID); !errors.Is(err, store.ErrNotFound) || got != nil {
		t.Fatalf("cross-owner read must fail: %+v err=%v", got, err)
	}
	changed, err := repos.WorkTasks.Transition(ctx, u.ID, task.ID, 9, store.WorkTaskQueued, time.Now())
	if err != nil || changed {
		t.Fatalf("stale version must fail: changed=%v err=%v", changed, err)
	}
	changed, err = repos.WorkTasks.Transition(ctx, u.ID, task.ID, 0, store.WorkTaskQueued, time.Now())
	if err != nil || !changed {
		t.Fatalf("valid CAS: changed=%v err=%v", changed, err)
	}
	if store.ValidWorkTaskTransition(store.WorkTaskCancelled, store.WorkTaskActive) {
		t.Fatal("cancelled must stay terminal")
	}
	if !store.ValidWorkTaskTransition(store.WorkTaskPendingAccept, store.WorkTaskCompleted) {
		t.Fatal("pending_acceptance -> completed must be allowed")
	}
}

func TestWorkTaskSinglePrimaryJobAndInboxIdempotency(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	repos := store.NewRepos(db)
	u := superAgentUser(t, repos, "job-primary")
	now := time.Now()
	task := &store.WorkTask{
		OwnerUserID: u.ID, Title: "任务", Goal: "目标", Status: store.WorkTaskActive,
		IdempotencyKey: "primary-task",
	}
	if err := repos.WorkTasks.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	makeJob := func(id string) *store.AgentJob {
		return &store.AgentJob{
			ID: id, OwnerUserID: u.ID, AgentID: store.AgentIDSuperAgent, Task: "执行",
			Status: store.AgentJobQueued, IdempotencyKey: id, WorkTaskID: task.ID,
			JobRole: store.AgentJobRolePrimary, CreatedAt: now, UpdatedAt: now,
		}
	}
	if err := repos.AgentJobs.Create(ctx, makeJob("primary-1")); err != nil {
		t.Fatal(err)
	}
	if err := repos.AgentJobs.Create(ctx, makeJob("primary-2")); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("second primary job must conflict, got %v", err)
	}
	delegated := makeJob("delegated-1")
	delegated.JobRole = store.AgentJobRoleDelegated
	if err := repos.AgentJobs.Create(ctx, delegated); err != nil {
		t.Fatalf("delegated job must coexist: %v", err)
	}

	item := &store.WorkTaskInbox{
		OwnerUserID: u.ID, TaskID: task.ID, SourceKind: store.InboxSourceUser,
		Kind: store.InboxKindSteer, Body: "补充", IdempotencyKey: "inbox-1",
	}
	created, first, err := repos.WorkTaskInbox.Add(ctx, item)
	if err != nil || !created || first.ID == "" {
		t.Fatalf("first inbox: created=%v item=%+v err=%v", created, first, err)
	}
	again := *item
	again.ID = "different"
	created, dup, err := repos.WorkTaskInbox.Add(ctx, &again)
	if err != nil || created || dup.ID != first.ID {
		t.Fatalf("inbox idempotency failed: created=%v dup=%+v err=%v", created, dup, err)
	}
	changed, err := repos.WorkTaskInbox.Consume(ctx, u.ID, first.ID, "primary-1", now)
	if err != nil || !changed {
		t.Fatalf("consume: changed=%v err=%v", changed, err)
	}
	changed, _ = repos.WorkTaskInbox.Consume(ctx, u.ID, first.ID, "primary-1", now)
	if changed {
		t.Fatal("inbox must only be consumed once")
	}
}

func TestArtifactVersionUniqueAndCAS(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	repos := store.NewRepos(db)
	u := superAgentUser(t, repos, "artifact-user")
	task := &store.WorkTask{OwnerUserID: u.ID, Title: "任务", Goal: "交付", IdempotencyKey: "artifact-task"}
	if err := repos.WorkTasks.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	artifact := &store.Artifact{OwnerUserID: u.ID, TaskID: task.ID, Kind: store.ArtifactKindText, Title: "草稿"}
	if err := repos.Artifacts.Create(ctx, artifact); err != nil {
		t.Fatal(err)
	}
	version := &store.ArtifactVersion{
		OwnerUserID: u.ID, ArtifactID: artifact.ID, Version: 1, ContentText: "v1", ContentHash: "h1",
	}
	if err := repos.ArtifactVersions.Add(ctx, version); err != nil {
		t.Fatal(err)
	}
	if err := repos.ArtifactVersions.Add(ctx, &store.ArtifactVersion{
		OwnerUserID: u.ID, ArtifactID: artifact.ID, Version: 1, ContentText: "duplicate",
	}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate artifact version must conflict, got %v", err)
	}
	changed, err := repos.Artifacts.Advance(ctx, u.ID, artifact.ID, 0, 1, store.ArtifactPending, time.Now())
	if err != nil || !changed {
		t.Fatalf("advance: changed=%v err=%v", changed, err)
	}
	changed, _ = repos.Artifacts.SetStatus(ctx, u.ID, artifact.ID, 0, store.ArtifactAccepted, time.Now())
	if changed {
		t.Fatal("accept with stale current_version must fail")
	}
}
