package superagent_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"goldenfinger/agent/internal/store"
	"goldenfinger/agent/internal/superagent"
)

func TestBriefGeneratorHonorsChannelSwitch(t *testing.T) {
	svc, db, u := newTestService(t)
	ctx := context.Background()
	for _, input := range []superagent.TriggerInput{
		{Channel: "activity", ExternalID: "episode:gate-1", ThreadExternalID: "activity:2026-11-01",
			Kind: "episode", Title: "活动事项", Body: "完成活动任务", ReceivedAt: testNow},
		{Channel: "email", ExternalID: "email:gate-1", ThreadExternalID: "email:gate",
			Kind: "email_message", Title: "邮件事项", Body: "邮件正文", ReceivedAt: testNow},
	} {
		if _, err := svc.Ingest(ctx, u.ID, input); err != nil {
			t.Fatal(err)
		}
	}
	generator := superagent.NewBriefGenerator(db, nil)
	generator.ChannelEnabled = func(_ context.Context, _ string, channel string) (bool, error) {
		return channel == "email", nil
	}
	brief, err := generator.Generate(ctx, u.ID, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(brief.ContentMD, "邮件事项") {
		t.Fatalf("email missing: %q", brief.ContentMD)
	}
	if strings.Contains(brief.ContentMD, "活动事项") {
		t.Fatalf("disabled channel leaked into brief: %q", brief.ContentMD)
	}
}

func TestBriefGeneratorCombinesChannelsAndVersions(t *testing.T) {
	svc, db, u := newTestService(t)
	ctx := context.Background()
	activityThread, activityTrigger := "", ""
	for _, input := range []superagent.TriggerInput{
		{Channel: "activity", ExternalID: "episode:brief-1", ThreadExternalID: "activity:2026-11-01",
			Kind: "episode", Title: "修复登录", Body: "完成了登录空指针修复", ReceivedAt: testNow},
		{Channel: "email", ExternalID: "email:brief-1", ThreadExternalID: "email:client:quote",
			Kind: "email_message", Title: "Q4 报价", Body: "客户要求周五前提交报价单", ReceivedAt: testNow},
		{Channel: "wechat", ExternalID: "wechat:brief-1", ThreadExternalID: "wechat:项目群",
			Kind: "message", Title: "项目群", Body: "决定把评审改到周三", ReceivedAt: testNow},
	} {
		result, err := svc.Ingest(ctx, u.ID, input)
		if err != nil {
			t.Fatal(err)
		}
		if input.Channel == "activity" {
			activityThread = result.Thread.ID
			activityTrigger = result.Trigger.ID
		}
	}
	repos := store.NewRepos(db)
	if changed, err := repos.Threads.UpdateSummary(ctx, u.ID, activityThread, 1,
		"自定义活动摘要：登录修复完成", testNow); err != nil || !changed {
		t.Fatalf("seed thread summary: changed=%v err=%v", changed, err)
	}
	if err := repos.ActionOutbox.Create(ctx, &store.ActionOutbox{
		OwnerUserID: u.ID, Channel: "email", Kind: "reply", Body: "待批准的回复草稿",
		RequiresApproval: true, IdempotencyKey: "brief-outbox-1",
	}); err != nil {
		t.Fatal(err)
	}
	job := &store.AgentJob{
		ID: "brief-worker-1", OwnerUserID: u.ID, AgentID: store.AgentIDSuperAgent,
		Task: "起草会议通知", Status: store.AgentJobCompleted, IdempotencyKey: "brief-worker-1",
		ThreadID: "thread-brief-worker", ResultSummary: "会议通知已起草",
		CreatedAt: testNow, UpdatedAt: testNow,
	}
	if err := repos.AgentJobs.Create(ctx, job); err != nil {
		t.Fatal(err)
	}

	generator := superagent.NewBriefGenerator(db, nil)
	brief, err := generator.Generate(ctx, u.ID, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if brief.Version != 1 || brief.ContentMD == "" {
		t.Fatalf("brief=%+v", brief)
	}
	var sections superagent.BriefSections
	if err := json.Unmarshal(brief.Sections, &sections); err != nil {
		t.Fatal(err)
	}
	if len(sections.NeedsAction) == 0 || len(sections.ImportantChanges) == 0 {
		t.Fatalf("sections=%+v", sections)
	}
	if !strings.Contains(brief.ContentMD, "需要你处理") ||
		!strings.Contains(brief.ContentMD, "自定义活动摘要") {
		t.Fatalf("content=%q", brief.ContentMD)
	}
	var refs []string
	if err := json.Unmarshal(brief.SourceRefs, &refs); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"trigger": false, "outbox": false, "worker": false}
	for _, ref := range refs {
		for prefix := range want {
			if strings.HasPrefix(ref, prefix+":") {
				want[prefix] = true
			}
		}
	}
	for prefix, found := range want {
		if !found {
			t.Fatalf("missing %s ref in %v", prefix, refs)
		}
	}
	refSet := make(map[string]bool, len(refs))
	for _, ref := range refs {
		refSet[ref] = true
	}
	if !refSet["thread:"+activityThread] {
		t.Fatalf("summarized thread missing from refs: %v", refs)
	}
	if refSet["trigger:"+activityTrigger] {
		t.Fatalf("raw trigger should be omitted when thread summary exists: %v", refs)
	}

	brief2, err := generator.Generate(ctx, u.ID, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if brief2.Version != 2 || brief2.ID == brief.ID {
		t.Fatalf("versioning broken: v1=%+v v2=%+v", brief, brief2)
	}
}
