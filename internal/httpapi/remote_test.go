package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"goldenfinger/agent/internal/relay"
)

// fakeRemote 模拟手机 → 电脑的控制面通道。
type fakeRemote struct {
	offline bool
	// rejected 让变更类指令返回 409，模拟电脑明确拒绝。
	rejected bool
	// unknown 让变更类指令返回「结果未知」，模拟超时或 5xx。
	unknown bool
	// data 是读取类指令返回的结构化数据。
	data string
	// calls 记录每次调用的参数。
	calls []string
	// keys 记录变更类指令收到的幂等键。
	keys []string
	// versions 记录变更类指令收到的乐观锁版本。
	versions []*int
}

func (f *fakeRemote) read(name string, result relay.ResultPayload, err error) (relay.ResultPayload, error) {
	f.calls = append(f.calls, name)
	if f.offline {
		return relay.ResultPayload{}, relay.ErrDesktopOffline
	}
	return result, err
}

func (f *fakeRemote) WorkTaskCreate(ctx context.Context, idempotencyKey, title, goal, profile string) (relay.ResultPayload, error) {
	return f.mutate("work_task_create:"+title, idempotencyKey)
}

func (f *fakeRemote) WorkTaskList(ctx context.Context, status string, limit int) (relay.ResultPayload, error) {
	return f.read("work_task_list:"+status, relay.ResultPayload{OK: true, Command: relay.CommandExecuted, Data: json.RawMessage(f.data)}, nil)
}

func (f *fakeRemote) WorkTaskGet(ctx context.Context, taskID string) (relay.ResultPayload, error) {
	return f.read("work_task_get:"+taskID, relay.ResultPayload{OK: true, Command: relay.CommandExecuted, Data: json.RawMessage(f.data)}, nil)
}

func (f *fakeRemote) WorkTaskMessage(ctx context.Context, idempotencyKey, taskID, text string, expectedVersion *int) (relay.ResultPayload, error) {
	f.versions = append(f.versions, expectedVersion)
	return f.mutate("work_task_message:"+taskID, idempotencyKey)
}

func (f *fakeRemote) WorkTaskAction(ctx context.Context, idempotencyKey, taskID, action string, expectedVersion *int) (relay.ResultPayload, error) {
	f.versions = append(f.versions, expectedVersion)
	return f.mutate("work_task_action:"+taskID+":"+action, idempotencyKey)
}

func (f *fakeRemote) ConversationList(ctx context.Context, kind string, limit int, deviceID string) (relay.ResultPayload, error) {
	return f.read("conversation_list:"+kind+":"+deviceID, relay.ResultPayload{OK: true, Command: relay.CommandExecuted, Data: json.RawMessage(f.data)}, nil)
}

func (f *fakeRemote) ConversationMessages(ctx context.Context, sessionID, before string, limit int) (relay.ResultPayload, error) {
	return f.read("conversation_messages:"+sessionID, relay.ResultPayload{OK: true, Command: relay.CommandExecuted, Data: json.RawMessage(f.data)}, nil)
}

func (f *fakeRemote) OutboxList(ctx context.Context, limit int) (relay.ResultPayload, error) {
	return f.read("outbox_list", relay.ResultPayload{OK: true, Command: relay.CommandExecuted, Data: json.RawMessage(f.data)}, nil)
}

func (f *fakeRemote) OutboxDecide(ctx context.Context, idempotencyKey, outboxID string, approve bool, expectedVersion *int, reason string) (relay.ResultPayload, error) {
	return f.mutate("outbox_decide:"+outboxID, idempotencyKey)
}

func (f *fakeRemote) ArtifactGet(ctx context.Context, artifactID string) (relay.ResultPayload, error) {
	return f.read("artifact_get:"+artifactID, relay.ResultPayload{OK: true, Command: relay.CommandExecuted, Data: json.RawMessage(f.data)}, nil)
}

func (f *fakeRemote) ArtifactDecide(ctx context.Context, idempotencyKey, artifactID string, accept bool, expectedVersion *int, reason string) (relay.ResultPayload, error) {
	return f.mutate("artifact_decide:"+artifactID, idempotencyKey)
}

