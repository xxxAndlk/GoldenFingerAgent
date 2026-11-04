package superagent_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"goldenfinger/agent/internal/memory"
	"goldenfinger/agent/internal/store"
	"goldenfinger/agent/internal/superagent"
	"goldenfinger/agent/internal/worktask"
)

type fakeMemorySearcher struct{}

func (fakeMemorySearcher) Search(context.Context, string, string, int) ([]memory.Snippet, error) {
	return []memory.Snippet{{
		ID: "fact-1", Text: "客户偏好周五下午沟通", Status: store.MemoryInferred,
	}}, nil
}

func TestContextBrokerIncludesTaskLiveThreadPolicyAndReceipts(t *testing.T) {
	_, db, u := newTestService(t)
	ctx := context.Background()
	repos := store.NewRepos(db)
	tasks := worktask.New(db)
	task, err := tasks.Create(ctx, worktask.CreateInput{
		OwnerID: u.ID, Title: "跟进客户", Goal: "整理客户需求并给出回复草案",
		CompletionCriteria: "包含三个待确认问题", Status: store.WorkTaskQueued,
		IdempotencyKey: "ctx-task",
	})
	if err != nil {
		t.Fatal(err)
	}
	thread := &store.Thread{OwnerUserID: u.ID, Channel: "email", ExternalID: "ctx-thread", Title: "客户邮件"}
	if err := repos.Threads.Create(ctx, thread); err != nil {
		t.Fatal(err)
	}
	if changed, err := repos.Threads.UpdateSummary(ctx, u.ID, thread.ID, 1, "客户希望周五前确认报价", time.Now()); err != nil || !changed {
		t.Fatalf("summary: %v %v", changed, err)
	}
	if err := repos.WorkTaskThreads.Add(ctx, &store.WorkTaskThread{
		OwnerUserID: u.ID, TaskID: task.ID, ThreadID: thread.ID,
		Relation: store.WorkTaskRelationOrigin, CreatedBy: "test",
	}); err != nil {
		t.Fatal(err)
	}
	policy := &store.PersonalPolicy{
		OwnerUserID: u.ID, PolicyType: "preference", Name: "沟通风格",
		Content: []byte(`"先给结论，再列证据"`), ScopeType: "global", Status: store.PolicyActive,
	}
	if err := repos.PersonalPolicies.Create(ctx, policy); err != nil {
		t.Fatal(err)
	}
	broker := superagent.NewContextBroker(repos, fakeMemorySearcher{})
	bundle, err := broker.Build(ctx, u.ID, task.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(bundle.Blocks, "\n")
	for _, want := range []string{"整理客户需求", "客户希望周五前确认报价", "待确认", "先给结论"} {
		if !strings.Contains(all, want) {
			t.Fatalf("context missing %q:\n%s", want, all)
		}
	}
	if len(bundle.Receipts) < 4 {
		t.Fatalf("receipts=%+v", bundle.Receipts)
	}
	for _, receipt := range bundle.Receipts {
		if receipt.Hash == "" || receipt.Permission != "owner" {
			t.Fatalf("bad receipt: %+v", receipt)
		}
	}
}

func TestContextBrokerRecordsTruncation(t *testing.T) {
	_, db, u := newTestService(t)
	ctx := context.Background()
	tasks := worktask.New(db)
	task, err := tasks.Create(ctx, worktask.CreateInput{
		OwnerID: u.ID, Title: "长任务", Goal: strings.Repeat("很长的目标", 200),
		Status: store.WorkTaskQueued, IdempotencyKey: "ctx-truncate",
	})
	if err != nil {
		t.Fatal(err)
	}
	broker := superagent.NewContextBroker(store.NewRepos(db), nil)
	broker.Budget.Task = 50
	bundle, err := broker.Build(ctx, u.ID, task.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, receipt := range bundle.Receipts {
		if receipt.SourceType == "work_task" && receipt.Truncated == "budget" {
			found = true
		}
	}
	if !found {
		t.Fatalf("truncation receipt missing: %+v", bundle.Receipts)
	}
}
