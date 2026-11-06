package superagent_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"goldenfinger/agent/internal/llm"
	"goldenfinger/agent/internal/llm/mock"
	"goldenfinger/agent/internal/memory"
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

// 简报正文必须进共享长期记忆：手机与电脑只共享这一份记忆，
// 简报要能在另一端被检索到，并且带着日期、版本与来源引用。
func TestBriefGeneratorWritesSharedMemory(t *testing.T) {
	svc, db, u := newTestService(t)
	ctx := context.Background()
	if _, err := svc.Ingest(ctx, u.ID, superagent.TriggerInput{
		Channel: "email", ExternalID: "email:shared-1", ThreadExternalID: "email:shared",
		Kind: "email_message", Title: "Q4 报价", Body: "客户要求周五前提交报价单", ReceivedAt: testNow,
	}); err != nil {
		t.Fatal(err)
	}
	writer := &sharedMemoryWriterStub{}
	generator := superagent.NewBriefGenerator(db, nil)
	generator.SharedMemory = writer
	brief, err := generator.Generate(ctx, u.ID, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(writer.saved) != 1 {
		t.Fatalf("saved=%d", len(writer.saved))
	}
	entry := writer.saved[0]
	if entry.OwnerUserID != u.ID {
		t.Fatalf("owner=%q", entry.OwnerUserID)
	}
	if entry.Slot != memory.BriefSlot(brief.BriefDate) {
		t.Fatalf("slot=%q", entry.Slot)
	}
	if !strings.Contains(entry.Text, brief.BriefDate) {
		t.Fatalf("text must carry the brief date: %q", entry.Text)
	}
	if !strings.Contains(entry.Text, "第 1 版") {
		t.Fatalf("text must carry the brief version: %q", entry.Text)
	}
	if !strings.Contains(entry.Text, "来源引用") {
		t.Fatalf("text must carry source refs: %q", entry.Text)
	}
	if !strings.Contains(entry.Text, "Q4 报价") {
		t.Fatalf("text must carry the brief body: %q", entry.Text)
	}
}

// sharedMemoryWriterStub 记录写入共享长期记忆的内容。
type sharedMemoryWriterStub struct {
	saved []memory.SharedInput
	err   error
}

func (s *sharedMemoryWriterStub) SaveShared(_ context.Context, in memory.SharedInput) error {
	if s.err != nil {
		return s.err
	}
	s.saved = append(s.saved, in)
	return nil
}

func TestBriefGeneratorReportsSharedMemoryFailure(t *testing.T) {
	_, db, u := newTestService(t)
	failure := errors.New("outbox unavailable")
	generator := superagent.NewBriefGenerator(db, nil)
	generator.SharedMemory = &sharedMemoryWriterStub{err: failure}
	ready := false
	generator.OnReady = func(context.Context, string, *store.DailyBrief) { ready = true }
	brief, err := generator.Generate(context.Background(), u.ID, testNow)
	var syncErr *superagent.BriefSharedSyncError
	if brief == nil || !errors.As(err, &syncErr) || !errors.Is(err, failure) {
		t.Fatalf("brief=%+v err=%v", brief, err)
	}
	if !ready {
		t.Fatal("共享同步失败不应阻止已生成简报的就绪通知")
	}
	if _, getErr := store.NewRepos(db).DailyBriefs.Get(context.Background(), u.ID, brief.ID); getErr != nil {
		t.Fatalf("简报应已保存：%v", getErr)
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

func TestBriefGeneratorAcceptsEmptyModelBrief(t *testing.T) {
	svc, db, u := newTestService(t)
	ctx := context.Background()
	if _, err := svc.Ingest(ctx, u.ID, superagent.TriggerInput{
		Channel: "email", ExternalID: "email:empty-brief", ThreadExternalID: "email:weekly-report",
		Kind: "email_message", Title: "普通周报", Body: "周报已读，没有需要机主处理的事项", ReceivedAt: testNow,
	}); err != nil {
		t.Fatal(err)
	}
	client := mock.New(llm.ChatResponse{Message: llm.Message{
		Role: llm.RoleAssistant, Content: `{"needs_action":[],"important_changes":[],"background":[]}`,
	}})
	generator := superagent.NewBriefGenerator(db, func() (llm.Client, string) { return client, "test-model" })
	brief, err := generator.Generate(ctx, u.ID, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(brief.ContentMD, "今日没有需要关注的重要变化") || strings.Contains(brief.ContentMD, "普通周报") {
		t.Fatalf("empty model result must not fall back to raw sources: %q", brief.ContentMD)
	}
	if len(client.Calls) != 1 {
		t.Fatalf("llm calls=%d", len(client.Calls))
	}
	prompt := client.Calls[0].Messages[0].Content
	for _, want := range []string{"不是制作活动日志或拼接来源摘要", "background 默认留空", "应用或窗口切换", "没有值得呈现的内容就返回空数组"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("brief prompt missing %q: %s", want, prompt)
		}
	}
}

func TestBriefGeneratorFallbackDropsRoutineActivity(t *testing.T) {
	svc, db, u := newTestService(t)
	ctx := context.Background()
	ingested, err := svc.Ingest(ctx, u.ID, superagent.TriggerInput{
		Channel: "activity", ExternalID: "episode:routine-brief", ThreadExternalID: "activity:2026-11-01",
		Kind: "episode", Title: "桌面活动", Body: "打开 config.yaml", ReceivedAt: testNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	summary := `既有摘要：
- 最新问题：csrss 约占 54.4 MB，持续约 1 分钟，未显示异常阈值或明确故障。
- 进行中：工作区停留在 GoldenFingerAgent 项目，仅能确认浏览/编辑界面状态。
决定：未发现明确决策。
已完成：完成一次 csrss 内存占用观测并记录界面活动。
关键结论：无证据表明存在具体问题或任务推进结果。
新增事件：
- 最新问题/关键结论：事项本身无实质变化，工作窗口由 Chrome 切换至 Visual Studio Code。
- 决定与截止时间：无新增决定，无明确截止时间。
- 已完成/进行中/受阻：进行中为查看 config.yaml；无明确受阻。
Remote Desktop：使用 RustDesk 远程桌面约 1 分钟，页面内容乱码。`
	if changed, err := store.NewRepos(db).Threads.UpdateSummary(ctx, u.ID, ingested.Thread.ID, 1, summary, testNow); err != nil || !changed {
		t.Fatalf("seed thread summary: changed=%v err=%v", changed, err)
	}
	generator := superagent.NewBriefGenerator(db, nil)
	brief, err := generator.Generate(ctx, u.ID, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(brief.ContentMD, "今日没有需要关注的重要变化") {
		t.Fatalf("routine activity should produce an empty brief: %q", brief.ContentMD)
	}
	for _, unwanted := range []string{"csrss", "config.yaml", "RustDesk", "## 重要变化"} {
		if strings.Contains(brief.ContentMD, unwanted) {
			t.Fatalf("routine activity leaked into brief (%s): %q", unwanted, brief.ContentMD)
		}
	}
}
