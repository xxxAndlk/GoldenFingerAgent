# API 手册

> 适用版本：v0.2
> Base URL：`http://<host>:8080`
> 约定：请求/响应均为 `application/json; charset=utf-8`；时间一律 RFC3339；演示环境通过 `X-User-Id` 头标识用户（MVP 认证为桩），安卓端额外带 `X-Token`。
> 本手册重点记录 v0.2 通知中心端点；其余端点列于末尾。

---

## 一、通知中心 `/api/inbox`

### 1. 上报通知
`POST /api/inbox/events`

请求：
```json
{
  "events": [
    {
      "source_pkg": "com.tencent.mm",
      "app_name": "微信",
      "title": "老王",
      "text": "我把地址发你了：xx路12号",
      "posted_at": "2026-10-04T12:00:00+08:00"
    }
  ]
}
```
响应：`{"ingested":1}`（实际入库条数；重复被去重时不计）

说明：
- 服务端按 `hash(owner+pkg+title+text+posted_at 分钟桶)` 去重（`UNIQUE(owner_user_id,dedupe_hash)`）。
- 入库时并行执行：线索正则提取、armed watch 匹配、embedding（失败置 NULL，不报错）。

### 2. 查询事件
`GET /api/inbox/events?kind=normal|clue|all&limit=50`

响应：
```json
{
  "events": [
    {
      "id": "ev_...",
      "source_pkg": "com.tencent.mm",
      "app_name": "微信",
      "title": "老王",
      "text": "...",
      "kind": "clue",
      "clue_type": "pickup",
      "clue_value": "12345678",
      "posted_at": "2026-10-04T12:00:00+08:00"
    }
  ]
}
```
- `kind` 缺省为 `all`；`limit` 缺省 50。

### 3. 语义搜索
`GET /api/inbox/search?q=老王的地址&limit=20`

响应（向量模式）：
```json
{
  "mode": "vector",
  "results": [
    {
      "id": "ev_...",
      "app_name": "微信",
      "title": "老王",
      "text": "地址是xx路12号",
      "posted_at": "2026-10-04T12:00:00+08:00",
      "score": 0.812
    }
  ]
}
```
- `mode`：`vector`（余弦语义）或 `keyword`（LIKE）。**当 embedder 缺失或嵌入失败时如实返回 `keyword`，禁止静默。**
- `score` 为相似度，越高越相关。

### 4. 创建稍后处理
`POST /api/inbox/snooze`

请求（二选一，`minutes` 优先）：
```json
{ "event_id": "ev_...", "minutes": 10 }
```
```json
{ "event_id": "ev_...", "fire_at": "2026-10-04T20:00:00+08:00" }
```
响应：
```json
{ "id": "sn_...", "fire_at": "2026-10-04T12:10:00+08:00" }
```
- 事件不存在返回 404；`fire_at` 非 RFC3339 返回 400。

### 5. 查询稍后处理
`GET /api/inbox/snooze`

响应（join 原事件，便于直接展示）：
```json
{
  "snoozes": [
    {
      "id": "sn_...",
      "event_id": "ev_...",
      "title": "老王",
      "text": "...",
      "fire_at": "2026-10-04T12:10:00+08:00",
      "status": "pending"
    }
  ]
}
```
- `status`：`pending|fired|cancelled`。

### 6. 创建等待项
`POST /api/inbox/watches`

请求：
```json
{ "title": "老王", "keyword": "地址", "source_pkg": "" }
```
响应：
```json
{
  "id": "wt_...",
  "title": "老王",
  "keyword": "地址",
  "source_pkg": "",
  "status": "armed",
  "created_at": "2026-10-04T12:00:00+08:00"
}
```
- `source_pkg` 为空表示不限来源 App。

### 7. 查询等待项
`GET /api/inbox/watches`

响应：
```json
{
  "watches": [
    { "id": "wt_...", "title": "老王", "keyword": "地址", "source_pkg": "", "status": "armed", "created_at": "..." }
  ]
}
```

### 8. 取消等待项
`DELETE /api/inbox/watches/{id}`

响应：`{"ok":true}`；不存在返回 404。

---

## 二、投递出口（供设备轮询）

### Outbox
`GET /api/outbox`

响应（读取后即清空，天然去重）：
```json
[ { "user_id": "u_...", "body": "该看了：微信\n老王 ...", "at": "12:10" } ]
```
- snooze 到期 / watch 命中产生的通知，经 30s Due 循环写入 Outbox；安卓 `NotifyPoller` 每 10s 轮询并转为系统通知。

