package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"goldenfinger/agent/internal/agent"
	"goldenfinger/agent/internal/agenthub"
	"goldenfinger/agent/internal/compliance"
	"goldenfinger/agent/internal/extsvc/stub"
	"goldenfinger/agent/internal/inbox"
	"goldenfinger/agent/internal/llm"
	"goldenfinger/agent/internal/llm/mock"
	"goldenfinger/agent/internal/memory"
	"goldenfinger/agent/internal/nlu"
	"goldenfinger/agent/internal/store"
	"goldenfinger/agent/internal/superagent"
	"goldenfinger/agent/internal/task"
)

var fixedNow = time.Date(2026, 3, 5, 10, 0, 0, 0, time.FixedZone("CST", 8*3600))

type fakeQueue struct{}

func (fakeQueue) EnqueueForTask(ctx context.Context, t *store.Task) error { return nil }
func (fakeQueue) CancelForTask(ctx context.Context, id string) error      { return nil }

// newTestServer 用脚本化 LLM 装配完整技术栈。
func newTestServer(t *testing.T, script ...llm.ChatResponse) (*Server, *store.Repos) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Connect(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	repos := store.NewRepos(db)

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
		Repos:           repos,
		Runtime:         rt,
		WebDir:          "../../web",
		Now:             func() time.Time { return fixedNow },
		Outbox:          &Outbox{},
		Inbox:           inbox.NewService(repos, nil, func() time.Time { return fixedNow }, 1024),
		SuperAgent:      superagent.New(db),
		AgentDispatcher: agenthub.NewDispatcher(db, agenthub.New(t.TempDir(), "")),
		Briefs:          superagent.NewBriefGenerator(db, nil),
		Memory:          mem,
	}
	// 共享记忆槽位：联系人备注与共享条目都要经它写入长期记忆。
	mem.SetSharedSlots(repos.SharedSlots)
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

func TestSummarizeEndpointUsesNoToolsAndSeparateContext(t *testing.T) {
	srv, _ := newTestServer(t, mock.TextResponse("今天主要修改了 settings.json，并检查了工作轨迹。"))
	h := srv.Handler()
	rec := postJSON(t, h, "/api/summarize", `{"context":"09:00 修改 settings.json；10:00 检查工作轨迹","focus":"今天做了什么"}`)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var resp summarizeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Reply, "settings.json") {
		t.Fatalf("summary reply = %q", resp.Reply)
	}
	scripted := srv.Runtime.LLM.(*mock.Scripted)
	if len(scripted.Calls) != 1 {
		t.Fatalf("llm calls = %d", len(scripted.Calls))
	}
	call := scripted.Calls[0]
	if len(call.Tools) != 0 {
		t.Fatalf("summary sub-agent must not expose tools: %+v", call.Tools)
	}
	if call.Messages[0].Role != llm.RoleSystem || !strings.Contains(call.Messages[1].Content, "修改 settings.json") {
		t.Fatalf("unexpected summary prompt: %+v", call.Messages)
	}
}

