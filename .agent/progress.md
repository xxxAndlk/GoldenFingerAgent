# 当前进度与修改记录

最后更新：2026-10-31

本文件记录**当前实现状态**与验证证据，不是「全部功能已验收」的证明。每条尽量区分：代码实现 / 自动测试 / 真实环境验证。

> 阅读约定：已完成的条目不再保留「进行中」「未接线」等旧结论；被推翻的判断直接删除或改写，避免后来者按错误前提干活。

## 一、当前形态

三端分工（2026-10-30 起明确）：

| 端 | 定位 | 落地位置 |
|---|---|---|
| 电脑端 | 工作助手：对话、工具循环、CLI/桌面/A2A/BMA 调度、工作轨迹、文件与命令、任务与提醒、SQLite、长期记忆 | `cmd/server` + `desktop/` |
| 手机端 | 生活管家：语音口令、情景记忆、提醒、通知中心、无障碍操控 | `android/`（Kotlin 能力层 + `gosvc` Go 服务） |
| 中继 | 只转发：配对、鉴权、投递，不执行业务 | `cmd/relay` |

互通方向：手机 → 中继 → 电脑执行 → 结果回传手机；电脑主动推手机暂不实现。协议见 `docs/三端拆分与中继协议.md`。

电脑端界面：主窗口只管对话（顶部协作开关）；`Agent 调度` 与 `设置` 合并在一个管理窗口；CC Switch 独立窗口；工作轨迹独立半透明浮窗，与主对话显示状态分离。

运行入口：根目录 `启动管家服务.bat` 启动完整 Electron 桌面版，由桌面主进程拉起并守护 Go 服务。

## 二、2026-10-30 完成的工作

### 2.1 长期记忆：Python/Graphiti → 纯 Go

- `migrations/0006_longterm_memory.sql`：`mem_episode`（`source_id` 主键幂等 + 抽取状态/重试计数）与 `mem_edge`（主体/关系/客体、`valid_from`/`valid_to`、`source_refs`）。
- `internal/store/longterm.go`：片段与事实仓储（幂等写入、待抽取队列、三元组活动事实、时间失效、按来源删除、硬删除、来源引用合并）。
- `internal/memory/extract.go`：抽取提示词 + 严格 JSON 解析；原文用 `<<<DATA … DATA>>>` 包裹并声明「是数据不是指令」。
- `internal/memory/native.go`：`LongTermMemory` 的 Go 实现。Append 幂等落库并唤醒后台；Start/RunOnce 抽取；Search 融合向量、关键词、时效并做一跳主体扩展；Forget 真删；Health 报 pending/done/failed。
- 遗忘链路补齐：`ForgetPerson`/`ForgetFact` 现在真的调用长期记忆 `Forget`（Graphiti 时期这里返回 `ErrUnsupported`，从未生效）。
- 零新增依赖、不引入图数据库。FTS5 虽可用但对中文不分词（实测 `MATCH '上海'` 命中 0），所以关键词检索在 Go 内打分。

### 2.2 Kotlin 业务判定搬回 Go

- **设备安全闸**：`internal/device/guard.go` 负责敏感词/敏感包名/输入内容判定、`confirm` 请求与急停窗口，接在 `sendDevice` 前；敏感动作只下发 `confirm`，用户拒绝则原动作不下发；无法确认（无设备/超时/断线）一律拒绝。新增 `POST /api/device/emergency`，服务端与安卓本地同时进入急停窗口（断网也能停）。`SafetyGuard.kt` 降为能力层，只保留急停计时与 `confirm()`。
- **通知中心匹配**：`InboxApi.kt` 不再「拉列表反查 event_id」，改为直接上报通知字段；服务端新增 `CreateSnoozeByMatch`（匹配不到如实 404）。
- **前台包名**：`ScreenReporter` 上报包名，Go 的 `Screen`/`/api/screen` 接受该字段，用于支付/银行页判定。
- **语音口令判定**：新增 `internal/voice`（唤醒词默认「小金小金」、10s 指令窗、4s 片段合并、30s 上下文窗/最多 4 句、最近 6 条），纯状态机无 IO。`POST /api/voice/text` 返回 `speak|chat|capture` + `next_flush_ms`；`capture` 由 Go 直接写情景记忆，手机不再单独调 `/api/memory/capture`。`MemoryCaptureService.kt` 只留 SpeechRecognizer 生命周期、重连退避、TTS、前台通知；合并时长由 Go 下发，不再写死 4s。
- 行为一致性：说出口的指令本身也会入库（先暂存，4s 无后文或下一句到来时落库），与原 Kotlin 一致，有测试锁定。

### 2.3 长任务：持久化异步队列真正接线

