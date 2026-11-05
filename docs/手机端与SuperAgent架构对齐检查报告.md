# 手机端与电脑端 SuperAgent 架构对齐检查报告

- 日期：2026-11-05
- 范围：`web/` 手机 H5、Android Go 服务、Electron 主对话与工作台、HTTP API、SuperAgent／WorkTask／中继链路
- 性质：静态代码审计与修复计划，本轮不修改功能代码、不触碰真实 `data/gfa.db`
- 验证边界：所附手机截图本轮未能读取，因此界面判断以页面源码为准；本报告不代表 Android 真机、真实模型或真实中继验收已经通过

## 一、先回答四个直接问题

### 1. “收件箱”原来有什么用？

它不是每日简报，也不是 Worker 任务中心，而是手机通知监听后的信息管理器，当前包含：

1. 通知全文／语义搜索；
2. 稍后处理；
3. 取件码、地址等线索提取与复制；
4. 按联系人和关键词创建“等待中”规则。

证据：`web/index.html:1796-1853`。

**产品决策：顶部“收件箱”入口改成“每日简报”，但底层通知采集、搜索、稍后处理、线索和关键词等待全部保留。** 将这些能力移到“设置 → 通知与稍后处理”或记忆面板的二级入口，不能因改入口而删除通知系统。

### 2. 每日简报现在的展示是否合适？

不合适。当前手机把简报插在聊天消息流顶部，并用 `<pre>` 直接输出 `content_md`：Markdown 不会渲染，等宽纯文本也不适合长文阅读。电脑工作台的简报页同样使用 `<pre>`。

证据：`web/index.html:1621-1624`、`desktop/workbench.js:571-600`。

应改成独立“每日简报”页面／抽屉，支持 Markdown、日期与版本、更新时间、历史简报、重新生成、加载／空／错误／离线缓存状态，并从聊天流移除重复卡片。

### 3. 对话是不是没有使用 SuperAgent？

要分电脑和手机回答：

- **电脑端不是没有使用。** 普通聊天先经过 `Coordinator.Handle`；命中 `/task`、新建／暂停／恢复／取消任务等明确指令时进入 WorkTask，未命中时由 `TurnService` 走主会话快速路径。Worker 与主聊天复用同一个 `TurnService`。证据：`internal/httpapi/chat.go:99-170`、`internal/superagent/coordinator.go:14-27`、`internal/agent/runner.go:24-44`。
- **快速路径是合理设计，不应把所有闲聊都强制改成 Trigger／Worker。** 否则会额外增加模型调用、延迟和任务噪声。
- **手机端确实没有完整接入。** Android `httpapi.Server` 没有装配 `Coordinator`、`SuperAgent`、`Tasks`、`Artifacts`、`Briefs` 和 `Collection`；运行时也仅注册 `agent.DefaultTools()`。证据：`android/gosvc/gosvc.go:244-273`、`android/gosvc/gosvc.go:286-302`。
- **真正缺陷是控制面与视图不统一。** 即使电脑创建了任务，聊天响应也把 Coordinator 的 `task_id/action` 丢掉，只返回文本；前端无法自然跳到任务或 Worker 对话。证据：`internal/httpapi/chat.go:114-140`。

### 4. 双端多对话是否已经完成？

没有。当前双端主要是“一个主对话 + 普通历史 + 独立任务界面”：

- 仓储层已有 `ChatSession.kind/thread_id/work_task_id`，Worker 会话也已持久化；
- 但 `ListByOwner` 明确过滤 `worker/router`；
- HTTP `sessionDTO` 又不暴露 `kind/task_id/job_id/status`；
- 电脑工作台只展示执行段摘要，没有 Worker 完整消息；
- 手机没有 Worker 对话和 WorkTask 任务中心。

证据：`internal/store/models.go:152-162`、`internal/store/sessions.go:143-170`、`internal/httpapi/chat.go:199-275`、`desktop/workbench.js:421-426`。

