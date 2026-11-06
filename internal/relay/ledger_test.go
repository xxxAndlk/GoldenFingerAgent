package relay

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// memoryLedger 是测试用的内存回执仓库，语义与落库实现一致。
type memoryLedger struct {
	mu      sync.Mutex
	entries map[string]memReceipt
	// beginErr / lookupErr 用于故障注入。
	beginErr  error
	lookupErr error
}

type memReceipt struct {
	status string
	result []byte
}

func newMemoryLedger() *memoryLedger {
	return &memoryLedger{entries: map[string]memReceipt{}}
}

func (m *memoryLedger) Lookup(_ context.Context, key string) (string, []byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lookupErr != nil {
		return "", nil, false, m.lookupErr
	}
	item, ok := m.entries[key]
	if !ok {
		return "", nil, false, nil
	}
	return item.status, item.result, true, nil
}

func (m *memoryLedger) Begin(_ context.Context, key string, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.beginErr != nil {
		return m.beginErr
	}
	if _, ok := m.entries[key]; ok {
		return nil
	}
	m.entries[key] = memReceipt{status: CommandReceiptRunning}
	return nil
}

func (m *memoryLedger) Complete(_ context.Context, key string, result []byte, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[key] = memReceipt{status: CommandReceiptDone, result: result}
	return nil
}

// startDesktopWithLedger 启动中继并让一台带持久化回执的电脑上线。
func startDesktopWithLedger(t *testing.T, ledger CommandLedger, handle Handler) *Phone {
	t.Helper()
	hub, srv := startRelay(t)
	desktopID, desktopToken, err := hub.RegisterDesktop("工作机")
	if err != nil {
		t.Fatal(err)
	}
	desktop := &Client{
		BaseURL: srv.URL, DesktopID: desktopID, Token: desktopToken,
		Handle: handle, Ledger: ledger, Logger: func(string, ...any) {},
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = desktop.Run(ctx) }()
	waitFor(t, func() bool { return hub.Stats().Online[RoleDesktop] > 0 })

	code, _, err := hub.CreatePairCode(desktopID, desktopToken)
	if err != nil {
		t.Fatal(err)
	}
	phone := &Phone{
		StatePath:   filepath.Join(t.TempDir(), "p.json"),
		SendTimeout: 5 * time.Second, Logger: func(string, ...any) {},
	}
	t.Cleanup(phone.Close)
	if _, err := phone.Pair(context.Background(), srv.URL, code, "我的手机"); err != nil {
		t.Fatalf("pair: %v", err)
	}
	return phone
}

func TestDesktopLedgerReplaysOriginalResult(t *testing.T) {
	var calls int
	var mu sync.Mutex
	ledger := newMemoryLedger()
	phone := startDesktopWithLedger(t, ledger, func(_ context.Context, payload json.RawMessage) (ResultPayload, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		var p struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(payload, &p)
		return ResultPayload{OK: true, Reply: "电脑收到：" + p.Text}, nil
	})

	payload := map[string]any{"text": "整理下载目录"}
	first, err := phone.SendWithID(context.Background(), "cmd-dup", payload)
	if err != nil {
		t.Fatalf("first send: %v", err)
	}
	if !first.OK || first.Reply != "电脑收到：整理下载目录" {
		t.Fatalf("first=%+v", first)
	}

	// 重投同一条指令（手机重试、中继重投都会走到这里）：
	// 电脑不能再执行一遍，手机要拿到第一次的结果。
	second, err := phone.SendWithID(context.Background(), "cmd-dup", payload)
	if err != nil {
		t.Fatalf("second send: %v", err)
	}
	if second.Reply != first.Reply {
		t.Fatalf("second=%+v first=%+v", second, first)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("指令执行了 %d 次，最多执行一次", calls)
	}
}