func (f *fakeRemote) ArtifactDeliver(ctx context.Context, idempotencyKey, artifactID string, version int) (relay.ResultPayload, error) {
	f.keys = append(f.keys, idempotencyKey)
	return f.read("artifact_deliver:"+artifactID, relay.ResultPayload{OK: true, Command: relay.CommandExecuted, Data: json.RawMessage(f.data)}, nil)
}

func (f *fakeRemote) BriefLatest(ctx context.Context, date string) (relay.ResultPayload, error) {
	return f.read("brief_latest:"+date, relay.ResultPayload{OK: true, Command: relay.CommandExecuted, Data: json.RawMessage(f.data)}, nil)
}

func (f *fakeRemote) BriefList(ctx context.Context, limit int) (relay.ResultPayload, error) {
	return f.read("brief_list", relay.ResultPayload{OK: true, Command: relay.CommandExecuted, Data: json.RawMessage(f.data)}, nil)
}

func (f *fakeRemote) BriefGet(ctx context.Context, briefID string) (relay.ResultPayload, error) {
	return f.read("brief_get:"+briefID, relay.ResultPayload{OK: true, Command: relay.CommandExecuted, Data: json.RawMessage(f.data)}, nil)
}

func (f *fakeRemote) BriefGenerate(ctx context.Context, idempotencyKey, date string) (relay.ResultPayload, error) {
	return f.mutate("brief_generate", idempotencyKey)
}

func (f *fakeRemote) CollectionList(ctx context.Context) (relay.ResultPayload, error) {
	return f.read("collection_list", relay.ResultPayload{OK: true, Command: relay.CommandExecuted, Data: json.RawMessage(f.data)}, nil)
}

func (f *fakeRemote) CollectionConfigure(ctx context.Context, idempotencyKey, channel string, enabled bool, config map[string]any) (relay.ResultPayload, error) {
	return f.mutate("collection_configure:"+channel, idempotencyKey)
}

func (f *fakeRemote) RemoteStatus() map[string]any {
	return map[string]any{"paired": true, "connected": !f.offline}
}

func (f *fakeRemote) ConversationRead(ctx context.Context, sessionID, deviceID string) (relay.ResultPayload, error) {
	f.calls = append(f.calls, "conversation_read:"+sessionID+":"+deviceID)
	if f.offline {
		return relay.ResultPayload{}, relay.ErrDesktopOffline
	}
	return relay.ResultPayload{OK: true, Command: relay.CommandExecuted,
		Data: json.RawMessage(`{"read":true}`)}, nil
}

// mutate 模拟一次变更类指令。
func (f *fakeRemote) mutate(name, idempotencyKey string) (relay.ResultPayload, error) {
	f.calls = append(f.calls, name)
	f.keys = append(f.keys, idempotencyKey)
	if f.offline {
		return relay.ResultPayload{}, relay.ErrDesktopOffline
	}
	if f.rejected {
		return relay.ResultPayload{OK: false, Error: "版本过期", Command: relay.CommandConflict}, nil
	}
	if f.unknown {
		// 超时 / 5xx：电脑可能已经执行成功，只是回执没回来。
		return relay.ResultPayload{OK: false, Error: "电脑没有在预期时间内返回结果", Command: relay.CommandUnknown}, nil
	}
	return relay.ResultPayload{OK: true, Command: relay.CommandExecuted, Data: json.RawMessage(`{"ok":true}`)}, nil
}

func newRemoteServer(t *testing.T) (*Server, *fakeRemote) {
	t.Helper()
	srv, _ := newTestServer(t)
	remote := &fakeRemote{data: `{"brief":{"id":"b-1","content_md":"# 今日"}}`}
	srv.Remote = remote
	return srv, remote
}

