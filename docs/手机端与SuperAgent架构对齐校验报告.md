# 手机端与 SuperAgent 架构对齐校验报告

- 复验时间：2026-11-05 22:52（UTC+8）
- 校验依据：`docs/手机端与SuperAgent架构对齐检查报告.md`
- 校验对象：当前工作区源码及本轮修复后的自动测试，不是已提交版本或最新打包产物
- 校验方式：上一轮 7 个剩余问题逐项复核 + Go 构建/测试/静态检查 + 桌面测试 + 手机 H5 浏览器校验
- 本轮边界：只校验并更新报告，未修改功能代码；未读写真实 `data/gfa.db`；H5 使用隔离数据和模拟模型
- 本报告替代上一轮结论

## 一、总体结论

**结论：部分通过。上一轮 7 个问题中，6 项已在代码和自动测试范围关闭；第 7 项的同键状态处理已修复，但本轮发现去重时没有校验 owner 和指令类型，仍有一个代码边界问题。Go、桌面和手机 H5 自动回归全部通过，但尚不能判定满足最终发布门槛。**

主要进展：

1. 离线重放遇到 unknown 后会标记失败，第二次 flush 不再自动调用；明确离线时仍保留 pending。
2. 聊天任务卡已同时返回 `conversation.id/session_id`，非流式响应和 SSE done 均有结构测试。
3. 委派执行记录在手机和桌面均为只读；H5 已实际打开委派记录并确认没有输入框、没有发送请求。
4. Ledger Begin 失败会拒绝执行业务；非法审批动作返回 400；Artifact 交付首次调用和重放复用同一幂等键。
5. 同键队列已能区分 pending、done、failed 和 payload 冲突，不再一律返回“已排队”。
6. 三份手机页面字节一致；H5 无页面脚本错误、无 390×844 横向溢出。

仍不能直接发布的原因：同键队列缺少 owner/kind 身份校验；真实 TLS 中继、断网重连、真实模型、真实连接器、Android 真机和最新打包版均未验收。

## 二、原检查报告主要项目复核

| 原编号 | 项目 | 本轮结论 | 证据与说明 |
|---|---|---|---|
| P0-1 | 手机 `taskAction` 被旧提醒方法覆盖 | **已修复** | 长期任务操作使用 `/work-tasks/{id}/{action}` 并携带版本；H5 已实际点击暂停和继续 |
| P0-2 | 桌面 Worker 输入绕开 Inbox、污染主会话 | **已修复** | Worker 发送走 `messageWorkTask`，主进程打开 Worker 不再改主聊天指针；桌面合约测试通过 |
| P0-3 | `unknown` 自动排队与重放 | **已修复** | 首次 unknown 不排队；重放 unknown 会 `MarkFailed`，第二次 flush 不再调用（`internal/httpapi/remote.go:824-843`；`internal/httpapi/remote_test.go:518-552`） |
| P0-4 | 电脑重启后去重失效 | **代码路径已修复** | SQLite 回执、done 回放和 running 中断 unknown 均存在；Begin 失败已改为不执行（`internal/relay/client.go:293-325,402-457`） |
| P1-5 | Android BFF 无远程任务创建入口 | **已修复** | 远程 WorkTask 创建在线/离线测试通过 |
| P1-6 | 离线重放丢失 `expected_version` | **已修复** | WorkTask 消息/动作重放恢复版本，专项测试通过 |
| P1-7 | Worker 分页、幂等、状态操作和终态限制不足 | **已修复** | 手机向上分页、Inbox 幂等键、恢复/重开、CAS 和终态禁发均已实现 |
| P1-8 | 双端聊天不消费结构化任务卡 | **已修复（源码与自动测试范围）** | 后端补齐 `session_id`，双端读取同一字段；非流式和 SSE done 结构测试通过（`internal/httpapi/chat.go:54-62,217-235`；`internal/httpapi/conversations_test.go:449-537`） |
| P1-9 | 统一投影设备语义、500 条边界和主历史旧接口 | **已修复** | 统一投影、按 ID+owner 直查、事实源设备和手机主历史链路保持通过 |
| P1-10 | 简报超时、离线按钮和已读缺口 | **已修复（代码与 H5 范围）** | 远程超时、离线禁用、按本机记录已读及 Markdown 展示均已验证 |
| P2-1 | 重复全局渠道开关 | **已修复** | 只保留一组全局开关和一个保存按钮 |
| P2-2 | 委派记录不能打开真实会话 | **已修复** | 有会话记录可打开且强制只读；H5 实际检查无输入框、无消息 POST（`web/index.html:2199-2208,3110-3119`；`scripts/verify-mobile-h5.js:229-274`） |
| P2-3 | 事实治理与通知跳转不完整 | **代码层面已补齐** | 事实确认、冲突、来源和通知路由存在；Android 真机通知点击仍未验收 |
| P2-4 | 中继协议文档未更新 | **已修复** | 白名单、schema、结果分类、重试和超时边界已记录 |
| P2-5 | 进度文档与源码不符 | **已更新，需随最后边界收尾** | 剩余同键 owner/kind 问题修复后，应同步进度文档 |
| P2-6 | 已删除聊天简报卡片的死 CSS | **已修复** | 未发现旧 `chat-brief`/`brief-card` 残留 |

