# Super-agent 个人 Agent 架构改造计划书

| 项目 | 内容 |
|---|---|
| 版本 | v0.2 |
| 日期 | 2026-11-01 |
| 状态 | 架构评审结论已收敛，待按 P0 执行，尚未实施 |
| 目标形态 | 单活跃大脑 + 电脑端 + 随身个人端（Android/Web） |
| 参考 | DeepSeek Harness 团队《Super-agent 设计探索：迈向更自主的 Agent 系统》 |
| 关联文档 | [项目大纲与技术报告](./项目大纲与技术报告.md)、[API 手册](./API手册.md)、[三端拆分与中继协议](./三端拆分与中继协议.md)、[AGENT.md](../AGENT.md) |

---

## 0. 摘要与评审结论

当前金手指是“单会话聊天 + 按需工具调用 + 本地工作轨迹”的个人 Agent。它已具备对话、记忆、提醒、CLI/BMA、手机通知、桌面工作轨迹和中继能力，但距离 Super-agent 的连续、高效、主动、持久仍有三个结构性缺口：

1. **没有统一事件模型**：Android 通知、常备意图、定时提醒、中继命令和桌面活动没有被同一套 Trigger 生命周期管理。
2. **没有 Thread 与 Router/Worker**：Session 是孤岛，主对话同步执行长任务，缺少跨任务协调和隔离并发。
3. **没有跨会话/跨渠道召回**：历史主要加载当前 Session，长期记忆没有 Thread 摘要、事件游标和引用图。

本计划先建立 **Trigger → Thread → Router → Worker → ActionOutbox** 运行骨架，再依次接入邮件、日历和微信。评审后确定以下不可变决策：

- **单活跃大脑**：任意时刻只有一套 Router、WorkerManager 和权威 SQLite；第一阶段运行在电脑，之后可整体迁移到常开小主机/NAS。
- **大脑与桌面壳解耦**：Electron 是交互端、桌面传感器和执行环境，不是架构上唯一可运行大脑的位置。
- **手机不运行第二套 Super-agent**：电脑在线时远程优先；离线时采集、排队和本地提醒，不运行轻量 Router，不分叉主记忆。
- **复用现有任务地基**：`agent_job` 是唯一 Agent 执行任务记录，不另建一套平行 Worker 队列。
- **动作与提醒分离**：新增 `agent_action_outbox` 管理草稿、审批和外部副作用；现有 `/api/outbox` 继续表示提醒投递。
- **中继只转发，不保存权威业务状态**：电脑维护递增事件序号；手机以增量补拉为准，推送只是“有更新”的提示。
- **默认人工审批**：首版不提供自动发送白名单；所有邮件、微信和日历写入都必须经 ActionOutbox 审批。
- **小闭环优先**：先用现有聊天、通知和定时器验证核心，再做邮件/日历，最后做微信和高级自治。

**渠道边界**：邮件与日历可通过标准协议获得结构；个人微信只能读取通知和用户明确授权下的可见内容，不承诺完整历史、成员列表和撤回消息，不做数据库解密、hook、注入或非官方协议。

---

## 1. 已核实现状与问题

### 1.1 当前实现

| 能力 | 已核实现状 | 关键位置 |
|---|---|---|
| 对话入口 | `/api/chat`、`/api/chat/stream` 同步跑完一轮 Agent | `internal/httpapi/chat.go:106` |
| Agent 循环 | 单 Session、最多 6 步工具循环，已有进度与澄清事件 | `internal/agent/loop.go:64` |
| 会话持久化 | 历史只还原 role/content；工具调用与结果尚未完整落库 | `internal/store/sessions.go:108` |
| 异步任务 | `agent_job` 已有 CAS、心跳、取消、幂等键和 interrupted 恢复 | `internal/agenthub/async.go`、`migrations/0005_agent_jobs.sql` |
| 任务 API | `/api/jobs` 已装配；`/api/agent-jobs` 因 Dispatcher 未传入而 503 | `cmd/server/main.go`、`internal/httpapi/agent_jobs.go` |
| 通知中心 | `inbox_event` 去重、提取线索和检索，但不启动 Agent | `internal/inbox/service.go:61` |
| 定时器 | 负责提醒、免打扰和每日摘要，是独立局部机制 | `internal/scheduler/scheduler.go` |
| 提醒 Outbox | `/api/outbox` 是现有提醒内存队列，不是动作审批 Outbox | `internal/httpapi/server.go:64` |
| 手机中继 | 仅手机→电脑命令和对应结果；电脑→手机主动事件尚未实现 | `internal/relay/protocol.go` |
| 中继队列 | 默认内存、TTL 队列；电脑端命令去重也是内存窗口 | `internal/relay/hub.go`、`internal/relay/client.go` |
| 手机本地服务 | gosvc 当前带完整 chat、memory、scheduler 和独立 SQLite，事实上存在第二大脑 | `android/gosvc/gosvc.go` |
| 桌面工作轨迹 | 本地 JSONL；已被 Go 作为聊天上下文消费，但未 Trigger 化 | `desktop/activity-trail.js`、`desktop/main.js:983` |
| 设置与凭据 | settings 支持模型配置和热加载，但密钥仍以明文文件保存 | `internal/settings/settings.go` |
| 桌面 UI | 460×680 主挂件 + 独立辅助窗/工作轨迹浮窗；渲染进程经 IPC 访问服务 | `desktop/main.js`、`desktop/preload.js` |