func TestRemoteReadReturnsLiveAndCaches(t *testing.T) {
	srv, remote := newRemoteServer(t)
	h := srv.Handler()

	rec := getJSON(t, h, "/api/remote/briefs/latest")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	body := decodeMap(t, rec)
	if body["_source"] != remoteSourceLive {
		t.Fatalf("source=%v body=%s", body["_source"], rec.Body)
	}
	if body["_offline"] != false {
		t.Fatalf("offline=%v", body["_offline"])
	}
	brief, ok := body["brief"].(map[string]any)
	if !ok || brief["id"] != "b-1" {
		t.Fatalf("brief shape broken: %s", rec.Body)
	}
	if len(remote.calls) != 1 {
		t.Fatalf("calls=%v", remote.calls)
	}

	// 结果已经写进缓存。
	users, _ := srv.Repos.Users.ListAll(context.Background())
	if len(users) == 0 {
		t.Fatal("no user")
	}
	cached, synced, err := srv.Repos.RemoteCache.Get(context.Background(), users[0].ID, "brief:latest:")
	if err != nil {
		t.Fatalf("cache missing: %v", err)
	}
	if len(cached) == 0 {
		t.Fatalf("cached payload empty")
	}
	if synced.IsZero() {
		t.Fatalf("synced=%v", synced)
	}
}

func TestRemoteReadFallsBackToCacheWhenOffline(t *testing.T) {
	srv, remote := newRemoteServer(t)
	h := srv.Handler()

	// 先在线取一次，建立缓存。
	rec := getJSON(t, h, "/api/remote/briefs/latest")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	// 电脑离线后再取。
	remote.offline = true
	rec = getJSON(t, h, "/api/remote/briefs/latest")
	if rec.Code != http.StatusOK {
		t.Fatalf("offline status=%d body=%s", rec.Code, rec.Body)
	}
	body := decodeMap(t, rec)
	if body["_source"] != remoteSourceCache {
		t.Fatalf("source=%v body=%s", body["_source"], rec.Body)
	}
	if body["_offline"] != true {
		t.Fatalf("offline flag must be true: %s", rec.Body)
	}
	if body["_synced_at"] == nil || body["_synced_at"] == "" {
		t.Fatalf("missing synced_at: %s", rec.Body)
	}
	if _, ok := body["brief"]; !ok {
		t.Fatalf("cached brief missing: %s", rec.Body)
	}
}

func TestRemoteReadWithoutCacheFailsHonestly(t *testing.T) {
	srv, remote := newRemoteServer(t)
	h := srv.Handler()
	remote.offline = true

	rec := getJSON(t, h, "/api/remote/briefs/latest")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "离线") {
		t.Fatalf("body=%s", rec.Body)
	}
}

