package relay

import (
	"context"
	"encoding/json"
	"time"
)

// 本文件是手机 → 电脑的长期任务控制面指令集。
//
// 约束（勿删）：
//   - 只发白名单业务命令，不做任意 HTTP 代理；
//   - 每条指令带 command_id（幂等键），重试复用同一个值；
//   - 查询类指令不排队：电脑离线时如实返回离线，由手机显示缓存；
//   - 变更类指令（追加指令、审批、验收）可以排队，恢复后最多执行一次。

// commandTimeout 查询类指令的等待上限。
const commandTimeout = 30 * time.Second

// mutationTimeout 变更类指令的等待上限。
const mutationTimeout = 60 * time.Second

// briefTimeout 生成简报的等待上限。
//
// 电脑端生成允许 90 秒，手机这边必须比它长：
// 否则 60–90 秒之间手机会先报失败，而电脑随后仍生成成功。
const briefTimeout = 150 * time.Second

// sendCommand 发送一条白名单指令。id 为空时自动生成幂等键。
func (p *Phone) sendCommand(ctx context.Context, id, kind string, payload map[string]any, timeout time.Duration) (ResultPayload, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["kind"] = kind
	payload["schema_version"] = CommandSchemaVersion
	if id == "" {
		id = newMessageID()
	}
	payload["idempotency_key"] = id
	return p.sendKind(ctx, id, payload, timeout)
}

// WorkTaskCreate 让电脑新建长期任务。idempotencyKey 为空时自动生成；
// 重试必须复用同一个键，否则会建出两个任务。
func (p *Phone) WorkTaskCreate(ctx context.Context, idempotencyKey, title, goal, profile string) (ResultPayload, error) {
	payload := map[string]any{"title": title, "goal": goal}
	if profile != "" {
		payload["profile"] = profile
	}
	return p.sendCommand(ctx, idempotencyKey, CmdWorkTaskCreate, payload, mutationTimeout)
}

// WorkTaskList 列出长期任务。status 为空表示全部。
func (p *Phone) WorkTaskList(ctx context.Context, status string, limit int) (ResultPayload, error) {
	payload := map[string]any{}
	if status != "" {
		payload["status"] = status
	}
	if limit > 0 {
		payload["limit"] = limit
	}
	return p.sendCommand(ctx, "", CmdWorkTaskList, payload, commandTimeout)
}

// WorkTaskGet 取一个任务的完整详情（含状态、等待原因、交付物与事件）。
func (p *Phone) WorkTaskGet(ctx context.Context, taskID string) (ResultPayload, error) {
	return p.sendCommand(ctx, "", CmdWorkTaskGet, map[string]any{"task_id": taskID}, commandTimeout)
}

// WorkTaskMessage 往任务 Inbox 追加一条指令。
//
// 指令不会立刻变成 Worker 的回答：它在下一个安全步骤边界被消费。
// 结果里的 delivery 说明是「当前段已消费」还是「新执行段已排队」。
func (p *Phone) WorkTaskMessage(ctx context.Context, idempotencyKey, taskID, text string, expectedVersion *int) (ResultPayload, error) {
	payload := map[string]any{"task_id": taskID, "text": text}
	if expectedVersion != nil {
		payload["expected_version"] = *expectedVersion
	}
	return p.sendCommand(ctx, idempotencyKey, CmdWorkTaskMsg, payload, mutationTimeout)
}

// WorkTaskAction 控制任务状态：pause / resume / cancel / recover / reopen / complete。
func (p *Phone) WorkTaskAction(ctx context.Context, idempotencyKey, taskID, action string, expectedVersion *int) (ResultPayload, error) {
	payload := map[string]any{"task_id": taskID, "action": action}
	if expectedVersion != nil {
		payload["expected_version"] = *expectedVersion
	}
	return p.sendCommand(ctx, idempotencyKey, CmdWorkTaskAction, payload, mutationTimeout)
}

