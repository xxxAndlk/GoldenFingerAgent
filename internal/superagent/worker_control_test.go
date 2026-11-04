package superagent_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"goldenfinger/agent/internal/agent"
	"goldenfinger/agent/internal/store"
	"goldenfinger/agent/internal/superagent"
	"goldenfinger/agent/internal/worktask"
)

func TestWorkerStepControllerConsumesInboxOnce(t *testing.T) {
	_, db, u := newTestService(t)
	ctx := context.Background()
	tasks := worktask.New(db)
	task, err := tasks.Create(ctx, worktask.CreateInput{
		OwnerID: u.ID, Title: "核对", Goal: "核对信息", IdempotencyKey: "step-task", Start: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	repos := store.NewRepos(db)
	job, err := repos.AgentJobs.ActivePrimaryForTask(ctx, u.ID, task.ID)
	if err != nil || job == nil {
		t.Fatalf("job=%+v err=%v", job, err)
	}
	if changed, err := repos.AgentJobs.MarkRunning(ctx, job.ID, time.Now()); err != nil || !changed {
		t.Fatalf("mark running: %v %v", changed, err)
	}
	session := &store.ChatSession{OwnerUserID: u.ID, Kind: "worker", WorkTaskID: task.ID}
	if err := repos.Sessions.Create(ctx, session); err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.SetActive(ctx, u.ID, task.ID, job.ID, session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.AddMessage(ctx, u.ID, task.ID, "先核对日期", nil, "inbox-step-1"); err != nil {
		t.Fatal(err)
	}

	controller := superagent.NewWorkerStepController(db, tasks, job)
	agentSession := &agent.Session{ID: session.ID, UserID: u.ID}
	messages, err := controller.BeforeStep(ctx, agentSession, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || !strings.Contains(messages[0].Content, "先核对日期") {
		t.Fatalf("messages=%+v", messages)
	}
	inbox, err := repos.WorkTaskInbox.ListByTask(ctx, u.ID, task.ID, 10)
	if err != nil || len(inbox) != 1 || inbox[0].Status != store.InboxConsumed ||
		inbox[0].ConsumedByJobID != job.ID {
		t.Fatalf("inbox=%+v err=%v", inbox, err)
	}
	persisted, err := repos.Sessions.RecentMessages(ctx, session.ID, 10)
	if err != nil || len(persisted) != 1 || !strings.Contains(persisted[0].Content, "先核对日期") {
		t.Fatalf("persisted=%+v err=%v", persisted, err)
	}

	again, err := controller.BeforeStep(ctx, agentSession, 2)
	if err != nil || len(again) != 0 {
		t.Fatalf("second step must not duplicate: %+v err=%v", again, err)
	}
	persisted, _ = repos.Sessions.RecentMessages(ctx, session.ID, 10)
	if len(persisted) != 1 {
		t.Fatalf("duplicate persistent message: %+v", persisted)
	}
}

func TestWorkerStepControllerPauseAndCancelBoundaries(t *testing.T) {
	_, db, u := newTestService(t)
	ctx := context.Background()
	tasks := worktask.New(db)
	task, err := tasks.Create(ctx, worktask.CreateInput{
		OwnerID: u.ID, Title: "暂停", Goal: "执行", IdempotencyKey: "pause-task", Start: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	repos := store.NewRepos(db)
	job, _ := repos.AgentJobs.ActivePrimaryForTask(ctx, u.ID, task.ID)
	if _, err := repos.AgentJobs.MarkRunning(ctx, job.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.SetActive(ctx, u.ID, task.ID, job.ID, ""); err != nil {
		t.Fatal(err)
	}
	controller := superagent.NewWorkerStepController(db, tasks, job)
	agentSession := &agent.Session{ID: "s", UserID: u.ID}

	if _, err := tasks.Pause(ctx, u.ID, task.ID, -1); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.BeforeStep(ctx, agentSession, 1); !errors.Is(err, agent.ErrStepPause) {
		t.Fatalf("pause boundary err=%v", err)
	}

	// 恢复后再次取消，控制器必须识别为取消而不是普通错误。
	if _, err := tasks.Resume(ctx, u.ID, task.ID, -1); err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.Cancel(ctx, u.ID, task.ID, -1); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.BeforeStep(ctx, agentSession, 2); !errors.Is(err, agent.ErrStepCancel) {
		t.Fatalf("cancel boundary err=%v", err)
	}
}
