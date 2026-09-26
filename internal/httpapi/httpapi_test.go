package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"goldenfinger/agent/internal/agent"
	"goldenfinger/agent/internal/compliance"
	"goldenfinger/agent/internal/extsvc/stub"
	"goldenfinger/agent/internal/llm"
	"goldenfinger/agent/internal/llm/mock"
	"goldenfinger/agent/internal/memory"
	"goldenfinger/agent/internal/nlu"
	"goldenfinger/agent/internal/store"
	"goldenfinger/agent/internal/task"
)

var fixedNow = time.Date(2026, 3, 5, 10, 0, 0, 0, time.FixedZone("CST", 8*3600))

type fakeQueue struct{}

func (fakeQueue) EnqueueForTask(ctx context.Context, t *store.Task) error { return nil }
func (fakeQueue) CancelForTask(ctx context.Context, id string) error      { return nil }

// newTestServer 用脚本化 LLM 装配完整技术栈。
func newTestServer(t *testing.T, script ...llm.ChatResponse) (*Server, *store.Repos) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping httpapi integration tests")
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

	th := nlu.Thresholds{TaskAuto: 0.85, TaskClarify: 0.6, PersonClarify: 0.8, FactConfirmed: 0.8}
	mem := memory.NewService(repos.Persons, repos.Facts, repos.Episodes, repos.Audit,
		&mock.FixedEmbedder{Dim: 1024}, th, 720*time.Hour, func() time.Time { return fixedNow })
	tasksSvc := task.NewService(repos.Tasks, fakeQueue{}, repos.Audit, func() time.Time { return fixedNow })
	guard := compliance.NewGuard(repos.Consents, repos.Audit, repos.Users)

	rt := &agent.Runtime{
		LLM:      mock.New(script...),
		Model:    "test",
		Registry: agent.NewRegistry(agent.DefaultTools()...),
		Prompt: &agent.PromptBuilder{
			Memory: mem,
			UserFn: func(ctx context.Context, userID string) agent.UserContext {
				u, _ := repos.Users.Get(ctx, userID)
				if u == nil {
					return agent.UserContext{UserID: userID, TZ: "Asia/Shanghai"}
				}
				return agent.UserContext{UserID: u.ID, UserName: u.Name, UserType: u.UserType, TZ: u.TZ}
			},
		},
		Tools: &agent.ToolServices{
			Memory: mem, Tasks: tasksSvc, Weather: stub.Weather{}, Guard: guard,
			Repos: repos, Now: func() time.Time { return fixedNow }, Th: th,
		},
		Clock: agent.FixedClock{T: fixedNow},
	}

	srv := &Server{
		Repos:   repos,
		Runtime: rt,
		WebDir:  "../../web",
		Now:     func() time.Time { return fixedNow },
		Outbox:  &Outbox{},
	}
	return srv, repos
}