注意：`AGENT.md:261` 曾约定“Worker 会话不得出现在 `/api/sessions`”。因此不能简单删除过滤条件、改变旧接口语义；应新增统一会话投影接口，让新双端界面使用，同时保留旧接口兼容性。

## 二、当前真实链路

### 2.1 电脑主对话

```text
用户输入
  → /api/chat 或 /api/chat/stream
  → Coordinator.Handle
      ├─ 明确任务指令 → WorkTask Service → 文本回复
      └─ 普通输入     → TurnService → agent.Run → 主会话持久化
```

优点是低成本、主聊天与 Worker 共用执行器；缺点是任务结果只有文本，缺少结构化任务卡、Worker 会话入口和统一状态投影。

### 2.2 电脑 SuperAgent／WorkTask

后端已经具备较完整的长期任务基础：

- WorkTask 列表、详情、追加指令、暂停／恢复／取消／恢复异常／验收；
- Worker 会话 ID：`work_task.current_session_id`、`agent_job.session_id`；
- WorkTask Inbox：运行中插话在安全步骤边界消费；
- ActionOutbox：版本化审批与驳回；
- Artifact：不可变版本、提交、验收、驳回与差异；
- SSE 事件与桌面待处理角标。

这些不是“后端没做”，而是手机没有控制入口，电脑也没有把 Worker 会话呈现出来。

### 2.3 手机主对话与电脑助手

```text
手机主对话 → Android 本地 TurnService → 本地主会话

手机“电脑助手”
  ├─ 同步指令 → /api/relay/phone/send → 电脑 /api/chat 或 /api/agents/delegate
  └─ 后台执行 → /api/relay/phone/job  → 电脑 POST /api/jobs → 旧 AgentJob
```

证据：`web/index.html:2550-2670`、`internal/httpapi/relay_phone.go:136-208`、`internal/relay/client.go:314-362`、`internal/relay/client.go:413-479`。

这条后台链路具备幂等、轮询和取消，但它是旧 AgentJob 控制面，不具备 WorkTask 的等待原因、运行中插话、审批、Artifact 验收和统一 Worker 会话视图。

### 2.4 每日简报

电脑服务装配了 `BriefGenerator`，手机没有。手机虽然请求 `/api/briefs/latest`，但只能读 Android 本地数据库；没有本地生成器，也没有从电脑／云端控制面获取简报的链路。因而“顶部换文案”只能解决入口，不能解决简报内容来源。

证据：`cmd/server/main.go:354-390`、`android/gosvc/gosvc.go:286-302`、`web/index.html:2884-2890`。

## 三、缺口分级矩阵

