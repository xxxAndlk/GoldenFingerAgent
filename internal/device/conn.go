package device

import (
	"encoding/json"
	"sync"

	"github.com/gorilla/websocket"
)

// 服务端→设备的帧类型。
const (
	msgRegistered = "registered"
	msgPing       = "ping"
	msgCommand    = "command"
)

// conn 封装一个设备 WebSocket 连接：写串行 + 读写泵分离。
type conn struct {
	ws     *websocket.Conn
	device string

	send   chan []byte
	sendMu sync.Mutex

	// 等待 command 结果的 chan（按 cmd_id 注册）。
	waitMu sync.Mutex
	waits  map[string]chan *Result

		// onHeartbeat 在收到心跳帧时回调（Hub 刷新 lastSeen）。
		onHeartbeat func(deviceID string, ts int64)

	closeOnce sync.Once
	closed    chan struct{}
}

// newConn 创建连接并启动读写泵。
func newConn(ws *websocket.Conn) *conn {
	c := &conn{
		ws:     ws,
		send:   make(chan []byte, 16),
		waits:  make(map[string]chan *Result),
		closed: make(chan struct{}),
	}
	go c.writePump()
	go c.readPump()
	return c
}

// setHeartbeat 绑定心跳回调（Hub 注入，避免循环引用）。
func (c *conn) setHeartbeat(fn func(deviceID string, ts int64)) { c.onHeartbeat = fn }

// isClosed 报告连接是否已关闭。
func (c *conn) isClosed() bool {
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}

// deviceID 返回注册后的设备 id（未注册为空串）。
func (c *conn) deviceID() string { return c.device }

// close 关闭连接并结束读写泵（幂等）。
func (c *conn) close() {
	c.closeOnce.Do(func() {
		close(c.closed)
		_ = c.ws.Close()
	})
}

// write 以互斥方式写一帧（write pump 之外偶尔直接写）。
func (c *conn) write(msg any) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	return c.ws.WriteJSON(msg)
}

// writePump 串行消费 send 队列并落盘。
func (c *conn) writePump() {
	for {
		select {
		case <-c.closed:
			return
		case data := <-c.send:
			c.sendMu.Lock()
			_ = c.ws.WriteMessage(websocket.TextMessage, data)
			c.sendMu.Unlock()
		}
	}
}

// readPump 读取设备帧并按 type 分发。读取失败即关闭连接。
func (c *conn) readPump() {
	defer c.close()
	for {
		_, data, err := c.ws.ReadMessage()
		if err != nil {
			return
		}
		var f struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(data, &f) != nil {
			continue
		}
		switch f.Type {
			case "register":
				var m struct {
					DeviceID string `json:"device_id"`
				}
				if json.Unmarshal(data, &m) == nil && m.DeviceID != "" {
					c.device = m.DeviceID
					// registered 帧由 Hub 在注册入表后回发（避免竞态）。
				}
			case "heartbeat":
				// 心跳由 Hub 刷新 lastSeen，这里仅确认帧格式合法后转发。
				var hb struct {
					DeviceID string `json:"device_id"`
					TS       int64  `json:"ts"`
				}
				if json.Unmarshal(data, &hb) != nil || c.onHeartbeat == nil {
					continue
				}
				id := hb.DeviceID
				if id == "" {
					id = c.deviceID()
				}
				c.onHeartbeat(id, hb.TS)
		case "result":
			var r Result
			if json.Unmarshal(data, &r) != nil {
				continue
			}
			c.deliverResult(&r)
		}
	}
}

// deliverResult 把 result 投递给等待该 cmd_id 的 chan（非阻塞）。
func (c *conn) deliverResult(r *Result) {
	c.waitMu.Lock()
	ch, ok := c.waits[r.CmdID]
	if ok {
		delete(c.waits, r.CmdID)
	}
	c.waitMu.Unlock()
	if ok {
		ch <- r
	}
}

// waitResult 为 cmd_id 注册等待 chan；返回的 cancel 用于超时/失败时移除。
func (c *conn) waitResult(cmdID string) (chan *Result, func()) {
	ch := make(chan *Result, 1)
	c.waitMu.Lock()
	c.waits[cmdID] = ch
	c.waitMu.Unlock()
	return ch, func() {
		c.waitMu.Lock()
		delete(c.waits, cmdID)
		c.waitMu.Unlock()
	}
}

// lastSeen 由 Hub 负责维护；此处返回连接存活状态。
func (c *conn) alive() bool { return !c.isClosed() }