### 1.2 结构性缺口

- **会话孤岛**：跨 Session、跨渠道信息缺少统一 Thread 和按需召回。
- **主动能力弱**：通知、意图和定时器没有统一的“事件 → 策略 → 行动 → 审批”链路。
- **并发能力弱**：主对话同步执行工具循环；现有异步任务未绑定 Thread 和独立 Worker Session。
- **异步语义分散**：jobs、agent-jobs、relay job、scheduler 和 inbox 各自表达部分任务语义。
- **双端状态可能分叉**：手机已有独立聊天和记忆，远程模式与离线模式没有清晰边界。
- **远程安全基础不足**：HTTP 认证仍是桩实现；新增 Trigger 和审批接口前必须补齐所有者校验与权限范围。
- **离线可靠性不足**：手机通知在本地服务不可达时可能丢失，中继内存队列不能作为持久保证。

### 1.3 目标

- **连续**：同一事项在电脑、手机和不同渠道中可通过 Thread 找回上下文。
- **高效**：主对话不被长任务阻塞，多个 Worker 隔离并发，用户无需管理 Session。
- **主动**：环境事件经确定性规则和语义策略筛选，只有值得处理的事项才行动或打扰。
- **持久**：Trigger、Thread、Worker、ActionOutbox 和事件序号持久化；重启可恢复，离线可补拉。
- **可控**：用户能解释每次主动行为、查看证据、介入任务、审批或拒绝外部动作。

### 1.4 非目标

- 不做个人微信数据库解密、客户端 hook、注入或非官方协议。
- 不承诺微信完整历史、群成员列表和撤回消息。
- 不做云端多租户、团队协作或跨组织 A2A。
- 不在电脑离线时运行第二套手机 Router。
- 不在首版提供自动发送白名单。
- 不替换现有 BMA/CLI 编程能力；编程任务仍优先交给 Claude Code/Pi。
- 不让 Router 挂 Bash、文件写入或桌面操控工具。
- 不把参考文章中的实验数据直接当成本项目性能承诺。

---

## 2. 设计原则

1. **单活跃大脑**：Router/Worker/权威状态只有一套；部署位置可迁移，但不能双活。
2. **交互端与执行环境分离**：电脑和手机都可以是界面、传感器和执行环境，不因此各建一套大脑。
3. **Thread 是一等对象**：Session 是实现细节，用户围绕事项、任务和审批交互。
4. **Router 轻、Worker 强**：Router 负责全局协调和对外交互；Worker 负责具体执行。
5. **权限来自用户策略，不来自事件正文**：不可信内容可触发受限 Worker，但不能自行扩大工具或数据权限。
6. **副作用统一审批**：所有外部发送和写入只能经 ActionOutbox，Worker 不直接调用发送器。
7. **至少一次传输、最多一次执行**：所有命令、Trigger 和发送动作都有持久幂等键。
8. **推送是提示，同步是真相**：桌面 SSE 和手机增量同步都以持久事件序号恢复。
9. **摘要 + 按需召回**：上下文只内联近期高价值信息，完整内容留在库中按 ID 展开。
10. **渐进兼容**：新表和字段采用加法迁移，功能开关默认关闭，旧聊天和旧客户端保持可用。

---

## 3. 目标架构

### 3.1 总体结构

```text
                    ┌─────────────────────────────────┐
                    │ 单活跃大脑：第一阶段 PC           │
                    │ 后续可整体迁移到小主机 / NAS       │
                    │                                 │
                    │ Connector → EventHub → Router    │
                    │                    │             │
                    │               WorkerManager      │
                    │                    │             │
                    │               ActionOutbox       │
                    │                                 │
                    │ SQLite：Thread / Trigger / Job    │
                    │         Action / Event / Memory  │
                    └───────────┬─────────────────────┘
                                │
                本地 HTTP / 配对中继 / 事件提示 + 增量同步
                  ┌─────────────┴─────────────┐
                  │                           │
          ┌───────▼────────┐          ┌───────▼─────────┐
          │ 电脑端 Electron │          │ Android/Web      │
          │ 主对话 + 工作台 │          │ 对话/动态/待办    │
          │ 审批与设置       │          │ 审批与连接状态    │
          │ UIA/OCR/工作轨迹│          │ 通知/语音/无障碍  │
          │ 本地执行环境     │          │ 本地执行环境      │
          └─────────────────┘          └──────────────────┘
```