## 三、上一轮 7 个剩余问题逐项复验

| 编号 | 项目 | 本轮结论 | 证据 |
|---|---|---|---|
| 1 | 离线队列重放错误后仍自动重试 | **已关闭** | 非离线错误调用 `MarkFailed`；`TestRemoteFlushStopsAfterUnknown` 验证第二次 flush 不再调用；`TestRemoteFlushKeepsPendingWhenOfflineAgain` 验证明确离线仍保留 |
| 2 | 任务卡 Worker 会话字段不匹配 | **已关闭** | `chatConversationRef` 同时输出 `id/session_id/kind`，两种聊天响应均保留任务结构 |
| 3 | 委派会话显示无效输入框 | **已关闭** | 输入区同时要求 `workerCanMessage && workerTaskId`，委派打开时显式 `can_message:false`；H5 已实际验证 |
| 4 | Ledger Begin 失败 fail-open | **已关闭** | Begin 失败返回 unknown 且不调用业务；`TestDesktopLedgerBeginFailureRefusesToExecute` 通过 |
| 5 | 非法审批动作默认驳回 | **已关闭** | 只接受精确的 `approve/reject`，其他值 400；`TestRemoteOutboxRejectsUnknownAction` 验证远程调用次数为 0 |
| 6 | Artifact 交付未复用队列幂等键 | **已关闭** | 接口、首次调用、重放和 Phone 实现均传递同一 key；`TestRemoteArtifactDeliverReusesKeyOnReplay` 通过 |
| 7 | 同键已存在仍返回“已排队” | **部分关闭** | pending/done/failed/payload 冲突已正确处理；但 `matchExisting` 只比较 payload，owner/kind 边界仍未闭合，见下节 |

## 四、本轮仍确认的问题

### P1：离线队列同键去重未校验 owner 和指令类型

- `RemotePendingRepo.Get` 只按全局 `idempotency_key` 查询（`internal/store/remote_cache.go:118-140`），数据库也以该键作为全局主键（`migrations/0022_remote_cache.sql:17-28`）。
- `matchExisting` 只调用 `sameJSON` 比较 payload，没有比较 `OwnerUserID` 和 `Kind`（`internal/store/remote_cache.go:110-115`）。
- `remoteMutate` 在已存在记录为 done 时会直接回放其 `Result`，failed 时返回其 `LastError`（`internal/httpapi/remote.go:211-253`）。

影响：在多 owner 部署中，如果另一 owner 使用相同幂等键和相同 payload，可能读到原 owner 的完成结果或失败原因；同 owner 用同键、同 payload 但不同指令类型时，也可能被当作同一条指令。现有 `TestRemoteQueueReportsExistingState` 只覆盖同 owner、同 kind，未覆盖这两个边界。

建议：

1. 同键记录必须同时匹配 `OwnerUserID`、`Kind` 和规范化 payload，否则返回统一冲突，不回放结果。
2. 增加跨 owner 同键同 payload、跨 kind 同键同 payload、并发入队三类测试。
3. 保持当前全局主键时，跨 owner 冲突不应泄露原记录内容。

## 五、阶段 0–4 复验结论

| 阶段 | 结论 | 摘要 |
|---|---|---|
| 阶段 0：安全与兼容基线 | **部分通过** | 会话消息 owner 隔离、旧接口兼容和统一投影测试通过；远程队列同键 owner/kind 隔离仍需修复 |
| 阶段 1：每日简报主链路 | **代码与 H5 通过，真实环境未验收** | Markdown、历史版本、生成、缓存、已读、离线状态和超时已对齐；未用真实模型和真实中继生成 |
| 阶段 2：统一会话与 Worker 对话 | **代码与自动测试通过，端到端仍需补测** | 任务卡字段、SSE done、委派只读和 Worker Inbox 已对齐；桌面真实点击与手机任务卡点击未做完整 E2E |
| 阶段 3：任务、审批与交付闭环 | **部分通过** | 重放、Ledger Begin、审批白名单和交付同键已修复；队列 owner/kind 边界未闭合 |
| 阶段 4：渠道真实性与记忆治理 | **代码与自动测试通过，真实环境未验收** | 三态、真实回执、事实治理和通知路由存在；连接器心跳、撤权停止和真机通知点击未验证 |

## 六、自动测试与实际结果