// ConversationList 取统一会话目录。kind 为空表示全部。
//
// deviceID 是这台设备的标识：未读数按设备统计，
// 必须与 conversation_read 用同一个标识，否则角标永远清不掉。
func (p *Phone) ConversationList(ctx context.Context, kind string, limit int, deviceID string) (ResultPayload, error) {
	payload := map[string]any{}
	if kind != "" {
		payload["conversation_kind"] = kind
	}
	if limit > 0 {
		payload["limit"] = limit
	}
	if deviceID != "" {
		payload["device_id"] = deviceID
	}
	return p.sendCommand(ctx, "", CmdConversationList, payload, commandTimeout)
}

// ConversationMessages 读一个会话的消息页。before 为空取最新一页。
func (p *Phone) ConversationMessages(ctx context.Context, sessionID, before string, limit int) (ResultPayload, error) {
	payload := map[string]any{"session_id": sessionID}
	if before != "" {
		payload["before"] = before
	}
	if limit > 0 {
		payload["limit"] = limit
	}
	return p.sendCommand(ctx, "", CmdConversationMessages, payload, commandTimeout)
}

// ConversationRead 记录该设备读到某个会话的位置。
//
// 游标必须记在会话真正所在的电脑上，并带上手机自己的设备标识，
// 否则手机读过的会话会把电脑的角标一起清掉。
func (p *Phone) ConversationRead(ctx context.Context, sessionID, deviceID string) (ResultPayload, error) {
	payload := map[string]any{"session_id": sessionID}
	if deviceID != "" {
		payload["device_id"] = deviceID
	}
	return p.sendCommand(ctx, "", CmdConversationRead, payload, commandTimeout)
}

// OutboxList 列出待审批的动作。
func (p *Phone) OutboxList(ctx context.Context, limit int) (ResultPayload, error) {
	payload := map[string]any{}
	if limit > 0 {
		payload["limit"] = limit
	}
	return p.sendCommand(ctx, "", CmdOutboxList, payload, commandTimeout)
}

// OutboxDecide 审批或驳回一条待办动作。expectedVersion 用于版本冲突检测。
func (p *Phone) OutboxDecide(ctx context.Context, idempotencyKey, outboxID string, approve bool, expectedVersion *int, reason string) (ResultPayload, error) {
	kind := CmdOutboxReject
	if approve {
		kind = CmdOutboxApprove
	}
	payload := map[string]any{"outbox_id": outboxID}
	if expectedVersion != nil {
		payload["expected_version"] = *expectedVersion
	}
	if reason != "" {
		payload["reason"] = reason
	}
	return p.sendCommand(ctx, idempotencyKey, kind, payload, mutationTimeout)
}

// ArtifactGet 取交付物详情与全部版本。
func (p *Phone) ArtifactGet(ctx context.Context, artifactID string) (ResultPayload, error) {
	return p.sendCommand(ctx, "", CmdArtifactGet, map[string]any{"artifact_id": artifactID}, commandTimeout)
}

// ArtifactDecide 对交付物做验收 / 驳回（要求修改）。
func (p *Phone) ArtifactDecide(ctx context.Context, idempotencyKey, artifactID string, accept bool, expectedVersion *int, reason string) (ResultPayload, error) {
	kind := CmdArtifactReject
	if accept {
		kind = CmdArtifactAccept
	}
	payload := map[string]any{"artifact_id": artifactID}
	if expectedVersion != nil {
		payload["expected_version"] = *expectedVersion
	}
	if reason != "" {
		payload["reason"] = reason
	}
	return p.sendCommand(ctx, idempotencyKey, kind, payload, mutationTimeout)
}

// ArtifactDeliver 取交付物的下载引用。
//
// idempotencyKey 必须由调用方给：离线排队后重放要复用同一个键，
// 电脑端才能认出「这条交付指令已经处理过」并回放原结果。
func (p *Phone) ArtifactDeliver(ctx context.Context, idempotencyKey, artifactID string, version int) (ResultPayload, error) {
	payload := map[string]any{"artifact_id": artifactID}
	if version > 0 {
		payload["version"] = version
	}
	return p.sendCommand(ctx, idempotencyKey, CmdArtifactDeliver, payload, commandTimeout)
}

