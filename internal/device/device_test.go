package device

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// dialWS 拨号到 WS 服务并注册清理。
func dialWS(t *testing.T, url string) *websocket.Conn {
	t.Helper()
	ws, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	return ws
}

// TestHubWSFullRoundTrip 覆盖协议全链路：register/heartbeat →
// 服务端下发 command → 设备回 result 投递到等待的 Send。
func TestHubWSFullRoundTrip(t *testing.T) {
	hub := NewHub()
	defer hub.Close()
	srv := httptest.NewServer(http.HandlerFunc(hub.ServeWS))
	defer srv.Close()
	ws := dialWS(t, "ws"+strings.TrimPrefix(srv.URL, "http"))

	// 注册：设备→服务端 register，服务端回 registered。
	if err := ws.WriteJSON(map[string]any{"type": "register", "device_id": "d1"}); err != nil {
		t.Fatal(err)
	}
	var reg struct {
		Type     string `json:"type"`
		DeviceID string `json:"device_id"`
	}
	if err := ws.ReadJSON(&reg); err != nil {
		t.Fatal(err)
	}
	if reg.Type != "registered" || reg.DeviceID != "d1" {
		t.Fatalf("register reply = %+v", reg)
	}
	if !hub.Connected("d1") {
		t.Fatal("hub not connected after register")
	}

	// 心跳：设备→服务端 heartbeat（含 ts）。
	if err := ws.WriteJSON(map[string]any{"type": "heartbeat", "device_id": "d1", "ts": int64(1700000000000)}); err != nil {
		t.Fatal(err)
	}

	// 服务端下发 command → 设备回 result。
	var got *Result
	done := make(chan struct{})
	go func() {
		defer close(done)
		got, _ = hub.Send(context.Background(), "tap", map[string]any{"node_index": 3})
	}()
	var cmd struct {
		Type   string         `json:"type"`
		CmdID  string         `json:"cmd_id"`
		Action string         `json:"action"`
		Params map[string]any `json:"params"`
	}
	if err := ws.ReadJSON(&cmd); err != nil {
		t.Fatal(err)
	}
	if cmd.Type != "command" || cmd.Action != "tap" {
		t.Fatalf("command frame = %+v", cmd)
	}
	if cmd.Params["node_index"] != float64(3) {
		t.Fatalf("params = %v", cmd.Params)
	}
	if err := ws.WriteJSON(map[string]any{
		"type": "result", "device_id": "d1", "cmd_id": cmd.CmdID,
		"ok": true, "error": "", "data": map[string]any{"done": true},
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Send did not return")
	}
	if got == nil || !got.OK {
		t.Fatalf("result = %+v", got)
	}
	var data map[string]bool
	if err := json.Unmarshal(got.Data, &data); err != nil || !data["done"] {
		t.Fatalf("result data = %s", got.Data)
	}
}

// TestStoreAndLatestScreen 验证最近一帧屏幕缓存。
func TestStoreAndLatestScreen(t *testing.T) {
	hub := NewHub()
	defer hub.Close()
	s := &Screen{
		DeviceID: "d1",
		Nodes:    []ScreenNode{{Text: "微信", Index: 0, Clickable: true}},
		ScreenshotB64: "aW1n",
		At:       time.Now(),
	}
	hub.StoreScreen(s)
	got := hub.LatestScreen()
	if got == nil || got.DeviceID != "d1" || len(got.Nodes) != 1 ||
		got.Nodes[0].Text != "微信" || got.ScreenshotB64 != "aW1n" {
		t.Fatalf("latest screen = %+v", got)
	}
}