### 3.2 三层职责

| 层 | 包含 | 权威状态 |
|---|---|---|
| 控制层 | EventHub、Router、WorkerManager、Watchdog | 只在单活跃大脑 |
| 状态层 | SQLite、Thread/记忆索引、ActionOutbox、事件日志 | 只在单活跃大脑；手机仅缓存/队列 |
| 环境层 | Electron、Android、桌面/手机工具、渠道连接器 | 留在设备原处，按需执行 |

第一阶段控制层和状态层与电脑环境同机部署，但代码边界不得假设永远同机。

### 3.3 组件职责

| 组件 | 职责 | 不做什么 |
|---|---|---|
| Connector | 把外部渠道转成 Trigger，维护游标、健康状态和凭据调用 | 不判断是否执行任务 |
| EventHub | 持久去重、规则过滤、批处理、订阅直通 | 不让 LLM 参与确定性过滤 |
| Router | 理解用户消息/候选事件，决定忽略、通知、回复、建 Worker 或请求澄清 | 不执行重工具 |
| WorkerManager | 基于 `agent_job` 限并发、心跳、取消、等待和恢复 | 不直接对外发送 |
| Worker | 在独立 Session 中执行，产出结果、证据、草稿和澄清问题 | 不读取其他 Worker 原始上下文 |
| ActionOutbox | 保存草稿、审批、版本、幂等发送和结果 | 不绕过权限 |
| EventLog | 保存递增序号，供 SSE、手机同步和审计恢复 | 不代替业务表 |
| Watchdog | 检查心跳、无进展、连接器滞后和队列堆积 | 不替代 Router 做业务决策 |

### 3.4 同步与异步路径

**用户主对话**：

```text
用户消息 → /api/chat/stream → Router
  ├─ 简单问答：直接回复
  ├─ 需要执行：创建 agent_job，立即返回任务卡
  └─ 需要澄清：追问，答复后创建或恢复任务
```

**环境事件**：

```text
Connector → Trigger → EventHub 规则过滤
  ├─ 已有明确订阅 → 按 worker_task_id 直通 Worker 邮箱
  └─ 未订阅 → 批次进入 Router
                    ├─ ignore / notify / reply
                    ├─ create_worker / route_to_worker
                    └─ ask_user
```

Worker 完成后在同一事务中写结果、Thread 更新和 EventLog；需要副作用时再创建 ActionOutbox。Router 负责统一汇总，不暴露 Worker 原始推理过程。

### 3.5 在线与部署等级

| 等级 | 部署 | 能力承诺 |
|---|---|---|
| L0 本期默认 | 大脑与电脑同机 | 电脑唤醒时主动工作；休眠时明确显示离线 |
| L1 推荐常开 | 同一 Go 服务与 SQLite 移到小主机/NAS | 7×24 Router/连接器；电脑和手机按需接入 |
| L2 后续评估 | 云端控制层 + 端侧 daemon | 需重新评审凭据、提示词注入和远程执行风险，不属于本期 |

中继不升级为云端大脑，也不保存 Thread、记忆或审批权威状态。

---

## 4. 电脑端与随身个人端设计

### 4.1 电脑端：主对话 + 独立工作台

**悬浮主对话保持单职责**，不在 460×680 挂件中塞入完整管理页面。消息流新增：

- 任务卡：目标、状态、耗时、查看、追加指令、取消。
- 审批卡：来源、接收对象、正文、风险，支持编辑、批准和拒绝。
- 澄清卡：问题回到主对话，答复绑定原任务并恢复执行。
- 主动建议卡：说明触发原因和证据，可忽略或静音同类事件。

**新增一个“工作台”辅助窗**，复用 `openAuxWindow` 和现有 tabs 模式，不拆成三个独立窗口：

1. **今日**：待审批、待澄清、运行中任务、即将开始的日程和每日摘要。
2. **动态**：统一 Thread 时间线，按渠道、项目、未读和采集状态筛选。
3. **任务**：运行、等待、完成、失败、中断；支持介入、取消和只读重试。
4. **审批**：草稿编辑、引用证据、接收对象、版本、发送结果和失败重试。

**工作轨迹浮窗保持独立**，只增加“作为传感器上报”开关和采集状态，不与业务 Thread 时间线合并。

**设置**复用 `agents.html` 设置区，新增 Super-agent 卡片：模型角色、渠道、权限、免打扰、保留期限和预算。渠道凭据只能进入 SecretStore。

### 4.2 随身个人端：远程入口 + 传感器 + 快速验收

手机 Web/Android 调整为四个主要入口：