---

## 三、Super-agent `/api/triggers`、`/api/threads`、`/api/worker-tasks`、`/api/action-outbox`、`/api/events`

本轮为 P1 地基：单用户单大脑，`owner_user_id` 仅作内部兼容字段；用户只面对一个主会话（`GET /api/sessions/main`），Worker/Router 会话不出现在 `/api/sessions`。`create_worker` 由 `agent_job` 承载并由 TurnService 执行；Worker 成功后结论同步写入统一记忆（episode）并更新所属 Thread 摘要。外部发送尚未接入，ActionOutbox 只负责草稿与人工审批。

### 1. 写入 / 查询 Trigger

`POST /api/triggers`（`201` 新建，`200` 重复投递返回原记录）：

```json
{
  "channel": "email",
  "external_id": "message-id-123",
  "thread_external_id": "thread-root-123",
  "kind": "email",
  "title": "周五能交付吗",
  "body": "请确认交付时间",
  "trusted": false,
  "privacy_level": "normal",
  "payload": {"from": "a@example.com"}
}
```

响应：`{"trigger":{...},"thread":{...},"created":true,"filtered":false}`。去重优先 `owner+channel+external_id`，无外部 ID 时使用 5 分钟时间桶内容哈希。

`GET /api/triggers?status=queued&thread_id=&limit=50`

### 2. Thread 与关联

| 方法 路径 | 用途 |
|---|---|
| `GET /api/threads?channel=&limit=50` | Thread 列表 |
| `GET /api/threads/{id}` | Thread 详情 + 最近 Trigger + 关联 |
| `POST /api/threads/{id}/link` | `{"linked_thread_id":"...","relation":"manual","confidence":0.8,"reason":"..."}`；relation 支持 `same_people/same_project/calendar/manual` |
| `DELETE /api/threads/{id}/link/{linkedID}?relation=manual` | 解除关联 |
| `GET /api/sessions/main` | 获取/创建唯一主会话；客户端启动时直接恢复它，不再依赖本地会话 ID |

### 3. 路由与 Worker

`POST /api/superagent/route?mode=deterministic|llm&limit=20` 手动路由一批 `queued` Trigger。响应 `{"results":[{"trigger_id":"...","action":"notify","status":"routed"}]}`。`mode=llm` 失败时自动回退确定性 Router。

| 方法 路径 | 用途 |
|---|---|
| `GET /api/worker-tasks?thread_id=&limit=50` | Worker 任务视图（`agent_job` 扩展字段） |
| `POST /api/worker-tasks/{id}/cancel` | 取消排队/运行中的 Worker |
| `POST /api/worker-tasks/{id}/steer` | `{"text":"先核对日期"}`；追加为任务事件 |
| `GET /api/superagent/health` | Trigger/Event/Outbox 计数、Worker 数与最新事件 ID |

后台 Router 默认关闭；`GFA_SUPERAGENT=1` 时每 2s 批量路由，`GFA_SUPERAGENT_ROUTER=deterministic` 可强制不走模型，`GFA_SUPERAGENT_MAX_WORKERS` 控制并发（1–8）。

### 4. ActionOutbox 草稿与版本审批

`POST /api/action-outbox`：

```json
{"channel":"email","kind":"reply","body":"草稿正文","requires_approval":true,"idempotency_key":"draft-1"}
```

| 方法 路径 | 用途 |
|---|---|
| `GET /api/action-outbox?status=pending_approval&limit=50` | 列表 |
| `GET /api/action-outbox/{id}` | 详情 |
| `PUT /api/action-outbox/{id}` | `{"version":0,"body":"编辑后"}`；版本不匹配返回 `409` |
| `POST /api/action-outbox/{id}/approve` | `{"version":0}` |
| `POST /api/action-outbox/{id}/reject` | `{"version":0,"reason":"不需要"}` |

每次编辑/审批递增 `version`；旧版本或重复审批返回 `409`，不会重复发送。本轮没有外部发送器，`approved` 只表示人工已确认。

### 5. 事件流

`GET /api/events?after=0&limit=100` 返回 `{"events":[...],"next_event_id":N,"has_more":false}`。

`GET /api/events?stream=1` 使用 SSE，支持 `Last-Event-ID` 或 `after` 断线补拉；每帧 `event: agent`、`id: <event_id>`。Worker 生命周期通过 `worker.created/running/completed/failed/timeout/cancelled/interrupted` 进入同一事件流，桌面任务卡可直接由 SSE 驱动。