| 优先级 | 缺口 | 电脑端 | 手机端 | 结论 |
|---|---|---|---|---|
| P0 安全 | 会话消息 owner 校验 | `/api/sessions/{id}/messages` 未解析当前用户并核对会话归属 | 共用同一 HTTP 处理器 | 知道会话 ID 且持有服务访问凭证时，路径本身没有 owner 隔离；多用户／共享服务部署下有越权读取风险 |
| P0 | 每日简报主入口与阅读模型 | 工作台有入口，但正文仍为 `<pre>` | 顶部是收件箱，简报混在聊天流且为 `<pre>` | 双端都要改为独立 Markdown 简报视图 |
| P0 | 简报数据来源 | 电脑可生成和读取 | 无生成器、无电脑简报拉取 | 手机必须接同一控制面，不能只改 UI |
| P0 | 主对话／Worker 多会话 | Worker 被历史过滤，工作台只有执行摘要 | 无 Worker 会话列表与详情 | 新增统一会话投影，不伪造 Worker 对话 |
| P0 | Android SuperAgent 控制面 | 已装配 Coordinator／WorkTask／Worker／Briefs | 未装配上述服务 | 手机目前不是同一 SuperAgent 控制面的完整客户端 |
| P1 | 聊天到任务的结构化跳转 | Coordinator 的 `task_id/action` 被 HTTP 响应丢弃 | 同样没有任务卡 | 增加结构化响应和“查看任务／进入 Worker 对话” |
| P1 | 长期任务控制 | 电脑工作台较完整 | 只有单个旧中继 Job 的进度与取消 | 手机增加任务列表、详情、等待原因、追加指令和状态控制 |
| P1 | 审批与验收 | 工作台已有 ActionOutbox 和 Artifact 操作 | 无审批与 Artifact 验收；只有文件交付下载 | 下载不等于验收，手机需补齐两种不同动作 |
| P1 | 中继控制面分裂 | `/api/jobs` 与 `/api/work-tasks` 并存 | 后台执行只走 `/api/jobs` | 保留 AgentJob 作为执行单元，用户长期任务统一投影为 WorkTask |
| P1 | 渠道开关真实性 | 有真实连接器状态接口和桌面采集管理器 | 开关写 Android 本地 DB，桌面渠道无对应执行器；未展示连接状态 | `activity/wechat_desktop/web` 必须标为电脑远程能力，不能保存成功就暗示已采集 |
| P1 | 手机聊天配置桌面采集 | 电脑有 `list/configure_collection_channel` 工具 | Android 默认工具明确不包含这些工具 | 不应把桌面工具直接塞进手机；应通过授权远程控制命令调用电脑 |
| P2 | 简报历史与未读 | API 已有 `/api/briefs`，电脑未完整使用 | 只读 latest，失败静默变空 | 增加历史、版本导航、错误与离线状态 |
| P2 | 记忆治理 | 有云端记忆配置和状态 | 也有连接状态、待发／失败计数 | 不是“完全没共享记忆”；后续补事实确认、冲突与来源入口即可 |
| P2 | 诊断能力 | 工作台暴露部分健康与事件信息 | 基本缺失 | 只提供用户需要的等待原因和错误，不照搬内部 Trigger／EventLog 全量诊断 |

### 安全问题的影响边界

`handleSessionMessages` 当前直接按 URL 中的会话 ID 读消息，没有调用 `currentUser`，也没有像 `TurnService` 那样检查 `loaded.OwnerUserID`。证据：`internal/httpapi/chat.go:287-315` 对比 `internal/agent/runner.go:84-90`。

本机单用户模式下不一定表现为“跨账号”，但它违反项目所有端点按 owner 过滤的约束。修复时应返回 404，避免泄露会话是否存在，并覆盖主会话、Worker 会话和跨用户消息分页测试。

## 四、推荐目标架构

### 4.1 核心原则

1. **同一个管家，不是每端各起一个 Worker。** 电脑继续作为 WorkTask、Worker、ActionOutbox、Artifact 和桌面采集的事实源。
2. **手机是远程视图与控制器。** Android 保留语音、通知、无障碍和本机生活管家能力，不复制一套桌面 SuperAgent 后台，避免双写和状态分裂。
3. **共享记忆不等于共享任务状态。** 云端长期记忆继续复用；任务、简报、审批和会话目录需要单独的控制面传输与缓存。
4. **普通聊天保持快速路径。** 只有明确任务指令进入 Coordinator；普通问答不强制创建 Trigger／WorkTask。
5. **主聊天与 Worker 共用 TurnService。** 前端追加 Worker 指令仍走 WorkTask Inbox，不能直接往聊天表插入一条用户消息冒充已注入。
6. **提醒 Task 与长期 WorkTask 继续分离。** 原“收件箱”中的稍后处理也不是 WorkTask。

### 4.2 推荐部署形态

首版采用“电脑事实源 + 中继显式控制命令 + 手机只读缓存”：

```text
电脑 Go 服务（唯一任务事实源）
  ├─ WorkTask / Worker Session / AgentJob
  ├─ ActionOutbox / Artifact / Brief
  └─ 桌面连接器与共享记忆
             ↑
       配对中继（白名单命令，不任意代理 HTTP）
             ↑
Android 本地 BFF
  ├─ 远程控制命令与幂等键
  ├─ 简报／任务／会话只读缓存
  └─ 本机通知、语音、无障碍、文件下载
```

