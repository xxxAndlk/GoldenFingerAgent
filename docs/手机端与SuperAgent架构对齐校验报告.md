# 手机端与 SuperAgent 架构对齐校验报告

- 复验时间：2026-11-05 22:52（UTC+8）；随后按用户最新口径修订共享范围
- 校验依据：`docs/手机端与SuperAgent架构对齐检查报告.md` 及用户最新产品口径
- 校验对象：当前工作区源码及本轮修复后的自动测试，不是已提交版本或最新打包产物
- 校验方式：代码复核 + Go 构建/测试/静态检查 + 桌面测试 + 手机 H5 浏览器校验
- 本轮边界：只校验并更新报告，未修改功能代码；未读写真实 `data/gfa.db`；H5 使用隔离数据和模拟模型
- 本报告替代上一轮结论

## 一、最新产品口径

**手机端与电脑端只共享同一份长期记忆；原始聊天消息和会话历史不要求跨端共享。**

需要共享的长期记忆至少包括：

1. 今天及历史的协同总结；
2. 工作简报；
3. 手机端记录的联系人事实、备注等信息。

对话中提炼出的长期记忆可以共享，但这不等于同步完整聊天历史。现有统一会话、Worker 远程查看等能力可以保留，但不再把“双端共享聊天记录”作为完成条件或发布门槛。

## 二、总体结论

**结论：部分通过，尚不能判定完成。上一轮 7 个问题中 6 项已关闭、1 项部分关闭，Go、桌面和手机 H5 自动回归全部通过。但按最新共享口径，协同总结、工作简报和联系人记录的共享链路仍未全部闭合；此外还有离线队列同键 owner/kind 校验缺口。**

主要进展：

1. 离线重放 unknown 后会停止自动重试；明确离线时保留 pending。
2. 聊天任务卡字段、非流式/SSE done 结构、委派只读、Ledger Begin、审批白名单和 Artifact 同键重放均已修复。
3. 双端已有云端长期记忆客户端，使用设备 Token 对应同一 owner，支持持久 Outbox、云端检索和遗忘。
4. 三份手机页面字节一致；H5 无页面脚本错误、无 390×844 横向溢出。

当前不能标记完成的核心原因：共享范围已明确为长期记忆，但三类指定内容尚不能都证明“任一端写入后，另一端能从同一长期记忆读取”。

## 三、长期记忆共享专项复核

| 内容 | 当前结论 | 代码证据与缺口 |
|---|---|---|
| 云端同一长期记忆通道 | **已有基础链路** | 电脑和手机均装配 `memory.Remote`（`cmd/server/main.go:905-950`；`android/gosvc/gosvc.go:155-208`）；写入先落 Outbox，再上传 `/api/memory/v1/episodes`，检索走同一云端 `/api/memory/v1/search`（`internal/memory/remote.go:95-123,275-341`） |
| 协同总结 | **部分具备，最终总结未闭合** | 协同采集摘要会调用 `capture(..., 'desktop_collaboration')` 并镜像长期记忆（`desktop/collaboration.js:253-267`；`internal/memory/capture.go:83-98`）；Worker 结果也会镜像（`cmd/server/main.go:503-506`）。但模型生成的最终协同总结 `reply` 只保存到桌面阶段状态，未显式写入长期记忆（`desktop/collaboration.js:264-310`） |
| 工作简报 | **远程查看已有，共享长期记忆未具备** | 简报只写入 `DailyBriefs` 并触发通知（`internal/superagent/brief.go:252-273`；`cmd/server/main.go:386-393`）；未发现把简报正文写入 `LongTermMemory.Append`、`SaveSystemEvent` 或 `SaveWorkerResult` 的链路。手机远程读取简报不等于简报已进入共享长期记忆 |
| 联系人事实 | **已有镜像链路** | `SaveFact` 保存后以 `person_fact` 来源镜像到长期记忆（`internal/memory/service.go:193-210`） |
| 联系人备注/手动记录 | **未共享到长期记忆** | HTTP 编辑直接调用 `PersonRepo.UpdateNotes`（`internal/httpapi/memory.go:62-83`）；`Service.UpdateNotes` 也只更新 SQLite 和审计，没有镜像长期记忆（`internal/memory/service.go:301-307`） |
| 原始聊天记录 | **不要求共享** | 不再以跨端会话历史一致性作为验收条件；对话提炼记忆与聊天历史同步是两件事 |

