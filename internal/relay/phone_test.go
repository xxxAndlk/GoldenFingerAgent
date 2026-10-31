package relay

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// startRelay 启动中继并用电脑端客户端占位（模拟电脑在线/离线）。
func startRelay(t *testing.T) (*Hub, *httptest.Server) {
	t.Helper()
	hub := NewHub(Config{})
	srv := httptest.NewServer(NewServer(hub, Config{}).Handler())
	t.Cleanup(srv.Close)
	return hub, srv
}

func pairPhone(t *testing.T, hub *Hub, srvURL, statePath string) *Phone {
	t.Helper()
	desktopID, desktopToken, err := hub.RegisterDesktop("工作机")
	if err != nil {
		t.Fatal(err)
	}
	code, _, err := hub.CreatePairCode(desktopID, desktopToken)
	if err != nil {
		t.Fatal(err)
	}
	phone := &Phone{StatePath: statePath, SendTimeout: 5 * time.Second, Logger: func(string, ...any) {}}
	if _, err := phone.Pair(context.Background(), srvURL, code, "我的手机"); err != nil {
		t.Fatalf("pair: %v", err)
	}
	return phone
}

func TestPhonePairPersistsAndStatusHidesToken(t *testing.T) {
	hub, srv := startRelay(t)
	statePath := filepath.Join(t.TempDir(), "relay-phone.json")
	phone := pairPhone(t, hub, srv.URL, statePath)
	t.Cleanup(phone.Close)

	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("state file: %v", err)
	}
	var saved PhoneState
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.ChannelID == "" || saved.PhoneToken == "" {
		t.Fatalf("state=%+v", saved)
	}

	status := phone.Status()
	if status["paired"] != true {
		t.Fatalf("status=%+v", status)
	}
	if _, leaked := status["phone_token"]; leaked {
		t.Fatalf("status must not expose token: %+v", status)
	}

	// 新实例从文件恢复后仍是已配对。
	again := &Phone{StatePath: statePath, Logger: func(string, ...any) {}}
	if err := again.Load(); err != nil {
		t.Fatal(err)
	}
	if again.Status()["paired"] != true {
		t.Fatal("state not reloaded")
	}
	_ = again
}

func TestPhoneSendReturnsDesktopResult(t *testing.T) {
	hub, srv := startRelay(t)
	desktopID, desktopToken, _ := hub.RegisterDesktop("工作机")
	code, _, _ := hub.CreatePairCode(desktopID, desktopToken)

	desktop := &Client{
		BaseURL: srv.URL, DesktopID: desktopID, Token: desktopToken,
		Handle: func(_ context.Context, payload json.RawMessage) (ResultPayload, error) {
			var p struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(payload, &p)
			return ResultPayload{OK: true, Reply: "电脑收到：" + p.Text}, nil
		},
		Logger: func(string, ...any) {},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = desktop.Run(ctx) }()
	waitFor(t, func() bool { return hub.Stats().Online[RoleDesktop] > 0 })

	phone := &Phone{StatePath: filepath.Join(t.TempDir(), "p.json"), SendTimeout: 5 * time.Second, Logger: func(string, ...any) {}}
	t.Cleanup(phone.Close)
	if _, err := phone.Pair(ctx, srv.URL, code, "手机"); err != nil {
		t.Fatal(err)
	}

	result, err := phone.Send(ctx, map[string]any{"text": "把下载目录按类型整理"})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if !result.OK || result.Reply != "电脑收到：把下载目录按类型整理" {
		t.Fatalf("result=%+v", result)
	}
}

func TestPhoneSendReportsDesktopOffline(t *testing.T) {
	hub, srv := startRelay(t)
	statePath := filepath.Join(t.TempDir(), "p.json")
	phone := pairPhone(t, hub, srv.URL, statePath)
	t.Cleanup(phone.Close)

	// 电脑未连线：中继会回 queued，手机必须拿到“已排队”而不是成功。
	_, err := phone.Send(context.Background(), map[string]any{"text": "整理桌面"})
	if !errors.Is(err, ErrDesktopOffline) {
		t.Fatalf("expected ErrDesktopOffline, got %v", err)
	}
	if stats := hub.Stats(); stats.Queued != 1 {
		t.Fatalf("command should be queued at relay: %+v", stats)
	}
}