- 查询电脑离线时不排队，展示最近缓存并明确“离线数据 + 最后同步时间”。
- 追加指令等变更可排队，必须持久化幂等键；恢复后最多执行一次。
- 审批、验收等高-risk 操作首版要求电脑在线并返回真实版本结果，不把“已排队”显示成“已批准”。
- 中继只增加明确的业务命令和 schema，不开放任意本地 HTTP 代理。
- 后续若需要电脑长期离线也能查看，可再把任务／简报只读投影放云端；本轮不以此阻塞首版。

## 五、目标交互设计

### 5.1 手机主窗口

顶部保留：记忆、每日简报、任务、历史、设置。电脑配对与设备状态放到“设置 → 设备”，任务详情顶部同时显示执行设备状态。

#### 每日简报抽屉

- 标题、简报日期、版本、生成／更新时间；
- 使用现有安全 Markdown 子集渲染，不允许原始 HTML 注入；
- 今日／历史日期、同一日期版本导航；
- 重新生成、标记已读；
- 加载中、尚未生成、生成失败、电脑离线、缓存数据状态；
- 来源 ID 可展开查看，不直接暴露内部大段日志；
- 删除聊天流中的 `todayBrief` 重复卡片。

原通知收件箱移到二级入口，保留原搜索、snooze、clue、watch 功能和 API。

#### 历史／会话目录

分为：

1. **主对话**：与管家的日常问答；
2. **工作任务**：每个 Worker 会话带任务标题、状态、等待原因、未读数；
3. **委派 Agent**：CLI／BMA／A2A 的执行记录；有真实会话才显示对话，没有则展示 Job 输出与事件，不伪造聊天消息。

同一个管家继续使用同一 owner 和共享长期记忆；分会话不等于分裂人格或分裂记忆。

#### Worker 对话页

- 顶部：任务标题、状态、当前阶段、等待原因；
- 正文：完整 Worker 用户／助手消息，向上分页；
- 底部：追加指令输入框；
- 操作：暂停／继续／取消、查看交付物、去审批；
- 发送后显示“已进入任务 Inbox，将在安全步骤边界处理”，不能立即伪显示 Worker 已回答；
- 任务已验收或取消后禁止继续发送，提供“重新打开”或“创建后续任务”。

### 5.2 手机任务中心

分组只保留用户需要的三类：

- **待你处理**：等待补充、等待审批、待验收、失败需核对；
- **执行中**：queued／active／paused；
- **已完成**：completed／cancelled。

任务详情依次展示：最新进展 → Worker 对话 → 待审批／交付物 → 最近事件。高级授权、工具调用对账和内部诊断折叠展示，不照搬电脑全部表单。

### 5.3 电脑主对话与工作台

- 主对话历史改用统一会话投影，能打开主对话和 Worker 对话；
- Coordinator 命中任务后，在聊天里渲染可点击任务卡，而不是只返回 ID 文本；
- 工作台任务卡增加“查看 Worker 对话”，跳回主窗口对应 Worker 会话；
- 工作台继续负责任务控制、审批和 Artifact，不另建消息存储；
- 简报页改为 Markdown，复用与手机一致的内容语义；
- 执行段摘要保留为诊断信息，不再把它当成 Worker 对话替代品。

## 六、API 与数据改造计划

### 6.1 先修会话归属

`GET /api/sessions/{id}/messages`：

1. 解析当前用户；
2. 按会话 ID 读取 `ChatSession`；
3. 比对 `OwnerUserID`；
4. 不存在或跨 owner 均返回 404；
5. 再执行消息分页。

新增接口也必须执行相同校验，不能仅依赖列表页已经过滤。

### 6.2 新增统一会话投影，不改变旧 `/api/sessions`