| 校验命令 | 结果 | 证据/说明 |
|---|---|---|
| `./.bma/tools/go/bin/go.exe build ./...` | **通过** | 退出码 0 |
| `./.bma/tools/go/bin/go.exe test ./... -count=1` | **通过** | 全部 Go 包通过，包括 `android/gosvc`、`internal/httpapi`、`internal/relay`、`internal/store`、`internal/superagent`、`internal/worktask` |
| `./.bma/tools/go/bin/go.exe vet ./...` | **通过** | 退出码 0，无输出 |
| `npm --prefix desktop test` | **通过** | 160 项通过，0 失败，0 跳过；包含委派只读和 Worker Inbox 合约测试 |
| `npm --prefix desktop run check` | **通过** | 桌面 JS 文件语法检查通过 |
| `GFA_H5_VERIFY_PORT=17102 npm run test:mobile-h5` | **通过** | `H5_VERIFY_OK viewport=390x844 settings=ok chat=mock persistence=ok history=ok delegated=readonly brief=markdown taskCenter=ok taskActions=work-tasks inbox=ok headerBrief=ok channels=tri-state route=ok horizontalOverflow=false pageErrors=0 responses=2` |
| 三份 H5 SHA-256 | **一致** | 三份均为 `c3a7e47766e193c08697a0d99adcbd6cf378230fc411c94e9054cbc15beec375` |
| `git diff --check` | **通过** | 无空白错误；仅有现存文件 CRLF→LF 提示 |
| `cd android && ./gradlew :app:compileDebugKotlin` | **未能执行，不是代码编译通过或失败** | 环境报错：`JAVA_HOME is not set and no 'java' command could be found in your PATH.` |

H5 使用 `artifacts/mobile-h5-verify/` 隔离数据与模拟 Responses，日志明确未使用正式数据库。截图位于 `artifacts/mobile-h5-verify/screenshots/`；本轮已查看 `tasks.png`、`brief.png` 和 `chat.png`，任务中心、Markdown 简报和聊天页展示正常。

H5 日志中的实际任务请求包括：

- `POST /api/work-tasks/{id}/pause` → 200；
- `POST /api/work-tasks/{id}/resume` → 200；
- 没有误发 `/api/tasks/{id}/{action}`。

委派只读检查还确认：面板内 `.worker-compose` 数量为 0、输入控件数量为 0、存在“这里只读”提示，且没有发出消息 POST。

## 七、测试覆盖仍不能证明的场景

1. H5 尚未完整覆盖 Worker 向上分页、WorkTask Inbox 追加、终态禁发、审批、Artifact 验收和聊天任务卡真实点击。
2. 桌面新增 Worker/委派测试主要是源码合约断言，不是 Electron 中真实发送、任务卡点击和主会话隔离的端到端测试。
3. 非流式和 SSE done 的任务结构已有后端测试，但尚缺双端基于这些响应的实际点击跳转测试。
4. 队列缺少跨 owner、跨 kind 同键测试；Ledger 已有 Begin/Lookup 故障注入，仍缺 Complete 写失败专项测试。
5. H5 的断网重试同幂等键、页面重载后的恢复、真实离线缓存与长时间重连未做浏览器级验证。
6. 本轮未重新打包桌面或 Android AAR/APK，也未运行打包版 smoke test；Android Kotlin 因缺 Java 未能编译。

## 八、最终发布门槛判断

| 发布门槛 | 当前判断 |
|---|---|
| 双端看到同一 WorkTask、Worker、审批和 Artifact 状态 | **代码主体通过，真实跨端未验收** |
| 主对话、Worker、委派记录入口真实且边界清楚 | **代码与自动测试通过，双端真实点击 E2E 待补** |
| 每日简报可生成、可读、可追溯版本，手机不依赖本地空数据 | **代码与 H5 通过，真实模型/中继未验收** |
| 追加指令、审批、验收不丢不重，离线状态不造假 | **未完全通过**：队列同键 owner/kind 边界仍未闭合，真实断网重连未验收 |
| 渠道开关对应真实设备与连接器状态 | **代码与自动测试通过，真实连接器未验收** |
| 会话 owner 隔离测试通过 | **会话接口通过；远程队列同键 owner 隔离未通过代码审查** |
| H5、桌面、真实跨端/真机分别留证 | **仅源码与自动测试有证据；真实跨端、真机和打包版未验证** |

因此，当前仍不应标记为“全部完成并通过最终发布验收”。更准确的表述是：**上一轮主要修复已经落地，自动回归全绿，7 个剩余问题中 6 个关闭、1 个部分关闭；还剩同键 owner/kind 隔离问题和真实环境验收。**

## 九、真实环境未验证项

以下项目不能由上述自动测试替代：

- 真实 TLS 中继、公网部署、电脑离线、手机断网、长时间重连和桌面重启。
- Android 安装与冷启动、Keystore、通知监听、通知点击、语音唤醒、无障碍、ROM 保活。
- Artifact 原生下载、SHA-256 校验、保存与分享。
- 真实模型下普通聊天不误建任务、明确任务指令正确进入 WorkTask。
- 真实电脑微信与工作轨迹连接器心跳、授权撤回后停止采集。
- 最新桌面打包版与 Android AAR/APK 的运行验收。

## 十、建议收尾顺序

1. 修复 `matchExisting` 的 owner/kind 校验，补跨 owner、跨 kind 和并发同键测试。
2. 补双端任务卡真实点击、Worker 分页/Inbox/终态、审批/Artifact 和 Ledger Complete 故障测试。
3. 安装/配置 Java 与 Android SDK 后执行 Kotlin 编译和 APK 构建，再重跑全量自动测试。
4. 最后进行真实中继、Android 真机、真实模型与连接器验收；全部通过后再更新发布结论。