### 按新口径需要补齐的闭环

1. 将最终协同总结以稳定、幂等的来源 ID 写入长期记忆，而不是只共享采集摘要或桌面阶段状态。
2. 工作简报生成后，将正文及日期、版本、来源引用写入长期记忆；保留现有简报历史接口作为展示和追溯能力。
3. 联系人备注新增/修改时写入或更新共享长期记忆，并保证遗忘联系人时对应记忆同步删除。
4. 分别做“电脑写入→手机检索”和“手机写入→电脑检索”的真实云端验收；只验证本机 SQLite 不算通过。

## 四、原检查报告主要项目复核

| 原编号 | 项目 | 本轮结论 | 说明 |
|---|---|---|---|
| P0-1 | 手机 `taskAction` 被旧提醒方法覆盖 | **已修复** | H5 已实际点击暂停和继续，均进入 `/api/work-tasks` |
| P0-2 | 桌面 Worker 输入绕开 Inbox、污染主会话 | **已修复** | Worker 发送走 WorkTask Inbox，主会话指针不被修改 |
| P0-3 | `unknown` 自动排队与重放 | **已修复** | 重放 unknown 会 `MarkFailed`，第二次 flush 不再调用（`internal/httpapi/remote.go:824-843`） |
| P0-4 | 电脑重启后去重失效 | **代码路径已修复** | SQLite 回执、done 回放、running 中断 unknown 和 Begin 失败拒绝执行均存在 |
| P1-5 | Android BFF 无远程任务创建入口 | **已修复** | 在线/离线测试通过 |
| P1-6 | 离线重放丢失 `expected_version` | **已修复** | 重放恢复版本，专项测试通过 |
| P1-7 | Worker 分页、幂等、状态操作和终态限制不足 | **已修复** | 分页、Inbox 幂等键、CAS 和终态禁发均已实现 |
| P1-8 | 双端聊天不消费结构化任务卡 | **已修复（源码与自动测试范围）** | 这是本地任务入口能力，不再解释为共享聊天历史；`conversation.session_id` 和 SSE done 已补齐 |
| P1-9 | 统一投影设备语义、500 条边界和主历史旧接口 | **已修复** | 作为远程查看能力保留，不是聊天共享要求 |
| P1-10 | 简报超时、离线按钮和已读缺口 | **代码与 H5 已修复；共享记忆未闭合** | 手机可读远程简报，但简报正文尚未写入同一长期记忆 |
| P2-1 | 重复全局渠道开关 | **已修复** | 只保留一组全局开关 |
| P2-2 | 委派记录不能打开真实会话 | **已修复** | 有会话可打开且强制只读；H5 验证无输入框、无消息 POST |
| P2-3 | 事实治理与通知跳转不完整 | **代码层面已补齐；联系人备注共享未闭合** | 事实确认、冲突、来源存在；备注修改未镜像长期记忆 |
| P2-4 | 中继协议文档未更新 | **已修复** | 白名单、schema、结果分类、重试和超时边界已记录 |
| P2-5 | 进度文档与源码不符 | **需按最新口径收尾** | 后续应记录“只共享长期记忆，不共享聊天历史”及剩余闭环 |
| P2-6 | 已删除聊天简报卡片的死 CSS | **已修复** | 未发现旧残留 |

## 五、上一轮 7 个剩余问题逐项复验

