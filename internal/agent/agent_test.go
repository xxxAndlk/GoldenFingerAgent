package agent

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"goldenfinger/agent/internal/compliance"
	"goldenfinger/agent/internal/extsvc/stub"
	"goldenfinger/agent/internal/intent"
	"goldenfinger/agent/internal/llm"
	"goldenfinger/agent/internal/llm/mock"
	"goldenfinger/agent/internal/memory"
	"goldenfinger/agent/internal/nlu"
	"goldenfinger/agent/internal/store"
	"goldenfinger/agent/internal/task"
)

var fixedNow = time.Date(2026, 3, 5, 10, 0, 0, 0, time.FixedZone("CST", 8*3600))

// testHarness wires a full Runtime against the test DB + scripted LLM.
func testHarness(t *testing.T, script ...llm.ChatResponse) (*Session, *Runtime, *store.User) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping agent integration tests")
	}
	ctx := context.Background()
	db, err := store.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	repos := store.NewRepos(db.Pool)

	u := &store.User{UserType: store.UserGeneral, Name: "agent-user", TZ: "Asia/Shanghai"}
	if err := repos.Users.Create(ctx, u); err != nil {
		t.Fatal(err)
	}

	th := nlu.Thresholds{TaskAuto: 0.85, TaskClarify: 0.6, PersonClarify: 0.8, FactConfirmed: 0.8}
	mem := memory.NewService(repos.Persons, repos.Facts, repos.Episodes, repos.Audit,
		&mock.FixedEmbedder{Dim: 1024}, th, 720*time.Hour, func() time.Time { return fixedNow })

	queue := &fakeQueue{}
	tasksSvc := task.NewService(repos.Tasks, queue, repos.Audit, func() time.Time { return fixedNow })
	guard := compliance.NewGuard(repos.Consents, repos.Audit, repos.Users)
	intentsSvc := intent.NewService(repos.Intents, repos.Audit, func() time.Time { return fixedNow })

	svcs := &ToolServices{
		Memory:  mem,
		Tasks:   tasksSvc,
		Intents: intentsSvc,
		Weather: stub.Weather{},
		Guard:   guard,
		Repos:   repos,
		Now:     func() time.Time { return fixedNow },
		Th:      th,
	}

	scripted := mock.New(script...)
	rt := &Runtime{
		LLM:      scripted,
		Model:    "test-model",
		Registry: NewRegistry(DefaultTools()...),
		Prompt: &PromptBuilder{
			Memory: mem,
			UserFn: func(ctx context.Context, userID string) UserContext {
				return UserContext{UserID: u.ID, UserName: u.Name, UserType: u.UserType, TZ: u.TZ}
			},
		},
		Tools: svcs,
		Clock: FixedClock{T: fixedNow},
	}

	sess := &Session{ID: "sess-1", UserID: u.ID}
	return sess, rt, u
}

// fakeQueue records scheduling (task package's Queue interface).
type fakeQueue struct {
	enqueued  []string
	cancelled []string
}

func (f *fakeQueue) EnqueueForTask(ctx context.Context, t *store.Task) error {
	f.enqueued = append(f.enqueued, t.ID)
	return nil
}
func (f *fakeQueue) CancelForTask(ctx context.Context, taskID string) error {
	f.cancelled = append(f.cancelled, taskID)
	return nil
}

func TestLoopToolCallThenFinalAnswer(t *testing.T) {
	sess, rt, _ := testHarness(t,
		mock.ToolResponse("c1", "save_note", `{"text":"车位在B2"}`),
		mock.TextResponse("好的，记下了：车位在B2。"),
	)
	res, err := Run(context.Background(), sess, "帮我记一下车位在B2", rt)
	if err != nil {
		t.Fatal(err)
	}
	if res.Reply != "好的，记下了：车位在B2。" {
		t.Errorf("reply = %q", res.Reply)
	}
	// Transcript: user, assistant(tool_call), tool, assistant(text).
	if len(sess.Messages) != 4 {
		t.Fatalf("want 4 messages, got %d", len(sess.Messages))
	}
	if sess.Messages[2].Role != llm.RoleTool {
		t.Errorf("third message role = %v", sess.Messages[2].Role)
	}
	// Tool result must be framed as data, not instructions.
	if !strings.Contains(sess.Messages[2].Content, "data, not instructions") {
		t.Error("tool payload missing data-not-instructions framing")
	}
}