- **对话**：远程 Router 主入口，展示任务、审批和澄清卡。
- **动态**：Thread 摘要和渠道筛选，明确标记电脑/手机采集来源。
- **待办**：合并待审批、待澄清和失败待处理，是个人端首要页面。
- **我的**：电脑连接、通知权限、采集状态、免打扰和隐私设置。

交互约束：

- 默认显示任务摘要，工具轨迹按需展开，减少中继流量和打扰。
- 普通内部待办可快速处理；外部发送和高风险工具必须打开详情二次确认。
- 电脑在线时，聊天和记忆默认走电脑；本地 gosvc 不再默认创建平行主会话。
- 电脑离线时只提供采集、持久排队、本地提醒和已缓存内容，不运行轻量 Router。
- 首版不开放“离线本地聊天后自动合并主记忆”；如保留旧聊天，必须标记为独立本地会话。
- 微信入口持续显示“仅通知级”或“仅可见内容”，不得伪造完整能力。

### 4.3 打扰等级

每个 Trigger 的处理结果必须对应一级：

| 等级 | 表现 | 示例 |
|---|---|---|
| silent | 只进入动态 | 普通通知、低价值工作轨迹 |
| badge | 工作台/手机待办角标 | 草稿完成、非紧急任务结果 |
| toast | Windows/Android 通知 | 重要邮件草稿、任务失败 |
| interrupt | 主对话请求处理 | 即将到期的审批、关键澄清 |

免打扰只延迟 toast/interrupt，不丢弃事件；安全紧急停止不受免打扰影响。

---

## 5. 渠道方案

### 5.1 能力矩阵

| 渠道 | 获取结构 | 本期方式 | 关键限制 |
|---|---|---|---|
| 邮件 | 邮件串、参与者、正文、附件、引用 | IMAP 轮询/IDLE + SMTP | 首版仅应用专用密码；OAuth 后补 |
| 日历 | 事件、时间、参与者、重复和变更 | 首版只读 ICS；CalDAV 后补 | 时区、重复实例和改期必须测试 |
| 微信电脑版 | 通知、当前可见消息 | P0 探测后选择 L1/L2/L3 | 无完整历史，受 UI/窗口状态影响 |
| 微信手机 | 通知标题/正文/时间 | 现有通知监听 + 持久离线队列 | 聚合、延迟和关闭通知会漏采 |
| 桌面活动 | 应用、窗口、页面摘要和证据 | 现有 activity-trail 摘要上报 | 默认低优先级，不默认上传截图 |
| 定时器/用户聊天 | 时间事件和可信用户输入 | 适配现有 scheduler/chat | 保持旧 API 兼容 |

### 5.2 邮件

- 游标使用 `UIDVALIDITY + UID`，处理邮箱重置。
- 以 `References` / `In-Reply-To` 根 Message-ID 建 Thread；缺失时才用规范化主题、参与者和时间窗回退。
- 正文转纯文本，附件保存元数据和 hash；原始敏感内容不进入日志。
- 回复草稿必须包含接收人、主题、`In-Reply-To` / `References`。
- SMTP 发送只能由 ActionOutbox 发送器执行；首版无自动发送。

### 5.3 日历

- P2 只做 ICS 订阅和只读事件，不在同一阶段加入 CalDAV 写入。
- 保存 UID、recurrence-id、原始时区和实例起止时间；改期后取消旧提醒。
- 事件可建 Thread，参与者通过身份映射关联，但低置信度身份不得自动合并。

### 5.4 微信

- **L1**：Windows/Android 通知，低带宽 Trigger。
- **L2**：用户显式开启的电脑窗口 UIA/OCR 只读观察，显示采集状态，可暂停。
- **L3**：用户明确发起的一次性可见内容快照，不后台持续滚动。
- 跨设备重复以关联/合并视图处理，保留来源记录，不物理删除原始证据。
- P0 只记录字段可用性、成功率和脱敏样本，不保存用户聊天正文。
- P0 是独立探测任务，不阻塞 P1/P2。

---

## 6. 核心对象与数据模型

新增 `migrations/0007_superagent.sql`。所有业务表带 `owner_user_id`；时间由 Go 写入；迁移保持加法，不删除旧表。

### 6.1 Trigger

字段包括：来源、外部 ID、Thread、参与者、类型、标题、正文、payload、接收时间、去重 hash、敏感级别、可信标记、状态和路由结果。

持久状态只使用：

```text
new → filtered | queued | ignored | failed
queued → routed | ignored | failed
routed → done | failed
```

`batched` 是运行时状态，不写入状态机。去重优先 `owner + channel + external_id`；无外部 ID 时使用带时间桶的 hash。重复投递必须返回原 Trigger，不重复创建 Worker。

### 6.2 Thread

`agent_thread` 保存渠道、外部 ID、标题、参与者、滚动摘要、状态、最近事件时间，以及：