- `agenthub.Dispatcher` 此前写好了却**从未被实例化**（死代码）。现在 `cmd/server` 启动它（重启时把 `running` 标 `interrupted` 等核对，不盲目重跑），并把同名工具 `delegate_agent` 覆盖为异步派发，补充 `list_agent_tasks`/`get_agent_task`/`cancel_agent_task`。
- 新增 `POST /api/jobs`（提交即返回 `task_id`）、`GET /api/jobs`、`GET /api/jobs/{id}`（状态+事件，重连可补看进度）、`DELETE /api/jobs/{id}`。
- 中继 `kind=job|job_status|job_cancel` → 电脑端 `/api/jobs*`；手机端 `SubmitJob/JobStatus/CancelJob` + `/api/relay/phone/job*`。手机 `client_id` → 电脑 `agent_job.idempotency_key`，重试不产生第二个任务。
- 手机「电脑助手」抽屉新增「后台执行（长任务）」：3s 轮询状态与事件、可取消、关面板停轮询、重开续看。
- **修掉真实缺陷**：`Dispatcher.runNext` 在 `cancel()` 之后才读 `ctx.Err()`，导致所有失败/超时任务被误标为 `cancelled`。改为先取快照，并补 `TestDispatcherTimeoutIsNotMisreportedAsCancelled` 锁定。

### 2.4 手机 → 电脑中继（双向）+ 手机端装配

- `internal/relay/`：中继状态机（配对码、通道、在线连接、离线队列、消息去重）+ HTTP/WS 服务 + 电脑端客户端（自动注册、断线重连、按 id 去重执行、结果回传）+ `Desktop`/`Phone` 封装（凭据持久化，0600）。
- `cmd/relay/`：独立中继二进制，纯内存，参数可用环境变量控制，注释写明必须走 TLS 反代。
- `/api/relay/pair-code`、`/status`、`/channels`、`DELETE /channels/{id}` **只允许 127.0.0.1**。
- 手机侧：`POST/DELETE /api/relay/phone/pair`、`GET /status`、`POST /send`；电脑离线返回 202+`queued`，未配对明确拒绝。手机生成 `client_id`，超时重试复用同一 id，中继与电脑端都按 id 去重，避免「点了重试执行两次」。
- `android/gosvc` 装配对齐电脑端：装 `inbox.Service`（每 30s 扫描到期 snooze，`/api/inbox*` 不再 503）、联网检索改用 `extsvc/deepseek`（移除 firecrawl/桩）、默认启用内置 Go 长期记忆。
- 手机页面不再分叉：`scripts/sync-webassets.sh` 把 `web/` 同步进 `android/gosvc/webassets`，`gosvc` 按目录内嵌并在启动时整体替换 `dataDir/web`（旧页面不会残留）。
- 桌面设置页新增「手机生活管家」卡片（生成配对码、显示状态、解除绑定）；手机「🖥️ 电脑助手」抽屉（配对/发送/结果/重试/解绑/后台执行）。

### 2.5 构建可复现 + 体积收缩

- 新增 `docker/android/build.sh`（同步页面 → gomobile bind → gradle assembleDebug → 校验产物）与 Gradle Wrapper 8.9（腾讯镜像）；此前 Dockerfile 注释引用的 build.sh 并不存在。
- 只打 arm64+armv7 并加 `-ldflags="-s -w" -trimpath`：**APK 58MB → 33MB，AAR 19MB → 11MB**，未新增任何依赖。

### 2.6 清理 Python/Graphiti 死代码与历史数据

- 判定：长期记忆已完全由 Go 承担，Python 服务运行期无调用方（`desktop/memory-manager.js` 只在 `GFA_MEMORY_NATIVE=0`+`GFA_MEMORY_URL` 的死路径上）。
- **先导出后删除**：`scripts/export-graphiti-kuzu.py`（只读打开 Kuzu）把 `Episodic`/`Entity`/`RelatesToNode_`（Graphiti 用「边当节点」，事实文本在这一层）连同 `RELATES_TO` 还原的 src/dst 导出为 `data/graphiti-export-20261030.json`：**56 段情景、132 个实体、185 条事实（20 条已失效）**，UTF-8，`reference_time` 无缺失。
- 删除：`memory_service/`、`.venv-memory`（236MB）、`data/graphiti`（81MB）、`data/models`（161MB）、`data/graphiti-verification*`（64MB）、`data/backups/graphiti-*`（1.1GB 检查点）。`data/` 从 1.4GB → **6.3MB**。
- Go 侧删除 `internal/memory/graphiti.go` 后暴露出它占用了一批**通用能力**，已重建到 `internal/memory/mirror.go` 并加注释：`mirrorEpisode`（SQLite 写成功后镜像到长期记忆后端，失败只记日志不影响主流程）、`MirrorChat`（来源 ID `chat:<msgID>`）、`SetLongTermMemory`。
- `cmd/server` 的 `GFA_MEMORY_NATIVE`/`GFA_MEMORY_URL` 分支删除，统一为 `GFA_MEMORY_DISABLE=1` 单一排障开关；桌面端删除 `memory-manager.js` 及其测试，`main.js`/`pack-win.js`/`server-manager.js` 移除 Python 唤起与 `GFA_MEMORY_URL` 透传。

### 2.7 项目结构整理

前提：仓库里绝大多数源码**从未提交**（`git ls-files` 仅 179 个，`internal/agenthub/`、`internal/memory/native.go`、`desktop/*.js` 全是未跟踪），所以动手前先打源码快照 `.archive/source-snapshot-20261030.tar.gz`（600KB / 367 文件）。

