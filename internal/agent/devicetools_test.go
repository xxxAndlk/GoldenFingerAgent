package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"goldenfinger/agent/internal/device"
	"goldenfinger/agent/internal/llm/mock"
)

// fakeDevice 模拟 Android 设备：注册 WS、记录收到的 command、自动回 ok result。
type fakeDevice struct {
	conn *websocket.Conn
	cmds chan map[string]any // 收到的 command 帧（action/params）
}

func startFakeDevice(t *testing.T, hub *device.Hub) *fakeDevice {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(hub.ServeWS))
	t.Cleanup(srv.Close)
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/device", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	if err := ws.WriteJSON(map[string]any{"type": "register", "device_id": "d1"}); err != nil {
		t.Fatal(err)
	}
	var reg map[string]any
	if err := ws.ReadJSON(&reg); err != nil { // registered 帧
		t.Fatal(err)
	}
	fd := &fakeDevice{conn: ws, cmds: make(chan map[string]any, 16)}
	go func() {
		for {
			var msg map[string]any
			if err := ws.ReadJSON(&msg); err != nil {
				return
			}
			if msg["type"] == "command" {
				fd.cmds <- msg
				_ = ws.WriteJSON(map[string]any{
					"type": "result", "device_id": "d1", "cmd_id": msg["cmd_id"],
					"ok": true, "error": "", "data": map[string]any{},
				})
			}
		}
	}()
	return fd
}

func (fd *fakeDevice) nextCmd(t *testing.T) (action string, params map[string]any) {
	t.Helper()
	select {
	case m := <-fd.cmds:
		action, _ = m["action"].(string)
		params, _ = m["params"].(map[string]any)
		return
	case <-time.After(3 * time.Second):
		t.Fatal("no command received")
		return
	}
}

// deviceToolCtx 构造带 Device hub 与 fake 设备的 ToolContext。
func deviceToolCtx(t *testing.T, sess *Session, rt *Runtime) (*fakeDevice, *ToolContext) {
	t.Helper()
	hub := device.NewHub()
	t.Cleanup(hub.Close)
	fd := startFakeDevice(t, hub)
	rt.Tools.Device = hub
	return fd, &ToolContext{Session: sess, Runtime: rt}
}