- `summary_event_id`：摘要覆盖到哪个事件；
- `summary_updated_at`：防止旧摘要覆盖新摘要；
- `retention_class`：正文和证据保留策略。

`agent_thread_link` 保存 `same_people`、`same_project`、`calendar`、`manual` 等关系、置信度和理由。自动关联只作为候选，用户可确认或解除。

### 6.3 WorkerTask：复用 agent_job

不创建 `agent_worker_task`。对 `agent_job` 增加可选列：

```text
thread_id
session_id
parent_trigger_id
profile
priority
result_summary
waiting_kind          none | clarify | approval
waiting_payload_json
version
```

- 原 `status` 保持兼容；`waiting_kind != none` 时 UI 展示逻辑状态 `waiting`，并释放并发槽。
- `agent_job_event` 继续保存任务步骤和状态事件。
- `agent_thread_subscription` 使用 `worker_task_id REFERENCES agent_job(id)`。
- `/api/jobs` 保持现有任务接口；`/api/agent-jobs` 接入同一 JobService，仅作兼容；`/api/worker-tasks` 是 Super-agent 任务的过滤视图。
- 所有领取、取消、等待、恢复和审批回调都使用状态/版本 CAS。

### 6.4 ActionOutbox

新表 `agent_action_outbox` 至少包含：

```text
id, owner_user_id, channel, thread_id, worker_task_id
kind, target_json, body, payload_json
status                  draft | pending_approval | approved | sending |
                        sent | failed | send_unknown | rejected | cancelled
requires_approval
idempotency_key
version
approved_by, approved_at
provider_message_id
last_error
created_at, updated_at, sent_at
UNIQUE(owner_user_id, idempotency_key)
```

审批请求携带 `outbox_id + version`；编辑后版本递增。重复审批返回同一结果，发送器以持久幂等键保证应用内最多提交一次。若 SMTP 等渠道在提交后、确认前断线，状态进入 `send_unknown`，保存渠道 Message-ID 并要求人工核对，不自动重试。旧 `/api/outbox` 不改名、不改语义。

### 6.5 EventLog 与同步

新增 `agent_event`：

```text
id INTEGER PRIMARY KEY AUTOINCREMENT
owner_user_id
kind
entity_type
entity_id
payload_json
created_at
```

- Trigger、Thread、Job、ActionOutbox 更新与 EventLog 在同一 SQLite 事务提交。
- 桌面 SSE 支持 `Last-Event-ID`，断线后补拉。
- 手机保存每通道 `last_synced_event_id`，通过 `sync` 拉取之后的权威事件。
- EventLog payload 只放同步所需摘要，完整正文从对应业务 API 获取。
- 清理业务正文时保留可重放所需的实体状态，不把 EventLog 当无限增长的正文仓库。

### 6.6 会话、身份与记忆

- `chat_session` 增加 `kind`、`thread_id`、`summary`。
- Worker Session 持久化必要工具调用和结果，恢复时按需加载，不全量回放。
- `person_identity` 关联邮箱、微信显示名和日历参与者；低置信度只提示，不自动合并。
- `mem_episode.source_id` 使用 `thread:<id>`、`worker:<job_id>` 等来源标记。
- 现有 `memory.context_token_budget` 配置需接通，不再只使用硬编码 1500 token。

---

## 7. 后端与协议设计

### 7.1 模块划分

```text
internal/superagent/
  hub.go          接入、去重、过滤、批处理、订阅直通
  router.go       决策、汇总、用户回复
  worker.go       agent_job 并发、邮箱、心跳、等待、取消、恢复
  outbox.go       ActionOutbox 审批与发送协调
  recall.go       Thread/记忆按需召回
  prompt.go       提示词、JSON Schema、确定性回退
  watchdog.go     确定性健康检查；LLM 判断后补

internal/connectors/
  connector.go
  email/
  calendar/
  wechat/
```

### 7.2 TurnService 抽取

从 `internal/httpapi/chat.go` 抽取 `internal/agent/runner.go`：

```go
type TurnService struct { /* 现有依赖 */ }
func (s *TurnService) Run(ctx context.Context, req TurnRequest) (*TurnResult, error)
```

- HTTP 和 Worker 共用消息持久化、Agent Run 和事件回调。
- 第一次抽取不改变现有聊天行为，先通过现有测试再接入 Router。
- Worker 请求带 `kind=worker`、`thread_id` 和受限工具策略。

### 7.3 Router 与 Worker 权限

Router 最多两步，只可使用 `recall_thread`、`search_memory`、`list_active_workers`、`create_worker`、`reply_to_user`。

`route_to_worker` 必须同时提供 `trigger_id + worker_task_id`；仅 `thread_id` 不足以表达多对多订阅。`create_worker` 必须有目标、profile、优先级和来源 Trigger。

