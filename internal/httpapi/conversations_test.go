package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"goldenfinger/agent/internal/store"
	"goldenfinger/agent/internal/superagent"
	"goldenfinger/agent/internal/worktask"
)

// ownerUser 建一个 owner 用户并返回。默认用户要等 resolveUser 才会创建，
// 测试里先自己建好，避免依赖请求副作用。
func ownerUser(t *testing.T, repos *store.Repos, name string) *store.User {
	t.Helper()
	u := &store.User{UserType: store.UserGeneral, Name: name, TZ: "Asia/Shanghai"}
	if err := repos.Users.Create(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	return u
}

// secondUser 建一个与默认用户不同的 owner，用于验证会话隔离。
func secondUser(t *testing.T, repos *store.Repos) *store.User {
	t.Helper()
	return ownerUser(t, repos, "另一个用户")
}

// sessionFor 建一个属于指定 owner 的会话并写入若干消息。
func sessionFor(t *testing.T, repos *store.Repos, ownerID, kind string, bodies ...string) *store.ChatSession {
	t.Helper()
	ctx := context.Background()
	session := &store.ChatSession{OwnerUserID: ownerID, Kind: kind}
	if err := repos.Sessions.Create(ctx, session); err != nil {
		t.Fatal(err)
	}
	for i, body := range bodies {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		if err := repos.Sessions.AppendMessage(ctx, &store.ChatMessage{
			SessionID: session.ID, Role: role, Content: body,
		}); err != nil {
			t.Fatal(err)
		}
	}
	return session
}

func TestSessionMessagesRejectCrossOwner(t *testing.T) {
	srv, repos := newTestServer(t)
	h := srv.Handler()

	ownerA := ownerUser(t, repos, "主人")
	ownerAID := ownerA.ID
	ownerB := secondUser(t, repos)

	sessionA := sessionFor(t, repos, ownerAID, "main", "A 的私密内容", "A 的回复")
	sessionB := sessionFor(t, repos, ownerB.ID, "main", "B 的私密内容", "B 的回复")

	// owner B 不能读到 owner A 的会话消息。
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sessionA.ID+"/messages", nil)
	req.Header.Set("X-User-Id", ownerB.ID)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-owner read status=%d body=%s, want 404", rec.Code, rec.Body)
	}
	if body := rec.Body.String(); containsSecret(body, "A 的私密内容") {
		t.Fatalf("cross-owner response leaked content: %s", body)
	}

	// owner A 自己能读到。
	req = httptest.NewRequest(http.MethodGet, "/api/sessions/"+sessionA.ID+"/messages", nil)
	req.Header.Set("X-User-Id", ownerAID)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("owner read status=%d body=%s", rec.Code, rec.Body)
	}
	if !containsSecret(rec.Body.String(), "A 的私密内容") {
		t.Fatalf("owner read lost content: %s", rec.Body)
	}

	// 不存在的会话同样 404。
	rec = getJSON(t, h, "/api/sessions/session-does-not-exist/messages")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing session status=%d want 404", rec.Code)
	}

	// owner B 能读自己的会话。
	req = httptest.NewRequest(http.MethodGet, "/api/sessions/"+sessionB.ID+"/messages", nil)
	req.Header.Set("X-User-Id", ownerB.ID)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("owner B read status=%d body=%s", rec.Code, rec.Body)
	}
}