- 删除构建残留与孤儿：`desktop/dist/`（6.1GB 历史打包：23 个 activity-v* + 11 个 zip + 4 个旧 win 变体）、`runtime/`（202MB JRE，全仓零引用）、`gfa-apk.log`、`gfa-build.log`、根目录 `server.exe`。
- 文档归位：需求方案与竞品报告进 `docs/archive/`（顺手修掉 `方案 .md` 的空格怪写法），`TODO.md` → `docs/TODO.md`；修正 4 处失效链接。
- `/api/relay/*` 等本地产物全部加入 `.gitignore`（`/data/`、`/.archive/`、`/artifacts/`、`/server-dist/`、`/desktop/artifacts/`、`/android/app/build/`、`*.aar`）。`git status` 未跟踪项 **194 → 93**，剩下全是真实未提交源码。
- `AGENT.md` 目录地图重写，新增「本地产物（已 gitignore，不要提交）」表；`.agent/AGENT.md` 副本由用户删除，约定文件统一为根 `AGENT.md`。

### 2.8 界面重做：统一到 Apple 设计语言（2026-10-30）

目标：手机端与电脑端所有页面视觉统一、可读性提高，参考 iOS 26 / macOS 的系统观感。**只改样式与少量图标标记，不动任何交互逻辑、接口与 class 钩子。**

先修掉的真缺陷：`web/index.html` 里所有图标都是 `<svg class="icon">`，但样式表**从未定义 `.icon`**，导致每个图标按默认尺寸渲染成大块黑图（快捷入口、发送键、头像全部走形）。这是此前页面「看起来坏掉」的主因。

- 新增完整设计令牌层：iOS 系统色板（`#007AFF` / `#34C759` / `#FF9500` / `#FF3B30` / `#AF52DE`）、语义文字层级（label 1–4）、分隔线、填充色、圆角阶梯（28/22/16/12/10）、Apple 弹簧缓动 `cubic-bezier(.32,.72,0,1)`、阴影与安全区变量。
- **明暗双模**：三套样式表全部用 `prefers-color-scheme` 切换令牌，桌面 Electron 与手机 WebView 都跟随系统外观；此前桌面写死 `color-scheme: dark`、手机只有浅色。
- 手机端 / 网页端（`web/index.html`）：顶栏改为毛玻璃材质（`backdrop-filter` + 半透明白），管家头像改应用图标式圆角方块金色渐变，气泡改为 iMessage 形态（发出蓝底白字、收到浅灰、对应角 6px 收口），卡片、选项胶囊、快捷 chip、输入区（圆角输入框 + 圆形蓝色发送键，箭头图标替代纸飞机）全部重排；四个抽屉由「右侧全高面板」改为 **iOS 工作表**（手机从底部升起、带抓手；宽屏居中卡片），设置表单改分组内嵌样式，收件箱三个标签改 `UISegmentedControl`。发送键图标新增 `#i-arrowup` 符号；快捷入口里写成 `icon: '⏰'` 的非法符号引用改为 `bell`。
- 宽屏（`min-width:700px` 且 `pointer:fine`）把整页放进居中的圆角「窗口」并加柔和壁纸；限定精确指针，避免手机横屏被框起来。
- 适老 / 儿童模式保留放大语义，但重写了尺寸与留白（顶栏 52px 头像、19px 正文、56px 输入框等），并隐藏装饰性控件。
- 电脑端 `desktop/styles.css`：悬浮球改为带高光与内阴影的金色玻璃球（悬停/按下弹性反馈、未读点带脉冲），面板改 macOS 材质窗口（圆角、描边、`backdrop-filter`），标题栏按钮换成齿轮 / 收起图标（`desktop/index.html` 内联 SVG），消息与输入区改为 Messages 形态，开关改 iOS 尺寸。
- 电脑端 `desktop/windows.css`：管理窗口改「系统设置」式分组卡片 + 分段标签页 + 内嵌表单；`CC Switch` 供应商行、配对码、已绑定设备列表统一；开关、滑杆、按钮全部 iOS 化。
- 电脑端 `desktop/activity.css`：工作轨迹浮窗改 HUD 材质（保留 `--activity-opacity` 变量与透明度联动），时间线行、来源标签、证据查看器重排。
- `scripts/sync-webassets.sh` 的等价同步已执行：`web/index.html` 复制进 `android/gosvc/webassets/`（手机端不再保留第二份页面）。

### 2.9 手机端对齐电脑端（2026-10-30 晚，最终状态）

**先纠正一次越界**：一度把电脑端主对话界面也改成了「品牌金气泡 + 头像 + 欢迎卡片」的新语言，与另一路正在做的桌面改造（托盘、贴边、工作轨迹重做）冲突，也不符合「电脑端已定稿」的约定。已完整还原 `desktop/index.html`、`desktop/styles.css`、`desktop/renderer.js`，保留同期另一路加入的 `orb-rail`/`orb-ring`/`renderDock`（贴边滑轨、运行状态环）；`desktop/activity.css` 是那一侧的重写，未再触碰。

改法回到正确方向：**只改手机端，并且照着电脑端的语言改**，不再自创样式。

