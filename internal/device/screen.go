package device

import "time"

// ScreenNode 描述屏幕节点树中的一个可操作/可见元素。
type ScreenNode struct {
	Text               string `json:"text"`
	ContentDescription string `json:"content_description,omitempty"`
	ClassName          string `json:"class_name,omitempty"`
	Bounds             [4]int `json:"bounds"` // [left, top, right, bottom]
	Clickable          bool   `json:"clickable"`
	Scrollable         bool   `json:"scrollable"`
	Index              int    `json:"index"` // 节点树中的序号（tap 按此定位）
}

// Screen 是最近一帧屏幕快照（节点树 + 截图 + 上报时间）。
type Screen struct {
	DeviceID      string       `json:"device_id"`
	Nodes         []ScreenNode `json:"nodes"`
	ScreenshotB64 string       `json:"screenshot_b64,omitempty"`
	At            time.Time    `json:"at"`
}