func TestDesktopLedgerInterruptedIsUnknown(t *testing.T) {
	var calls int
	var mu sync.Mutex
	ledger := newMemoryLedger()
	phone := startDesktopWithLedger(t, ledger, func(_ context.Context, payload json.RawMessage) (ResultPayload, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return ResultPayload{OK: true, Reply: "不该执行"}, nil
	})

	// 模拟上次执行到一半进程退出：只留下 running 记录。
	ctx := context.Background()
	if err := ledger.Begin(ctx, "cmd-interrupted", time.Now()); err != nil {
		t.Fatal(err)
	}

	result, err := phone.SendWithID(ctx, "cmd-interrupted", map[string]any{"text": "改任务状态"})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if result.OK {
		t.Fatalf("中断的指令不能报告成功: %+v", result)
	}
	if result.Command != CommandUnknown {
		t.Fatalf("command=%q，应如实标 unknown", result.Command)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 0 {
		t.Fatalf("中断的指令不能盲目重跑，实际执行了 %d 次", calls)
	}
}

func TestPhoneRetryUsesSameIdempotencyKeyWithNewEnvelope(t *testing.T) {
	// 中继按信封 ID 去重：重投沿用旧信封 ID 会被中继当重复丢掉，
	// 手机永远拿不到结果。这里确认重投能真正到达电脑，
	// 并且电脑按幂等键认出是同一条指令、不执行第二遍。
	var seenKeys []string
	var executions int
	var mu sync.Mutex
	phone := startDesktopWithLedger(t, nil, func(_ context.Context, payload json.RawMessage) (ResultPayload, error) {
		var p struct {
			IdempotencyKey string `json:"idempotency_key"`
		}
		_ = json.Unmarshal(payload, &p)
		mu.Lock()
		seenKeys = append(seenKeys, p.IdempotencyKey)
		executions++
		mu.Unlock()
		return ResultPayload{OK: true, Reply: "已执行"}, nil
	})

	payload := map[string]any{"text": "继续"}
	for i := 0; i < 2; i++ {
		result, err := phone.SendWithID(context.Background(), "retry-key", payload)
		if err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
		if !result.OK || result.Reply != "已执行" {
			t.Fatalf("send %d result=%+v", i, result)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seenKeys) != 1 || seenKeys[0] != "retry-key" {
		t.Fatalf("重投应复用同一个幂等键并只执行一次，实际 %v", seenKeys)
	}
	if executions != 1 {
		t.Fatalf("指令执行了 %d 次，最多执行一次", executions)
	}
}

func TestDesktopLedgerBeginFailureRefusesToExecute(t *testing.T) {
	// 执行前落 running 回执失败时，绝对不能继续执行业务：
	// 没有这条记录，电脑重启后无从判断指令是否已经执行过。
	var executions int
	var mu sync.Mutex
	ledger := newMemoryLedger()
	ledger.beginErr = errors.New("disk full")
	phone := startDesktopWithLedger(t, ledger, func(_ context.Context, _ json.RawMessage) (ResultPayload, error) {
		mu.Lock()
		executions++
		mu.Unlock()
		return ResultPayload{OK: true, Reply: "不该执行"}, nil
	})

	result, err := phone.SendWithID(context.Background(), "cmd-no-receipt", map[string]any{"text": "改任务状态"})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if result.OK {
		t.Fatalf("回执写不进去时不能报告成功: %+v", result)
	}
	if result.Command != CommandUnknown {
		t.Fatalf("command=%q，应如实标 unknown", result.Command)
	}
	mu.Lock()
	defer mu.Unlock()
	if executions != 0 {
		t.Fatalf("回执写不进去仍在执行，实际执行了 %d 次", executions)
	}
}

func TestDesktopLedgerLookupFailureIsRetried(t *testing.T) {
	// 回执读失败时只能回 unknown，但这个结果不能被缓存：
	// 否则一次短暂的数据库故障会把这条幂等键永久钉成「不确定」。
	var executions int
	var mu sync.Mutex
	ledger := newMemoryLedger()
	ledger.lookupErr = errors.New("database is locked")
	phone := startDesktopWithLedger(t, ledger, func(_ context.Context, _ json.RawMessage) (ResultPayload, error) {
		mu.Lock()
		executions++
		mu.Unlock()
		return ResultPayload{OK: true, Reply: "已执行"}, nil
	})

	first, err := phone.SendWithID(context.Background(), "cmd-flaky-read", map[string]any{"text": "继续"})
	if err != nil {
		t.Fatalf("first send: %v", err)
	}
	if first.OK || first.Command != CommandUnknown {
		t.Fatalf("回执读失败应返回 unknown: %+v", first)
	}

	ledger.mu.Lock()
	ledger.lookupErr = nil
	ledger.mu.Unlock()

	second, err := phone.SendWithID(context.Background(), "cmd-flaky-read", map[string]any{"text": "继续"})
	if err != nil {
		t.Fatalf("second send: %v", err)
	}
	if !second.OK || second.Reply != "已执行" {
		t.Fatalf("回执恢复后应真正执行: %+v", second)
	}
	mu.Lock()
	defer mu.Unlock()
	if executions != 1 {
		t.Fatalf("实际执行了 %d 次，应恰好一次", executions)
	}
}
