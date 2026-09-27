package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"goldenfinger/agent/internal/device"
)

// newDeviceTestServer 装配带 Device Hub 的最小 Server。
func newDeviceTestServer(t *testing.T) (*device.Hub, *httptest.Server) {
	t.Helper()
	hub := device.NewHub()
	t.Cleanup(hub.Close)
	srv := &Server{Device: hub, Now: time.Now}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return hub, ts
}

// TestScreenEndpointCachesFrame 验证 POST /api/screen 写入最近一帧缓存。
func TestScreenEndpointCachesFrame(t *testing.T) {
	hub, ts := newDeviceTestServer(t)
	body := `{"device_id":"d1","nodes":[{"text":"设置","index":1,"clickable":true,"bounds":[0,0,100,200]}],"screenshot_b64":"aW1n"}`
	resp, err := http.Post(ts.URL+"/api/screen", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	s := hub.LatestScreen()
	if s == nil || s.DeviceID != "d1" || len(s.Nodes) != 1 ||
		s.Nodes[0].Text != "设置" || s.ScreenshotB64 != "aW1n" {
		t.Fatalf("cached screen = %+v", s)
	}
}

// TestDeviceWSRoute 验证 GET /ws/device 路由：设备注册往返。
func TestDeviceWSRoute(t *testing.T) {
	hub, ts := newDeviceTestServer(t)
	_ = hub
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/device"
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
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
}