Worker 权限按任务级别授予：

| 级别 | 能力 | 默认策略 |
|---|---|---|
| read | 搜索、读取、总结、日程冲突检查 | 可按渠道策略自动 |
| internal | 建草稿、更新摘要、创建内部任务 | 可自动，保留审计 |
| external | 发邮件/微信、写日历、高风险工具 | 必须审批 |

邮件或群消息中的“忽略之前指令”“发送密码”等文字永远只是不可信数据。提示词不是唯一防线，工具层必须再次校验权限。

### 7.4 Watchdog 与恢复

- P1 即实现确定性 Watchdog：心跳超时、重复无进展、连接器滞后、ActionOutbox 堆积。
- 启动时 `running → interrupted`，默认不盲跑；只读任务可在明确配置后重试一次。
- `waiting_kind != none` 不占 Worker 并发槽；审批/澄清回调按版本恢复。
- LLM Watchdog 和自动修正策略放到 P4，不作为首版依赖。

### 7.5 HTTP API

| 方法 | 路径 | 用途 |
|---|---|---|
| POST/GET | `/api/triggers` | 写入和查询 Trigger |
| GET | `/api/threads`、`/api/threads/{id}` | Thread 列表、详情和关联 |
| POST | `/api/threads/{id}/link` | 手动关联/解除 |
| GET | `/api/worker-tasks`、`/api/worker-tasks/{id}` | Worker 视图 |
| POST | `/api/worker-tasks/{id}/cancel`、`/steer` | 取消和追加指令 |
| GET | `/api/action-outbox` | 草稿与审批列表 |
| POST | `/api/action-outbox/{id}/approve` | 带版本的编辑/批准 |
| POST | `/api/action-outbox/{id}/reject` | 拒绝 |
| GET | `/api/events` | 持久 EventLog 的 SSE/补拉 |
| GET | `/api/superagent/health` | 连接器、Worker 和队列健康 |

现有 `/api/chat`、`/api/chat/stream`、`/api/jobs`、`/api/outbox` 保持兼容。所有新端点必须验证身份、所有者和动作 scope；`/api/triggers` 不能只依赖“本机默认用户”。

### 7.6 中继 v2

协议新增 `event`、`sync` 和带序号的 `ack`，版本协商失败时旧客户端继续忽略新类型。

- `event`：电脑→手机提示某实体有更新，携带 `event_id/entity_type/entity_id`，不是完整业务快照。
- `sync`：手机以 `last_synced_event_id` 拉取增量，响应带 `next_event_id/has_more`。
- 审批、取消、追加指令复用 `command`，payload 带幂等键和资源版本。
- 电脑持久保存命令 receipt；重启后重复 command 不再次执行。
- 手机先把待发送命令和传感器事件写入本地持久队列，再尝试中继发送。
- 中继内存队列只表示传输暂存；UI 区分“手机已保存”“中继已接收”“电脑已接收”“已执行”。
- 配对通道只授予明确 scope；远程聊天、审批和设备执行不能共用无边权权限。

---

## 8. 配置、安全与隐私

### 8.1 配置

```yaml
superagent:
  enabled: false
  max_workers: 3
  trigger_batch_ms: 2000
  trigger_batch_max: 20
  quiet_hours: "22:00-08:00"
  retention_days: 90
  daily_token_budget: 0

connectors:
  email:
    enabled: false
  calendar:
    enabled: false
  wechat_desktop:
    enabled: false
  wechat_mobile:
    enabled: true
```

settings 增加 `router_model`、`worker_model` 及各自 reasoning effort，复用现有模型方案，不新建平行的模型供应商配置。

### 8.2 凭据与远程权限

- Windows 使用 DPAPI，Android 使用 Keystore；业务表和 settings 只保存 `secret_ref`。
- 凭据由 Connector/SecretStore 直接使用，不进入模型上下文、日志或中继 payload。
- 新远程 API 上线前完成 token、所有者校验和 scope；pairing token 不出现在 URL query 或日志中。
- 高风险设备工具沿用 Guard/急停，Super-agent 不绕过现有安全链。

### 8.3 审计与保留

审计记录 Trigger 过滤原因、Router 决策、Worker 状态、审批版本和发送结果，不记录完整敏感正文。

默认保留：Trigger 正文 90 天、附件 90 天、ActionOutbox 30 天、截图证据沿用 3 天；Thread/Worker 结果摘要可长期保留。用户可清空 Thread 和相关未总结队列。

---

## 9. 分阶段执行计划

每个阶段按顺序执行，步骤应可独立提交和验收；功能开关默认关闭。P0 的微信探测可与主线并行，但不能阻塞核心闭环。

### P0：基线、安全与语义收敛

**目标**：不增加 Super-agent 功能，先消除会让后续返工的基础问题。

