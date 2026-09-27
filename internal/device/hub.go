package device

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// Result 是设备对一条 command 的回报（协议 result 帧的 data 部分）。
type Result struct {
	DeviceID string          `json:"device_id"`
	CmdID    string          `json:"cmd_id"`
	OK       bool            `json:"ok"`
	Error    string          `json:"error"`
	Data     json.RawMessage `json:"data,omitempty"`
}

// 常见错误。
var (
	ErrNotConnected = errors.New("device: 设备未连接")
	ErrTimeout      = errors.New("device: 等待设备结果超时")
)

// DefaultSendTimeout 是 Send 未携带 deadline 时的等待上限。
const DefaultSendTimeout = 30 * time.Second

// idleTimeout 是无心跳连接的清理阈值。
const idleTimeout = 90 * time.Second

var upgrader = websocket.Upgrader{
	ReadBufferSize: 4096, WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true // 非浏览器客户端（测试/服务端拨号）不校验
		}
		u, err := url.Parse(origin)
		if err != nil {
			return false
		}
		// 仅放行本机来源（Android WebView/测试/本地页面）。
		switch strings.ToLower(u.Hostname()) {
		case "127.0.0.1", "localhost", "::1":
			return true
		}
		return false
	},
}

// Hub 管理全部设备连接（按 device id），缓存最近一帧屏幕。
type Hub struct {
	mu      sync.RWMutex
	conns   map[string]*conn
	lastSeen map[string]time.Time
	screen  *Screen
	closed  chan struct{}
	once    sync.Once
}

// NewHub 创建 Hub 并启动后台清理 goroutine。
func NewHub() *Hub {
	h := &Hub{
		conns:    make(map[string]*conn),
		lastSeen: make(map[string]time.Time),
		closed:   make(chan struct{}),
	}
	go h.reaper()
	return h
}

// Close 停止清理 goroutine 并关闭全部连接。
func (h *Hub) Close() {
	h.once.Do(func() {
		close(h.closed)
		h.mu.Lock()
		defer h.mu.Unlock()
		for _, c := range h.conns {
			c.close()
		}
		h.conns = nil
	})
}

// ServeWS 升级 HTTP 请求为设备 WebSocket 并注册到 Hub。
// 设备随后通过 register 帧声明自己的 device_id。
func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[device] ws upgrade: %v", err)
		return
	}
	c := newConn(ws)
	c.setHeartbeat(h.Heartbeat)
	go func() {
		<-c.closed
		h.unregister(c)
	}()
	// 等待设备注册（最多 10s），注册成功后绑定到 Hub。
	deadline := time.Now().Add(10 * time.Second)
	for c.deviceID() == "" && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if c.deviceID() == "" {
		c.close()
		return
	}
	h.register(c)
}

// register 把连接绑定到 device id；同 id 旧连接被替换关闭。
func (h *Hub) register(c *conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if old, ok := h.conns[c.deviceID()]; ok && old != c {
		old.close()
	}
	h.conns[c.deviceID()] = c
	h.lastSeen[c.deviceID()] = time.Now()
	c.write(map[string]any{"type": msgRegistered, "device_id": c.deviceID()})
	log.Printf("[device] registered device=%s", c.deviceID())
}

// unregister 移除连接（仅当仍是同一连接时）。
func (h *Hub) unregister(c *conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if cur, ok := h.conns[c.deviceID()]; ok && cur == c {
		delete(h.conns, c.deviceID())
		delete(h.lastSeen, c.deviceID())
		log.Printf("[device] unregistered device=%s", c.deviceID())
	}
}

// Heartbeat 刷新设备心跳时间（设备离线判定依据）。
func (h *Hub) Heartbeat(deviceID string, ts int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.conns[deviceID]; ok {
		h.lastSeen[deviceID] = time.Now()
	}
}

// Send 向已连接设备下发一条 command 并等待 result。
// 单设备场景：目标为最近注册的连接。params 会被编码进协议
// command 帧（action/params 逐字遵循契约）。
func (h *Hub) Send(ctx context.Context, action string, params map[string]any) (*Result, error) {
	timeout := DefaultSendTimeout
	if dl, ok := ctx.Deadline(); ok {
		timeout = time.Until(dl)
		if timeout <= 0 {
			return nil, ErrTimeout
		}
	}

	h.mu.RLock()
	var c *conn
	for _, cand := range h.conns { // 单设备家庭场景：取任一在线连接
		if cand != nil && !cand.isClosed() {
			c = cand
			break
		}
	}
	h.mu.RUnlock()
	if c == nil {
		return nil, ErrNotConnected
	}

	cmdID := uuid.NewString()
	frame := map[string]any{
		"type": msgCommand, "cmd_id": cmdID,
		"action": action, "params": params,
	}
	raw, err := json.Marshal(frame)
	if err != nil {
		return nil, err
	}

	ch, cancel := c.waitResult(cmdID)
	defer cancel()
	if err := c.writeMessage(raw); err != nil {
		return nil, err
	}

	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.closed:
		return nil, ErrNotConnected
	case <-t.C:
		return nil, ErrTimeout
	case res := <-ch:
		return res, nil
	}
}

// writeMessage 通过连接的写泵下发一帧。
func (c *conn) writeMessage(data []byte) error {
	select {
	case <-c.closed:
		return ErrNotConnected
	case c.send <- data:
		return nil
	}
}

// Connected 报告设备是否在线。
func (h *Hub) Connected(deviceID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	c, ok := h.conns[deviceID]
	return ok && c != nil && !c.isClosed()
}

// StoreScreen 缓存最近一帧屏幕（POST /api/screen 写入）。
func (h *Hub) StoreScreen(s *Screen) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.screen = s
}

// LatestScreen 返回最近一帧屏幕（无帧时返回 nil）。
func (h *Hub) LatestScreen() *Screen {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.screen
}

// reaper 周期性清理超时未心跳的连接。
func (h *Hub) reaper() {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-h.closed:
			return
		case <-t.C:
			h.mu.Lock()
			now := time.Now()
			for id, c := range h.conns {
				seen, ok := h.lastSeen[id]
				if !ok || now.Sub(seen) > idleTimeout || c.isClosed() {
					c.close()
					delete(h.conns, id)
					delete(h.lastSeen, id)
					log.Printf("[device] reaped idle device=%s", id)
				}
			}
			h.mu.Unlock()
		}
	}
}
