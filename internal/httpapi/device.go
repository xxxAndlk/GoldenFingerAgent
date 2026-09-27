package httpapi

import (
	"encoding/json"
	"net/http"

	"goldenfinger/agent/internal/device"
)

// handleDeviceWS 升级设备 WebSocket 连接（GET /ws/device）。
// 未装配 Device Hub 时返回 503，避免路由悬挂。
func (s *Server) handleDeviceWS(w http.ResponseWriter, r *http.Request) {
	if s.Device == nil {
		writeError(w, 503, "device hub 未装配")
		return
	}
	s.Device.ServeWS(w, r)
}

// screenRequest 是 POST /api/screen 的入参（节点树 + 截图）。
type screenRequest struct {
	DeviceID      string            `json:"device_id"`
	Nodes         []device.ScreenNode `json:"nodes"`
	ScreenshotB64 string            `json:"screenshot_b64"`
}

// handleScreen 接收设备上报的屏幕帧并缓存为最近一帧。
func (s *Server) handleScreen(w http.ResponseWriter, r *http.Request) {
	if s.Device == nil {
		writeError(w, 503, "device hub 未装配")
		return
	}
	var req screenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "bad request body")
		return
	}
	s.Device.StoreScreen(&device.Screen{
		DeviceID:      req.DeviceID,
		Nodes:         req.Nodes,
		ScreenshotB64: req.ScreenshotB64,
		At:            s.Now(),
	})
	writeJSON(w, 200, map[string]any{"ok": true})
}
