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

### 3. WorkTask 长期任务

WorkTask 跨多条消息、多个执行段和多个渠道保存目标、实时状态、控制 Inbox、Artifact 版本、ActionGrant 与 Checkpoint。所有变更接口都按 `owner_user_id` 过滤；状态迁移使用 `expected_version` CAS，冲突返回 `409`。

| 方法 路径 | 用途 |
|---|---|
| `POST /api/work-tasks` | `{"title":"...","goal":"...","completion_criteria":"...","profile":"read|internal|external","start":true}`；`start=false` 时只创建草稿 |
| `GET /api/work-tasks?status=&limit=100` | 任务列表 |
| `GET /api/work-tasks/{id}` | 任务 + live_state + Threads + Inbox + Jobs + Artifacts + Checkpoints + Grants + ToolInvocations；Artifact 最新正文在 `artifact_latest` |
| `POST /api/work-tasks/{id}/messages` | `{"text":"...","expected_version":N}`；等待中的任务会恢复并创建新执行段，其他运行中任务在下一个安全步骤边界消费 |
| `POST /api/work-tasks/{id}/pause` | `{"expected_version":N}`；在当前安全步骤边界停止，释放 Worker 槽位 |
| `POST /api/work-tasks/{id}/resume` | `{"expected_version":N}`；从暂停恢复，创建新执行段 |
| `POST /api/work-tasks/{id}/cancel` | 取消任务、活动 Job、未消费 Inbox 和未执行 Grant |
| `POST /api/work-tasks/{id}/recover` | 从最近 Checkpoint 创建**新 Job**；旧 Job 保持结束状态，不重放未知副作用 |
| `POST /api/work-tasks/{id}/reopen` | 已完成任务重新打开 |
| `POST /api/work-tasks/{id}/complete` | `{"expected_version":N}`；无文件交付、仅有结果摘要时人工验收完成 |
| `GET /api/work-tasks/{id}/events?after_id=` | 任务级事件时间线 |
| `POST /api/work-tasks/{id}/threads/{thread_id}` | 关联来源 Thread（`relation=origin|related|output`） |
| `DELETE /api/work-tasks/{id}/threads/{thread_id}?relation=` | 解除关联 |
| `GET /api/work-tasks/{id}/artifacts` | 交付物列表 |
| `POST /api/work-tasks/{id}/artifacts` | 创建草稿：`{"kind":"text","title":"...","content":"...","source_refs":[],"job_id":""}` |
| `GET /api/artifacts/{id}` | 交付物与全部不可变版本 |
| `GET /api/artifacts/{id}/versions/{version}/download` | 按 owner 校验后下载文本版本 |
| `GET /api/artifacts/{id}/diff?from=&to=` | 服务端 unified diff |
| `POST /api/artifacts/{id}/submit` | 提交验收 |
| `POST /api/artifacts/{id}/accept` / `reject` | 验收/要求修改；所有待验收 Artifact 通过后 Task 完成 |
| `GET /api/work-tasks/{id}/grants` | 任务授权 |
| `POST /api/work-tasks/{id}/grants` | 手工创建带范围/次数/有效期的 Grant |
| `DELETE /api/grants/{id}` | 撤销授权 |

**状态机**：`draft → queued → active → waiting_* / paused / pending_acceptance → completed`；`failed` 可恢复重排队，`cancelled` 为终态。执行段完成只进入 `pending_acceptance`，不会自动算作任务完成。

**插话语义**：`POST /messages` 返回 `202 + inbox_id + delivery`。运行中的 Job 不保证立即注入，而是在下一个模型请求前由 `StepController` 事务性消费（写 Worker 会话用户消息 + 标记 consumed）；进程崩溃不会重复消费或丢消息。外部 CLI 不支持运行中输入时，消息保持 queued，下一段执行生效。

**工具权限**：`read` 只能调用只读工具；`internal` 允许本机准备和授权工作目录内写入；`external` 允许外部草稿。`bash`、`delegate_agent`、`switch_claude_provider` 和未知工具按敏感能力处理，必须有覆盖工具名、资源范围和内容 hash 的有效 Grant。授权过期、撤销、次数耗尽、内容变化或设备撤销都会拒绝执行，并记录 `tool_invocation(status=denied)`。

### 4. 路由与 Worker