// TestDeviceToolActionMapping 验证 7 个设备工具经 fake 设备执行时
// action/params 正确映射到协议固定取值。
func TestDeviceToolActionMapping(t *testing.T) {
	sess := &Session{ID: "s1", UserID: "u1"}
	rt := &Runtime{Tools: &ToolServices{}}
	fd, tc := deviceToolCtx(t, sess, rt)

	// tap：node_index → click
		if _, err := (tapTool{}).Execute(context.Background(), json.RawMessage(`{"node_index":3}`), tc); err != nil {
		t.Fatal(err)
	}
	if a, p := fd.nextCmd(t); a != "click" || int(p["node_index"].(float64)) != 3 {
		t.Fatalf("tap node_index → %s %v", a, p)
	}

	// tap：坐标 → gestureTap
		if _, err := (tapTool{}).Execute(context.Background(), json.RawMessage(`{"x":10,"y":20}`), tc); err != nil {
		t.Fatal(err)
	}
	if a, p := fd.nextCmd(t); a != "gestureTap" || p["x"].(float64) != 10 || p["y"].(float64) != 20 {
		t.Fatalf("tap xy → %s %v", a, p)
	}

	// tap：长按 → longClick
		if _, err := (tapTool{}).Execute(context.Background(), json.RawMessage(`{"node_index":1,"long_press":true}`), tc); err != nil {
		t.Fatal(err)
	}
	if a, _ := fd.nextCmd(t); a != "longClick" {
		t.Fatalf("long press → %s", a)
	}

	// input_text → setText
		if _, err := (inputTextTool{}).Execute(context.Background(), json.RawMessage(`{"text":"你好"}`), tc); err != nil {
		t.Fatal(err)
	}
	if a, p := fd.nextCmd(t); a != "setText" || p["text"] != "你好" {
		t.Fatalf("input_text → %s %v", a, p)
	}

	// swipe：direction=up → scrollBackward
		if _, err := (swipeTool{}).Execute(context.Background(), json.RawMessage(`{"direction":"up"}`), tc); err != nil {
		t.Fatal(err)
	}
	if a, p := fd.nextCmd(t); a != "scrollBackward" || p["direction"] != "up" {
		t.Fatalf("swipe up → %s %v", a, p)
	}

	// swipe：坐标 → gestureSwipe
		if _, err := (swipeTool{}).Execute(context.Background(), json.RawMessage(`{"x1":0,"y1":100,"x2":0,"y2":400}`), tc); err != nil {
		t.Fatal(err)
	}
	if a, p := fd.nextCmd(t); a != "gestureSwipe" || p["y2"].(float64) != 400 {
		t.Fatalf("swipe xy → %s %v", a, p)
	}

	// back → globalBack；home=true → globalHome
		if _, err := (backTool{}).Execute(context.Background(), json.RawMessage(`{}`), tc); err != nil {
		t.Fatal(err)
	}
	if a, _ := fd.nextCmd(t); a != "globalBack" {
		t.Fatalf("back → %s", a)
	}
		if _, err := (backTool{}).Execute(context.Background(), json.RawMessage(`{"home":true}`), tc); err != nil {
		t.Fatal(err)
	}
	if a, _ := fd.nextCmd(t); a != "globalHome" {
		t.Fatalf("home → %s", a)
	}

	// open_app：中文名映射包名
		if _, err := (openAppTool{}).Execute(context.Background(), json.RawMessage(`{"app_name":"微信"}`), tc); err != nil {
		t.Fatal(err)
	}
	if a, p := fd.nextCmd(t); a != "openApp" || p["package"] != "com.tencent.mm" {
		t.Fatalf("open_app 微信 → %s %v", a, p)
	}

	// open_app：未知应用名不猜包名，返回提示
	res, err := openAppTool{}.Execute(context.Background(), json.RawMessage(`{"app_name":"奇异应用"}`), tc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusError || !strings.Contains(res.Data.(map[string]string)["error"], "不确定") {
		t.Fatalf("unknown app → %+v", res)
	}

	// wait：默认 1s（走设备动作计数）
		if _, err := (waitTool{}).Execute(context.Background(), json.RawMessage(`{}`), tc); err != nil {
		t.Fatal(err)
	}
	if sess.DeviceSteps == 0 {
		t.Error("wait did not count a device step")
	}
}

// TestDeviceToolScreenObserve 验证 screen_observe 读出结构化文本。
func TestDeviceToolScreenObserve(t *testing.T) {
	sess := &Session{ID: "s1", UserID: "u1"}
	rt := &Runtime{Tools: &ToolServices{}}
	hub := device.NewHub()
	t.Cleanup(hub.Close)
	rt.Tools.Device = hub
	hub.StoreScreen(&device.Screen{
		DeviceID: "d1",
		Nodes:    []device.ScreenNode{{Text: "微信", Index: 0, Clickable: true, Bounds: [4]int{0, 0, 100, 200}}},
		At:       time.Now(),
	})
	tc := &ToolContext{Session: sess, Runtime: rt}
	res, err := screenObserveTool{}.Execute(context.Background(), json.RawMessage(`{}`), tc)
	if err != nil {
		t.Fatal(err)
	}
	text := res.Data.(map[string]string)["text"]
	if !strings.Contains(text, "[0] 微信") || !strings.Contains(text, "clickable=true") {
		t.Fatalf("observe text = %q", text)
	}
}

// TestDeviceToolUnwired 验证 Device==nil 时工具返回"设备未连接"。
func TestDeviceToolUnwired(t *testing.T) {
	tc := &ToolContext{Session: &Session{ID: "s1"}, Runtime: &Runtime{Tools: &ToolServices{}}}
	res, err := screenObserveTool{}.Execute(context.Background(), json.RawMessage(`{}`), tc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusError || !strings.Contains(res.Data.(map[string]string)["error"], "设备未连接") {
		t.Fatalf("unwired screen_observe → %+v", res)
	}
	// 设备工具同样返回未连接，且不 panic。
	res, err = tapTool{}.Execute(context.Background(), json.RawMessage(`{"node_index":0}`), tc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusError || !strings.Contains(res.Data.(map[string]string)["error"], "设备未连接") {
		t.Fatalf("unwired tap → %+v", res)
	}
}

// TestDeviceStepLimit 验证单任务设备动作 ≤15 步、超限给"我没能完成"。
func TestDeviceStepLimit(t *testing.T) {
	sess := &Session{ID: "s1", UserID: "u1"}
	tc := &ToolContext{Session: sess}
	sess.DeviceSteps = 14
	if msg, ok := deviceStep(tc); !ok || msg != "" {
		t.Fatalf("step 15 should pass, got %q ok=%v", msg, ok)
	}
	sess.DeviceSteps = 15
	msg, ok := deviceStep(tc)
	if ok {
		t.Fatal("step 16 should be blocked")
	}
	if !strings.Contains(msg, "我没能完成") {
		t.Fatalf("limit message = %q", msg)
	}
}

// TestDeviceLoopMultiTurn 验证操控循环多轮调用设备工具并正常收尾。
func TestDeviceLoopMultiTurn(t *testing.T) {
	sess, rt, _ := testHarness(t,
		mock.ToolResponse("t1", "tap", `{"node_index":0}`),
		mock.ToolResponse("t2", "tap", `{"node_index":1}`),
		mock.TextResponse("点完了"),
	)
	hub := device.NewHub()
	t.Cleanup(hub.Close)
	startFakeDevice(t, hub)
	rt.Tools.Device = hub
	res, err := Run(context.Background(), sess, "帮我点两个按钮", rt)
	if err != nil {
		t.Fatal(err)
	}
	if res.Reply != "点完了" {
		t.Fatalf("reply = %q", res.Reply)
	}
	if sess.DeviceSteps != 2 {
		t.Fatalf("device steps = %d, want 2", sess.DeviceSteps)
	}
}