| 编号 | 项目 | 本轮结论 | 证据 |
|---|---|---|---|
| 1 | 离线队列重放错误后仍自动重试 | **已关闭** | `TestRemoteFlushStopsAfterUnknown` 和 `TestRemoteFlushKeepsPendingWhenOfflineAgain` 通过 |
| 2 | 任务卡 Worker 会话字段不匹配 | **已关闭** | `chatConversationRef` 输出 `id/session_id/kind`，非流式和 SSE 结构测试通过 |
| 3 | 委派会话显示无效输入框 | **已关闭** | 手机强制只读，H5 实际验证通过；桌面也有只读拦截 |
| 4 | Ledger Begin 失败 fail-open | **已关闭** | Begin 失败返回 unknown 且不执行；故障注入测试通过 |
| 5 | 非法审批动作默认驳回 | **已关闭** | 仅允许精确 `approve/reject`，非法动作 400 且远程调用为 0 |
| 6 | Artifact 交付未复用队列幂等键 | **已关闭** | 首次调用和重放复用同一 key，专项测试通过 |
| 7 | 同键已存在仍返回“已排队” | **部分关闭** | pending/done/failed/payload 冲突已处理；owner/kind 校验仍缺失 |

## 六、其他仍确认的代码问题

### P1：离线队列同键去重未校验 owner 和指令类型

- `RemotePendingRepo.Get` 只按全局 `idempotency_key` 查询（`internal/store/remote_cache.go:118-140`）。
- `matchExisting` 只比较 payload，没有比较 `OwnerUserID` 和 `Kind`（`internal/store/remote_cache.go:110-115`）。
- `remoteMutate` 对 done/failed 记录会回放结果或错误（`internal/httpapi/remote.go:211-253`）。

影响：多 owner 部署中，同键同 payload 可能读到另一 owner 的完成结果或失败原因；同 owner 的同键、同 payload、不同 kind 也可能被误判为同一指令。应同时匹配 owner、kind 和规范化 payload，否则返回统一冲突，并补跨 owner、跨 kind、并发入队测试。

## 七、阶段 0–4 复验结论

| 阶段 | 结论 | 摘要 |
|---|---|---|
| 阶段 0：安全与兼容基线 | **部分通过** | 会话 owner 隔离和兼容测试通过；远程队列同键 owner/kind 仍需修复 |
| 阶段 1：每日简报主链路 | **展示链路通过，共享记忆未通过** | Markdown、历史、缓存和远程读取已具备；简报未写入同一长期记忆 |
| 阶段 2：统一会话与 Worker 对话 | **代码与自动测试通过** | 属于远程查看/任务入口能力；不再要求同步聊天记录，双端真实点击 E2E 待补 |
| 阶段 3：任务、审批与交付闭环 | **部分通过** | 主要缺陷已修复；队列 owner/kind 边界未闭合 |
| 阶段 4：渠道真实性与记忆治理 | **部分通过** | 云端长期记忆基础链路存在；三类指定内容的完整共享及真实连接器尚未验收 |

## 八、自动测试与实际结果

| 校验命令 | 结果 | 证据/说明 |
|---|---|---|
| `./.bma/tools/go/bin/go.exe build ./...` | **通过** | 退出码 0 |
| `./.bma/tools/go/bin/go.exe test ./... -count=1` | **通过** | 全部 Go 包通过 |
| `./.bma/tools/go/bin/go.exe vet ./...` | **通过** | 退出码 0，无输出 |
| `npm --prefix desktop test` | **通过** | 160 项通过，0 失败，0 跳过 |
| `npm --prefix desktop run check` | **通过** | 桌面 JS 文件语法检查通过 |
| `GFA_H5_VERIFY_PORT=17102 npm run test:mobile-h5` | **通过** | `H5_VERIFY_OK ... history=ok delegated=readonly brief=markdown taskCenter=ok taskActions=work-tasks ... horizontalOverflow=false pageErrors=0 responses=2` |
| 三份 H5 SHA-256 | **一致** | 三份均为 `c3a7e47766e193c08697a0d99adcbd6cf378230fc411c94e9054cbc15beec375` |
| `git diff --check` | **通过** | 无空白错误；仅有现存文件 CRLF→LF 提示 |
| `cd android && ./gradlew :app:compileDebugKotlin` | **环境阻塞，未执行编译** | `JAVA_HOME is not set and no 'java' command could be found in your PATH.` |