func TestChatContextIsInjectedButNotPersisted(t *testing.T) {
	srv, _ := newTestServer(t, mock.TextResponse("好的"))
	h := srv.Handler()
	rec := postJSON(t, h, "/api/chat", `{"text":"今天做了什么","context":"今天的工作阶段总结：09:00 修改了设置"}`)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	scripted := srv.Runtime.LLM.(*mock.Scripted)
	if len(scripted.Calls) != 1 {
		t.Fatalf("llm calls = %d", len(scripted.Calls))
	}
	last := scripted.Calls[0].Messages[len(scripted.Calls[0].Messages)-1]
	if !strings.Contains(last.Content, "修改了设置") || !strings.Contains(last.Content, "只读数据") {
		t.Fatalf("work context missing from llm request: %q", last.Content)
	}
	var resp struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	messages, err := srv.Repos.Sessions.RecentMessages(context.Background(), resp.SessionID, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		if strings.Contains(message.Content, "修改了设置") {
			t.Fatalf("work context leaked into persisted messages: %q", message.Content)
		}
	}
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

func TestInternalChatDoesNotCreateHistory(t *testing.T) {
	srv, _ := newTestServer(t, mock.TextResponse("内部总结"))
	h := srv.Handler()

	rec := postJSON(t, h, "/api/chat", `{"text":"内部协作提示词","internal":true}`)
	if rec.Code != 200 {
		t.Fatalf("internal chat status %d: %s", rec.Code, rec.Body)
	}
	var resp struct {
		SessionID string `json:"session_id"`
		Reply     string `json:"reply"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.SessionID != "" || resp.Reply != "内部总结" {
		t.Errorf("internal chat response = %+v", resp)
	}
	list := httptest.NewRecorder()
	h.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/sessions", nil))
	var sessions struct {
		Sessions []sessionDTO `json:"sessions"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &sessions); err != nil {
		t.Fatal(err)
	}
	if len(sessions.Sessions) != 0 {
		t.Errorf("internal chat must not create a history session, got %+v", sessions.Sessions)
	}
}

func TestInternalChatWithSessionDoesNotPersistSummary(t *testing.T) {
	srv, _ := newTestServer(t,
		mock.TextResponse("普通回复"),
		mock.TextResponse("本轮协作总结"),
	)
	h := srv.Handler()
	first := postJSON(t, h, "/api/chat", `{"text":"普通问题"}`)
	var created struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &created); err != nil || created.SessionID == "" {
		t.Fatalf("create session failed: %s", first.Body)
	}

	internal := postJSON(t, h, "/api/chat", `{"session_id":"`+created.SessionID+`","text":"以下是用户授权协作模式采集到的页面标题","internal":true}`)
	if internal.Code != 200 {
		t.Fatalf("internal chat status %d: %s", internal.Code, internal.Body)
	}
	var internalResp struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(internal.Body.Bytes(), &internalResp); err != nil {
		t.Fatal(err)
	}
	if internalResp.SessionID != "" {
		t.Errorf("internal summary must not claim a persisted session, got %q", internalResp.SessionID)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sessions/"+created.SessionID+"/messages", nil))
	var msgs []messageDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &msgs); err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("want original user+assistant only, got %+v", msgs)
	}
	for _, msg := range msgs {
		if strings.Contains(msg.Content, "以下是用户授权协作模式") || msg.Content == "本轮协作总结" {
			t.Errorf("internal summary must not be persisted: %+v", msgs)
		}
	}
}

func TestUploadEndpoint(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.UploadDir = t.TempDir()
	h := srv.Handler()

	data := base64.StdEncoding.EncodeToString([]byte("fake-image-bytes"))
	rec := postJSON(t, h, "/api/uploads", `{"name":"photo.png","mime":"image/png","kind":"image","data":"`+data+`"}`)
	if rec.Code != 200 {
		t.Fatalf("upload status %d: %s", rec.Code, rec.Body)
	}
	var uploaded uploadResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &uploaded); err != nil {
		t.Fatal(err)
	}
	if uploaded.Kind != "image" || uploaded.URL == "" || uploaded.Name != "photo.png" {
		t.Errorf("bad upload response: %+v", uploaded)
	}
	getReq := httptest.NewRequest(http.MethodGet, uploaded.URL, nil)
	getRec := httptest.NewRecorder()
	h.ServeHTTP(getRec, getReq)
	if getRec.Code != 200 || getRec.Body.String() != "fake-image-bytes" {
		t.Errorf("uploaded file not served: %d %q", getRec.Code, getRec.Body.String())
	}

	bad := postJSON(t, h, "/api/uploads", `{"name":"x.exe","mime":"application/x-msdownload","kind":"file","data":"`+data+`"}`)
	if bad.Code != 400 {
		t.Errorf("non image/video upload should be rejected, got %d", bad.Code)
	}
}