### 6. 渠道接入与增量摘要

渠道原始内容统一写 `agent_trigger`；`activity/email/wechat` 新事件会异步更新所属 Thread 的滚动摘要（LLM 失败时确定性回退）。

| 方法 路径 | 用途 |
|---|---|
| `POST /api/channels/activity/episodes` | 桌面工作片段：`episode_id/started_at/ended_at/app/title/summary/status/outputs/evidence_ids/cursor`；按日期归入 `activity:<YYYY-MM-DD>` Thread |
| `POST /api/channels/email/messages` | 单封邮件：`account/message_id/thread_key/from/to/subject/text/received_at`；同一 `thread_key` 复用 Thread |
| `POST /api/channels/wechat/messages` | 可见微信内容：`source/message_id/sender/text/incomplete`；默认拒绝，来源需显式允许 |
| `POST /api/channels/calendar/events` | 日历事件：`calendar/event_id/series_id/title/description/location/start_at/end_at/attendees/status`；按 series/event 归 Thread |
| `GET /api/settings/channels` | 返回渠道策略、今日跳过计数；微信默认 deny，其他渠道默认 allow |
| `PUT /api/settings/channels` | `{"channel":"wechat","source":"项目群","allowed":true}`；拒绝来源不落正文，只增加跳过计数 |
| `GET/PUT /api/settings/email` | IMAP 收件配置（host/port/username/password/mailbox/tls/initial_fetch/poll_seconds）；GET 密码打码，空密码表示保留原值 |

可选 IMAP 采集器只读取正文（`BODY.PEEK[]`），不标记已读、不发送邮件；游标保存在 `agent_channel_cursor`，重启不重复拉取。

### 7. 跨渠道每日简报

| 方法 路径 | 用途 |
|---|---|
| `GET /api/briefs?limit=30` | 简报版本列表（旧版本保留） |
| `GET /api/briefs/latest?date=YYYY-MM-DD` | 当天最新版本；未生成返回 404 |
| `POST /api/briefs/generate?date=YYYY-MM-DD` | 手动生成新版本；晚到事件可重新生成，不覆盖旧版 |
| `GET /api/briefs/{id}` | 指定版本 |
| `POST /api/briefs/{id}/delivered` | 标记已读；重复标记返回 409 |

简报固定三段：`needs_action`（审批、明确问题、当天到期）、`important_changes`（决定/改期/完成/阻塞）、`background`。每条必须带来源 ref（`trigger:` / `thread:` / `worker:` / `outbox:` / `task:`）。到达 `digest.time` 后自动生成首版，之后有新事件最多每 30 分钟补一版。

---

## 四、其余既有端点（摘要）

| 方法 路径 | 功能 |
|---|---|
| POST /api/chat | 发送对话，驱动 agent 循环 |
| GET /api/sessions/{id}/messages?limit=50&before=<message_id> | 游标分页取会话消息（默认 50，最大 200，返回时间正序一页） |
| GET /api/persons | 人物列表 |
| GET /api/persons/{id} | 人物详情 |
| PUT /api/persons/{id} | 更新人物备注 |
| DELETE /api/persons/{id} | 遗忘人物（级联） |
| DELETE /api/facts/{id} | 删除事实 |
| GET /api/tasks | 任务列表 |
| POST /api/tasks/{id}/{action} | 任务动作（confirm/done/cancel/snooze） |
| GET /api/intents | 常驻意图列表 |
| DELETE /api/intents/{id} | 取消常驻意图 |
| GET /api/memory/export.md | 导出 Markdown 记忆档案 |
| GET /api/reminders | 提醒列表 |
| GET /api/me | 当前用户与时区 |
| POST /api/me/consent | 授予/撤销同意 |
| GET /api/settings/llm | 取 LLM 设置（mask key） |
| PUT /api/settings/llm | 更新 LLM 设置 |
| POST /api/voice/transcribe | 语音转文字 |
| POST /api/voice/speak | 文字转语音 |
| GET /ws/device | 设备 WebSocket |
| POST /api/screen | 屏幕内容上报 |

---

## 五、错误格式

统一：`{"error":"<message>"}`，常见状态码 400（请求/格式错误）、404（资源不存在）、500（服务端错误）、503（对应服务未装配，如 Inbox=nil）。