H5 使用 `artifacts/mobile-h5-verify/` 隔离数据与模拟 Responses，未使用正式数据库。本轮已查看 `tasks.png`、`brief.png` 和 `chat.png`。实际任务暂停/继续请求均返回 200，委派只读检查确认没有输入框和消息 POST。

这些自动测试没有验证真实云端长期记忆的双端读写，因此不能据此宣称协同总结、工作简报和联系人记录已经共享成功。

## 九、测试覆盖仍不能证明的场景

1. 电脑生成最终协同总结后，手机能从同一长期记忆检索；手机写入联系人记录后，电脑能检索。
2. 工作简报的日期、版本、正文和来源引用进入长期记忆，且双端读取一致。
3. 联系人备注的新增、修改、遗忘能同步到长期记忆，不出现本机已删、云端仍可召回。
4. H5 尚未完整覆盖 Worker 分页、WorkTask Inbox、终态禁发、审批、Artifact 和任务卡真实点击。
5. 桌面测试主要是源码合约断言，不是 Electron 真实端到端测试。
6. 队列缺少跨 owner、跨 kind 同键测试；Ledger 缺 Complete 写失败专项测试。
7. 真实断网重试、页面重载恢复、长时间重连未验证。
8. 未重新打包桌面或 Android AAR/APK；Android Kotlin 因缺 Java 未能编译。

## 十、最终发布门槛判断

| 发布门槛 | 当前判断 |
|---|---|
| 双端只共享同一长期记忆 | **基础通道存在，但指定内容未全部闭合** |
| 协同总结、工作简报、联系人记录双端可用 | **未通过**：最终协同总结、简报、联系人备注仍有缺口 |
| 原始聊天记录不共享 | **不作为缺陷**；无需以聊天历史一致性验收 |
| 任务控制、审批和交付状态真实 | **代码主体通过；队列边界和真实跨端未验收** |
| 渠道开关对应真实设备与连接器状态 | **代码与自动测试通过，真实连接器未验收** |
| owner 隔离 | **会话接口通过；远程队列同键 owner 隔离未通过代码审查** |
| H5、桌面、真实跨端/真机分别留证 | **自动测试有证据；真实云端记忆、中继、真机和打包版未验证** |

因此，当前准确结论是：**自动回归全绿，上一轮主要问题已修复；按最新“只共享长期记忆、不共享聊天记录”的口径，还需完成协同总结、工作简报、联系人记录的共享闭环，并修复队列同键 owner/kind 隔离。**

## 十一、真实环境未验证项

- 真实云端长期记忆的双端写入、检索、更新和遗忘。
- 真实 TLS 中继、公网部署、电脑离线、手机断网、长时间重连和桌面重启。
- Android 安装、冷启动、Keystore、通知、语音、无障碍和 ROM 保活。
- Artifact 原生下载、SHA-256 校验、保存与分享。
- 真实模型、真实电脑微信与工作轨迹连接器、授权撤回后停止采集。
- 最新桌面打包版与 Android AAR/APK 运行验收。

## 十二、建议收尾顺序

1. 先按长期记忆口径补齐最终协同总结、工作简报和联系人备注的写入/更新/遗忘链路，并做双向云端测试。
2. 修复队列 `matchExisting` 的 owner/kind 校验，补跨 owner、跨 kind 和并发同键测试。
3. 补双端任务卡、Worker、审批/Artifact 和 Ledger Complete 故障测试。
4. 配置 Java/Android SDK，完成 Kotlin、APK 和桌面打包 smoke test。
5. 最后进行真实云端记忆、中继、Android 真机、真实模型与连接器验收。