func TestRemoteMutateQueuesWhenOffline(t *testing.T) {
	srv, remote := newRemoteServer(t)
	h := srv.Handler()
	remote.offline = true

	rec := postJSON(t, h, "/api/remote/work-tasks/wt-1/messages", `{"text":"先核对日期","idempotency_key":"key-1"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	body := decodeMap(t, rec)
	if body["queued"] != true {
		t.Fatalf("queued=%v body=%s", body["queued"], rec.Body)
	}
	if body["_command"] != relay.CommandQueued {
		t.Fatalf("command=%v", body["_command"])
	}
	if body["_idempotency_key"] != "key-1" {
		t.Fatalf("idempotency key must be echoed: %s", rec.Body)
	}

	// 指令已经落库，等待重放。
	users, _ := srv.Repos.Users.ListAll(context.Background())
	if len(users) == 0 {
		t.Fatal("no user")
	}
	items, err := srv.Repos.RemotePending.ListPending(context.Background(), users[0].ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("pending=%v", items)
	}
	if items[0].IdempotencyKey != "key-1" {
		t.Fatalf("pending key=%s", items[0].IdempotencyKey)
	}
	if items[0].Kind != relay.CmdWorkTaskMsg {
		t.Fatalf("pending kind=%s", items[0].Kind)
	}
}

func TestRemoteMutateConflictIsNotQueued(t *testing.T) {
	srv, remote := newRemoteServer(t)
	h := srv.Handler()
	remote.rejected = true

	rec := postJSON(t, h, "/api/remote/work-tasks/wt-1/pause", `{"idempotency_key":"key-2"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	body := decodeMap(t, rec)
	if body["_queued"] == true {
		t.Fatalf("conflict must not be queued: %s", rec.Body)
	}
	if !strings.Contains(body["error"].(string), "版本过期") {
		t.Fatalf("error=%v", body["error"])
	}
}

func TestRemoteFlushReplaysWithSameIdempotencyKey(t *testing.T) {
	srv, remote := newRemoteServer(t)
	h := srv.Handler()
	remote.offline = true

	rec := postJSON(t, h, "/api/remote/work-tasks/wt-1/messages", `{"text":"先核对日期","idempotency_key":"key-3"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	// 电脑恢复在线。
	remote.offline = false
	rec = postJSON(t, h, "/api/remote/flush", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("flush status=%d body=%s", rec.Code, rec.Body)
	}
	body := decodeMap(t, rec)
	if body["replayed"].(float64) != 1 {
		t.Fatalf("replayed=%v body=%s", body["replayed"], rec.Body)
	}
	// 重放必须复用同一个幂等键。
	found := false
	for _, key := range remote.keys {
		if key == "key-3" {
			found = true
		}
	}
	if !found {
		t.Fatalf("keys=%v", remote.keys)
	}
	// 队列已清空。
	users, _ := srv.Repos.Users.ListAll(context.Background())
	items, _ := srv.Repos.RemotePending.ListPending(context.Background(), users[0].ID, 10)
	if len(items) != 0 {
		t.Fatalf("pending=%v", items)
	}
}

func TestRemoteConversationReadCarriesDeviceID(t *testing.T) {
	srv, remote := newRemoteServer(t)
	h := srv.Handler()
	srv.DeviceID = "android-device-1"

	rec := postJSON(t, h, "/api/remote/conversations/s-1/read", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	// 已读游标必须带上这台设备的标识，否则会清掉电脑的角标。
	found := false
	for _, call := range remote.calls {
		if call == "conversation_read:s-1:android-device-1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("calls=%v", remote.calls)
	}
}

func TestRemoteConversationListAndReadShareDeviceKey(t *testing.T) {
	srv, remote := newRemoteServer(t)
	h := srv.Handler()
	srv.DeviceID = "android-device-1"

	rec := getJSON(t, h, "/api/remote/conversations?kind=worker")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", rec.Code, rec.Body)
	}
	rec = postJSON(t, h, "/api/remote/conversations/s-1/read", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("read status=%d body=%s", rec.Code, rec.Body)
	}

	// 未读统计与已读记录必须用同一个设备标识，否则角标永远清不掉。
	listKey, readKey := "", ""
	for _, call := range remote.calls {
		switch {
		case strings.HasPrefix(call, "conversation_list:"):
			listKey = strings.TrimPrefix(call, "conversation_list:")
		case strings.HasPrefix(call, "conversation_read:"):
			readKey = strings.TrimPrefix(call, "conversation_read:")
		}
	}
	if listKey == "" || readKey == "" {
		t.Fatalf("calls=%v", remote.calls)
	}
	if !strings.HasSuffix(listKey, ":android-device-1") {
		t.Fatalf("list device key=%q", listKey)
	}
	if !strings.HasSuffix(readKey, ":android-device-1") {
		t.Fatalf("read device key=%q", readKey)
	}
}

func TestRemoteUnknownResultIsNeverQueued(t *testing.T) {
	srv, remote := newRemoteServer(t)
	h := srv.Handler()
	remote.unknown = true

	rec := postJSON(t, h, "/api/remote/work-tasks/wt-1/messages", `{"text":"先核对日期","idempotency_key":"key-unknown"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	body := decodeMap(t, rec)
	if body["_queued"] == true {
		t.Fatalf("unknown result must not be queued: %s", rec.Body)
	}
	if body["_command"] != relay.CommandUnknown {
		t.Fatalf("command=%v body=%s", body["_command"], rec.Body)
	}
	if !strings.Contains(body["error"].(string), "结果未知") {
		t.Fatalf("error=%v", body["error"])
	}

	// 结果未知的指令不能落进离线队列：电脑可能已经执行成功，
	// 排队重放会执行第二遍。
	users, _ := srv.Repos.Users.ListAll(context.Background())
	items, err := srv.Repos.RemotePending.ListPending(context.Background(), users[0].ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("pending=%v", items)
	}
}

func TestRemoteFlushRestoresExpectedVersion(t *testing.T) {
	srv, remote := newRemoteServer(t)
	h := srv.Handler()
	remote.offline = true

	rec := postJSON(t, h, "/api/remote/work-tasks/wt-1/messages",
		`{"text":"继续","idempotency_key":"key-ver","expected_version":7}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	// 排队时要连版本一起存下来。
	users, _ := srv.Repos.Users.ListAll(context.Background())
	items, err := srv.Repos.RemotePending.ListPending(context.Background(), users[0].ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("pending=%v", items)
	}
	var stored struct {
		ExpectedVersion *int `json:"expected_version"`
	}
	if err := json.Unmarshal(items[0].Payload, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.ExpectedVersion == nil || *stored.ExpectedVersion != 7 {
		t.Fatalf("queued payload lost version: %s", items[0].Payload)
	}

	// 重放必须带上原来的版本，不能绕过 CAS 检查。
	remote.offline = false
	rec = postJSON(t, h, "/api/remote/flush", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("flush status=%d body=%s", rec.Code, rec.Body)
	}
	if decodeMap(t, rec)["replayed"].(float64) != 1 {
		t.Fatalf("replayed body=%s", rec.Body)
	}
	found := false
	for _, version := range remote.versions {
		if version != nil && *version == 7 {
			found = true
		}
	}
	if !found {
		t.Fatalf("replay lost expected_version: %v", remote.versions)
	}
}

func TestRemoteCreatesWorkTaskThroughComputer(t *testing.T) {
	srv, remote := newRemoteServer(t)
	h := srv.Handler()

	rec := postJSON(t, h, "/api/remote/work-tasks",
		`{"title":"整理本周邮件","goal":"按主题分类","idempotency_key":"key-create"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	if decodeMap(t, rec)["_command"] != relay.CommandExecuted {
		t.Fatalf("body=%s", rec.Body)
	}
	found := false
	for _, call := range remote.calls {
		if call == "work_task_create:整理本周邮件" {
			found = true
		}
	}
	if !found {
		t.Fatalf("calls=%v", remote.calls)
	}
	for _, key := range remote.keys {
		if key == "key-create" {
			return
		}
	}
	t.Fatalf("idempotency key not forwarded: %v", remote.keys)
}

func TestRemoteCreateWorkTaskQueuesWhenOffline(t *testing.T) {
	srv, remote := newRemoteServer(t)
	h := srv.Handler()
	remote.offline = true

	rec := postJSON(t, h, "/api/remote/work-tasks", `{"title":"整理本周邮件","idempotency_key":"key-create-off"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	if decodeMap(t, rec)["queued"] != true {
		t.Fatalf("body=%s", rec.Body)
	}
	users, _ := srv.Repos.Users.ListAll(context.Background())
	items, err := srv.Repos.RemotePending.ListPending(context.Background(), users[0].ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Kind != relay.CmdWorkTaskCreate {
		t.Fatalf("pending=%v", items)
	}
}

func TestRemoteFlushStopsAfterUnknown(t *testing.T) {
	srv, remote := newRemoteServer(t)
	h := srv.Handler()
	remote.offline = true

	rec := postJSON(t, h, "/api/remote/work-tasks/wt-1/messages", `{"text":"继续","idempotency_key":"key-retry"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("queue status=%d body=%s", rec.Code, rec.Body)
	}
	// 电脑恢复「在线」，但结果是未知（超时/5xx）。
	remote.offline = false
	remote.unknown = true
	rec = postJSON(t, h, "/api/remote/flush", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("first flush status=%d body=%s", rec.Code, rec.Body)
	}
	if decodeMap(t, rec)["failed"].(float64) != 1 {
		t.Fatalf("first flush body=%s", rec.Body)
	}
	callsAfterFirst := len(remote.calls)

	// 第二次 flush 不能再重放：unknown 之后必须停下来等用户核对。
	rec = postJSON(t, h, "/api/remote/flush", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("second flush status=%d body=%s", rec.Code, rec.Body)
	}
	if len(remote.calls) != callsAfterFirst {
		t.Fatalf("unknown 之后又自动重放了：calls=%v", remote.calls[callsAfterFirst:])
	}
	users, _ := srv.Repos.Users.ListAll(context.Background())
	items, _ := srv.Repos.RemotePending.ListPending(context.Background(), users[0].ID, 10)
	if len(items) != 0 {
		t.Fatalf("失败的指令不应继续 pending：%v", items)
	}
}

func TestRemoteFlushKeepsPendingWhenOfflineAgain(t *testing.T) {
	srv, remote := newRemoteServer(t)
	h := srv.Handler()
	remote.offline = true

	postJSON(t, h, "/api/remote/work-tasks/wt-1/messages", `{"text":"继续","idempotency_key":"key-off-again"}`)
	// 仍然离线：保留 pending，等电脑真正上线再试。
	rec := postJSON(t, h, "/api/remote/flush", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("flush status=%d body=%s", rec.Code, rec.Body)
	}
	users, _ := srv.Repos.Users.ListAll(context.Background())
	items, _ := srv.Repos.RemotePending.ListPending(context.Background(), users[0].ID, 10)
	if len(items) != 1 {
		t.Fatalf("明确离线时不能丢掉排队指令：%v", items)
	}
}

func TestRemoteOutboxRejectsUnknownAction(t *testing.T) {
	srv, remote := newRemoteServer(t)
	h := srv.Handler()

	for _, action := range []string{"typo", "approved", "cancel"} {
		rec := postJSON(t, h, "/api/remote/action-outbox/ao-1/"+action, `{"expected_version":1}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("action=%s status=%d body=%s", action, rec.Code, rec.Body)
		}
	}
	if len(remote.calls) != 0 {
		t.Fatalf("非法动作不能调用远程控制：%v", remote.calls)
	}
}

func TestRemoteArtifactDeliverReusesKeyOnReplay(t *testing.T) {
	srv, remote := newRemoteServer(t)
	h := srv.Handler()
	remote.offline = true

	rec := postJSON(t, h, "/api/remote/artifacts/ar-1/deliver",
		`{"version":2,"idempotency_key":"key-deliver"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("queue status=%d body=%s", rec.Code, rec.Body)
	}
	remote.offline = false
	rec = postJSON(t, h, "/api/remote/flush", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("flush status=%d body=%s", rec.Code, rec.Body)
	}
	if len(remote.keys) < 2 {
		t.Fatalf("交付指令没有走两次调用：%v", remote.keys)
	}
	for _, key := range remote.keys {
		if key != "key-deliver" {
			t.Fatalf("交付重放必须复用同一个幂等键：%v", remote.keys)
		}
	}
}

func TestRemoteQueueReportsExistingState(t *testing.T) {
	srv, remote := newRemoteServer(t)
	h := srv.Handler()
	remote.offline = true

	// 第一次：真正入队（这次请求也会创建用户）。
	rec := postJSON(t, h, "/api/remote/work-tasks/wt-1/messages",
		`{"text":"继续","idempotency_key":"key-state"}`)
	if rec.Code != http.StatusAccepted || decodeMap(t, rec)["queued"] != true {
		t.Fatalf("first status=%d body=%s", rec.Code, rec.Body)
	}
	users, _ := srv.Repos.Users.ListAll(context.Background())
	ownerID := users[0].ID

	// 同键同内容：如实返回「已在队列中」，不能新增一条。
	rec = postJSON(t, h, "/api/remote/work-tasks/wt-1/messages",
		`{"text":"继续","idempotency_key":"key-state"}`)
	if rec.Code != http.StatusAccepted || decodeMap(t, rec)["queued"] != true {
		t.Fatalf("repeat status=%d body=%s", rec.Code, rec.Body)
	}
	items, _ := srv.Repos.RemotePending.ListPending(context.Background(), ownerID, 10)
	if len(items) != 1 {
		t.Fatalf("同键不能重复入队：%v", items)
	}

	// 同键不同内容：冲突，不能悄悄替换。
	rec = postJSON(t, h, "/api/remote/work-tasks/wt-1/messages",
		`{"text":"换一句","idempotency_key":"key-state"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("conflict status=%d body=%s", rec.Code, rec.Body)
	}

	// 已完成的键：直接回放原结果，不再说「已排队」。
	if err := srv.Repos.RemotePending.MarkDone(context.Background(), "key-state",
		[]byte(`{"ok":true,"reply":"已执行"}`), srv.Now()); err != nil {
		t.Fatal(err)
	}
	rec = postJSON(t, h, "/api/remote/work-tasks/wt-1/messages",
		`{"text":"继续","idempotency_key":"key-state"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("done replay status=%d body=%s", rec.Code, rec.Body)
	}
	body := decodeMap(t, rec)
	if body["_replayed"] != true || body["reply"] != "已执行" || body["queued"] == true {
		t.Fatalf("done replay body=%s", rec.Body)
	}

	// 已失败的键：要求用户核对，不能假装还能排队执行。
	if err := srv.Repos.RemotePending.MarkFailed(context.Background(), "key-state", "上次超时", srv.Now()); err != nil {
		t.Fatal(err)
	}
	rec = postJSON(t, h, "/api/remote/work-tasks/wt-1/messages",
		`{"text":"继续","idempotency_key":"key-state"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("failed status=%d body=%s", rec.Code, rec.Body)
	}
	if decodeMap(t, rec)["_queued"] == true {
		t.Fatalf("失败的指令不能报告已排队：%s", rec.Body)
	}
}

func TestRemoteRequiresControlPlane(t *testing.T) {
	srv, _ := newTestServer(t)
	h := srv.Handler()
	rec := getJSON(t, h, "/api/remote/briefs/latest")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
}

func TestRemoteHighRiskActionsAreNotQueued(t *testing.T) {
	srv, remote := newRemoteServer(t)
	h := srv.Handler()
	remote.offline = true

	// 审批、验收、生成简报都必须等电脑在线：不能把「已排队」显示成「已批准」。
	cases := []struct {
		name string
		path string
		body string
	}{
		{"approve", "/api/remote/action-outbox/ao-1/approve", `{"expected_version":1}`},
		{"reject", "/api/remote/action-outbox/ao-1/reject", `{"expected_version":1,"reason":"不需要"}`},
		{"accept", "/api/remote/artifacts/ar-1/accept", `{"expected_version":1}`},
		{"artifact-reject", "/api/remote/artifacts/ar-1/reject", `{"expected_version":1}`},
		{"brief-generate", "/api/remote/briefs/generate", `{}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := postJSON(t, h, tc.path, tc.body)
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
			}
			body := decodeMap(t, rec)
			if body["_queued"] == true {
				t.Fatalf("high-risk action must not be queued: %s", rec.Body)
			}
		})
	}

	// 一条都不能落进离线队列。
	users, _ := srv.Repos.Users.ListAll(context.Background())
	items, err := srv.Repos.RemotePending.ListPending(context.Background(), users[0].ID, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("pending=%v", items)
	}

	// 追加指令这类低风险变更仍然可以排队。
	rec := postJSON(t, h, "/api/remote/work-tasks/wt-1/messages", `{"text":"继续","idempotency_key":"key-hr"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("message status=%d body=%s", rec.Code, rec.Body)
	}
	if decodeMap(t, rec)["queued"] != true {
		t.Fatalf("low-risk action should be queued: %s", rec.Body)
	}
}