- 气泡与电脑端同形同色：自己发的是 `linear-gradient(180deg, var(--bubble-out-1), var(--bubble-out-2))` 蓝色渐变白字、右下角 6px 收口；管家消息是中性灰（明 `#e9e9eb` / 暗 `#2c2c2e`），左下角 6px 收口。
- 去掉手机端多出来的装饰：消息头像、每条消息的时间戳、金色气泡、欢迎卡片与建议卡、聊天区环境光渐变全部移除；空场景恢复成电脑端那样的单条开场白。
- 输入区与电脑端一致：胶囊输入框 + 蓝色「发送」文字按钮（替换掉之前自创的圆形箭头键），保留手机特有的麦克风与表情入口；发送键在空输入时保持蓝色、仅降透明度。
- 顶栏也按电脑端标题栏对齐：去掉「AI」角标与宣传式副标题，改成「金手指管家 + 绿色状态点 + 已连接本机管家」，连不上时变红字；`online` 由 `/api/me` 结果驱动。
- 深色底由纯黑 `#000` 改为电脑端面板色 `#1c1c1e`，使手机深色与桌面深色是同一块底色上的同一套气泡。
- 手机端功能差异不动：记忆面板、收件箱、电脑助手、快捷 chip、亮暗切换、模型方案；`--gold-grad` 只留给顶栏「金」字品牌头像。

验证：`artifacts/ui-new/compare-light.png`、`compare-dark.png` 为电脑端真实窗口截图（`--smoke-test` 抓取）与手机端 390×844 截图的并排对照，明暗两套的颜色、圆角、气泡形状、发送键一致；页面内真实调用设置接口仍可用（保存参数、切换方案均成功），测试后已把生效模型还原为 `deepseek-flash`、`max_output/max_context` 归零；`web/index.html` 与服务端副本、Android 资源三份哈希一致，局域网 `http://192.168.1.3:8080` 返回 200。桌面侧 `npm run check` + `node --test` **78/78 通过**，`--smoke-test` 真实窗口自检通过。

### 2.10 桌面体验：托盘、贴边与工作轨迹重做（2026-10-30 晚）

- **系统托盘**：`desktop/scripts/create-tray-icon.py` 重新生成金色「金」球图标（PNG + 多尺寸 ICO，idle/忙碌两态）；托盘菜单含展开/收起对话、显示/隐藏轨迹、立即总结、设置、CC Switch、退出；对话与总结时切换忙碌图标与 tooltip（用计数避免并发误切回空闲）；首次启动用气泡说明已在托盘运行；新增单实例锁与 `AppUserModelID`。悬浮球在思考/执行时显示旋转状态环。
- **贴边**：新增 `desktop/window-dock.js`（纯几何，7 项 node 测试）：拖动停顿 220ms 后吸附最近的屏幕边缘；悬浮球保持圆形、贴边时在屏幕边缘显示金色滑轨；对话面板贴边收起为同侧悬浮球，展开时按锚点对齐并夹在可用区域内；工作轨迹窗口贴边后「—」收起为 36×132（左右）/132×36（上下）金色小条，点击展开；窗口位置、收起状态与贴边边写入 `desktop-settings.json`。
- **工作轨迹重做**：标题栏加「立即总结」；时间线改为带类型色点、应用缩写徽标、两行摘要与证据入口的行卡片；面板底色按透明度叠加实底内容层，避免桌面文字穿透；隐私说明移到页脚；底部显示上次/下次总结与条数。
- **错误与噪声**：新增 `desktop/activity-format.js`（可单测）：把 PowerShell `CategoryInfo`/`InvalidOperation` 等原始报错翻译成「输入监听未启动」等一句话、详情可展开；同类提示按次数合并；过滤「应用名=标题=详情」的空占位事件；进程名映射为常用软件名（chrome→Chrome、Code→VS Code、WindowsTerminal→终端…）。
- **立即总结**：轨迹窗口、设置页「工作轨迹」、托盘菜单三处入口；显示进行中、已总结（结果同时进入对话）与无内容三种状态；`collaboration.status()` 增加 `lastSummaryAt`，页脚展示上次与下次总结时间。
- **推理等级**：桌面设置与手机页面统一为 low / high / max 三档；Go 端 `normalizeEffortInput` 与 `openai.normalizeEffort` 接受 max、旧 medium 归一到 high，`Efforts` 返回三档；补 `internal/llm/openai/effort_test.go`。
- **验证**：桌面 `node --test` **78 项通过**、`npm run check` 通过；`--smoke-test`（临时 userData，避免与正在运行的实例抢锁）输出 `DESKTOP_SMOKE_OK tray=created windows=independent agents=5 ... activity=independent-after-collapse capture=ok`；离屏截图核对工作轨迹明/暗、友好报错、立即总结、贴边小条、悬浮球待机/贴边/忙碌与设置页；Go `go test ./...` 与 `go vet ./...` 通过（用独立 `GOCACHE` 绕开被其他进程占用的默认构建缓存）。真实窗口里托盘图标会留在通知区，若被 Win11 折叠进「隐藏的图标」需在系统设置里固定；首次启动气泡只出现一次。

### 2.11 托盘可见性、悬浮球点击与轨迹独立记录（2026-10-31）