`POST /api/superagent/route?mode=deterministic|llm&limit=20` 手动路由一批 `queued` Trigger。响应 `{"results":[{"trigger_id":"...","action":"notify","status":"routed"}]}`。`mode=llm` 失败时自动回退确定性 Router。

| 方法 路径 | 用途 |
|---|---|
| `GET /api/worker-tasks?thread_id=&limit=50` | Worker 任务视图（`agent_job` 扩展字段） |
| `POST /api/worker-tasks/{id}/cancel` | 取消排队/运行中的 Worker |
| `POST /api/worker-tasks/{id}/steer` | 旧接口：若 Job 已绑定 WorkTask，则写 Task Inbox 并返回 `202 + inbox_id + queued`；没有 WorkTask 的旧 Job 返回 `409`，不再假称已注入 |
| `GET /api/superagent/health` | Trigger/Event/Outbox 计数、Worker 数与最新事件 ID |

后台 Router 默认关闭；`GFA_SUPERAGENT=1` 时每 2s 批量路由，`GFA_SUPERAGENT_ROUTER=deterministic` 可强制不走模型，`GFA_SUPERAGENT_MAX_WORKERS` 控制并发（1–8）。

### 5. ActionOutbox 草稿与版本审批

`POST /api/action-outbox`：

```json
{"channel":"email","kind":"reply","body":"草稿正文","work_task_id":"...","expected_effect":{},"requires_approval":true,"idempotency_key":"draft-1"}
```

| 方法 路径 | 用途 |
|---|---|
| `GET /api/action-outbox?status=pending_approval&limit=50` | 列表 |
| `GET /api/action-outbox/{id}` | 详情 |
| `PUT /api/action-outbox/{id}` | `{"version":0,"body":"编辑后"}`；版本不匹配返回 `409` |
| `POST /api/action-outbox/{id}/approve` | `{"version":0}` |
| `POST /api/action-outbox/{id}/reject` | `{"version":0,"reason":"不需要"}` |

每次编辑/审批递增 `version`；旧版本或重复审批返回 `409`，不会重复发送。本轮没有外部发送器，`approved` 只表示人工已确认。绑定 `work_task_id` 的审批会生成一次性、24 小时、内容 hash 绑定的 `action_grant`；再次编辑草稿会撤销旧 Grant。外部调用超时记录 `tool_invocation=unknown`，不会自动重试。

### 6. 事件流

`GET /api/events?after=0&limit=100` 返回 `{"events":[...],"next_event_id":N,"has_more":false}`。

`GET /api/events?stream=1` 使用 SSE，支持 `Last-Event-ID` 或 `after` 断线补拉；每帧 `event: agent`、`id: <event_id>`。Worker 生命周期通过 `worker.created/running/completed/failed/timeout/cancelled/interrupted` 进入同一事件流，桌面任务卡可直接由 SSE 驱动。

### 7. 渠道接入与增量摘要

渠道原始内容统一写 `agent_trigger`；`activity/email/wechat` 新事件会异步更新所属 Thread 的滚动摘要（LLM 失败时确定性回退）。

| 方法 路径 | 用途 |
|---|---|
| `POST /api/channels/activity/episodes` | 桌面工作片段：`episode_id/context_key/started_at/ended_at/app/apps/project_candidates/title/summary/status/outputs/evidence_ids/cursor/association_confidence`；有 `context_key` 时归入 `activity:project:*` / `activity:web:*` / `activity:unassigned:*`，否则按日期 |
| `POST /api/channels/email/messages` | 单封邮件：`account/message_id/thread_key/from/to/subject/text/received_at`；同一 `thread_key` 复用 Thread |
| `POST /api/channels/wechat/messages` | 可见微信内容：`source/source_id/display_name/message_id/event_id/sender/direction/message_type/text/observed_at/occurred_at/content_hash/completeness/incomplete`；默认拒绝，来源需显式允许 |
| `POST /api/channels/calendar/events` | 日历事件：`calendar/event_id/series_id/title/description/location/start_at/end_at/attendees/status`；按 series/event 归 Thread |
| `GET /api/settings/channels` | 返回渠道策略、今日跳过计数；微信默认 deny，其他渠道默认 allow |
| `PUT /api/settings/channels` | `{"channel":"wechat","source":"项目群","allowed":true}`；拒绝来源不落正文，只增加跳过计数 |
| `GET/PUT /api/settings/email` | IMAP 收件配置（host/port/username/password/mailbox/tls/initial_fetch/poll_seconds）；GET 密码打码，空密码表示保留原值 |

