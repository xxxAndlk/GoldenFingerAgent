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
	// Command 是这次指令的分类结果，见 CommandExecuted 等常量。
	// 手机端据此区分「真的执行了」「排队中」「版本冲突」「被拒绝」「结果未知」。
	Command string `json:"command,omitempty"`
	// SchemaVersion 是响应结构的版本号，手机按此解析 Data。
	SchemaVersion int `json:"schema_version,omitempty"`
	// Data 承载结构化业务结果（任务、会话、简报、审批、交付物等）。
	Data json.RawMessage `json:"data,omitempty"`
	// WorkTaskID 在结果对应长期任务时给出；JobID 在结果对应执行段时给出。
	// AgentJob 仍是执行单元，不要把所有 Job 强行改名为 WorkTask。
	WorkTaskID string `json:"work_task_id,omitempty"`
	JobID      string `json:"job_id,omitempty"`
}

// 指令结果分类。手机端按这个分类决定提示语与是否允许自动重试。
const (
	// CommandExecuted 表示电脑真的执行了并返回了真实结果。
	CommandExecuted = "executed"
	// CommandQueued 表示电脑当前离线，指令已进中继队列，等待电脑上线后执行。
	CommandQueued = "queued"
	// CommandConflict 表示版本冲突（例如 expected_version 过期），需要重新读取后再操作。
	CommandConflict = "conflict"
	// CommandRejected 表示电脑明确拒绝了这次操作（权限、状态不允许等）。
	CommandRejected = "rejected"
	// CommandUnknown 表示结果未知：不要自动重试，先向用户核对。
	CommandUnknown = "unknown"
)

// CommandSchemaVersion 是本轮扩展指令的响应结构版本。
const CommandSchemaVersion = 1

// 电脑端支持的白名单指令。手机只能调用这些业务命令，
// 中继不做任意 HTTP 代理。
const (
	CmdWorkTaskCreate = "work_task_create"
	CmdWorkTaskList   = "work_task_list"
	CmdWorkTaskGet    = "work_task_get"
	CmdWorkTaskMsg    = "work_task_message"
	CmdWorkTaskAction = "work_task_action"

	CmdConversationList     = "conversation_list"
	CmdConversationMessages = "conversation_messages"
	CmdConversationRead     = "conversation_read"

	CmdOutboxList    = "outbox_list"
	CmdOutboxApprove = "outbox_approve"
	CmdOutboxReject  = "outbox_reject"

	CmdArtifactGet     = "artifact_get"
	CmdArtifactAccept  = "artifact_accept"
	CmdArtifactReject  = "artifact_reject"
	CmdArtifactDeliver = "artifact_deliver"

	CmdBriefLatest   = "brief_latest"
	CmdBriefList     = "brief_list"
	CmdBriefGet      = "brief_get"
	CmdBriefGenerate = "brief_generate"

	CmdCollectionList      = "collection_list"
	CmdCollectionConfigure = "collection_configure"
)

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