// 新会话投影的消息接口必须和旧接口一样按 owner 隔离：
// Worker 会话和委派执行记录都不能被别的用户读到。
func TestConversationMessagesRejectCrossOwner(t *testing.T) {
	srv, repos := newTestServer(t)
	h := srv.Handler()
	ctx := context.Background()

	ownerA := ownerUser(t, repos, "主人")
	ownerB := secondUser(t, repos)
	worker := sessionFor(t, repos, ownerA.ID, "worker", "Worker 私密步骤", "Worker 私密结果")
	delegated := &store.AgentJob{
		ID: "delegated-job", OwnerUserID: ownerA.ID, AgentID: "bma",
		Task: "委派私密任务", Status: store.AgentJobCompleted,
		IdempotencyKey: "delegated-job-key", SessionID: worker.ID,
		CreatedAt: fixedNow, UpdatedAt: fixedNow,
	}
	if err := repos.AgentJobs.Create(ctx, delegated); err != nil {
		t.Fatal(err)
	}

	read := func(owner, id string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/conversations/"+id+"/messages", nil)
		req.Header.Set("X-User-Id", owner)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	if rec := read(ownerB.ID, worker.ID); rec.Code != http.StatusNotFound ||
		containsSecret(rec.Body.String(), "Worker 私密") {
		t.Fatalf("cross-owner worker read=%d body=%s", rec.Code, rec.Body)
	}
	if rec := read(ownerB.ID, delegated.ID); rec.Code != http.StatusNotFound ||
		containsSecret(rec.Body.String(), "委派私密") {
		t.Fatalf("cross-owner delegated read=%d body=%s", rec.Code, rec.Body)
	}
	if rec := read(ownerA.ID, worker.ID); rec.Code != http.StatusOK ||
		!containsSecret(rec.Body.String(), "Worker 私密步骤") {
		t.Fatalf("owner worker read=%d body=%s", rec.Code, rec.Body)
	}

	// 委派列表本身也按 owner 隔离。
	req := httptest.NewRequest(http.MethodGet, "/api/conversations?kind=delegated", nil)
	req.Header.Set("X-User-Id", ownerB.ID)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delegated list status=%d body=%s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "委派私密任务") {
		t.Fatalf("cross-owner delegated list leaked: %s", rec.Body)
	}
}

func TestSessionMessagesPaginationCursor(t *testing.T) {
	srv, repos := newTestServer(t)
	h := srv.Handler()
	ownerID := ownerUser(t, repos, "主人").ID
	session := sessionFor(t, repos, ownerID, "main", "第一条", "第二条", "第三条", "第四条")

	rec := getJSON(t, h, "/api/sessions/"+session.ID+"/messages?limit=2")
	if rec.Code != http.StatusOK {
		t.Fatalf("page1 status=%d body=%s", rec.Code, rec.Body)
	}
	var page1 []messageDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &page1); err != nil {
		t.Fatal(err)
	}
	if len(page1) != 2 {
		t.Fatalf("page1 len=%d body=%s", len(page1), rec.Body)
	}
	// 第一页取最新两条。
	if page1[0].Content != "第三条" || page1[1].Content != "第四条" {
		t.Fatalf("page1 order=%+v", page1)
	}
	before := page1[0].ID
	rec = getJSON(t, h, "/api/sessions/"+session.ID+"/messages?limit=2&before="+before)
	if rec.Code != http.StatusOK {
		t.Fatalf("page2 status=%d body=%s", rec.Code, rec.Body)
	}
	var page2 []messageDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &page2); err != nil {
		t.Fatal(err)
	}
	if len(page2) != 2 || page2[0].Content != "第一条" || page2[1].Content != "第二条" {
		t.Fatalf("page2=%+v body=%s", page2, rec.Body)
	}
	// 无效游标返回空页，客户端据此停止加载。
	rec = getJSON(t, h, "/api/sessions/"+session.ID+"/messages?before=not-a-real-id")
	if rec.Code != http.StatusOK {
		t.Fatalf("bad cursor status=%d", rec.Code)
	}
	var empty []messageDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &empty); err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Fatalf("bad cursor must return empty page, got=%d", len(empty))
	}
}

func TestLegacySessionsEndpointHidesWorkerSessions(t *testing.T) {
	srv, repos := newTestServer(t)
	h := srv.Handler()
	ownerID := ownerUser(t, repos, "主人").ID

	sessionFor(t, repos, ownerID, "main", "主对话", "回复")
	sessionFor(t, repos, ownerID, "worker", "Worker 内容", "Worker 回复")
	sessionFor(t, repos, ownerID, "router", "内部路由", "内部回复")

	rec := getJSON(t, h, "/api/sessions")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	body := decodeMap(t, rec)
	sessions := body["sessions"].([]any)
	for _, item := range sessions {
		entry := item.(map[string]any)
		preview := entry["preview"].(string)
		if containsSecret(preview, "Worker 内容") || containsSecret(preview, "内部路由") {
			t.Fatalf("legacy endpoint leaked hidden session: %s", rec.Body)
		}
	}
}