建议：`GET /api/conversations?kind=all|main|worker|delegated&limit=&before=`

返回字段：

```json
{
  "id": "session-or-job-id",
  "kind": "main|worker|delegated",
  "title": "任务或会话标题",
  "preview": "最近一条有效预览",
  "message_count": 12,
  "updated_at": "...",
  "session_id": "可读取消息的会话 ID，可为空",
  "task_id": "WorkTask ID，可为空",
  "job_id": "AgentJob ID，可为空",
  "agent_id": "委派 Agent，可为空",
  "status": "active|waiting_user|completed|...",
  "waiting_kind": "none|clarify|approval|...",
  "unread_count": 0,
  "can_message": true,
  "source_device": "desktop|android|cloud"
}
```

实现来源：

- main／worker：`chat_session`  JOIN `work_task`／`agent_job`；
- delegated：`agent_job` + 事件／输出；`session_id` 为空时 `can_message=false`；
- 内部 router 会话继续排除；
- 空消息 Worker 会话也可按任务显示，不能因 `messages=0` 消失。

消息读取仍复用会话消息接口。未读首版可按设备保存 `conversation_read_cursor`，不能用全局已读让手机阅读后电脑角标也消失。

### 6.3 Worker 追加指令

继续使用：`POST /api/work-tasks/{id}/messages`

请求必须支持持久化 `idempotency_key`；客户端重试复用同一键。响应包含 `task/inbox/job/delivery`，前端据 `delivery` 显示“当前段安全边界消费”或“新执行段已排队”。不要改成普通 `/api/chat`，不要绕开 Inbox。

旧 `/api/worker-tasks/{id}/steer` 保持兼容；未绑定 WorkTask 的旧 Job 继续返回 409。

### 6.4 聊天结构化响应

`chatResponse` 增加可选字段：

```json
{
  "session_id": "...",
  "reply": "...",
  "task_id": "...",
  "action": "create|show|message|pause|resume|cancel|list",
  "task": {"id":"...","title":"...","status":"..."},
  "conversation": {"id":"...","kind":"worker"}
}
```

普通聊天响应保持不变。SSE `done` 使用同一 DTO，避免流式与非流式行为分裂。

### 6.5 中继协议扩展

新增白名单命令，而不是任意 URL 转发：

- `work_task_create/list/get/message/action`；
- `conversation_list/messages`；
- `outbox_list/approve/reject`；
- `artifact_get/accept/reject/deliver`；
- `brief_latest/list/get/generate`；
- `collection_list/configure`（需明确授权与桌面执行回执）。

每条命令带 `command_id/idempotency_key`、owner 绑定、schema 版本和必要 `expected_version`。响应区分：已执行、已排队、离线缓存、冲突、拒绝、结果未知。

旧 `job/job_status/job_cancel` 保留一个兼容周期；返回同时带 `job_id` 与可有的 `work_task_id`。AgentJob 仍是 Worker 或委派 Agent 的执行段，不要把所有 AgentJob 强行改名为 WorkTask。

### 6.6 简报与缓存

- 电脑 `/api/briefs*` 继续是事实源；
- Android BFF 将远程结果映射给手机页面，保留原 API 形状以降低 UI 改造成本；
- 本地缓存只保存简报展示所需数据与同步时间，不塞进长期记忆；
- 电脑离线时手动生成按钮禁用并解释原因；不得伪造新生成版本。

### 6.7 渠道状态与远程配置

手机渠道项必须显示三态：

- **本机可用**：如手机通知微信；
- **需电脑在线**：电脑微信、工作轨迹、授权网页；
- **未接入**：飞书、钉钉等尚无真实连接器的能力。

保存远程渠道配置必须等桌面真实回执；失败时保留原状态并提示。连接器健康来自桌面心跳，不用“历史上有消息”推断正在采集。

## 七、分阶段修复计划

### 阶段 0：安全与兼容基线（最先做）