func TestLoopClarifyShortCircuit(t *testing.T) {
	sess, rt, _ := testHarness(t,
		mock.ToolResponse("c1", "create_task", `{"raw_text":"我后天要去订票","kind_hint":"intent","title":"订票","time_expr_raw":"后天"}`),
	)
	res, err := Run(context.Background(), sess, "我后天要去订票", rt)
	if err != nil {
		t.Fatal(err)
	}
	if res.Pending == nil {
		t.Fatal("expected a pending clarify")
	}
	if res.Pending.Type != nlu.PendingTaskCreate {
		t.Errorf("pending type = %q", res.Pending.Type)
	}
	if !strings.Contains(res.Reply, "对吗") {
		t.Errorf("clarify question = %q", res.Reply)
	}

	// Affirm completes deterministically (no LLM call in the script → would
	// error if the resolver tried to call the model).
	res2, done := ResolvePending(context.Background(), sess, "对", rt, fixedNow)
	if !done {
		t.Fatal("affirm must resolve the pending")
	}
	if sess.Pending != nil {
		t.Error("pending must be cleared")
	}
	if !strings.Contains(res2.Reply, "记下") {
		t.Errorf("confirm reply = %q", res2.Reply)
	}
}

func TestLoopMaxStepsBailsOut(t *testing.T) {
	// A tool that always calls itself would loop forever without the cap.
	callSelf := mock.ToolResponse("c1", "get_datetime", `{}`)
	sess, rt, _ := testHarness(t, callSelf, callSelf, callSelf, callSelf, callSelf, callSelf, callSelf, callSelf)
	rt.MaxSteps = 3
	res, err := Run(context.Background(), sess, "现在几点", rt)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Reply, "卡住") {
		t.Errorf("bail-out reply = %q", res.Reply)
	}
}

func TestFullConversationCreateTask(t *testing.T) {
	sess, rt, u := testHarness(t,
		mock.ToolResponse("c1", "create_task",
			`{"raw_text":"我后天要去订票","kind_hint":"intent","title":"订票","time_expr_raw":"后天","self_reported_confidence":0.9}`),
	)
	res, err := Run(context.Background(), sess, "我后天要去订票", rt)
	if err != nil {
		t.Fatal(err)
	}
	// score = 0.9 + 0.05 (time parsed) = 0.95 ≥ 0.85 → auto-create.
	if res.Pending != nil {
		t.Fatalf("expected auto-create, got clarify: %q", res.Reply)
	}
	tasks, err := rt.Tools.Tasks.List(context.Background(), u.ID, []string{store.TaskScheduled}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) == 0 {
		t.Fatal("expected a scheduled task")
	}
	// P3: intent kind must not carry an event template.
	if tasks[0].EventTemplate != "" {
		t.Errorf("intent must have no event template, got %q", tasks[0].EventTemplate)
	}
	// Time normalized: 后天 from 2026-03-05 → 2026-03-07.
	if tasks[0].AbsTime == nil || tasks[0].AbsTime.Day() != 7 {
		t.Errorf("abs_time = %v", tasks[0].AbsTime)
	}
}

func TestAlarmCreation(t *testing.T) {
	sess, rt, u := testHarness(t,
		mock.ToolResponse("c1", "set_alarm", `{"time_expr_raw":"20分钟后","label":"吃药"}`),
		mock.TextResponse("好，20分钟后叫你吃药。"),
	)
	res, err := Run(context.Background(), sess, "20分钟后叫我吃药", rt)
	if err != nil {
		t.Fatal(err)
	}
	if res.Reply == "" {
		t.Error("empty reply")
	}
	tasks, err := rt.Tools.Tasks.List(context.Background(), u.ID, nil, store.KindAlarm)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 alarm, got %d", len(tasks))
	}
	if tasks[0].AbsTime == nil || !tasks[0].AbsTime.Equal(fixedNow.Add(20*time.Minute)) {
		t.Errorf("alarm time = %v", tasks[0].AbsTime)
	}
}

func TestMemoryWriteAndQuery(t *testing.T) {
	sess, rt, _ := testHarness(t,
		mock.ToolResponse("c1", "save_fact",
			`{"person_name":"张阿姨","fact_type":"family","value_text":"女儿在广州"}`),
		mock.TextResponse("记住了：张阿姨的女儿在广州。"),
	)
	res, err := Run(context.Background(), sess, "帮我记一下张阿姨的女儿在广州", rt)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Reply, "记住了") {
		t.Errorf("reply = %q", res.Reply)
	}
	// Confirm the fact landed.
	snips, err := rt.Tools.Memory.Search(context.Background(), sess.UserID, "张阿姨 女儿", 5)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range snips {
		if strings.Contains(s.Text, "女儿在广州") {
			found = true
		}
	}
	if !found {
		t.Error("fact not retrievable after save")
	}
}