- **图标回退**：`create-tray-icon.py` 改回原版金色球体 + 深棕「金」，统一生成 app-icon、tray-icon（idle/忙碌）与悬浮球位图；悬浮球与运行状态环恢复圆形。
- **悬浮球点击**：整颗球是 drag 区域会吞掉点击，补一块 `no-drag` 球心热区；点击展开主对话，外圈仍可拖动。
- **托盘**：单击/双击、托盘菜单与第二实例都会展开并聚焦主对话。Windows 11 把新图标放进「^」隐藏区：启动后读取 `NotifyIconSettings`，把本程序已有记录的 `isPromoted` 置 1 并重建托盘；仍不可见时发系统通知提示去「^」固定，设置页新增「打开任务栏设置」。
- **轨迹独立**：`activityTrail` 不再依赖协作模式，「展示工作轨迹」开启即记录，关闭且未开协作时停止；托盘菜单按 隐藏 / 展开 / 收起 三态切换，设置开关打开时自动展开；`AGENT.md` 的观察授权说明同步更新。
- **托盘不可用的根因**：用进程令牌实测确认，`--no-sandbox` 启动的 Electron 进程为 Low 完整性（SID `S-1-16-4096`），Job 对象/桌面都正常；Windows 会拒绝 Low 完整性进程的 `Shell_NotifyIcon`。Kimi、VS Code 等正常启动的 Electron 为 High（`S-1-16-12288`），托盘正常。因此不要用 `--no-sandbox` 正常启动；「启动管家服务.bat」本身不带该参数，打包版正常双击即可。低完整性提示界面已按用户要求移除，保留启动方式说明。
- **验证**：`node --test` 78/78、`npm run check` 通过；`--smoke-test`（独立 userData）通过托盘、主窗口与「聊天收起后轨迹独立显示」检查，随后因 8080 服务只返回 1 个 Agent 停在 Agent 扫描自检（环境数据差异，非本次改动）；离屏截图核对设置页新分区与圆形悬浮球。桌面打包成功：`desktop/dist/GoldenFinger-win/GoldenFinger.exe`。

### 2.12 界面细节修复（2026-10-31）

- **模型编辑表单错位**：`.model-editor-head` 是网格第一项却没有跨列，标题被塞进 96px 的标签列，「取消」竖排，标签与输入框整体错位；补 `grid-column: 1 / -1`，输入框/下拉改为占满右列。
- **Agent 调度过滤**：扫描器预置但 `installed=false` 的条目不再渲染，只列已安装或正在运行的 Agent；同时避免 detail 前再拼一次「已安装 / 已配置」。新增 `visibleAgents()` 与对应测试。
- **手机页面**：设置按钮换成标准齿轮图标（原为太阳形）；输入栏移除表情按钮及 `insertEmoji`；快捷入口补 `touch-action`/`overscroll-behavior` 与触摸拖动兜底。`web/index.html` 已同步到 `android/gosvc/webassets` 和 `desktop/server/web`。
- **验证**：桌面 `npm run check`、`node --test` **79/79**；离屏截图 `artifacts/ui-new/d-model-editor.png`（表单左标签右输入框）、`d-agents-filtered.png`（只剩已安装项）、`live-phone-fixes.png`（齿轮、无表情键、快捷项已滚动 120px）。`npm --prefix desktop run dist` 重新打包成功；桌面进程已重启，8080 正常响应。

### 2.13 手机端模型配置对齐桌面端（2026-10-31）

- 设置抽屉去掉旧的「预设 / 单项模型字段 / `prompt` 另存为方案」，改为与桌面端一致的方案列表：点行切换、铅笔编辑、垃圾桶删除，下面是虚线「新增模型」按钮。
- 内嵌编辑器字段与桌面端对齐：方案名称、Base URL、模型名、接口协议、API Key、推理强度、最大上下文、最大输出；保存即新增或更新并立即启用（`/api/settings/models`）。
- 联网搜索 Key 拆成独立字段与「保存搜索密钥」按钮，不再依赖旧的总保存。
- 验证：390×844 离屏渲染核对列表、编辑、新增三种状态（`live-phone-models.png`、`live-phone-model-editor.png`、`live-phone-model-new.png`）；页面脚本语法检查通过；`web/index.html` 三份副本哈希一致。**没有做真实保存写入**（避免改动用户的 `settings.json`），保存路径与桌面端复用同一接口。
- 手机 APK 内嵌页面仍需重新打包安装后生效。

### 2.14 多会话历史与关闭按钮（2026-10-31）

- **后端会话列表**：`SessionRepo.ListByOwner` 返回最近会话、消息数与首条用户消息预览；新增 `GET /api/sessions?date=YYYY-MM-DD`，按创建日期筛选，过滤空会话与旧版「以下是用户授权协作模式采集到的页面…」自动总结会话。补 HTTP 测试（列表、日期筛选、非法日期 400、消息读取、总结会话不出现）。
- **电脑端历史**：顶栏新增时钟按钮，打开覆盖式历史抽屉；按今天/昨天/具体日期分组，日期选择器筛选，会话名就是时间；支持「新对话」与恢复旧会话。上次会话写入 `localStorage`，启动时自动恢复；协作总结改为写入当前会话，不再每轮新建。
- **手机端历史**：原顶栏主题按钮换成历史入口，外观切换移入设置页「外观」；历史抽屉同样按日期分组、日期筛选、新对话、恢复会话，并把上次会话写入 `localStorage`。`web/index.html` 已同步到 `android/gosvc/webassets` 与 `desktop/server/web`。
- **关闭按钮**：手机抽屉、电脑历史抽屉等自定义关闭按钮统一改成普通 `×` 字符，不再使用 SVG 图形。
- **验证**：Go 全量测试通过；桌面 `npm run check`、`node --test` **81/81**；离屏截图核对电脑历史明/暗与恢复会话、手机历史列表/日期筛选/恢复会话、设置页外观；真实接口 `/api/sessions` 过滤后从 62 条降到 3 条对话；重新构建服务端与桌面包并重启（8080 正常）。手机 APK 需重新打包安装后生效。

