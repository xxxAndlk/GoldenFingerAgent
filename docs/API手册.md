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

## 三、其余既有端点（摘要）

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

## 四、错误格式

统一：`{"error":"<message>"}`，常见状态码 400（请求/格式错误）、404（资源不存在）、500（服务端错误）、503（对应服务未装配，如 Inbox=nil）。