func TestConversationsProjectionCoversAllKinds(t *testing.T) {
	srv, repos := newTestServer(t)
	h := srv.Handler()
	ctx := context.Background()
	ownerID := ownerUser(t, repos, "主人").ID

	main := sessionFor(t, repos, ownerID, "main", "今天天气如何", "晴天")
	worker := sessionFor(t, repos, ownerID, "worker", "Worker 开始干活", "Worker 干完了")
	sessionFor(t, repos, ownerID, "router", "内部路由", "内部回复")

	// 一个没有消息的 Worker 会话也必须出现。
	emptyWorker := &store.ChatSession{OwnerUserID: ownerID, Kind: "worker"}
	if err := repos.Sessions.Create(ctx, emptyWorker); err != nil {
		t.Fatal(err)
	}
	if err := repos.WorkTasks.Create(ctx, &store.WorkTask{
		OwnerUserID: ownerID, Title: "空任务", Goal: "等指令", Status: store.WorkTaskQueued,
		IdempotencyKey: "conv-test-empty", Profile: store.WorkTaskProfileRead,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Sessions.SetWorkTaskID(ctx, emptyWorker.ID, ""); err != nil {
		t.Fatal(err)
	}

	rec := getJSON(t, h, "/api/conversations")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	items := decodeMap(t, rec)["conversations"].([]any)
	byID := map[string]map[string]any{}
	for _, item := range items {
		entry := item.(map[string]any)
		byID[entry["id"].(string)] = entry
	}
	if _, ok := byID[main.ID]; !ok {
		t.Fatalf("main conversation missing: %s", rec.Body)
	}
	entry, ok := byID[worker.ID]
	if !ok {
		t.Fatalf("worker conversation missing: %s", rec.Body)
	}
	if entry["kind"] != store.ConversationWorker {
		t.Fatalf("worker kind=%v", entry["kind"])
	}
	if entry["can_message"] != true {
		t.Fatalf("worker must be messageable: %v", entry)
	}
	if entry["message_count"].(float64) != 2 {
		t.Fatalf("worker message_count=%v", entry["message_count"])
	}
	if _, ok := byID[emptyWorker.ID]; !ok {
		t.Fatalf("empty worker session must still appear: %s", rec.Body)
	}
	for id := range byID {
		if containsSecret(id, "router") {
			t.Fatalf("router session leaked: %s", rec.Body)
		}
	}

	// kind 过滤只返回对应类别。
	rec = getJSON(t, h, "/api/conversations?kind=worker")
	items = decodeMap(t, rec)["conversations"].([]any)
	if len(items) != 2 {
		t.Fatalf("kind=worker items=%d body=%s", len(items), rec.Body)
	}
	for _, item := range items {
		if item.(map[string]any)["kind"] != store.ConversationWorker {
			t.Fatalf("kind filter leaked: %v", item)
		}
	}

	// 非法 kind 必须 400。
	rec = getJSON(t, h, "/api/conversations?kind=bogus")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad kind status=%d", rec.Code)
	}
}

func TestConversationsDelegatedWithoutSessionIsNotMessageable(t *testing.T) {
	srv, repos := newTestServer(t)
	h := srv.Handler()
	ctx := context.Background()
	ownerID := ownerUser(t, repos, "主人").ID

	job := &store.AgentJob{
		OwnerUserID: ownerID, AgentID: "cli", Task: "跑一次构建", WorkDir: "/tmp",
		Status: store.AgentJobQueued, IdempotencyKey: "conv-delegated-1",
		JobRole: store.AgentJobRoleDelegated,
	}
	if err := repos.AgentJobs.Create(ctx, job); err != nil {
		t.Fatal(err)
	}
	if _, err := repos.AgentJobs.MarkRunning(ctx, job.ID, fixedNow); err != nil {
		t.Fatal(err)
	}
	if _, err := repos.AgentJobs.Finish(ctx, job.ID, store.AgentJobCompleted, "构建成功", "", "", fixedNow); err != nil {
		t.Fatal(err)
	}

	rec := getJSON(t, h, "/api/conversations?kind=delegated")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	items := decodeMap(t, rec)["conversations"].([]any)
	if len(items) != 1 {
		t.Fatalf("delegated items=%d body=%s", len(items), rec.Body)
	}
	entry := items[0].(map[string]any)
	if entry["kind"] != store.ConversationDelegated {
		t.Fatalf("kind=%v", entry["kind"])
	}
	if entry["can_message"] != false {
		t.Fatalf("delegated without session must not be messageable: %v", entry)
	}
	if entry["agent_id"] != "cli" {
		t.Fatalf("agent_id=%v", entry["agent_id"])
	}
	if !containsSecret(entry["preview"].(string), "构建成功") {
		t.Fatalf("preview=%v", entry["preview"])
	}
}

func TestConversationReadCursorIsPerDevice(t *testing.T) {
	srv, repos := newTestServer(t)
	h := srv.Handler()
	ctx := context.Background()
	ownerID := ownerUser(t, repos, "主人").ID
	session := sessionFor(t, repos, ownerID, "main", "第一条", "第二条")

	// 手机读完之后再产生一条新消息。
	if err := repos.Sessions.AppendMessage(ctx, &store.ChatMessage{
		SessionID: session.ID, Role: "assistant", Content: "第三条",
	}); err != nil {
		t.Fatal(err)
	}

	readAs := func(device string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/conversations/"+session.ID+"/read", nil)
		req.Header.Set("X-User-Id", ownerID)
		req.Header.Set("X-Device-Id", device)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	listAs := func(device string) map[string]any {
		req := httptest.NewRequest(http.MethodGet, "/api/conversations", nil)
		req.Header.Set("X-User-Id", ownerID)
		req.Header.Set("X-Device-Id", device)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("list status=%d body=%s", rec.Code, rec.Body)
		}
		var out struct {
			Conversations []map[string]any `json:"conversations"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		for _, item := range out.Conversations {
			if item["id"] == session.ID {
				return item
			}
		}
		return nil
	}

	rec := readAs("android-1")
	if rec.Code != http.StatusOK {
		t.Fatalf("read status=%d body=%s", rec.Code, rec.Body)
	}
	// 手机读完之后新增一条消息：手机看到 1 条未读。
	if err := repos.Sessions.AppendMessage(ctx, &store.ChatMessage{
		SessionID: session.ID, Role: "assistant", Content: "第四条",
	}); err != nil {
		t.Fatal(err)
	}
	phone := listAs("android-1")
	if phone == nil || phone["unread_count"].(float64) != 1 {
		t.Fatalf("phone unread=%v", phone)
	}
	// 电脑是另一台设备，未读数不能被手机清零。
	desktop := listAs("desktop-1")
	if desktop == nil || desktop["unread_count"].(float64) != 0 {
		t.Fatalf("desktop unread must stay independent: %v", desktop)
	}
	// 读过的会话读第二次不报错。
	if rec := readAs("android-1"); rec.Code != http.StatusOK {
		t.Fatalf("second read status=%d", rec.Code)
	}
}

// sseDonePayload 取出 SSE 最后一帧 done 的 JSON 数据。
func sseDonePayload(t *testing.T, body string) map[string]any {
	t.Helper()
	var payload string
	for _, block := range strings.Split(body, "\n\n") {
		lines := strings.Split(strings.TrimSpace(block), "\n")
		if len(lines) < 2 || !strings.Contains(lines[0], "event: done") {
			continue
		}
		for _, line := range lines {
			if strings.HasPrefix(line, "data: ") {
				payload = strings.TrimPrefix(line, "data: ")
			}
		}
	}
	if payload == "" {
		t.Fatalf("no done event in stream: %q", body)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(payload), &out); err != nil {
		t.Fatalf("decode done payload: %v (%q)", err, payload)
	}
	return out
}

func TestChatResponseKeepsTaskStructureForCoordinator(t *testing.T) {
	srv, repos := newTestServer(t)
	tasks := worktask.New(repos.DB)
	srv.Tasks = tasks
	srv.Coordinator = &superagent.Coordinator{Tasks: tasks}
	h := srv.Handler()

	rec := postJSON(t, h, "/api/chat", `{"text":"新建任务：整理本周邮件"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("chat status=%d body=%s", rec.Code, rec.Body)
	}
	body := decodeMap(t, rec)
	taskID, _ := body["task_id"].(string)
	if taskID == "" {
		t.Fatalf("task_id missing: %s", rec.Body)
	}
	if body["action"] != "create" {
		t.Fatalf("action=%v body=%s", body["action"], rec.Body)
	}
	task, ok := body["task"].(map[string]any)
	if !ok {
		t.Fatalf("task card missing: %s", rec.Body)
	}
	if task["id"] != taskID || task["status"] != store.WorkTaskQueued {
		t.Fatalf("task card=%v", task)
	}
	if task["title"] != "整理本周邮件" {
		t.Fatalf("task title=%v", task["title"])
	}

	// 绑定 Worker 会话后，任务卡必须带上可跳转的会话引用：
	// 前端两端都读 conversation.session_id，只给 id 会点不动。
	ctx := context.Background()
	users, err := repos.Users.ListAll(ctx)
	if err != nil || len(users) == 0 {
		t.Fatalf("default user missing: %v", err)
	}
	worker := sessionFor(t, repos, users[0].ID, "worker", "开始整理邮件")
	if err := repos.Sessions.SetWorkTaskID(ctx, worker.ID, taskID); err != nil {
		t.Fatal(err)
	}

	rec = postJSON(t, h, "/api/chat", `{"text":"/task show `+taskID+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("show task status=%d body=%s", rec.Code, rec.Body)
	}
	body = decodeMap(t, rec)
	conversation, ok := body["conversation"].(map[string]any)
	if !ok {
		t.Fatalf("conversation ref missing: %s", rec.Body)
	}
	if conversation["session_id"] != worker.ID || conversation["id"] != worker.ID {
		t.Fatalf("conversation=%v want session %s", conversation, worker.ID)
	}
	if conversation["kind"] != store.ConversationWorker {
		t.Fatalf("conversation kind=%v", conversation["kind"])
	}

	// SSE done 帧必须带同一份任务结构，否则流式聊天里任务卡点不动。
	streamReq := httptest.NewRequest(http.MethodPost, "/api/chat/stream",
		strings.NewReader(`{"text":"/task show `+taskID+`"}`))
	streamReq.Header.Set("Content-Type", "application/json")
	streamRec := httptest.NewRecorder()
	h.ServeHTTP(streamRec, streamReq)
	done := sseDonePayload(t, streamRec.Body.String())
	streamConv, ok := done["conversation"].(map[string]any)
	if !ok {
		t.Fatalf("stream conversation ref missing: %s", streamRec.Body)
	}
	if streamConv["session_id"] != worker.ID {
		t.Fatalf("stream conversation=%v", streamConv)
	}
	if streamTask, ok := done["task"].(map[string]any); !ok || streamTask["id"] != taskID {
		t.Fatalf("stream task card=%v", done["task"])
	}

	// 普通聊天不返回任务结构。
	rec = postJSON(t, h, "/api/chat", `{"text":"今天天气怎么样"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("plain chat status=%d body=%s", rec.Code, rec.Body)
	}
	body = decodeMap(t, rec)
	if _, ok := body["task"]; ok {
		t.Fatalf("plain chat must not carry task card: %s", rec.Body)
	}
	if _, ok := body["task_id"]; ok {
		t.Fatalf("plain chat must not carry task_id: %s", rec.Body)
	}
}

func TestConversationCanMessageFalseForTerminalTask(t *testing.T) {
	srv, repos := newTestServer(t)
	h := srv.Handler()
	ctx := context.Background()
	ownerID := ownerUser(t, repos, "主人").ID

	task := &store.WorkTask{
		OwnerUserID: ownerID, Title: "已完成任务", Goal: "做完",
		Status: store.WorkTaskActive, IdempotencyKey: "conv-terminal-1",
		Profile: store.WorkTaskProfileRead,
	}
	if err := repos.WorkTasks.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	worker := sessionFor(t, repos, ownerID, "worker", "执行中", "完成")
	if err := repos.Sessions.SetWorkTaskID(ctx, worker.ID, task.ID); err != nil {
		t.Fatal(err)
	}
	// 把任务推进到终态。
	stored, err := repos.WorkTasks.Get(ctx, ownerID, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repos.WorkTasks.Transition(ctx, ownerID, task.ID, stored.Version, store.WorkTaskCompleted, fixedNow); err != nil {
		t.Fatal(err)
	}

	rec := getJSON(t, h, "/api/conversations?kind=worker")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	items := decodeMap(t, rec)["conversations"].([]any)
	var found map[string]any
	for _, item := range items {
		entry := item.(map[string]any)
		if entry["id"] == worker.ID {
			found = entry
		}
	}
	if found == nil {
		t.Fatalf("worker conversation missing: %s", rec.Body)
	}
	if found["can_message"] != false {
		t.Fatalf("terminal task must not be messageable: %v", found)
	}
	if found["status"] != store.WorkTaskCompleted {
		t.Fatalf("status=%v", found["status"])
	}
}

func TestConversationMarkReadWorksBeyondFirstPage(t *testing.T) {
	srv, repos := newTestServer(t)
	h := srv.Handler()
	ctx := context.Background()
	ownerID := ownerUser(t, repos, "主人").ID

	// 造出超过一页（500 条）的会话：旧的「先列表再反查」实现
	// 会把后面的合法会话误报成 404。
	// 主对话每个 owner 只能有一条，这里用 Worker 会话铺量。
	var last *store.ChatSession
	for i := 0; i < 520; i++ {
		session := &store.ChatSession{OwnerUserID: ownerID, Kind: "worker"}
		if err := repos.Sessions.Create(ctx, session); err != nil {
			t.Fatal(err)
		}
		if err := repos.Sessions.AppendMessage(ctx, &store.ChatMessage{
			SessionID: session.ID, Role: "user", Content: "消息",
		}); err != nil {
			t.Fatal(err)
		}
		last = session
	}

	req := httptest.NewRequest(http.MethodPost, "/api/conversations/"+last.ID+"/read", nil)
	req.Header.Set("X-User-Id", ownerID)
	req.Header.Set("X-Device-Id", "android-1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("mark read status=%d body=%s", rec.Code, rec.Body)
	}
	if decodeMap(t, rec)["read"] != true {
		t.Fatalf("body=%s", rec.Body)
	}
}

func TestConversationProjectionReportsFactSourceDevice(t *testing.T) {
	srv, repos := newTestServer(t)
	h := srv.Handler()
	ownerID := ownerUser(t, repos, "主人").ID
	sessionFor(t, repos, ownerID, "main", "你好", "你好呀")
	srv.DeviceID = "desktop-1"

	// 请求方是手机，但会话事实源在电脑：source_device 必须是电脑。
	req := httptest.NewRequest(http.MethodGet, "/api/conversations", nil)
	req.Header.Set("X-User-Id", ownerID)
	req.Header.Set("X-Device-Id", "android-1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	items := decodeMap(t, rec)["conversations"].([]any)
	if len(items) == 0 {
		t.Fatalf("no conversations: %s", rec.Body)
	}
	for _, item := range items {
		if got := item.(map[string]any)["source_device"]; got != "desktop-1" {
			t.Fatalf("source_device=%v，应为会话事实源设备", got)
		}
	}
}

func containsSecret(haystack, needle string) bool {
	return needle != "" && strings.Contains(haystack, needle)
}