1. 修复会话消息 owner 校验；
2. 增加主会话、Worker 会话、跨 owner、分页游标测试；
3. 固化旧 `/api/sessions`、`/api/jobs` 和 WorkTask API 的兼容测试；
4. 在 `AGENT.md` 明确新的 `/api/conversations` 投影，保留 Worker 不进入旧 `/api/sessions` 的兼容约束。

**完成标准**：owner A 不能读取 owner B 的任何会话消息；旧客户端不需要同步升级。

### 阶段 1：每日简报主链路

1. 手机顶部“收件箱”改为“每日简报”；
2. 新增独立简报抽屉，Markdown 渲染，移除聊天流卡片；
3. 原通知功能迁入设置二级入口，API 不删；
4. 电脑简报页同步支持 Markdown；
5. Android 通过中继读取电脑 latest/list/get/generate；
6. 增加历史、版本、错误、空状态和离线缓存。

**完成标准**：电脑生成简报后手机可看到同一版本；断网时明确显示缓存；不会再把 Markdown 源码当等宽正文；通知搜索与关键词等待仍可使用。

### 阶段 2：统一会话与 Worker 对话

1. 新增 `/api/conversations` 只读投影；
2. 手机历史分主对话／工作任务／委派 Agent；
3. 电脑历史和主窗口支持 Worker 会话；
4. 工作台增加“查看 Worker 对话”；
5. Worker 追加指令统一走 WorkTask Inbox；
6. Coordinator 聊天响应增加任务卡与跳转字段。

**完成标准**：双端都能从主聊天进入 Worker 完整对话并追加指令；任务推进后消息仍在原 Worker 会话；不会误写主会话或重复创建 Worker。

### 阶段 3：手机任务、审批与交付闭环

1. 手机任务中心接 WorkTask 列表／详情／事件；
2. 支持追加指令、暂停、恢复、取消、异常恢复、重新打开；
3. 增加 ActionOutbox 审批／驳回和版本冲突处理；
4. 增加 Artifact 查看、验收、要求修改、版本差异与下载；
5. 旧中继 Job 与新 WorkTask 做明确映射和兼容展示；
6. 离线变更持久化排队，查询只读缓存，审批／验收要求在线确认。

**完成标准**：手机发起长期任务后，可离开页面、断线重连、继续查看同一任务，补充指令、处理审批、验收结果；重试不重复执行，不出现“手机显示完成、电脑仍等待”的假状态。

### 阶段 4：渠道真实性与记忆治理

1. 手机渠道开关接桌面真实配置和连接器状态；
2. 未接入能力禁用或隐藏，远程能力标注设备与最后心跳；
3. 手机聊天通过授权远程命令配置电脑采集，不直接注册桌面工具；
4. 增加事实确认、冲突、来源与共享记忆同步详情入口；
5. 统一任务未读、简报未读与系统通知点击跳转。

**完成标准**：每个开关都能解释“在哪台设备生效、当前是否真的运行”；撤回授权后桌面停止采集；共享记忆状态与任务控制状态不再混淆。

## 八、验收与测试矩阵

### 8.1 Go／HTTP

- owner A／B 会话消息隔离，跨 owner 返回 404；
- 会话投影包含 main／worker／delegated，router 不出现；
- 空 Worker 会话、长消息分页、任务状态与未读数正确；
- Coordinator 流式／非流式响应都保留任务结构；
- WorkTask 追加指令幂等，安全步骤边界消费，CAS 冲突返回 409；
- 审批、Artifact 验收重复请求和旧版本请求不重复生效；
- 简报历史与版本不可变，已交付版本不被覆盖；
- 中继断线、重连、桌面重启后无丢失、无重复执行，unknown 不自动重试。

### 8.2 桌面

- 主对话、历史、工作台三处跳转到同一 Worker 会话；
- Worker 对话向上分页位置稳定，追加指令不污染主会话；
- 任务卡状态与 SSE 更新一致；
- 简报 Markdown、代码块、链接与原始 HTML 转义正确；
- 运行 `npm test` 与 `npm run check`，窗口改动后重新打包并做 smoke test。