func TestPhoneSendRequiresPairing(t *testing.T) {
	phone := &Phone{StatePath: filepath.Join(t.TempDir(), "none.json"), Logger: func(string, ...any) {}}
	if _, err := phone.Send(context.Background(), map[string]any{"text": "x"}); !errors.Is(err, ErrNotPaired) {
		t.Fatalf("expected ErrNotPaired, got %v", err)
	}
}

func TestPhoneUnpairClearsState(t *testing.T) {
	hub, srv := startRelay(t)
	statePath := filepath.Join(t.TempDir(), "p.json")
	phone := pairPhone(t, hub, srv.URL, statePath)
	t.Cleanup(phone.Close)
	if err := phone.Unpair(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("state file should be removed, err=%v", err)
	}
	if phone.Status()["paired"] != false {
		t.Fatal("status should report unpaired")
	}
}

func TestPhonePairRejectsBadCode(t *testing.T) {
	_, srv := startRelay(t)
	phone := &Phone{StatePath: filepath.Join(t.TempDir(), "p.json"), Logger: func(string, ...any) {}}
	if _, err := phone.Pair(context.Background(), srv.URL, "000000", "手机"); err == nil {
		t.Fatal("bad code should fail")
	}
	if _, err := phone.Pair(context.Background(), "ftp://x", "000000", "手机"); err == nil {
		t.Fatal("invalid relay url should fail")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

// TestPhoneRetryWithSameIDExecutesOnce 验证手机端超时重试复用幂等键时，
// 电脑端只会执行一次（否则“点了重试”就会把活干两遍）。
func TestPhoneRetryWithSameIDExecutesOnce(t *testing.T) {
	hub, srv := startRelay(t)
	desktopID, desktopToken, _ := hub.RegisterDesktop("工作机")
	code, _, _ := hub.CreatePairCode(desktopID, desktopToken)

	var mu sync.Mutex
	executions := 0
	desktop := &Client{
		BaseURL: srv.URL, DesktopID: desktopID, Token: desktopToken,
		Handle: func(_ context.Context, _ json.RawMessage) (ResultPayload, error) {
			mu.Lock()
			executions++
			mu.Unlock()
			time.Sleep(150 * time.Millisecond) // 模拟耗时任务
			return ResultPayload{OK: true, Reply: "已完成"}, nil
		},
		Logger: func(string, ...any) {},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = desktop.Run(ctx) }()
	waitFor(t, func() bool { return hub.Stats().Online[RoleDesktop] > 0 })

	phone := &Phone{StatePath: filepath.Join(t.TempDir(), "p.json"), SendTimeout: 2 * time.Second, Logger: func(string, ...any) {}}
	t.Cleanup(phone.Close)
	if _, err := phone.Pair(ctx, srv.URL, code, "手机"); err != nil {
		t.Fatal(err)
	}

	// 第一次：故意用极短超时，模拟界面超时。
	shortCtx, shortCancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer shortCancel()
	if _, err := phone.SendWithID(shortCtx, "retry-key-1", map[string]any{"text": "跑测试"}); err == nil {
		t.Fatal("expected timeout on first attempt")
	}

	// 第二次：同一幂等键重试，应拿到结果。
	result, err := phone.SendWithID(ctx, "retry-key-1", map[string]any{"text": "跑测试"})
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if !result.OK {
		t.Fatalf("result=%+v", result)
	}
	mu.Lock()
	defer mu.Unlock()
	if executions != 1 {
		t.Fatalf("desktop executed %d times, want 1", executions)
	}
}

// fakeDesktopAPI 模拟电脑端本地 HTTP API（/api/jobs*），用于验证 kind 路由。
func fakeDesktopAPI(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/jobs", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			AgentID        string `json:"agent_id"`
			Task           string `json:"task"`
			IdempotencyKey string `json:"idempotency_key"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		seen = append(seen, "submit:"+body.AgentID+":"+body.IdempotencyKey)
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{"task": map[string]any{"id": "job-abc12345", "status": "queued"}})
	})
	mux.HandleFunc("GET /api/jobs/", func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, "status:"+r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"task":   map[string]any{"id": "job-abc12345", "status": "running", "output": ""},
			"events": []map[string]any{{"type": "queued", "body": "任务已持久化"}, {"type": "running", "body": "后台开始执行"}},
		})
	})
	mux.HandleFunc("DELETE /api/jobs/", func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, "cancel:"+r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{"task": map[string]any{"id": "job-abc12345", "status": "cancelled"}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &seen
}

// TestExecuteLocalRoutesJobKinds 验证中继指令能正确打到电脑端的异步任务接口。
func TestExecuteLocalRoutesJobKinds(t *testing.T) {
	api, seen := fakeDesktopAPI(t)
	handle := ExecuteLocal(api.URL, nil)
	ctx := context.Background()

	submit, err := handle(ctx, json.RawMessage(`{"kind":"job","text":"跑完整测试","agent_id":"codex","idempotency_key":"k1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !submit.OK || submit.TaskID != "job-abc12345" || submit.Status != "queued" {
		t.Fatalf("submit=%+v", submit)
	}

	status, err := handle(ctx, json.RawMessage(`{"kind":"job_status","task_id":"job-abc12345"}`))
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != "running" || len(status.Events) != 2 {
		t.Fatalf("status=%+v", status)
	}

	cancel, err := handle(ctx, json.RawMessage(`{"kind":"job_cancel","task_id":"job-abc12345"}`))
	if err != nil {
		t.Fatal(err)
	}
	if cancel.Status != "cancelled" {
		t.Fatalf("cancel=%+v", cancel)
	}

	want := []string{"submit:codex:k1", "status:/api/jobs/job-abc12345", "cancel:/api/jobs/job-abc12345"}
	if len(*seen) != len(want) {
		t.Fatalf("calls=%v", *seen)
	}
	for i := range want {
		if (*seen)[i] != want[i] {
			t.Fatalf("calls=%v", *seen)
		}
	}
}

// TestExecuteLocalJobStatusSurfacesDesktopError 电脑返回错误时不能伪装成功。
func TestExecuteLocalJobStatusSurfacesDesktopError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/jobs/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "任务不存在"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	handle := ExecuteLocal(srv.URL, nil)
	res, err := handle(context.Background(), json.RawMessage(`{"kind":"job_status","task_id":"missing"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.OK || !strings.Contains(res.Error, "任务不存在") {
		t.Fatalf("res=%+v", res)
	}
}

// TestPhoneJobRoundTrip 覆盖手机 → 中继 → 电脑 → 结果 的完整长任务链路。
func TestPhoneJobRoundTrip(t *testing.T) {
	api, _ := fakeDesktopAPI(t)
	hub, srv := startRelay(t)
	desktopID, desktopToken, _ := hub.RegisterDesktop("工作机")
	code, _, _ := hub.CreatePairCode(desktopID, desktopToken)

	desktop := &Client{
		BaseURL: srv.URL, DesktopID: desktopID, Token: desktopToken,
		Handle: ExecuteLocal(api.URL, api.Client()),
		Logger: func(string, ...any) {},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = desktop.Run(ctx) }()
	waitFor(t, func() bool { return hub.Stats().Online[RoleDesktop] > 0 })

	phone := &Phone{StatePath: filepath.Join(t.TempDir(), "p.json"), Logger: func(string, ...any) {}}
	t.Cleanup(phone.Close)
	if _, err := phone.Pair(ctx, srv.URL, code, "手机"); err != nil {
		t.Fatal(err)
	}

	submit, err := phone.SubmitJob(ctx, JobRequest{Text: "跑完整测试", AgentID: "codex", ClientID: "k-job"})
	if err != nil {
		t.Fatalf("submit job: %v", err)
	}
	if submit.TaskID != "job-abc12345" {
		t.Fatalf("submit=%+v", submit)
	}

	status, err := phone.JobStatus(ctx, submit.TaskID)
	if err != nil {
		t.Fatalf("job status: %v", err)
	}
	if status.Status != "running" || len(status.Events) != 2 {
		t.Fatalf("status=%+v", status)
	}

	cancelled, err := phone.CancelJob(ctx, submit.TaskID)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if cancelled.Status != "cancelled" {
		t.Fatalf("cancel=%+v", cancelled)
	}
}
