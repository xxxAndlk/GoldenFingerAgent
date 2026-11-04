package superagent_test

import (
	"context"
	"strings"
	"testing"

	"goldenfinger/agent/internal/llm/mock"
	"goldenfinger/agent/internal/store"
	"goldenfinger/agent/internal/superagent"
	"goldenfinger/agent/internal/worktask"
)

func TestRouterAssociatesThreadEventToIndexedTask(t *testing.T) {
	svc, db, u := newTestService(t)
	tasks := worktask.New(db)
	svc.Tasks = tasks
	task, err := tasks.Create(context.Background(), worktask.CreateInput{
		OwnerID: u.ID, Title: "整理客户邮件", Goal: "汇总客户邮件并列出待回复事项",
		Status: store.WorkTaskQueued, IdempotencyKey: "router-task-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	ingested, err := svc.Ingest(context.Background(), u.ID, superagent.TriggerInput{
		Channel: "email", ExternalID: "assoc-mail", ThreadExternalID: "assoc-thread",
		Kind: "email", Title: "客户补充", Body: "请在汇总里加上今天的邮件",
	})
	if err != nil {
		t.Fatal(err)
	}
	content := `{"decisions":[{"trigger_id":"` + ingested.Trigger.ID +
		`","action":"update_task","work_task_id":"` + task.ID +
		`","confidence":0.92,"association_reason":"同一位客户与同一批邮件","reason":"补充已有任务"}]}`
	router := &superagent.LLMRouter{
		Client: mock.New(mock.TextResponse(content)), Model: "router-test",
		TaskIndex: svc.TaskIndex,
	}
	results, err := svc.RoutePending(context.Background(), u.ID, router, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].WorkTaskID != task.ID {
		t.Fatalf("results=%+v", results)
	}
	inbox, err := db.WorkTaskInbox().ListByTask(context.Background(), u.ID, task.ID, 10)
	if err != nil || len(inbox) != 1 {
		t.Fatalf("inbox=%+v err=%v", inbox, err)
	}
	if inbox[0].SourceKind != store.InboxSourceThread || !strings.Contains(inbox[0].Body, "加上今天") {
		t.Fatalf("inbox item=%+v", inbox[0])
	}
	scripted := router.Client.(*mock.Scripted)
	if len(scripted.Calls) != 1 || !strings.Contains(scripted.Calls[0].Messages[1].Content, task.ID) {
		t.Fatalf("router prompt missing task index: %+v", scripted.Calls)
	}
}

func TestRouterLowConfidenceAsksInsteadOfMutating(t *testing.T) {
	svc, db, u := newTestService(t)
	tasks := worktask.New(db)
	svc.Tasks = tasks
	task, err := tasks.Create(context.Background(), worktask.CreateInput{
		OwnerID: u.ID, Title: "写报告", Goal: "形成报告", Status: store.WorkTaskQueued,
		IdempotencyKey: "router-task-2",
	})
	if err != nil {
		t.Fatal(err)
	}
	ingested, err := svc.Ingest(context.Background(), u.ID, superagent.TriggerInput{
		Channel: "email", ExternalID: "low-conf-mail", Kind: "email", Title: "不确定",
	})
	if err != nil {
		t.Fatal(err)
	}
	content := `{"decisions":[{"trigger_id":"` + ingested.Trigger.ID +
		`","action":"update_task","work_task_id":"` + task.ID +
		`","confidence":0.3,"question":"这条要归到写报告吗？"}]}`
	router := &superagent.LLMRouter{
		Client: mock.New(mock.TextResponse(content)), Model: "router-test", TaskIndex: svc.TaskIndex,
	}
	results, err := svc.RoutePending(context.Background(), u.ID, router, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Action != superagent.ActionAskUser {
		t.Fatalf("results=%+v", results)
	}
	inbox, _ := db.WorkTaskInbox().ListByTask(context.Background(), u.ID, task.ID, 10)
	if len(inbox) != 0 {
		t.Fatalf("low confidence must not write inbox: %+v", inbox)
	}
}

func TestRouterRejectsTaskOutsideIndex(t *testing.T) {
	svc, db, u := newTestService(t)
	svc.Tasks = worktask.New(db)
	ingested, err := svc.Ingest(context.Background(), u.ID, superagent.TriggerInput{
		Channel: "email", ExternalID: "bad-assoc", Kind: "email", Title: "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	content := `{"decisions":[{"trigger_id":"` + ingested.Trigger.ID +
		`","action":"update_task","work_task_id":"not-in-index","confidence":0.9}]}`
	router := &superagent.LLMRouter{
		Client: mock.New(mock.TextResponse(content)), Model: "router-test", TaskIndex: svc.TaskIndex,
	}
	results, _ := svc.RoutePending(context.Background(), u.ID, router, 10)
	if len(results) != 1 || results[0].Status != store.TriggerStatusFailed {
		t.Fatalf("results=%+v", results)
	}
}

func TestCoordinatorCreatesAndControlsTask(t *testing.T) {
	_, db, u := newTestService(t)
	tasks := worktask.New(db)
	coordinator := &superagent.Coordinator{Tasks: tasks}

	result, handled, err := coordinator.Handle(context.Background(), u.ID, "/task new 整理本周邮件")
	if err != nil || !handled || result.TaskID == "" {
		t.Fatalf("create: result=%+v handled=%v err=%v", result, handled, err)
	}
	detail, err := tasks.Get(context.Background(), u.ID, result.TaskID)
	if err != nil || detail.Task.Status != store.WorkTaskQueued || detail.Jobs == nil || len(detail.Jobs) != 1 {
		t.Fatalf("detail=%+v err=%v", detail, err)
	}

	result, handled, err = coordinator.Handle(context.Background(), u.ID, "暂停任务 "+result.TaskID)
	if err != nil || !handled || result.Action != "pause" {
		t.Fatalf("pause: result=%+v handled=%v err=%v", result, handled, err)
	}
	detail, _ = tasks.Get(context.Background(), u.ID, detail.Task.ID)
	if detail.Task.Status != store.WorkTaskPaused {
		t.Fatalf("status=%s", detail.Task.Status)
	}

	result, handled, err = coordinator.Handle(context.Background(), u.ID, "恢复任务 "+detail.Task.ID[:8])
	if err != nil || !handled || result.Action != "resume" {
		t.Fatalf("resume by prefix: result=%+v handled=%v err=%v", result, handled, err)
	}
	detail, _ = tasks.Get(context.Background(), u.ID, detail.Task.ID)
	if detail.Task.Status != store.WorkTaskQueued {
		t.Fatalf("status=%s", detail.Task.Status)
	}

	if _, handled, _ := coordinator.Handle(context.Background(), u.ID, "你好"); handled {
		t.Fatal("ordinary chat must use the fast path")
	}
}
