// Package relay 实现「手机 → 电脑」的单向指令中继：
//
//	手机生活管家 --WS--> 中继(公网,无状态优先) --WS--> 电脑工作助手
//
// 中继只做三件事：配对、鉴权、转发。它不理解业务内容，不执行任何任务，
// 也不持久化到磁盘（默认内存队列 + TTL），因此可以跑在 2C2G 的小机器上。
//
// 设计约束（勿删）：
//   - 通道身份由中继盖章（channel/from/to），客户端自填一律被覆盖，防止冒用。
//   - 每条消息必须有客户端生成的 id，中继与电脑端都要按 id 去重，
//     从而在“至少一次”投递下仍保证“最多执行一次”。
//   - 电脑端主动推送到手机（desktop → phone 发起）暂不实现；
//     当前 phone → desktop 的「结果回传」属于同一次请求的响应，不算主动推送。
package relay

import (
	"encoding/json"
	"errors"
	"time"
)

// 协议版本。字段变更必须升版本，不能悄悄改语义。
// v2 新增 ArtifactRef；双方都声明支持时才下发，v1 端继续收到原报文。
const (
	ProtocolVersion   = 1
	ProtocolVersionV2 = 2
)

// 消息类型。
const (
	// KindHello 由中继在连接建立后下发，说明本端身份与通道。
	KindHello = "hello"
	// KindCommand 手机发起的指令。
	KindCommand = "command"
	// KindResult 电脑端对某条 command 的执行结果（reply_to = command id）。
	KindResult = "result"
	// KindAck 中继对收到消息的确认（accepted / duplicate）。
	KindAck = "ack"
	// KindError 任一端处理失败的显式错误（不静默吞掉）。
	KindError = "error"
	// KindPing/KindPong 应用层心跳；WS 控制帧 ping 也在用。
	KindPing = "ping"
	KindPong = "pong"
)

// Envelope 是中继转发的唯一报文格式。
type Envelope struct {
	V       int             `json:"v"`
	ID      string          `json:"id"`
	Kind    string          `json:"kind"`
	Channel string          `json:"channel,omitempty"`
	From    string          `json:"from,omitempty"`
	To      string          `json:"to,omitempty"`
	TS      int64           `json:"ts"`
	ReplyTo string          `json:"reply_to,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// AckPayload 是中继回给发送方的确认内容。
type AckPayload struct {
	Status string `json:"status"` // accepted | duplicate
	Queued bool   `json:"queued,omitempty"`
}

// HelloPayload 在中继与端之间对齐身份。
type HelloPayload struct {
	Role      string `json:"role"` // phone | desktop
	Channel   string `json:"channel,omitempty"`
	DesktopID string `json:"desktop_id,omitempty"`
	Desktop   string `json:"desktop_name,omitempty"`
	// Version 是协商后的协议版本（1 或 2）。
	Version int `json:"version,omitempty"`
}

// ArtifactRef 是异步任务／对话结果中的交付物引用。
// 中继只传元数据，不传文件正文，也不传公开长期 URL。
type ArtifactRef struct {
	ArtifactID string `json:"artifact_id"`
	Version    int    `json:"version"`
	DeliveryID string `json:"delivery_id,omitempty"`
	Name       string `json:"name"`
	MIME       string `json:"mime_type"`
	Size       int64  `json:"size_bytes"`
	SHA256     string `json:"sha256"`
}

// ResultPayload 是电脑端回传的执行结果。Reply 面向用户，Error 面向排障。
type ResultPayload struct {
	OK      bool   `json:"ok"`
	Reply   string `json:"reply,omitempty"`
	Error   string `json:"error,omitempty"`
	Elapsed int64  `json:"elapsed_ms,omitempty"`
	// TaskID 是电脑端异步任务 id（kind=job 时有值），手机可据此继续查询进度。
	TaskID string `json:"task_id,omitempty"`
	// Status 是异步任务状态（queued/running/completed/failed/timeout/cancelled）。
	Status string `json:"status,omitempty"`
	// Events 是任务状态事件文本，用于在手机上显示进度。
	Events []string `json:"events,omitempty"`
	// Artifacts 是可选交付物引用；仅协议 v2 端会收到。
	Artifacts []ArtifactRef `json:"artifacts,omitempty"`
}

// 中继错误。
var (
	ErrUnauthorized  = errors.New("relay: unauthorized")
	ErrNotFound      = errors.New("relay: not found")
	ErrExpired       = errors.New("relay: expired")
	ErrTooMany       = errors.New("relay: too many requests")
	ErrPayloadTooBig = errors.New("relay: payload too large")
	ErrQueueFull     = errors.New("relay: target queue full")
)

// NewEnvelope 生成带版本与时间戳的空报文。
func NewEnvelope(kind string) *Envelope {
	return &Envelope{V: ProtocolVersion, Kind: kind, TS: time.Now().UnixMilli()}
}