### 8.3 手机 H5（390×844）

- 顶部“每日简报”替代“收件箱”，原通知功能仍可进入；
- 主对话／Worker／委派目录可切换，Worker 可查看与追加指令；
- 任务等待、审批、验收、空／错／离线状态完整；
- 长 Markdown、代码块、任务标题无横向溢出；
- 断网重试保留同一幂等键；
- 三份页面源保持同步：`web/`、`desktop/server/web/`、`android/gosvc/webassets/`；
- 使用 `npm run test:mobile-h5` 做回归，但模拟 Responses 不代表真实模型通过。

### 8.4 真实跨端与 Android 真机

H5 不能替代以下验收：

- 真实 TLS 中继、电脑离线、手机断网与长时间重连；
- Android Keystore、通知监听、语音唤醒、无障碍、ROM 保活；
- Artifact 原生下载、校验、保存／分享；
- 电脑微信与工作轨迹连接器的真实心跳和撤权停止；
- 真实模型下普通聊天不误建任务、明确任务指令可正确进入 WorkTask。

## 九、主要修改位置

| 模块 | 文件 | 修改方向 |
|---|---|---|
| 会话安全 | `internal/httpapi/chat.go` | owner 校验、结构化聊天响应 |
| 会话投影 | `internal/httpapi/conversations.go`（新增）、`internal/store/sessions.go` | 统一目录、Worker／任务关联、未读投影 |
| 任务控制 | `internal/httpapi/work_tasks.go`、`internal/worktask/service.go` | 保持 Inbox／CAS 语义，补客户端所需摘要 |
| 中继 | `internal/relay/protocol.go`、`phone.go`、`client.go`、`internal/httpapi/relay_phone.go` | 白名单 WorkTask／简报／审批／会话命令与离线语义 |
| Android | `android/gosvc/gosvc.go` | 远程控制 BFF 与只读缓存；不复制桌面 Worker |
| 手机 UI | `web/index.html` | 每日简报、统一会话、任务中心、审批／Artifact、真实渠道状态 |
| 电脑 UI | `desktop/renderer.js`、`workbench.js`、`assistant-api.js` | Worker 对话跳转、任务卡、Markdown 简报 |
| 架构约定 | `AGENT.md`、`.agent/progress.md` | 记录新投影、跨端事实源与验收证据 |

## 十、明确不做的事

1. 不把所有闲聊改成 SuperAgent Trigger 或 Worker；
2. 不在 Android 再启动一套桌面 Worker／Router，避免双事实源；
3. 不删除原通知收件箱能力；
4. 不把提醒 Task、通知 snooze 与长期 WorkTask 合并；
5. 不把 Job 事件列表伪装成 Worker 对话；
6. 不让手机绕开 WorkTask Inbox 直接写 Worker 聊天记录；
7. 不把文件“已下载”当成 Artifact“已验收”；
8. 不把本地渠道开关“保存成功”当成桌面连接器“已运行”；
9. 不把 Trigger、EventLog、Watchdog 等全部内部诊断照搬到手机；
10. 不以 H5 模拟结果宣称真机、真实模型或真实中继已验收。

## 十一、最终发布门槛

只有同时满足以下条件，才能称为“双端跟上 SuperAgent 架构”：

- 手机与电脑看到的是同一 WorkTask、Worker 会话、审批和 Artifact 状态；
- 主对话、Worker 对话、委派执行记录都有明确入口，且边界真实；
- 每日简报可生成、可读、可追溯版本，手机不再依赖本地空数据；
- 任务追加指令、审批与验收不会丢失或重复，离线状态不造假；
- 采集渠道开关对应真实设备和连接器状态；
- 会话 owner 隔离测试通过；
- H5、桌面自动测试与真实跨端／真机验收分别留证，不相互冒充。