### 2.15 修复对话 500 与工作轨迹空状态（2026-10-31）

- **桌面对话 HTTP 500 根因**：`withLogging` 包装的 `statusWriter` 只实现了 `WriteHeader`，没有转发 `http.Flusher`，`/api/chat/stream` 的 `w.(http.Flusher)` 断言失败后直接返回 500。给 `statusWriter` 补 `Flush` 与 `Unwrap`，并新增 `TestChatStreamEndpoint` 锁定。真实接口用空文本请求验证：现在返回 200 + `event: error`（text is required），不再 500。
- **工作轨迹点击报错**：没有本地截图的记录行以前仍会调用 `activityEvidence`，抛错变成红色提示；现在只在 `hasEvidence` 时打开证据，否则显示普通信息提示「这条记录没有本地截图，只能查看文字」。
- **工作轨迹空白**：正文/截图不可用（例如当前前台窗口是提权应用）时，采集源仅在窗口切换时记录一次；现在增加 60 秒窗口心跳，至少持续显示当前窗口。补 `activity-source` 测试。
- **说明**：截图里的「输入监听未启动」是因为当前自动化实例用 `--no-sandbox` 启动（Low 完整性），PowerShell 输入钩子无法安装；从 `desktop/scripts/test-input-monitor.js` 在本机正常完整性下运行是 `INPUT_MONITOR_OK`。正常双击启动不受影响。
- **验证**：Go `./internal/httpapi` 通过；桌面 `npm run check`、`node --test` **82/82**；离屏截图 `d-activity-click.png` 显示信息提示且不打开证据；服务端与桌面包已重建、应用已重启。

### 2.16 轨迹采集修复、总结超时与图片/视频附件（2026-10-31）

- **轨迹只剩 Idle 的根因**：`active-window.js` 用 `Add-Type` 编译 C# 调 `GetForegroundWindow`；`--no-sandbox` 启动的 Electron 是 Low 完整性，`Add-Type` 报 `A required privilege is not held by the client`，脚本继续执行后把 PID 0 解析成 `Idle`。改为 UI Automation `FocusedElement` 向上找顶层 Window，低完整性下也能读到 VS Code / 记事本等窗口标题；进程名读不到时从窗口标题尾部推断。`page-content.js` 同样改用焦点元素找根窗口，不再编译 C#。
- **观察卡死**：受保护/异常窗口会让截图或 OCR 永久挂起，`ActivityTrail.busy` 一直为真，2 秒轮询全部跳过，轨迹永久空白。给整体观察（20s）、截图（6–8s）、画面比较（3s）、OCR（8s）都加超时，超时后释放 busy 并继续下一轮；同时过滤本应用窗口、`desktop/*.html` 页面和「未知应用 + 空标题」占位。
- **输入监听**：Low 完整性下无法安装 PowerShell 输入钩子，改为跳过监听并不弹提示；窗口与正文仍由 UIA 轮询记录。正常完整性启动时照常启用。
- **立即总结卡住**：协作总结的 `/api/chat` 增加 30 秒 AbortSignal 上限，超时后返回错误并让按钮恢复，不再等 30 分钟。
- **图片/视频输入**：新增 `POST /api/uploads`（base64 → UploadDir，静态 `/uploads/` 提供）、聊天 `attachments` 字段与提示词本地路径说明。电脑端输入区加入附件按钮、图片/视频缩略图、消息内预览，输入框与发送键高度统一为 36px；手机端加入图片/视频选择、上传进度、附件条与消息内预览。
- **验证**：Go 全量测试通过；桌面 `node --test` **83/83**、`npm run check` 通过；真实 UIA 探测在 Electron Low 下能读到 `settings.json - … - Visual Studio Code` 与记事本正文；重启后轨迹实际记录了 VS Code / 记事本窗口与正文，不再出现 Idle 心跳，也不再记录本应用自己的页面；`/api/chat/stream` 空请求返回 200 SSE；真实桌面聊天经 IPC 返回回复（1.3s，无 500）；「立即总结」实际返回结果（5.9s，不再卡住）；上传接口返回 200 且静态 URL 可取回文件；离屏截图核对桌面附件与输入高度、手机附件条。服务端与桌面包已重建、应用已重启。
- **限制**：附件当前只上传/展示并把本地路径写进提示词，尚未把图片作为多模态内容发给 LLM；模型能否理解图片内容取决于后续多模态协议接入。

### 2.17 技能系统、BMA 桌面版集成与总结气泡（2026-10-31）