1. **冻结基线**：运行 Go、桌面和 Android 现有测试，记录当前 API、任务和提醒语义。
2. **统一任务入口**：修复 `/api/agent-jobs` 的 JobService 装配；确认 `/api/jobs` 为现有执行入口，兼容接口共用同一实现。
3. **补身份边界**：修复现有 Session/Job 的 owner 校验，并为 Trigger 等新接口制定强制 owner 校验；制定本地 token 与配对 scope，禁止匿名任意用户回退访问新 API。
4. **确定存储契约**：评审 0007 加法迁移，确认复用 `agent_job`、新 `agent_action_outbox`、新 `agent_event`，不占用旧 `/api/outbox`。
5. **建立 SecretStore 接口**：先提供 Windows DPAPI 和非敏感引用，P2 不允许绕过它保存邮箱凭据。
6. **中继契约评审**：确定 v2 envelope、持久 command receipt、手机持久待发队列和 UI 排队语义。
7. **并行微信 P0**：输出字段矩阵和 L1/L2/L3 建议，不保存正文。

**退出标准**：现有功能不回归；任务兼容接口可用；新 API 鉴权方案、迁移草案和中继 v2 契约评审通过；微信探测只影响 P3，不影响 P1 开工。

### P1：单大脑最小闭环

**目标**：只用聊天、手机通知和 scheduler，验证 Trigger → Router → Worker → 结果/审批 → 双端同步。

1. **迁移与仓储**：实现 Thread、Trigger、ActionOutbox、EventLog、agent_job 扩展和事务性事件写入。
2. **无行为变化抽取 TurnService**：HTTP 旧路径先切换到共用执行器并跑回归。
3. **EventHub**：接入手机通知、scheduler 和用户聊天；完成去重、过滤、批处理和状态机。
4. **WorkerManager**：复用 agent_job，完成并发、邮箱、等待、取消、心跳、中断恢复和幂等。
5. **最小 Router**：先支持 ignore/notify/reply/create_worker/route_to_worker/ask_user；JSON 失败走确定性回退。
6. **ActionOutbox 与确定性 Watchdog**：先支持内部草稿/通知，不接外部发送器；完成超时和队列检查。
7. **召回 v1**：旧聊天摘要、Thread 最近事件和 `recall_thread`；暂不做复杂实体图。
8. **事件分发**：实现 `/api/events`、Last-Event-ID、relay v2 event/sync/ack 和电脑持久 receipt。
9. **电脑端**：主对话任务/审批/澄清卡；一个工作台辅助窗；保留工作轨迹浮窗。
10. **手机端**：远程优先聊天、动态、待办和连接状态；离线持久排队；不启用第二 Router。
11. **打包与回归**：新桌面文件加入 check、pack 白名单和 smoke；Web 资源同步到 Android。

**退出标准**：

- 两个 Worker 并行时主对话仍可响应。
- 同一通知重复投递只生成一个 Trigger/Worker。
- 重启后 running 任务变 interrupted，不重复执行。
- 审批旧版本不能覆盖新草稿，重复审批不重复执行。
- 手机断线重连可按序号补齐，电脑重启后重复 command 不再次执行。
- 电脑离线时手机明确显示本地已排队，不伪造电脑已接收。
- 旧聊天、旧 `/api/jobs` 和旧 `/api/outbox` 保持可用。

### P2：邮件 + 只读日历垂直闭环

**目标**：完成第一个真实外部渠道闭环。

1. 实现 IMAP 同步、MIME/附件、Thread 映射和重复同步测试。
2. 实现邮件 Worker 和结构化草稿，所有回复进入 ActionOutbox。
3. 实现带版本的桌面/手机审批和 SMTP 幂等发送。
4. 实现 ICS 只读同步、重复实例、改期、取消和时区处理。
5. 接入身份映射 v1，低置信度身份只允许人工确认。
6. 增加渠道设置、SecretStore 凭据引用和连接器健康页。

**退出标准**：测试邮箱完成“新邮件 → Thread → 草稿 → 电脑或手机审批 → SMTP 发送 → Thread 回填”；重复同步和重复审批不重复发送；ICS 改期/时区测试通过。

### P3：微信接入

**目标**：按已验证能力接入，不扩大承诺。

1. 按 P0 结论实现 L1，按需增加 L2/L3。
2. 手机通知进入统一 Trigger，保留 inbox 兼容。
3. 完成电脑/手机同 Thread 去重和来源保留。
4. 增加观察开关、采集指示、暂停和能力限制说明。
5. 如实现发送，必须走 ActionOutbox；首版仍不允许自动发送。

**退出标准**：只承诺 P0 已验证字段；双端重复事件不产生重复 Worker；采集状态可见、可暂停、可审计。

### P4：高级异步与自治

**目标**：在核心稳定后优化，不作为首版前置条件。