// BriefLatest 取某一天的每日简报。date 为空表示今天。
func (p *Phone) BriefLatest(ctx context.Context, date string) (ResultPayload, error) {
	payload := map[string]any{}
	if date != "" {
		payload["date"] = date
	}
	return p.sendCommand(ctx, "", CmdBriefLatest, payload, commandTimeout)
}

// BriefList 列出历史简报。
func (p *Phone) BriefList(ctx context.Context, limit int) (ResultPayload, error) {
	payload := map[string]any{}
	if limit > 0 {
		payload["limit"] = limit
	}
	return p.sendCommand(ctx, "", CmdBriefList, payload, commandTimeout)
}

// BriefGet 取指定简报。
func (p *Phone) BriefGet(ctx context.Context, briefID string) (ResultPayload, error) {
	return p.sendCommand(ctx, "", CmdBriefGet, map[string]any{"brief_id": briefID}, commandTimeout)
}

// BriefGenerate 让电脑生成简报。电脑离线时不会伪造新版本。
func (p *Phone) BriefGenerate(ctx context.Context, idempotencyKey, date string) (ResultPayload, error) {
	payload := map[string]any{}
	if date != "" {
		payload["date"] = date
	}
	return p.sendCommand(ctx, idempotencyKey, CmdBriefGenerate, payload, briefTimeout)
}

// CollectionList 列出桌面采集渠道与连接器状态。
func (p *Phone) CollectionList(ctx context.Context) (ResultPayload, error) {
	return p.sendCommand(ctx, "", CmdCollectionList, map[string]any{}, commandTimeout)
}

// CollectionConfigure 配置桌面采集渠道。电脑必须回执真实结果，
// 保存失败时手机不能显示成已生效。
func (p *Phone) CollectionConfigure(ctx context.Context, idempotencyKey, channel string, enabled bool, config map[string]any) (ResultPayload, error) {
	payload := map[string]any{"channel": channel, "enabled": enabled}
	for key, value := range config {
		payload[key] = value
	}
	return p.sendCommand(ctx, idempotencyKey, CmdCollectionConfigure, payload, mutationTimeout)
}

// CollectionInput 是手机远程配置采集渠道的结构化参数。
type CollectionInput struct {
	Channel         string
	Action          string
	Enabled         bool
	Query           string
	SourceID        string
	URL             string
	SiteID          string
	Scope           string
	IntervalSeconds int
}

// CollectionConfigureInput 用结构化参数配置电脑端的采集渠道。
// action 支持 add/remove/enable/disable，其余参数与电脑端工具一致。
func (p *Phone) CollectionConfigureInput(ctx context.Context, idempotencyKey string, input CollectionInput) (ResultPayload, error) {
	payload := map[string]any{
		"channel": input.Channel, "enabled": input.Enabled,
	}
	if input.Action != "" {
		payload["action"] = input.Action
	}
	if input.Query != "" {
		payload["query"] = input.Query
	}
	if input.SourceID != "" {
		payload["source_id"] = input.SourceID
	}
	if input.URL != "" {
		payload["url"] = input.URL
	}
	if input.SiteID != "" {
		payload["site_id"] = input.SiteID
	}
	if input.Scope != "" {
		payload["scope"] = input.Scope
	}
	if input.IntervalSeconds > 0 {
		payload["interval_seconds"] = input.IntervalSeconds
	}
	return p.sendCommand(ctx, idempotencyKey, CmdCollectionConfigure, payload, mutationTimeout)
}

// DecodeData 把指令结果里的结构化数据解码出来。
func DecodeData(payload ResultPayload, target any) error {
	if len(payload.Data) == 0 {
		return nil
	}
	return json.Unmarshal(payload.Data, target)
}