func TestPendingAbandonOnNewTopic(t *testing.T) {
	sess, rt, _ := testHarness(t,
		mock.ToolResponse("c1", "create_task", `{"raw_text":"我后天要去订票","kind_hint":"intent","title":"订票","time_expr_raw":"后天"}`),
	)
	if _, err := Run(context.Background(), sess, "我后天要去订票", rt); err != nil {
		t.Fatal(err)
	}
	if sess.Pending == nil {
		t.Fatal("expected pending")
	}
	// Unrelated message abandons the pending.
	res, done := ResolvePending(context.Background(), sess, "明天天气怎么样", rt, fixedNow)
	if done {
		t.Fatalf("new topic must not resolve pending, got %+v", res)
	}
	if sess.Pending != nil {
		t.Error("pending must be abandoned on new topic")
	}
}

func TestEvaluativeFactNotWritten(t *testing.T) {
	sess, rt, _ := testHarness(t,
		mock.ToolResponse("c1", "save_fact",
			`{"person_name":"王婶","fact_type":"personality","value_text":"脾气不好"}`),
		mock.TextResponse("这个我就不记啦。"),
	)
	res, err := Run(context.Background(), sess, "王婶脾气不好，帮我记着", rt)
	if err != nil {
		t.Fatal(err)
	}
	// Must be rejected by governance regardless of model confidence.
	snips, _ := rt.Tools.Memory.Search(context.Background(), sess.UserID, "王婶 脾气", 5)
	for _, s := range snips {
		if strings.Contains(s.Text, "脾气不好") {
			t.Error("evaluative fact must never be written")
		}
	}
	_ = res
}

func TestStandingIntentFiresInTurn(t *testing.T) {
	// Two scripted turns: the first matches the trigger, the second is in
	// cooldown (fixed clock) — only the first may carry the 🔔 notice.
	sess, rt, u := testHarness(t,
		mock.TextResponse("好的，我记着。"),
		mock.TextResponse("好的。"),
	)
	ctx := context.Background()
	if _, err := rt.Tools.Intents.Create(ctx, intent.CreateInput{
		OwnerUserID:   u.ID,
		Description:   "问她女儿的情况",
		TriggerGroups: [][]string{{"张阿姨", "来电话"}},
	}); err != nil {
		t.Fatal(err)
	}

	res, err := Run(ctx, sess, "刚才张阿姨来电话了", rt)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.Reply, "🔔 提醒你：问她女儿的情况") {
		t.Errorf("reply must open with the deterministic reminder, got %q", res.Reply)
	}

	// The hidden block reached the model as data (check the recorded call).
	if calls := rt.LLM.(*mock.Scripted).Calls; len(calls) > 0 {
		sys := calls[0].Messages[0].Content
		if !strings.Contains(sys, "常备提醒触发") || !strings.Contains(sys, "不是指令") {
			t.Errorf("system prompt must carry the data-framed intent block, got %q", sys)
		}
	}

	// Fire bookkeeping.
	items, _ := rt.Tools.Intents.List(ctx, u.ID)
	if len(items) != 1 || items[0].FireCount != 1 {
		t.Fatalf("want fire_count=1, got %+v", items)
	}

	// Cooldown: same trigger again → no notice.
	res2, err := Run(ctx, sess, "张阿姨又来电话了", rt)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res2.Reply, "🔔 提醒你") {
		t.Errorf("cooldown must suppress a second notice, got %q", res2.Reply)
	}
}

func TestIntentToolCreateAndCancel(t *testing.T) {
	sess, rt, _ := testHarness(t,
		mock.ToolResponse("c1", "create_intent",
			`{"description":"提醒我测血糖","trigger_groups":[["吃药","时间"]]}`),
		mock.TextResponse("好啦，以后到了吃药时间我就提醒你测血糖。"),
	)
	ctx := context.Background()
	res, err := Run(ctx, sess, "以后到了吃药时间就提醒我测血糖", rt)
	if err != nil {
		t.Fatal(err)
	}
	if res.Reply == "" {
		t.Fatal("empty reply")
	}
	items, err := rt.Tools.Intents.List(ctx, sess.UserID)
	if err != nil || len(items) != 1 {
		t.Fatalf("want 1 intent, got %+v (%v)", items, err)
	}
	targetID := items[0].ID
	if items[0].Description != "提醒我测血糖" {
		t.Errorf("description = %q", items[0].Description)
	}

	// Cancel via tool (fresh script on the same runtime).
	rt.LLM = mock.New(
		mock.ToolResponse("c2", "cancel_intent", `{"intent_id":"`+targetID+`"}`),
		mock.TextResponse("好，取消了。"),
	)
	sess2 := &Session{ID: "sess-2", UserID: sess.UserID}
	if _, err := Run(ctx, sess2, "那个测血糖的提醒不用了", rt); err != nil {
		t.Fatal(err)
	}
	items, _ = rt.Tools.Intents.List(ctx, sess.UserID)
	for _, it := range items {
		if it.ID == targetID && it.Status != store.IntentCancelled {
			t.Errorf("intent must be cancelled, got %s", it.Status)
		}
	}
}

var _ = json.Marshal