func postJSON(t *testing.T, h http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestChatEndpointCreatesSessionAndTask(t *testing.T) {
	srv, repos := newTestServer(t,
		mock.ToolResponse("c1", "create_task",
			`{"raw_text":"我后天要去订票","kind_hint":"intent","title":"订票","time_expr_raw":"后天","self_reported_confidence":0.9}`),
		mock.TextResponse("好的，记下了：订票。"),
	)
	h := srv.Handler()

	rec := postJSON(t, h, "/api/chat", `{"text":"我后天要去订票"}`)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var resp struct {
		SessionID string `json:"session_id"`
		Reply     string `json:"reply"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.SessionID == "" || resp.Reply == "" {
		t.Fatalf("bad response: %+v", resp)
	}

	// 任务以规范化时间持久化（后天 → 2026-03-07）。
	users, err := repos.Users.ListAll(context.Background())
	if err != nil || len(users) == 0 {
		t.Fatal("user missing")
	}
	tasks, err := repos.Tasks.ListByOwner(context.Background(), users[0].ID, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) == 0 {
		t.Fatal("expected a task")
	}
	if tasks[0].AbsTime == nil || tasks[0].AbsTime.Day() != 7 {
		t.Errorf("abs_time = %v", tasks[0].AbsTime)
	}

	// 会话记录已持久化（用户 + 助手）。
	msgs, err := repos.Sessions.RecentMessages(context.Background(), resp.SessionID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) < 2 {
		t.Fatalf("want persisted messages, got %d", len(msgs))
	}
}

func TestClarifyRoundTripOverHTTP(t *testing.T) {
	srv, _ := newTestServer(t,
		mock.ToolResponse("c1", "create_task",
			`{"raw_text":"我后天要去订票","kind_hint":"intent","title":"订票","time_expr_raw":"后天","self_reported_confidence":0.6}`),
	)
	h := srv.Handler()

	rec := postJSON(t, h, "/api/chat", `{"text":"我后天要去订票"}`)
	var resp struct {
		SessionID string             `json:"session_id"`
		Reply     string             `json:"reply"`
		Pending   *nlu.PendingAction `json:"pending"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Pending == nil {
		t.Fatalf("expected pending clarify, got %s", rec.Body)
	}

	// 第二轮：“对”确定性地解决（脚本已用尽——若解析器调用
	// LLM 就会失败）。
	rec2 := postJSON(t, h, "/api/chat", `{"session_id":"`+resp.SessionID+`","text":"对"}`)
	if rec2.Code != 200 {
		t.Fatalf("confirm failed: %s", rec2.Body)
	}
	var resp2 struct {
		Reply   string             `json:"reply"`
		Pending *nlu.PendingAction `json:"pending"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &resp2); err != nil {
		t.Fatal(err)
	}
	if resp2.Pending != nil {
		t.Error("pending must be cleared after confirm")
	}
	if !strings.Contains(resp2.Reply, "记下") {
		t.Errorf("confirm reply = %q", resp2.Reply)
	}
}

func TestTaskActionEndpoints(t *testing.T) {
	srv, repos := newTestServer(t)
	h := srv.Handler()
	ctx := context.Background()

	users, _ := repos.Users.ListAll(ctx)
	var userID string
	if len(users) == 0 {
		u := &store.User{UserType: store.UserGeneral, Name: "action-user", TZ: "Asia/Shanghai"}
		if err := repos.Users.Create(ctx, u); err != nil {
			t.Fatal(err)
		}
		userID = u.ID
	} else {
		userID = users[0].ID
	}
	tk := &store.Task{
		OwnerUserID: userID, Kind: store.KindIntent, Status: store.TaskPendingConfirm,
		Schema: []byte(`{"title":"测试任务"}`),
	}
	if err := repos.Tasks.Create(ctx, tk); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/tasks/"+tk.ID+"/confirm", nil)
	req.Header.Set("X-User-Id", userID)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("confirm: %d %s", rec.Code, rec.Body)
	}
	got, err := repos.Tasks.Get(ctx, userID, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.TaskScheduled {
		t.Errorf("status = %q", got.Status)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/tasks/"+tk.ID+"/done", nil)
	req.Header.Set("X-User-Id", userID)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("done: %d %s", rec.Code, rec.Body)
	}
	got, _ = repos.Tasks.Get(ctx, userID, tk.ID)
	if got.Status != store.TaskDone {
		t.Errorf("status = %q", got.Status)
	}
}

func TestMemoryEndpointsAndForget(t *testing.T) {
	srv, repos := newTestServer(t)
	h := srv.Handler()
	ctx := context.Background()

	u := &store.User{UserType: store.UserGeneral, Name: "mem-api-user", TZ: "Asia/Shanghai"}
	if err := repos.Users.Create(ctx, u); err != nil {
		t.Fatal(err)
	}
	p := &store.Person{OwnerUserID: u.ID, CanonicalName: "张阿姨"}
	if err := repos.Persons.Create(ctx, p); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/persons", nil)
	req.Header.Set("X-User-Id", u.ID)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "张阿姨") {
		t.Fatalf("list persons: %d %s", rec.Code, rec.Body)
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/persons/"+p.ID, nil)
	req.Header.Set("X-User-Id", u.ID)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("forget: %d %s", rec.Code, rec.Body)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/persons", nil)
	req.Header.Set("X-User-Id", u.ID)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), "张阿姨") {
		t.Error("forgotten person must not appear in the list")
	}
}

func TestMemoryExportAndIntents(t *testing.T) {
	srv, repos := newTestServer(t)
	h := srv.Handler()
	ctx := context.Background()

	u := &store.User{UserType: store.UserGeneral, Name: "export-user", TZ: "Asia/Shanghai"}
	if err := repos.Users.Create(ctx, u); err != nil {
		t.Fatal(err)
	}
	p := &store.Person{OwnerUserID: u.ID, CanonicalName: "李叔叔"}
	if err := repos.Persons.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := repos.Facts.Insert(ctx, &store.Fact{
		PersonID: p.ID, FactType: "hobby", ValueText: "爱下象棋",
		Confidence: 0.9, Status: store.FactConfirmed,
	}); err != nil {
		t.Fatal(err)
	}
	it := &store.StandingIntent{
		OwnerUserID: u.ID, Description: "问他棋局",
		TriggerGroups: [][]string{{"李叔叔", "来"}}, Status: store.IntentArmed,
		MaxFires: 3, CooldownSeconds: 86400,
	}
	if err := repos.Intents.Insert(ctx, it); err != nil {
		t.Fatal(err)
	}

	// Markdown 档案：人类可读的记忆导出。
	req := httptest.NewRequest(http.MethodGet, "/api/memory/export.md", nil)
	req.Header.Set("X-User-Id", u.ID)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("export: %d %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/markdown") {
		t.Errorf("content-type = %q", ct)
	}
	for _, want := range []string{"李叔叔", "爱下象棋", "问他棋局", "认识的人", "常备提醒"} {
		if !strings.Contains(body, want) {
			t.Errorf("export missing %q:\n%s", want, body)
		}
	}

	// 常备提醒列表 + 显式取消。
	req = httptest.NewRequest(http.MethodGet, "/api/intents", nil)
	req.Header.Set("X-User-Id", u.ID)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "问他棋局") {
		t.Fatalf("list intents: %d %s", rec.Code, rec.Body)
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/intents/"+it.ID, nil)
	req.Header.Set("X-User-Id", u.ID)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("cancel intent: %d %s", rec.Code, rec.Body)
	}
	got, err := repos.Intents.Get(ctx, u.ID, it.ID)
	if err != nil || got.Status != store.IntentCancelled {
		t.Fatalf("want cancelled, got %+v (%v)", got, err)
	}
}