微信字段语义：`source_id` 是会话稳定键，`display_name` 是用户可见名称；`direction` 只能是 `self/other/system/unknown`；`completeness` 为 `complete/current_visible/partial/unknown`；不知道真实发送时间时 `occurred_at=null`，只写 `observed_at`。`event_id` 用于幂等，`content_hash` 只用于内容变化判断。`current_visible`/`partial` 会在正文末尾标注“当前可见内容，不代表完整聊天记录”。

桌面端微信连接器只读取当前打开会话的可见屏幕；微信 4.x 的 Qt 窗口不提供消息气泡 UIA，因此消息边界来自截图 OCR 坐标。未授权会话不写检查点、不入队、不进入模型。桌面工作台「接入设置 → 感知采集状态」可见当前会话、最近观察/入库、完整性、待发/过滤/失败和错误；`wechat:test-capture` IPC 只做测试预览，不写入 Spool。

可选 IMAP 采集器只读取正文（`BODY.PEEK[]`），不标记已读、不发送邮件；游标保存在 `agent_channel_cursor`，重启不重复拉取。

### 8. 跨渠道每日简报

| 方法 路径 | 用途 |
|---|---|
| `GET /api/briefs?limit=30` | 简报版本列表（旧版本保留） |
| `GET /api/briefs/latest?date=YYYY-MM-DD` | 当天最新版本；未生成返回 404 |
| `POST /api/briefs/generate?date=YYYY-MM-DD` | 手动生成新版本；晚到事件可重新生成，不覆盖旧版 |
| `GET /api/briefs/{id}` | 指定版本 |
| `POST /api/briefs/{id}/delivered` | 标记已读；重复标记返回 409 |

简报固定三段：`needs_action`（审批、明确问题、当天到期）、`important_changes`（决定/改期/完成/阻塞）、`background`。每条必须带来源 ref（`trigger:` / `thread:` / `worker:` / `outbox:` / `task:`）。到达 `digest.time` 后自动生成首版，之后有新事件最多每 30 分钟补一版。

### 9. 渠道与全局开关

六类渠道 `activity`、`email`、`feishu`、`dingtalk`、`wechat_desktop`、`wechat_mobile` 分别保存五个开关，**默认全部关闭**：

| 字段 | 语义 |
|---|---|
| `enabled` | 是否采集新信息；关闭时在入库前拦截，只增加跳过计数 |
| `summary_enabled` | 是否因新事件调用 Thread 摘要模型 |
| `index_enabled` | 是否生成新向量（关闭不影响按日期/来源 ID/关键词查询） |
| `brief_enabled` | 是否进入自动或手动每日简报 |
| `auto_worker_enabled` | 是否允许 Router 因该渠道事件自动创建 Worker |

全局开关：`router_enabled`、`worker_enabled`、`daily_brief_enabled`、`proactive_notify_enabled`。渠道自动 Worker 需要渠道与全局同时允许。

| 方法 路径 | 用途 |
|---|---|
| `GET /api/settings/channels` | 返回 `configs`、`global`、`policies`、`skips_today` 与渠道列表 |
| `PUT /api/settings/channels/config/{channel}` | 覆盖单个渠道的五个开关 |
| `PUT /api/settings/channels/global` | 覆盖四个全局开关 |
| `PUT /api/settings/channels` | 来源策略 `{"channel","source","allowed"}`；微信、飞书、钉钉来源默认拒绝 |

`/api/inbox/events` 在写入旧通知中心前应用 `wechat_mobile` 开关和来源策略；被拒微信通知只计数，不落正文、不生成向量索引。

### 10. 邮箱 UIDVALIDITY 与筛选

`GET/PUT /api/settings/email` 在原有字段外增加：

```json
{
  "mailboxes": ["INBOX", "项目/客户"],
  "senders": ["client@example.com"],
  "subject_filters": ["报价", "合同"],
  "since_days": 30
}
```

游标保存 `UIDVALIDITY + UID`；邮箱重建导致 UIDVALIDITY 变化时自动按首次拉取策略重置，不按旧 UID 漏信。筛选同时在 IMAP SEARCH 和本地解析后执行。仍使用 `EXAMINE` / `BODY.PEEK[]`，不修改已读状态、不自动发送。