- **总结气泡**：旧版协作总结把内部提示词写成用户消息，聊天里会出现一条超长的蓝色气泡；现在 `internal:true` 的总结调用不写用户消息、没有历史会话时也不新建会话，只持久化助手总结；恢复旧会话时过滤旧的内部提示词。总结现在只显示为普通助手气泡。
- **BMA 桌面版**：GFA 自动发现 `D:\WebApp\BMA-Desktop`（或 `GFA_BMA_HOME` / `%LOCALAPPDATA%\Programs\BlockMemoryAgent` / 旧安装目录），读取 `.env` 的 `HTTP_ADDR`（本机 10011）与 `BMA_API_TOKEN`；未运行时通过 `app\launch.ps1 -NoBrowser -Quiet` 拉起。BMA 未运行但安装器存在时也标记为已安装，分派时自动启动；API 请求带 Bearer Token。已实测 `/api/agents` 返回 BMA installed/running，endpoint 为 `http://127.0.0.1:10011/api`。
- **Skill 系统**：新增 `internal/skill`（`skills/<name>/SKILL.md`，frontmatter `name`/`description`）、提示词技能清单、`list_skills`/`load_skill` 工具、`GET /api/skills` 和管理窗口「技能」卡片。首个技能 `skills/bma/SKILL.md` 覆盖 BMA 架构、安装/升级、启动器、HTTP 会话接口、任务编排、插件/技能、开发测试与排障。
- **验证**：Go 全量测试、`go vet` 通过；桌面 `npm run check`、`node --test` **83/83**；技能聊天实测返回 `trace:["load_skill → ok"]`；管理页技能卡片截图核对；BMA 健康检查/会话列表/能力检查在 10011 实测可用。
- **BMA 运行态提醒**：本机 BMA 的 `/api/capabilities` 报 `role meta 的 model_ref "doubao-seed-evolving" 不在模型注册表中`（安装目录缺 `config\models.json` 或绑定失效），需要先在 BMA 模型页修复，否则 BMA 任务会失败；GFA 侧发现与分派链路不受影响。
- 手机 APK 仍需重新打包以加载附件/历史/设置页面更新。

## 三、验证记录（2026-10-31 最新）

实际执行并通过：

- `go test ./...`：**20 个包全通过**；`go vet ./...`、`gofmt -l` 干净。
- 桌面：`node --test` **79 项通过**（含 `window-dock` 7 项、`activity-format` 7 项、Agent 列表过滤 1 项）、`node --check`（29 个 JS 文件）全通过。
- 界面重做本身用真实渲染截图核对（Electron 45 离屏渲染，非浏览器模拟）：390×844 手机竖屏（普通明/暗、适老、儿童、设置面板明/暗、记忆面板明/暗、收件箱、电脑助手）、1280×900 与 900×420 宽屏（明/暗）、管理窗口 900×780（明/暗）与 600×780 窄窗、CC Switch 720×700、工作轨迹 340×440、悬浮球 76×76、面板 460×680；并用 `getBoundingClientRect` 实测 390 宽下 `html/body/#app/header/#messages/#quick/#inputbar` 全部为 390，`scrollWidth` 无横向溢出，发送键右边界 376（=390-14），三枚顶栏按钮位于 254/298/342 各 34 宽、互不重叠。
- 过程中修掉的两个真实样式缺陷：`.icon` 从未定义导致图标按默认尺寸渲染；`.pc-check input` 被 `.field input` 的 `width:100%/min-height:44px/padding` 撑成大方块（已显式重置）。
- 说明：截图里的列表内容是注入的示例数据，用于检查样式；**不代表真实 Agent 扫描、真实配对或真实模型调用结果**。
- 二次改版后复验：桌面 `npm run check` + `node --test` 通过（本轮已增至 78 项），`--smoke-test` 真实窗口自检通过；手机端 390×844 明/暗两种主题下对话态与空状态截图复核，顶栏 4 个按钮 34px 互不重叠、聊天区无横向溢出；手机页面内真实调用设置接口（保存参数 → 生效 → 激活方案）返回 `medium/4096/64000` 与 `deepseek-v4-pro/high/8192`，验证后已切回 `deepseek-flash`。
- Android：Docker 内 `ANDROID_BUILD_OK`，AAR 11MB / APK 33MB，Kotlin `assembleDebug` 通过。
- 全仓 24 个 Markdown 相对链接 **0 失效**。
- **真实库副本**冒烟（`gfa.db`+wal+shm 复制到临时目录 + 独立 `GFA_CONFIG`）：迁移 `0001-0004 → 0006` 成功；`/api/me`、`/api/inbox/snooze`、`/api/jobs` 均 200；`/api/voice/text` 正确剥离唤醒词返回 `chat` 动作，flush 后片段落库且能被 `/api/memory/search?q=开会` 检索到。
- 中继真实进程联调：`cmd/relay` + `cmd/server`，配对码 → 配对 → WS command → result 全链路通过（`forwarded=2`；当时故意用无效 Key，验证的是链路与错误处理，不是模型质量）。

本轮新增测试清单：

