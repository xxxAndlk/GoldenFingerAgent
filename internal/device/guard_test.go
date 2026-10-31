package device

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// guardHub 启动一个真实 WS 设备端点，并在设备侧对 command 自动回 result。
// approve/deny 决定 confirm 帧的答复；seen 收集收到的动作名。
func guardHub(t *testing.T, approve bool, seen *[]string) *Hub {
	t.Helper()
	hub := NewHub()
	t.Cleanup(hub.Close)
	srv := httptest.NewServer(http.HandlerFunc(hub.ServeWS))
	t.Cleanup(srv.Close)
	ws := dialWS(t, "ws"+strings.TrimPrefix(srv.URL, "http"))
	if err := ws.WriteJSON(map[string]any{"type": "register", "device_id": "phone-1"}); err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			var frame struct {
				Type   string          `json:"type"`
				CmdID  string          `json:"cmd_id"`
				Action string          `json:"action"`
				Params json.RawMessage `json:"params"`
			}
			if err := ws.ReadJSON(&frame); err != nil {
				return
			}
			if frame.Type != "command" {
				continue
			}
			*seen = append(*seen, frame.Action)
			// 安卓侧把 data 作为 JSON 字符串放入 result 帧（与 Executor 一致）。
			data, _ := json.Marshal(map[string]any{"approved": approve})
			_ = ws.WriteJSON(map[string]any{
				"type": "result", "device_id": "phone-1", "cmd_id": frame.CmdID, "ok": true, "data": string(data),
			})
		}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for !hub.Connected("phone-1") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !hub.Connected("phone-1") {
		t.Fatal("fake device did not register")
	}
	return hub
}

func TestGuardAllowsReadOnlyAndPlainActions(t *testing.T) {
	seen := []string{}
	hub := guardHub(t, true, &seen)
	guard := NewGuard(GuardConfig{})
	hub.StoreScreen(&Screen{DeviceID: "phone-1", Package: "com.android.settings", Nodes: []ScreenNode{{Text: "无线局域网"}}})

	for _, action := range []string{"screen_observe", "screenshot", "wait", "tap"} {
		if err := guard.Check(context.Background(), hub, action, map[string]any{"node_index": 1}); err != nil {
			t.Fatalf("%s should pass: %v", action, err)
		}
	}
	if len(seen) != 0 {
		t.Fatalf("no confirm expected, got %v", seen)
	}
}

func TestGuardRequiresConfirmOnSensitiveScreenText(t *testing.T) {
	seen := []string{}
	hub := guardHub(t, false, &seen)
	guard := NewGuard(GuardConfig{})
	hub.StoreScreen(&Screen{DeviceID: "phone-1", Nodes: []ScreenNode{{Text: "确认转账 500 元"}, {Text: "立即转账"}}})

	err := guard.Check(context.Background(), hub, "tap", map[string]any{"node_index": 2})
	if !errors.Is(err, ErrDeclined) {
		t.Fatalf("expected declined, got %v", err)
	}
	if len(seen) != 1 || seen[0] != "confirm" {
		t.Fatalf("device should be asked to confirm, got %v", seen)
	}
	if msg := HumanMessage(err); !strings.Contains(msg, "自己确认") {
		t.Fatalf("human message=%q", msg)
	}
}

func TestGuardApprovedConfirmAllowsAction(t *testing.T) {
	seen := []string{}
	hub := guardHub(t, true, &seen)
	guard := NewGuard(GuardConfig{})
	hub.StoreScreen(&Screen{DeviceID: "phone-1", Nodes: []ScreenNode{{Text: "删除联系人"}}})

	if err := guard.Check(context.Background(), hub, "tap", map[string]any{}); err != nil {
		t.Fatalf("approved confirm should allow: %v", err)
	}
	if len(seen) != 1 || seen[0] != "confirm" {
		t.Fatalf("seen=%v", seen)
	}
}

func TestGuardBlocksSensitivePackageEvenWithoutKeywords(t *testing.T) {
	seen := []string{}
	hub := guardHub(t, false, &seen)
	guard := NewGuard(GuardConfig{})
	hub.StoreScreen(&Screen{DeviceID: "phone-1", Package: "com.eg.android.AlipayGphone", Nodes: []ScreenNode{{Text: "收付款"}}})

	if err := guard.Check(context.Background(), hub, "input_text", map[string]any{"text": "100"}); !errors.Is(err, ErrDeclined) {
		t.Fatalf("bank package must require confirm, got %v", err)
	}
}

func TestGuardChecksInputTextParams(t *testing.T) {
	seen := []string{}
	hub := guardHub(t, false, &seen)
	guard := NewGuard(GuardConfig{})
	hub.StoreScreen(&Screen{DeviceID: "phone-1", Nodes: []ScreenNode{{Text: "聊天"}}})

	if err := guard.Check(context.Background(), hub, "input_text", map[string]any{"text": "帮我把这笔钱转账给他"}); !errors.Is(err, ErrDeclined) {
		t.Fatalf("sensitive input text must require confirm, got %v", err)
	}
}

func TestGuardEmergencyBlocksEverything(t *testing.T) {
	seen := []string{}
	hub := guardHub(t, true, &seen)
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	guard := NewGuard(GuardConfig{Now: func() time.Time { return now }})
	guard.MarkEmergency()

	if err := guard.Check(context.Background(), hub, "screen_observe", nil); !errors.Is(err, ErrEmergency) {
		t.Fatalf("expected emergency block, got %v", err)
	}
	if len(seen) != 0 {
		t.Fatalf("nothing should be sent during emergency: %v", seen)
	}
	// 急停窗口结束后恢复。
	now = now.Add(11 * time.Second)
	if err := guard.Check(context.Background(), hub, "screen_observe", nil); err != nil {
		t.Fatalf("should recover after window: %v", err)
	}
}

func TestGuardDeniesWhenConfirmUnavailable(t *testing.T) {
	guard := NewGuard(GuardConfig{})
	screen := &Screen{DeviceID: "phone-1", Nodes: []ScreenNode{{Text: "确认支付"}}}
	// 没有设备连接 → 无法确认 → 必须拒绝而不是放行。
	hub := NewHub()
	defer hub.Close()
	hub.StoreScreen(screen)
	err := guard.Check(context.Background(), hub, "tap", map[string]any{})
	if !errors.Is(err, ErrConfirmUnavailable) {
		t.Fatalf("expected confirm-unavailable, got %v", err)
	}
	if msg := HumanMessage(err); !strings.Contains(msg, "没有执行") {
		t.Fatalf("human message=%q", msg)
	}
}

func TestGuardVerdictIsPure(t *testing.T) {
	guard := NewGuard(GuardConfig{})
	if need, _ := guard.Verdict(nil, "tap", map[string]any{}); need {
		t.Fatal("empty screen should not require confirm")
	}
	need, reason := guard.Verdict(&Screen{Nodes: []ScreenNode{{Text: "确认下单"}}}, "tap", nil)
	if !need || !strings.Contains(reason, "下单") {
		t.Fatalf("need=%v reason=%q", need, reason)
	}
	if need, _ := guard.Verdict(&Screen{Nodes: []ScreenNode{{Text: "确认下单"}}}, "screen_observe", nil); need {
		t.Fatal("read-only action must not require confirm")
	}
}