### 11. 通用网页采集

`POST /api/channels/web/items`：

```json
{
  "site_id": "example",
  "account": "me",
  "url": "https://example.com/items/42",
  "canonical_url": "https://example.com/items/42?a=1&b=2",
  "item_id": "42",
  "title": "条目标题",
  "text": "脱敏后的正文",
  "captured_at": "2026-11-01T10:00:00+08:00",
  "extracted_by": "adapter|readability|dom|ocr_fallback",
  "completeness": "complete|partial|unknown",
  "evidence_ids": []
}
```

服务端校验站点开关、来源策略、授权 origin/路径和正文大小，对规范化正文重算 SHA-256；`ExternalID=web:<site>:<account>:<item>:<hash>`，`ThreadExternalID=web:<site>:<account>:<canonical_url>`。同一内容重复上报返回原 Trigger，内容变化产生同一 Thread 的新版本。网页内容固定 `trusted=false`。

| 方法 路径 | 用途 |
|---|---|
| `GET /api/settings/web/sites` | 站点列表与说明 |
| `PUT /api/settings/web/sites/{id}` | 新增/覆盖站点；`interval_seconds` 不得低于 60，`max_pages ≤ 20`，`max_depth ≤ 3`；开启自动登录必须提供 HTTPS `login_url`、`credential_ref` 和三个确定性选择器 |
| `DELETE /api/settings/web/sites/{id}` | 删除站点（已采集记录不删除） |

站点开关：`enabled`、`summary_enabled`、`index_enabled`、`brief_enabled`、`auto_worker_enabled`、`navigation_enabled`、`session_enabled`、`auto_login_enabled`。桌面工作台“网页采集”页可新增/保存、立即采集、人工/自动登录、保存/删除凭据、停止和读取当前页；浏览器使用独立 profile，不导出 Cookie，不读取其他标签页。

自动登录相关字段：`login_url`、`credential_ref`、`username_selector`、`password_selector`、`submit_selector`。密码不经过 HTTP API，只由 Electron 主进程写入 `safeStorage`（Windows 上为 DPAPI）加密文件；每次填充前弹窗确认，origin/登录地址不匹配或遇到验证码/二次验证立即停止。

---

## 四、记忆、策略与设备治理

| 方法 路径 | 用途 |
|---|---|
| `GET /api/memory/longterm?status=&limit=` | 长期事实列表；status 支持 `inferred/confirmed/draft/conflicted` |
| `POST /api/memory/longterm/{id}/confirm` | `{"expected_version":N}`；用户确认 inferred/conflicted 事实 |
| `POST /api/memory/longterm/{id}/conflict` | 标记冲突，保留证据等待人工处理 |
| `GET /api/personal-policies?scope_type=&scope_id=&status=` | 个人策略/SOP 列表 |
| `POST /api/personal-policies` | `{"policy_type":"focus|preference|boundary|sop|notification","name":"...","content":{},"scope_type":"global|task|channel|device","scope_id":"","status":"draft|active","valid_days":0}` |
| `POST /api/personal-policies/{id}/status` | `{"status":"active|disabled","expected_version":N}` |
| `GET /api/devices` | 设备环境列表 |
| `POST /api/devices` | `{"id":"dev-1","name":"手机","device_type":"android","online":true}` |
| `POST /api/devices/{id}/revoke` | 撤销设备；撤销后绑定动作与恢复均拒绝 |
| `GET/POST /api/devices/{id}/capabilities` | 查询/新增设备能力：`{"tool_name":"tap","resource_scope":{},"work_dir":"","limits":{}}` |
| `POST /api/work-tasks/{id}/devices/{device_id}` | 绑定任务与设备能力：`{"capability_id":"..."}`；`waiting_device` 恢复前重新校验在线且未撤销 |

自动抽取事实默认 `inferred`，不会覆盖 `confirmed`；冲突保留两条证据并标 `conflicted`。Worker 结论不得自动成为稳定 Personal Policy，必须用户确认后 `active`。工具执行还要同时满足 Profile、设备能力和 ActionGrant；任一失效都会拒绝并记录 denied 调用。

## 五、其余既有端点（摘要）

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

## 六、错误格式

统一：`{"error":"<message>"}`，常见状态码 400（请求/格式错误）、404（资源不存在）、500（服务端错误）、503（对应服务未装配，如 Inbox=nil）。