1. 长工具异步 `start/stop`，结果作为新事件回到 Worker。
2. LLM Watchdog 判断偏离和无实质进展，仍受确定性权限限制。
3. 只读/草稿类投机执行；未采用结果静默丢弃。
4. 渠道输出规范：IM 短结论，邮件/文档结构化。
5. 指标、token 预算和每日成本面板。
6. 评估是否允许极窄自动发送白名单；默认仍关闭，需单独安全评审。

---

## 10. 测试、验收与发布

### 10.1 必测项

- Trigger 去重、状态机、过滤和批处理。
- Thread 映射、摘要游标、关联与身份置信度。
- 多对多订阅按 `worker_task_id` 正确直通。
- agent_job 并发、等待、取消、心跳、重启和幂等。
- ActionOutbox 版本、重复审批、应用内最多提交一次，以及 `send_unknown` 不自动重试。
- EventLog 事务一致性、SSE 补拉、手机增量同步和持久 command receipt。
- 外部提示词注入不能扩大工具权限；未审批不能发送。
- 未认证/跨 owner 请求必须失败；日志和模型请求不包含密钥。

### 10.2 端到端场景

1. 主对话同时发起两个任务，仍可继续聊天并追加指令。
2. 电脑重启、手机断网后恢复，状态一致且无重复执行。
3. 邮件草稿在电脑编辑后，手机持旧版本审批会被拒绝并提示刷新。
4. 电脑离线时手机命令持久排队，恢复后按幂等键只执行一次。
5. 微信只显示实际采集范围，不将通知内容伪装为完整历史。

### 10.3 性能与预算

以本机模型和真实数据测量 Router p50/p95、批大小、Worker 完成时间和 token 成本。参考文章的合成实验只用于确定测量维度，不作为验收数值。默认目标仅为：3 个 Worker 时主对话可响应、40 条/分钟 Trigger 压测不丢事件且不重复执行、每日预算超限暂停自动 Worker。

### 10.4 发布命令

- Go：`go test ./...`、`go vet ./...`。
- 桌面：`npm --prefix desktop test`、`npm --prefix desktop run check`、`npm --prefix desktop run dist`，打包后执行 `--smoke-test`。
- Android：同步 `web/`，构建 AAR/APK，真机验收通知、离线队列、无障碍和中继恢复。
- 文档：同步 API 手册、项目大纲、三端协议和 AGENT.md。

回滚只关闭 `superagent.enabled`，保留新表和审计数据，不执行破坏性 SQL。

---

## 11. 已确定决策与后续开放项

| 问题 | 本期决策 |
|---|---|
| 大脑部署 | 单活跃；先 PC，可整体迁移小主机/NAS |
| 电脑离线手机是否跑 Router | 否，只采集、排队和本地提醒 |
| 是否上云端 Router | 本期不上，中继不保存权威业务状态 |
| Worker 数据模型 | 扩展 agent_job，不建平行队列 |
| 审批 Outbox | 新 agent_action_outbox；旧提醒 /api/outbox 不动 |
| 外部发送 | 首版全部人工审批 |
| 邮件/日历首期范围 | IMAP/SMTP + 只读 ICS；OAuth/CalDAV 后补 |
| 微信 | P0 后选择 L1/L2/L3，不阻塞核心 |
| 跨渠道身份 | 高置信度关联，低置信度人工确认 |
| 高级 Watchdog/投机执行 | P4，P1 先做确定性可靠性 |

后续开放项仅保留：是否部署 L1 常开主机、P4 是否引入极窄自动发送白名单、是否以及何时评估云端控制层。它们不阻塞 P0-P3。

---

## 12. 执行检查清单

### 立即开始：P0

- [ ] 基线测试与 API/任务语义清单
- [ ] 修复 agent-jobs 装配并统一 JobService
- [ ] owner/token/scope 安全边界
- [ ] 0007 迁移契约评审
- [ ] SecretStore 接口与 DPAPI 实现
- [ ] 中继 v2 与双端持久队列契约
- [ ] 并行微信只读探测

### 核心闭环：P1

- [ ] 加法迁移与仓储
- [ ] TurnService 无行为变化抽取
- [ ] EventHub 与现有来源适配
- [ ] agent_job WorkerManager
- [ ] 最小 Router 与订阅直通
- [ ] ActionOutbox 与确定性 Watchdog
- [ ] Thread/旧会话召回 v1
- [ ] EventLog/SSE/relay 增量同步
- [ ] 电脑主对话卡 + 单工作台
- [ ] 手机远程优先 + 离线持久队列
- [ ] 全量回归与打包 smoke

### 渠道与高级能力

- [ ] P2 邮件审批发送闭环
- [ ] P2 ICS 只读日历
- [ ] P3 微信分级接入
- [ ] P4 异步工具、LLM Watchdog、投机执行和预算面板