func TestChatStreamEndpoint(t *testing.T) {
	srv, _ := newTestServer(t, mock.TextResponse("好的，收到了。"))
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodPost, "/api/chat/stream", strings.NewReader(`{"text":"你好"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("stream status %d: %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "event: done") || !strings.Contains(body, "好的，收到了。") {
		t.Fatalf("stream should emit done with reply, got %q", body)
	}
}

func TestSessionListEndpoint(t *testing.T) {
	srv, repos := newTestServer(t, mock.TextResponse("好的，记下了。"))
	h := srv.Handler()

	rec := postJSON(t, h, "/api/chat", `{"text":"帮我记一下张阿姨的电话是13800000000"}`)
	if rec.Code != 200 {
		t.Fatalf("chat status %d: %s", rec.Code, rec.Body)
	}
	var created struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	// 协作总结是自动消息，不应出现在对话历史里。
	users, err := repos.Users.ListAll(context.Background())
	if err != nil || len(users) == 0 {
		t.Fatal("user missing")
	}
	summarySession := &store.ChatSession{OwnerUserID: users[0].ID}
	if err := repos.Sessions.Create(context.Background(), summarySession); err != nil {
		t.Fatal(err)
	}
	for _, message := range []store.ChatMessage{
		{SessionID: summarySession.ID, Role: "user", Content: "以下是用户授权协作模式采集到的页面标题、去重后的页面正文和有效操作。"},
		{SessionID: summarySession.ID, Role: "assistant", Content: "本阶段没有实质变化。"},
	} {
		msg := message
		if err := repos.Sessions.AppendMessage(context.Background(), &msg); err != nil {
			t.Fatal(err)
		}
	}

	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	rec = get("/api/sessions")
	if rec.Code != 200 {
		t.Fatalf("sessions status %d: %s", rec.Code, rec.Body)
	}
	var list struct {
		Sessions []sessionDTO `json:"sessions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Sessions) != 1 {
		t.Fatalf("want 1 session, got %d: %s", len(list.Sessions), rec.Body)
	}
	item := list.Sessions[0]
	if item.ID != created.SessionID || item.Messages < 2 || !strings.Contains(item.Preview, "张阿姨") {
		t.Errorf("bad session summary: %+v", item)
	}
	if item.CreatedAt.IsZero() {
		t.Error("created_at must be present for date grouping")
	}

	today := time.Now().Format("2006-01-02")
	if rec = get("/api/sessions?date=" + today); rec.Code != 200 {
		t.Fatalf("date filter status %d: %s", rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Sessions) != 1 {
		t.Fatalf("today filter should find the session: %s", rec.Body)
	}
	if rec = get("/api/sessions?date=2000-01-01"); rec.Code != 200 {
		t.Fatalf("old date status %d: %s", rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Sessions) != 0 {
		t.Fatalf("old date must be empty: %s", rec.Body)
	}
	if rec = get("/api/sessions?date=2026-99-99"); rec.Code != 400 {
		t.Errorf("invalid date should be 400, got %d", rec.Code)
	}

	rec = get("/api/sessions/" + created.SessionID + "/messages")
	if rec.Code != 200 {
		t.Fatalf("messages status %d: %s", rec.Code, rec.Body)
	}
	var msgs []messageDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &msgs); err != nil {
		t.Fatal(err)
	}
	if len(msgs) < 2 || msgs[0].Role != "user" || msgs[1].Role != "assistant" {
		t.Errorf("want user+assistant messages, got %+v", msgs)
	}

	rec = get("/api/sessions/" + created.SessionID + "/messages?limit=1")
	var page []messageDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || len(page) != 1 || page[0].ID == "" {
		t.Fatalf("latest page = %+v err=%v", page, err)
	}
	rec = get("/api/sessions/" + created.SessionID + "/messages?limit=1&before=" + page[0].ID)
	var older []messageDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &older); err != nil || len(older) != 1 || older[0].ID == page[0].ID {
		t.Fatalf("older page = %+v err=%v", older, err)
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