| 包 | 覆盖 |
|---|---|
| `internal/voice` | 13 项：唤醒词/窗口过期/标点清洗/连续合并/flush 幂等/上下文窗口与条数上限/设备隔离/自定义唤醒词 |
| `internal/agenthub` | 8 项：执行+通知落库、幂等键、排队取消、失败记录、**超时不误标**、运行中取消、重启标 interrupted、空/超长拒绝 |
| `internal/httpapi` | jobs 生命周期 + 409/404/503；voice 端点含「记忆服务缺失必须报错」 |
| `internal/relay` | 手机侧配对持久化/结果往返/离线 queued/未配对拒绝/解绑/错误配对码/**同幂等键重试只执行一次**；job 三种 kind 路由 |
| `android/gosvc` | 真实起服务走 HTTP：token 必须、Inbox 可用、情景记忆写入+检索、设置不泄漏 Key、页面含 pc-panel/inbox-panel、voice 全链路 |

## 四、尚未完成 / 待验收

优先级较高：

1. **Android 真机验收**：安装、冷启动、口令唤醒、通知监听权限、各 ROM 保活、无障碍闭环。目前只有编译通过 + APK 产出，**一次真机都没跑过**。
2. **历史图谱回填**：导出 JSON 尚未写进 `mem_episode`/`mem_edge`，需要新的 `cmd/import-graphiti`（幂等、可重跑）。会写用户真实库，必须先备份。
3. **中继部署验收**：未在真实 2C2G + TLS 反代下压测，未做长时间断线重连观测。
4. **真实 CLI 非交互分派验收**：版本命令与终端启动已通过，不等于分派可用。
5. **桌面软件分派可靠性**：仍依赖窗口焦点、剪贴板与模拟按键。
6. **工作轨迹总结质量**：是否真正抓住「做了什么」而不只是页面静态文本。

工程卫生：

- 仓库里近百个源文件**从未提交到 git**。在提交之前 `.archive/source-snapshot-20261030.tar.gz` 是唯一回滚手段。这一条比上面任何功能都更该先处理。

## 五、历史阶段（2026-10-28 ~ 10-29，已完成，保留索引）

<details>
<summary>桌面协作与工作轨迹</summary>

工作轨迹从主聊天窗口拆出，支持透明度与显示开关；从「记录打开了什么」升级为记录当前窗口与页面正文；增加桌面内容读取、OCR、当前标签页识别、侧栏过滤、正文裁剪；相同内容不重复记录，过滤轻微鼠标变化与 OCR 抖动；修复 Enter 发送后只记录空白提示；成功总结后清理已纳入总结的截图证据，失败则保留。
</details>

<details>
<summary>模型配置与提示缓存</summary>

模型配置 JSON 持久化并热更新；支持 Responses 与 Chat Completions 两种协议（桌面实测仍为 Responses）；系统稳定提示与本轮动态上下文分离以复用缓存前缀；记录缓存命中指标。
</details>

<details>
<summary>Agent 调度与 CC Switch</summary>

Agent Hub 统一桌面软件、CLI、A2A、BMA、CC Switch；桌面软件支持发现/打开/唤起/分派；CLI 支持交互终端与非交互分派；Windows `.cmd/.bat/.ps1` 走参数化 PowerShell 通道，避免命令拼接；BMA 原生会话接收 `work_dir`。CC Switch 供应商与额度独立窗口，Claude 切换要求确认并备份。
</details>

<details>
<summary>CLI 双层检测（2026-10-29 本机实测）</summary>

修复：只依赖服务继承的 PATH、KimiWork 的 `Kimi.exe` 与 Kimi Code 同名导致漏扫、未扫 pnpm shim、`node.exe` 背后的 Agent 未识别。现读取 Windows 用户/系统持久 PATH，扫描 npm/pnpm/Bun/Cargo 与用户级目录，实际执行版本命令验证，并按进程命令行识别真实 Agent。

| Agent | 版本命令 | 启动终端 |
|---|---|---|
| Kimi Code | `2.3.0` | `state=opened` |
| Claude Code | `2.1.309 (Claude Code)` | `state=opened` |
| Pi | `1.4.0` | `state=opened` |

Hermes 未发现本机命令入口。以上只证明「入口发现、版本执行、终端启动」，不等于非交互分派已验收。
</details>

<details>
<summary>Pi 风格基础工具与 DeepSeek 联网（2026-10-29）</summary>

新增 `read`/`bash`/`write` 基础工具（相对路径以当前项目为基准）；`read` 支持起始行与行数限制，`write` 自动建父目录，`bash` 返回输出与退出码（默认 120s、最长 300s、限制输出长度）；`web_fetch` 与 `web_search` 改用 DeepSeek 官方 `/v1/tools/search`、`/v1/tools/fetch`，复用模型 Key 并热更新，未配置时明确报错而非返回桩数据。
</details>

## 六、维护约定

- 每次完成阶段性功能后更新本文件，不只改代码。
- 验证结果写明实际执行到哪一层，禁止把模拟测试写成真实任务成功。
- 不要只追加历史：过期的「进行中／未接线／未验证」结论必须删掉或改写。
- 不记录 API Key、Token、用户截图正文、数据库内容或其他隐私数据。
- 大范围修改前先读根目录 `AGENT.md`，保持启动、记忆与 Agent 调度边界。
