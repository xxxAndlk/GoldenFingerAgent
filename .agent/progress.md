# 当前进度与修改记录

最后更新：2026-11-05

本文件记录**当前实现状态**与验证证据，不是「全部功能已验收」的证明。每条尽量区分：代码实现 / 自动测试 / 真实环境验证。

> 阅读约定：已完成的条目不再保留「进行中」「未接线」等旧结论；被推翻的判断直接删除或改写，避免后来者按错误前提干活。

## 〇、2026-11-05 工作台界面重做、图标替换与托盘隐藏区

- **工作台重做**：`desktop/workbench.html` 换成新的设计系统（顶栏 + 分段标签栏 + 折叠区块 + 统计磁贴 + 卡片网格 + 明暗双配色），`desktop/workbench.js` 配套重写渲染逻辑。顶栏带应用图标、连接状态胶囊与图标刷新按钮；标签栏从普通按钮改成胶囊分段控件。
- **展开/收起**：新增 `collapseSection()` 组件，标题行本身就是开关——点一次展开、再点一次收起。「感知采集状态」「微信会话授权」「工作台怎么用」都用它；「渠道与邮箱」「网页采集」沿用原生 `details`，但改成同一套视觉与箭头。
- **微信会话卡片**：每个联系人一张独立卡片——圆形首字头像 + 名字 + 一行灰色小字（群聊/私聊与最近消息时间）+ 授权/撤销授权按钮；已授权的卡片描边变绿。稳定 `source_id` 不再占版面，只留在卡片 `title` 里备查。
- **两个按钮的分工**：「加载授权会话」只读会话名单，不碰正文；「测试数据库连接」会真的读一条最近消息来验证只读连接。原来测试按钮会静默先拉一次会话名单，看起来和加载按钮是同一个功能——现在两者分开，并在界面上写明区别。
- **图标替换**：新增 `desktop/scripts/extract-icon-source.py`，从用户提供的截图重建 512×512 透明背景源图（深色圆形 + 原子图案）；`create-tray-icon.py` 改为消费该源图，重新生成 `app-icon.*`、`tray-icon.*`、`tray-icon-busy.*` 与悬浮球位图 `app-icon.css`；工作台顶栏用 `app-icon.png`，与标题栏、任务栏、托盘同源。
- **托盘留在隐藏区**：`promoteTrayIcon` 改为 `demoteTrayIcon`，把 `NotifyIconSettings` 里本程序的 `isPromoted` 置 0，图标留在任务栏「^」隐藏区，点开即可看到；单击/双击托盘图标仍然展开主对话，右键菜单里的「工作台」入口不变。提示文案同步改成「点『^』展开隐藏图标区」。
- **验证**：桌面 `npm test` 146/146 通过、`npm run check` 通过；用 Playwright 渲染 `workbench.html`（注入桩 API）离屏截图核对：明暗两套配色、4 张会话卡片渲染正确、折叠后 `open` 变 false、再点一次恢复 true；`node --check` 通过。
- **未验证/未做**：托盘隐藏区的真实效果需要在 Windows 上重新启动应用才能看到（当前运行的是旧打包版，图标仍是旧的）；手机页面头部仍是「金」字文字头像，不是图片图标，本轮未替换；未做真机双端交付。

## 〇、2026-11-04 聊天式渠道管理与连接器复核

- **ACK 丢失分支修复**：Go 连接器现在读取桌面回执的 `ok/discard`。Spool 落盘失败使用 `ok=false, discard=false`，保留 pending 且不推进高水位；越权消息使用 `ok=false, discard=true`，允许丢弃但不写 Seen；只有 `ok=true` 才表示正文已持久化。补充 Go/JS 失败与越权测试。
- **聊天配置工具**：新增 `internal/collection` 与 `list_collection_channels/configure_collection_channel`，仅在电脑主服务注册。支持微信联系人/群、网页、桌面工作轨迹三类真实能力；配置工具按敏感能力分类，WorkTask 中仍需 Grant，Android 默认工具不包含它们。
- **微信发现**：新增桌面短命命令 `GET /api/channels/connector/requests` 与完成端点，按 owner 隔离。聊天添加联系人时由桌面刷新渠道并发现候选，唯一匹配才保存稳定 `source_id`；同名多项要求机主选择。候选只回安全字段，不回正文、Summary、密钥或数据库路径。默认 `latest_only`。
- **网页与工作轨迹**：网页默认 `current_page`（1 页、深度 0、不导航、60–86400 秒），只有明确授权才允许 `scope=site`。`activity` 会通过桌面命令真实调用协作开关、保存桌面设置并同步主窗口状态，然后才保存服务端配置；它是桌面轨迹总开关，不是单应用白名单。飞书、钉钉等未接通能力不对模型宣称可用。
- **桌面执行器**：新增 `desktop/connector-requests.js`，2 秒轮询、防止重入、错误安全截断；已加入语法检查和 Windows 打包清单。请求 12 秒未响应会失败并清理，不能把“请求已发出”当成配置成功。
- **验证证据**：`go test ./... -count=1` 全部通过，`go vet ./...` 通过；桌面 `npm test` 143 项通过，`npm run check` 通过；`git diff --check` 通过。测试覆盖网页默认边界与启停删除、微信唯一/歧义候选、撤权、桌面请求 owner 隔离和超时、活动真实确认、工具风险分类、桌面安全字段及防重入。
- **尚待真机验收**：尚未用真实模型在主聊天执行“增加某联系人/某网页”，也未重新做真实微信新消息持续监听与桌面启停联调；自动测试通过不等于这些真机路径已经验收。
- **启动失败根因（打包启动时发现）**：仓库根目录被标成 Low 完整性，应用树整体继承 Low，Electron 主进程在 Low 下无法初始化 Chromium 沙箱——启动即静默退出（退出码 3，无任何日志），最小 Electron 空应用同样复现，与本次代码无关。用 `icacls D:\WebData\github\GoldenFingerAgent /setintegritylevel M /T` 修正后应用正常启动（5 个 GoldenFinger 进程 + 服务端 + 连接器），健康心跳 `observing`，待发/失败/拒绝均为 0，微信 Trigger 稳定在清理后的 38 条。打包脚本已补：主程序、服务端、连接器三个 exe 都显式设为 Medium。
- **聊天窗口：重新打开只留最近 50 条并落在底部**：`.messages` 的 `scroll-behavior: smooth` 让连着渲染 50 条时的滚动动画停在中间；改成 `scrollToBottom()` 瞬时定位，面板展开时 `reloadLatestMessages()` 重新拉最近一页（`MESSAGE_PAGE_SIZE=50`），清屏和翻页加载过的旧内容不再留在窗口里。
- **聊天窗口：清屏按钮**：标题栏新增「清屏」（橡皮擦图标）。清屏只把当前这一屏消息从视口摘掉（加 `cleared-hidden`），服务端内容一律不动。清屏后不留等高空位——界面保持干净，新消息直接贴在顶部，不会悬在空白中间；内容撑不满一屏时用向上的滚轮手势恢复，能滚动时滚到顶部恢复。清屏范围用时间点（`clearedCutoff`）记录而不是 DOM 快照，中途重绘也不会把清掉的内容放回来；刚清完时浏览器会把滚动位置夹回顶部，用 `clearSettleUntil` 排除这一次误判。切换会话或重新打开窗口会清掉清屏状态。
- **聊天窗口：今日简报可关闭**：简报卡片拆成「打开工作台」按钮 + 独立「×」按钮（避免按钮嵌套）。关闭后记录 `dismissedBriefId`，同一版简报不再自动出现；出了新版本（新 `id`）才重新显示。
- **聊天窗口：时间贴边**：消息时间不再居中，机主消息右对齐（右上方），回复左对齐（左上方）。
- **回答失败给出具体原因**：LLM 调用失败不再只回一句“稍后再试”。`failureReply` 把上游错误（HTTP 状态、限流、网络、解析失败等）折叠空白、脱敏 `sk-***`、限长 300 字后直接写进回复；工具步数用尽也会说明“用完了 N 步”。待办解析里的建档/查询失败同样带上原因。
- **验证**：`desktop/scripts/widget-smoke.js` 用 Playwright 驱动真实 Electron 窗口验证：打开后距底 0px、今日简报可关闭、清屏后可见消息 0 条且内容不被撑高、向上滚轮恢复 50 条、关闭再打开后仍是最近一页且在底部（`WIDGET_SMOKE_OK time=right/left brief=dismissed messages=50 cleared=0 restored=50 grown=52 reopened=50`）；截图见 `desktop/artifacts/widget-opened.png`、`widget-cleared.png`。桌面 `npm test` 146 项通过（含 `test/chat-clear-contract.test.js`）。

## 〇、2026-11-04 云端共享记忆与跨端文件交付（M0–M7）

方案来源：`docs/云端共享记忆与跨端文件交付实施方案.md`。本轮按 M1→M6 顺序实现，云端与端侧各自可独立测试。

### 0.1 云端共享记忆（M1）

- 新增 `cmd/cloudserver`、`internal/memorycloud`、`internal/cloudauth`；云端独立 DB，复用 `store.LongTerm` 与 `memory.Native`。
- `cloud_device` 只存 Token 哈希；owner 永远由 Bearer Token 解析。scope 分 `memory:read/write`、`delivery:read/write`，无效/撤销 Token 统一 401。
- `POST /api/memory/v1/episodes`：`mem_episode.source_id` 全局主键，云端保存为 `cloud:<owner>:<original>`；`cloud_episode_meta` 维护双向映射与遗忘墓碑。相同来源相同正文返回 `duplicate`，不同正文 409，已遗忘 410。
- `POST /search` 返回派生事实并把 `source_refs` 还原为端侧原始 ID；`POST /forget` 支持 source/fact，source 遗忘写入墓碑并删除片段与派生边；`GET /health` 只返回数量/状态，不返回正文。
- 设备管理：`cloudserver device create|list|revoke`；旧数据迁移：`cloudserver migrate export|import`（导出 schema_version=1，导入校验引用、阻塞重复导入，Embedder 维度不一致时丢弃旧向量并重建）。
- 抽取失败现在会记录来源 ID 与错误（不输出正文），健康详情可看到 `last_error`。

### 0.2 RemoteMemory、Outbox 与自动召回（M2）

- 新增 `internal/memory/remote.go`：`Append` 先写 `memory_sync_outbox` 再异步上传；网络/429/5xx 指数退避并尊重 `Retry-After`；duplicate 视为成功；400/409 停止重试；401/403 暂停同步；410 标记为已遗忘。
- 新增 `BuildContextForQuery`：按当前问题检索共享长期记忆（1.2 秒超时，不阻断回答），与本地人物/事实去重后在 token 预算内打包；`PromptBuilder.BuildDynamicFor` 由对话循环传入当前用户原话。
- 电脑 `cmd/server` 与 Android `gosvc` 都按设置选择 Native 或 Remote；云模式不再启动本地抽取，避免双主写。
- 设置页新增「云端共享记忆」：地址、Device ID、Owner、Token（掩码）、交付开关、测试连接、待发/失败/暂停状态；保存后明确提示重启生效。

### 0.3 Artifact 通用文件化（M4）

- 迁移 `0018_artifact_files.sql` 增加 `storage_kind/original_name/source_kind/source_ref`；文本版本默认 `storage_kind=text`，历史数据无需迁移。
- `CreateFileDraft`/`AddFileVersion`：校验普通文件、解析符号链接、限制 100 MiB、默认拒绝可执行文件/脚本；先写临时文件边写边算 SHA-256，校验大小后原子改名，数据库版本与快照一致。
- 下载接口改为流式 `http.ServeContent`（支持 Range），使用原始文件名与 RFC 5987 文件名；文本版本保持原行为。
- 新增 `create_file_artifact` 工具（WorkTask 专用，需要 Grant），路径可相对工作目录。

### 0.4 云端交付与手机下载（M5）

- 新增 `internal/deliverycloud`：multipart 上传（幂等键 + 实际大小/SHA-256 校验 + 原子落盘）、收件箱、元数据、流式下载、ACK、撤销与 TTL 清理；默认 100 MiB / 72 小时；下载必须匹配目标设备、owner 与 `delivery:read`。
- 新增 `internal/deliveryclient`：上传客户端 + 持久 `artifact_delivery_outbox` Worker；同一 Artifact 版本 + 目标设备使用确定性幂等键，重复交付返回同一 Delivery；重启后继续上传。
- 新增 `deliver_artifact` 工具与本地 `/api/delivery/{inbox,status,content,ack}` 端点；交付完成后写入一条不含正文的 `system` 记忆摘要。
- 中继协议扩展 v2：`ArtifactRef` 与 `HelloPayload.Version`；两端通过 `?pv=2` 协商，v1 端收不到 ArtifactRef；任务结果按目标端能力裁剪。
- 手机 Web UI 新增「交付物」列表：浏览器端下载后校验 SHA-256 再 ACK；Android 由 Kotlin `DeliveryBridge` 下载到 App 私有目录、校验、ACK，并用 FileProvider 弹出保存/分享。

### 0.5 A2A 版本与文件产物（M6）

- 新增 `a2a_codec.go`/`a2a_artifact.go`：按 Agent Card `supportedInterfaces[].protocolVersion` 选择 v0.3 / v1.0 Codec；未声明版本时不把旧报文标成 1.0，默认 v0.3（端点可显式指定）。
- v0.3 使用 `message/send` + `kind:text`；v1.0 使用 `SendMessage` + 统一 Part（`text/url/raw/data`、`mediaType/filename`）与 `ROLE_USER`；TaskState 统一归一化。
- `Invocation.Artifacts` 接收外部文件：内联 bytes 严格限制 8 MiB 并保存到工作区 `.agent-artifacts/`；外部 URI 只保留元数据，不随 `delegate_agent` 自动联网。需要下载时必须由模型另行调用 WorkTask 专用工具 `download_agent_artifact`，并取得覆盖该工具及 `external_uri` 的独立 Grant。
- 独立下载仍只允许公网 HTTPS，连接时校验目标 IP，禁止私网/本机/元数据地址与非法重定向，下载后重新计算 SHA-256（上限 100 MiB），成功后才转为当前 WorkTask Artifact。
- 无文本但有文件时返回明确的文件产物说明，不再误报“没有文本结果”。

### 0.6 验证证据

- **自动测试**：`go test ./... -count=1` 全部通过；`go vet ./...` 通过；`git diff --check` 通过；桌面 `npm test` **146/146** 通过。
- **云端真实进程 E2E（无 LLM 模式）**：`cloudserver` + 临时 DB 实测：episodes `accepted → duplicate → 409`，forget 后重传 `410`；交付上传 `201`，android 收件箱可见，下载内容与源文件 `cmp` 一致、`X-Content-Hash` 匹配，ACK 200，撤销后下载 410。
- **未完成真机验证**：`config.yaml` 当前 LLM Key 返回 HTTP 401，因此“API 写入 → 真实模型抽取 → 搜索到派生事实”的实网闭环尚未通过；健康接口如实返回 `failed=1` 与 `last_error=HTTP 401`，代码路径由 fake backend 单测覆盖。换有效 Key 后需补做该验收。
- **Android 构建**：2026-11-05 已用项目内隔离的 JDK 17、Android SDK 34、NDK 26.3.11579264 和 Gradle 8.9 完成最新 `assembleDebug`，编译证据见 0.7；没有连接设备，未做真机运行验收。
- **部署边界**：`cloudserver` 监听 HTTP，公网必须放在 HTTPS 反向代理后（与 relay 相同）；云端首次部署需用有效 `llm`/`embedder` 配置才能抽取与向量检索。

### 0.7 验收复核修复（2026-11-05）

- **A2A 文件闭环**：新增 `Manager.ImportArtifacts`，同步 `delegate_agent` 通过 `attachArtifacts` 把已有本地文件转为当前 WorkTask 交付物；异步 Dispatcher 在任务结束前导入文件，没有 WorkTask 时创建「外部 Agent 文件产物」任务。普通导入只接受已有 `LocalPath`，绝不自动下载 `ExternalURI`；公网 URL 文件必须经 `download_agent_artifact` 独立授权，委派授权本身不能覆盖下载。无文本但有文件时只陈述“返回了 N 个文件产物”，导入成功后才补充“已转为 WorkTask 交付物”。
- **健康隔离**：新增 `EpisodeCountsByOwner`，云端 `/health` 只统计当前设备 owner；补充两个 owner 交叉计数测试。
- **手机配置入口**：手机 Web 设置面板新增「云端共享记忆」：地址、Device ID、Token、交付开关、测试连接与保存状态。
- **Token 安全存储**：新增 `internal/secretstore`；Windows 使用 DPAPI，Android 通过 gomobile `SecretStore` 桥调用 Kotlin Android Keystore，以不可导出的 AES-256 密钥和 AES/GCM/NoPadding（128-bit tag）加密。密文文件带 `GFAK1` 版本前缀；旧版无前缀明文 Token 在成功读取后立即重写为密文。Android 未注册 Keystore 桥时明确失败，禁止回退为私有目录明文。`settings.json` 只保留 `token_ref`，API 返回 `token=""`、`has_token=true`，不回传 Token。
- **可执行文件许可**：新增迁移 `0020_delivery_allow_executable.sql`，Outbox 持久化 `allow_executable`，Worker 上传时继续传给云端；补充 `.bat` 交付测试。
- **自动验证**：`go test ./... -count=1` 全部通过（含独立下载 Grant、URL 安全校验、桥接密文与旧明文迁移测试）；`go vet ./...` 通过；`git diff --check` 通过；桌面 `npm run check` 通过，`npm test` **146/146** 通过。
- **Android 编译验证**：gomobile 实际生成并 `javap` 核对 `SecretStore.encrypt/decrypt` 与 `Gosvc.setSecretStore` 签名。同步最新 H5 页面后，`go test ./android/gosvc -count=1` 通过，`./gradlew.bat --no-daemon --no-parallel --max-workers=1 :app:assembleDebug` 再次成功。当前 AAR 为 12,043,844 字节，SHA-256 `7eec867d988ce9daeabf047fb012f57548faa697920d87f2434efbc5fa980ae4`。APK 位于 `android/app/build/outputs/apk/debug/app-debug.apk`，51,269,836 字节，SHA-256 `9024fe5b0f23e560edf9ec6a6d69da5854a59b397eb0de11180e32a37fc87cf6`；包名 `com.goldenfinger.butler`，minSdk 26 / targetSdk 34，包含 arm64-v8a 与 armeabi-v7a，v2 调试签名验证通过。
- **仍未完成**：当前 `adb devices` 无设备，Android Keystore 真机加解密、旧 Token 真机迁移和 App 启动尚未验证；未做真实公网 HTTPS 外部文件下载与真实电脑→手机跨端交付；`config.yaml` 的 LLM Key 401 仍未解决，真实抽取闭环待有效 Key。单元测试、接口生成和 APK 编译不能替代这些实机链路验收。

### 0.8 手机版 H5 电脑调试（2026-11-05）

- 新增根目录 `package.json` 与 `scripts/dev-mobile-h5.js`：`npm run dev -- --host 127.0.0.1 --port 7100` 复用 `cmd/server` 和 `web/` 启动手机页面，支持 Kimi Work 转发 host/port；只允许回环地址，避免把鉴权不完整的调试接口暴露到局域网。
- H5 数据库、设置和上传文件隔离到 `artifacts/mobile-h5-dev/`，不读写正式 `data/gfa.db` 或根目录 `settings.json`；默认 `GFA_MEMORY_DISABLE=1`，需要完整记忆链路时显式打开。
- 新增 `scripts/verify-mobile-h5.js` 与 `npm run test:mobile-h5`：模拟 Responses / Embeddings，不消耗真实模型额度；启动真实 Edge 浏览器，以 390×844、移动触摸上下文验证首页、设置、发送消息、刷新恢复和历史面板，结束后清理服务进程。
- 实测结果：`H5_VERIFY_OK viewport=390x844 settings=ok chat=mock persistence=ok history=ok horizontalOverflow=false pageErrors=0 responses=1`。截图在 `artifacts/mobile-h5-verify/screenshots/`，已人工查看；这只证明 H5 页面和 HTTP 链路，不代表真实模型调用成功。
- 发现并修正共享页面分叉：`desktop/server/web/index.html` 比根目录 `web/index.html` 多 195 行云端记忆与交付物代码。已逐段对齐，三份页面（根目录、桌面服务、Android 内嵌）当前一致；根目录与桌面副本 SHA-256 均为 `22c5271639d947443ffb08e8aff9410847d8266c4022adf0617968c48d62304e`。
- 浏览器文件交付已有独立分支：普通 H5 直接下载、Web Crypto 校验 SHA-256 后 ACK；Android 使用 `gfaDelivery` 原生下载和保存/分享。本轮没有配置云端交付服务，未实测文件下载业务闭环。
- H5 可替代手机 UI 与 Web API 的日常调试，但不能验证 Android Keystore、语音唤醒、无障碍、通知监听、原生保存/分享和真实 WebView 行为；这些真机项仍保留，不因 H5 通过而标记完成。

## 〇、2026-11-03 感知采集层重设计（M0–M4）

方案来源：`docs/感知采集层重设计方案.md`。本轮把“配置了接收入口”改成“能解释是否真的看见、是否真的入库”。

### 0.1 统一信封与可靠投递

- 新增 `desktop/observation-envelope.js`：统一 `event_id`、`source_id/display_name`、`observed_at`、`occurred_at=null`、`completeness`、`content_hash`、`change`、`privacy`；`event_id` 用于幂等，`content_hash` 只用于内容变化。
- 新增 `desktop/observation-spool.js`：本地 JSON 持久队列；先入队再上传；2xx 标记 delivered，202 filtered 或 4xx 丢弃正文并保留原因，5xx/网络错误指数退避；重启恢复 pending。
- 新增客户端本地健康状态，工作台「接入设置 → 感知采集状态」显示：当前状态、当前会话、最近观察、最近入库、完整性、待发/过滤/失败、最近错误。

### 0.2 电脑微信本地数据库连接器（M1）

- 新增 `desktop/wechat-db-connector.js`、`cmd/wechat-db-connector`、`internal/wechatdb`：Electron 监督独立 Go 只读进程，NDJSON 协议；Go 直接完成 Windows 进程取钥、SQLCipher 页解密、WAL 合并和 modernc SQLite 查询，无 Python 运行时、无 UIA/OCR/发送模块。
- 微信 4.x 本地 SQLCipher 数据库按页只读解密到用户目录缓存；密钥从已登录微信进程内存只读提取，只保存在连接器进程内存中，不落盘；原始微信库不执行 checkpoint/VACUUM/写 SQL。
- 按稳定 `source_id = wechat-db:<account_key>:<chat_key>` 授权，不再按窗口标题；`message_key = <account_key>:<chat>:<shard>:<local_id>` 派发确定性 `event_id`，`(chat, local_id)` 不作为身份。
- 首版默认 `latest_only`：首次启用只记录当前高水位，不回灌历史；之后轮询授权会话最近 500 条，按稳定消息键增量发送。重复正文保留为多条，跨分片同 `local_id` 不覆盖。
- 方向/发送者来自 `sender_username` 与本机账号比较；群聊按分片解析真实发送者；`occurred_at` 用数据库 `create_time`，`observed_at` 是连接器观察到的时间，`completeness=complete` 只表示单条完整。
- 未授权来源不写检查点、不入队、不进入模型；旧 OCR Trigger 保留为历史数据，但不被新工具查询。
- 删除旧 `wechat-window.js`、`wechat-parser.js`、`wechat-connector.js` 及对应测试；工作台改为「加载授权会话 / 按稳定 source_id 授权 / 测试数据库连接」。
- 真实环境验收：本机微信 4.1.15.13、25 个库全部解密、0 个无密钥库；定位「严胜易」稳定 `wxid` 并读到 20 条真实消息。受控端到端补交后，`/api/channels/wechat/messages` 返回 201/Trigger，`source_id=wechat-db:...`、`occurred_at`、`completeness=complete` 正确；重启后数量不增长，未重复导入。

### 0.3 网页监听可靠性（M2）

- `BrowserCollector` 增加 `browser-collector-state.json`：持久化 `lastRunAt/lastSuccessAt/nextDueAt/lastError/pages`；桌面重启后按原调度恢复，不立即重复全站抓取。
- 每页保存 `hash/change/lastSeenAt/lastChangedAt`；正文无变化不重复上传，变化形成同 Thread 新版本；上传失败不推进成功检查点。
- 调度器改为桌面启动即运行；只有存在启用站点时才加载 Playwright 和浏览器会话。

### 0.4 工作片段语义化（M3）

- 观察事件增加 `contextKey/projectCandidates`：工作区项目名优先、网页按 host+首段路径、文档按标题、无法判断进入 `activity:unassigned:<日期>`。
- `CollaborationMode` 按上下文、空闲间隔、最大片段时长拆分 Episode；每个 Episode 独立 Thread，不再把一天所有项目混成一个 `activity:<日期>`。
- Episode 上报进入同一持久 Spool，失败可恢复；低置信度只带 `association_confidence`，不自动改写 WorkTask。
- 服务端 Episode 请求新增 `context_key/apps/project_candidates/association_confidence`，Thread 使用 `activity:project:*` / `activity:web:*` / `activity:unassigned:*`。

### 0.5 验证与限制

- 自动测试：桌面 134 项通过（旧 OCR 测试已删除）、`go test ./...` 通过；新增 Go schema/稳定 ID/方向、Go NDJSON 连接器、Spool、授权会话 UI、微信只读工具和路由硬阻断测试。
- 真实环境：微信 4.1.15.13 本地数据库读取成功，25 个库全部解密；`严胜易` 真实文本消息和既有动画消息均由 Go 连接器读取，event ID 与 Python 旧记录一致；切换 Go 后新文本消息经 Electron → Spool → Trigger 入库，重启不重复。Python 连接器与 venv 已删除，桌面包不再包含 Python。
- 限制：数据库密钥提取依赖微信已登录和 Medium 及以上权限；连接器只承诺本机数据库已保存且已授权会话；首次历史回填、媒体下载、撤回/修订事件和真实发送器尚未实现；BMA/UI 不再作为隐式 fallback。

### 0.6 微信连接器验收修复（2026-11-04）

验收发现四个阻断问题，本轮逐项修复并补齐可复验证据。

- **持续刷新**：`internal/wechatdb` 新增 `Reader.Refresh()`。每轮轮询前把源库同步到解密副本——源库首页指纹/大小/修改时间变化时重新解密整库，并重放 WAL 中已提交的帧。WAL 只应用到最后一个提交帧，未提交事务不会落到副本上；盐值不匹配说明 WAL 已重置，后续帧作废。
- **投递确认后才推进检查点**：连接器新增 `ack` 请求。Go 侧发出消息后挂在 pending 集合里，Electron 只有在 Spool 真正落盘后才回 `ack`，收到确认才计入已处理。高水位不会越过未确认消息，进程重启会重投；30 秒无确认自动重发。检查点文件新增 `pending` 与 `baselined`。
- **搜索授权过滤**：`search_wechat_messages` 不再只在传入 `source_id` 时校验授权，任何情况下都按当前授权来源逐条过滤；撤销授权后历史正文读不出来。
- **撤权清队列**：桌面端新增 `POST /api/channels/connector/status` 心跳；连接器配置变更时对被撤销来源调用 `ObservationSpool.revokeSource()`，待发记录立刻丢弃正文并置为终态，不再等服务端过滤。
- **真实健康状态**：桌面每 15 秒上报连接器状态（状态、授权数、最近读库、最近事件、待发/失败/拒绝、错误）。`get_connector_health` 读这份心跳，心跳超过 90 秒视为 `stale`；历史上有消息不再等于“正在观察”。桌面报 observing 但服务端无授权时标 `authorization_mismatch`。
- **记忆角色区分**：`mem_episode` 增加 `role` 列（`migrations/0015_longterm_role.sql`）。抽取提示词区分机主/联系人/系统陈述：联系人说的话只作为第三方事实，不得记成机主的意愿或安排。
- **测试**：新增 Go 连接器 NDJSON 协议与检查点测试、WAL 帧选择与合并测试、刷新探针测试、撤销授权搜索测试、健康状态心跳测试；桌面新增监督器 ACK、落盘失败不确认、撤权清队列测试。桌面 138 项、`go test ./...` 全部通过。
- **真机证据**：用真实微信密钥构造合成 SQLCipher 库，验证连接器不重启即可读到 WAL 中的新消息；本机实测补回今天上午 08:33–09:44 到达的 34 条消息（旧版静态副本永远看不到），三次重启后 `wechat_desktop` Trigger 数稳定在 536，无重复导入。
- **清理**：用户确认后删除上一轮切换 Go 时回灌的 498 条历史 Trigger 及其 996 条 `trigger.created/trigger.queued` 事件，并 VACUUM；`data/gfa.db` 微信 Trigger 从 536 降到 38（34 条今天真实消息 + 11-03 23:49 一条 + 10-30 17:08 测试一条 + 旧 OCR 两条）。删除前已备份到 `.archive/db-backup-20261104/`。重启后数量稳定，连接器不会回灌。

## 〇、2026-11-02 SuperAgent 完整修复（M0–M9）

方案来源：`docs/SuperAgent完整修复实施方案.md`。本轮把“Worker 只写日志”升级为真正的长期任务控制面。

### 0.1 数据与迁移

- 新增 `migrations/0011_work_tasks.sql`、`0012_execution_control.sql`、`0013_memory_policy_devices.sql`：
  - `work_task`、`work_task_live_state`、`work_task_thread`、`work_task_inbox`、`artifact`、`artifact_version`。
  - `work_task_checkpoint`、`action_grant`、`tool_invocation`，以及 ActionOutbox 的 grant/invocation/expected effect/reconciled 字段。
  - `mem_edge` 增加 `status/scope/confidence/version/confirmed_*`；新增 `personal_policy`、`device_environment`、`device_capability`、`work_task_device_binding`。
  - `agent_job` 增加 `work_task_id/job_role`，`chat_session` 与 `agent_action_outbox` 增加 `work_task_id`。
  - 旧 `agent_id=superagent` Job 按确定性 `legacy-<job_id>` 回填 Task，第二次迁移为空操作；已加 0010 旧库升级测试。
- 单主执行约束：partial unique index 只限制 `work_task_id <> '' AND job_role='primary' AND status IN ('queued','running')`，允许主 Worker 同时拥有多个 delegated 子执行。

### 0.2 控制面

- `internal/worktask/`：Create/Get/List、AddMessage、Pause/Resume/Cancel/Recover/Reopen、Wait、SetActive、PendingAcceptance、Complete/Fail、Thread 关联、ContextReceipt。
- `/api/work-tasks*` 全量 API；所有变更支持 `expected_version` CAS，冲突 `409`。
- `internal/superagent/coordinator.go`：`/task new|list|show|msg|pause|resume|cancel` 与“新建任务/暂停任务/恢复任务/取消任务”低成本分流；普通聊天仍走主会话快速路径，不额外调用 Router 模型。
- Router 新增只读 `TaskIndex`（最多 20 个活跃任务）和 `create_task/update_task/prepare_task/pause_task/resume_task/cancel_task`；模型只能选择索引内 Task，低置信度自动转 `ask_user`，越界 Task 直接失败。
- Thread 新事件写入 WorkTask Inbox，不再只写 JobEvent；旧 `create_worker/route_to_worker` 兼容为创建/更新 WorkTask。
- 旧 `/api/worker-tasks/{id}/steer`：绑定 WorkTask 时写 Task Inbox 并返回 `202 + inbox_id + queued`；没有 WorkTask 的旧 Job 返回 `409`，不再假称实时注入。

### 0.3 执行链路

- `agent.Runtime` 增加 `StepController/ToolProfile/WorkTaskID/JobID`；每次模型请求前执行 `BeforeStep`，包括工具步数耗尽后的最终回答前。
- `WorkerStepController` 在事务内完成“写 Worker 会话用户消息 + 标记 Inbox consumed + 事件”，进程在消费前后崩溃都不会丢消息或重复消费；普通聊天 `StepController=nil`。
- Pause 在下一个安全步骤边界停止并释放 Worker 槽位；Cancel 继续走 context cancel；Resume/Recover 创建**新 Job**，旧 Job 不复活。
- `syncTaskSegment` 把执行段结果映射为 `waiting_user`、`failed` 或 `pending_acceptance`；Job completed 不再自动等于 Task completed。
- `create_artifact` 工具只对 WorkTask 暴露，创建不可变 Artifact 版本；执行段结束时 Supervisor 自动把草稿提交验收。
- `internal/watchdog/` 默认 15s：心跳 90s 停滞标 interrupted、只读任务自动从 Checkpoint 恢复、重复工具循环/超预算/unknown 副作用暂停并通知；有副作用 unknown 不自动重放。
- `ContextBroker` 注入任务目标、Live State、Artifact 索引、按任务范围检索的记忆、Thread 摘要、Personal Policy；预算约 6000 token，超限记录截断原因和 ContextReceipt。

### 0.4 权限与记忆治理

- ToolDescriptor 增加风险、环境、Grant 要求；`read` 只能只读，`internal` 允许准备和授权工作目录内写入，`external` 允许外部草稿。
- `bash`、`delegate_agent`、`switch_claude_provider` 与未知工具必须有覆盖工具名、资源范围和内容 hash 的有效 Grant；过期/撤销/次数耗尽/hash 变化/设备撤销都会拒绝并记录 denied `tool_invocation`。
- 参数落库前脱敏 password/token/secret/cookie/authorization/api_key；ToolResult 按不可信数据处理。
- ActionOutbox 审批生成一次性、24h、内容 hash 绑定的 external_write Grant；编辑草稿撤销旧 Grant。
- `mem_edge` 自动抽取默认 inferred/conflicted；inferred 不得让 confirmed 失效，冲突保留两条证据；提供 confirm/conflict API（version CAS）。
- Personal Policy、设备/能力/Task 绑定 API 已接线；`waiting_device` 恢复前重新校验设备在线且未撤销。

### 0.5 桌面端

- 工作台从 Job 列表升级为 Task 中心：执行中/等待/验收/暂停异常/完成分组；目标、完成标准、阶段、下一步、Profile、状态版本。
- 支持追加指令（queued/consumed 语义）、暂停/继续/取消/恢复异常/重新打开；详情展示 Live State、Artifact 版本与验收、Grant 撤销/创建、任务事件时间线、执行段。
- Electron 链路 `assistant-api.js → preload.js → main.js IPC → workbench.js → renderer.js 角标` 已全部更新；主窗口待处理条改为统计 WorkTask 等待/待验收/异常。
- 保留渠道采集、网页采集、简报与审批页。

### 0.6 验证

已执行：

- `go test ./... -count=1`：全部通过。
- `go vet ./...`：通过。
- `cd desktop && npm run check`：通过。
- `cd desktop && npm test`：通过（含 WorkTask API、Workbench 契约、ToolGateway、Watchdog、ContextBroker、记忆冲突）。
- 新增测试覆盖：0010 旧库升级与二次空操作、Task CAS/owner 隔离/单主 Job、Inbox 幂等与单次消费、Artifact 版本唯一与验收、BeforeStep 注入/pause 边界、Worker Task 等待后新执行段、Router TaskIndex/低置信度/越界、Grant 过期撤销 hash、调用参数脱敏、Outbox 审批生成 Grant、Watchdog 心跳/只读恢复/循环/unknown、Context Receipt/截断、inferred 不覆盖 confirmed、治理与设备 API。
- **真实模型验收**：用真实 LLM 创建“验收：长期任务”，Worker 正常进入 `pending_acceptance`，结果摘要为“我可以接着上一次的进度继续做，跨执行段也不会断线，当前这个任务的标题是「验收：长期任务」”；事件时间线包含 `task.created → task.active → task.context.receipt → task.pending_acceptance`；Worker 会话未出现在 `/api/sessions`；调用 complete 后 Task 变为 completed/v3。
- **真实旧库升级验收**：`data/gfa.db` 已成功应用 0011/0012/0013，原有用户/会话仍可读取。升级过程中发现并修复了 0011 中 `UPDATE chat_session` 子查询返回 NULL 导致 NOT NULL 失败的缺陷，补充了“无 Job 的旧会话”升级测试。
- **桌面打包与 smoke**：`GFA_DIST_DIR=GoldenFinger-win-build2 npm --prefix desktop run dist` 成功；在工程外隔离目录执行 `GoldenFinger.exe --smoke-test` 返回 0，输出 `DESKTOP_SMOKE_OK tray=created windows=independent agents=3 bma=callable directory=present protocol=responses activity=independent-after-collapse capture=ok`。官方 `desktop/dist/GoldenFinger-win` 已替换为包含 0011–0013 和 WorkTask UI 的构建，重置 Medium 完整性标签后启动，`/api/work-tasks`、`/api/settings/channels` 返回 200。

未验证 / 已知限制：

- 未对真实外部 CLI 做“运行中插话”验收；不支持运行输入的适配器仍只能下一段生效。
- 设备在线状态目前由 API/绑定表维护，尚未把 Android WebSocket 生命周期自动同步进 `device_environment`。
- `unknown` 副作用只暂停并提示，未实现自动只读 Reconciler。
- 真实模型验收只覆盖无 Artifact 的摘要路径；Artifact 版本/验收由自动测试覆盖。

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
- **BMA 运行态提醒（已修复，见 2.18）**：升级后安装目录缺 `config\models.json`，所有角色曾未绑定；现已恢复注册表并把角色切换到可用的 `API-deepseek-v4.1-flash`，`/api/capabilities` 的 `all_ok=true`。原 ARK 模型仍受月度配额 429 限制，不要切回。
- 手机 APK 仍需重新打包以加载附件/历史/设置页面更新。

### 2.18 BMA 澄清回路与模型绑定恢复（2026-10-31）

- **真实 BMA 链路验证**：直接 API 实测 fast 档会话创建 → 在临时工作目录写入 `BMA-E2E-OK.txt` → `result` 回传；同会话 `POST /messages` 继续提问并读到更新后的 `result`；GFA `POST /api/agents/delegate` 走 daily 档，12.3 秒返回最终回复并验证 `GFA-BMA-E2E.txt` 内容。
- **修复 `awaiting_clarify` 卡死**：BMA 计划确认或 `ask_user` 提问时，旧 GFA 只认终态，会一直轮询到 5 分钟空闲/30 分钟上限；桌面调用方 10 分钟 HTTP 超时。现在 `waitBMASession` 识别 `pending_clarify` / `awaiting_clarify` / `paused`，立即返回问题、选项、`question_id` 和会话 ID。
- **新增澄清答复**：`POST /api/agents/clarify`（`session_id`/`answer`/`question_id`）转发到 BMA `/sessions/{id}/clarify` 并继续等待终态；Agent 调度结果区会显示问题、选项按钮和自定义答复输入框。实测 GFA 返回 `awaiting_clarify` 后回答 `yes`，BMA 继续创建 `CLARIFY-E2E.txt` 并返回最终结果。
- **模型注册表恢复**：从源码模板恢复 `D:\WebApp\BMA-Desktop\config\models.json`（10 个条目）；原 ARK 端点返回 `AccountQuotaExceeded`（2026-11-17 重置），`od-deepseek` 指向未启动的 3425 代理；探测 `API-deepseek-v4.1-flash` 可用后，把 11 个角色全部绑定到该模型（切换前临时备份，验证后已清理）。
- **验证**：`go test ./...` 全通过、`go vet ./...` 无输出；新增 agenthub 澄清测试；桌面 `node --test` **84/84**、`npm run check` 通过；无头 Edge 加载真实 `agents.html`（stub API）确认问题/选项/答复后完成态渲染正确。
- **注意**：GFA 要求 `work_dir` 已存在且是目录；BMA 会在其中自动创建 `.bma/tmp/<session>`。若需要全新目录，先由调用方创建再分派。

### 2.19 BMA 桌面操控与 Agent 分工（2026-10-31）

- **桌面操控实测**：`host_computer_use` 插件 running，提供 33 个真机工具。用 `cluster` 档在“画图”窗口完成 `list_windows` → `set_target` → `activate_window` → `get_frontmost_app` → `get_window` → `click`（HID，屏幕坐标 959,578）→ `screenshot`；返回 `effect=changed`、`route=input`、`delivery=hid`，未触碰其他窗口。
- **daily 档打通**：把安装目录 `config/plugins.yaml` 的 `host_computer_use.roles` 从 `meta/test_assistant` 扩为 `meta/domain/test_assistant`，`POST /api/plugins/reload` 生效；随后通过 GFA 默认 BMA daily 分派再次点击画图成功（DomainAgent 无需切 cluster）。升级 BMA 后需复查该 roles。
- **安全边界**：首次用记事本测试时，BMA 误把用户已打开、未保存的记事本当作目标并弹出“打开方式”对话框；它在焦点不确定时没有继续输入，会话被手动取消，用户内容未被改动。之后改为唯一标题的“画图”窗口并显式 `set_target`，才继续测试。桌面任务必须锁定唯一窗口，焦点不确定就停止。
- **Agent 分工**：BMA = 日常助理 + 真机 GUI 操控；Claude Code / Pi = 编程主力。`butler.md`、`AGENT.md`、`skills/bma/SKILL.md` 和 Agent 目录描述已写入该分工；GFA 的自动派发权重未改，编程任务应显式选择 CC/Pi，GUI 任务选 BMA。
- **验证**：GFA `daily` 分派返回 completed；`/api/plugins` roles 包含 domain；测试用画图窗口和 BMA 会话已清理；用户未保存记事本保持原样。

### 2.20 工作轨迹持久化、阶段总结与 3 天证据保留（2026-10-31）

- **轨迹持久化**：`ActivityTrail` 新增 `userData/activity-events.jsonl` 追加式存储；事件、总结标记、证据清理都以记录/更新行保存，启动时按事件 ID 合并恢复。内存最多 5000 条，界面仍展示最近 300 条；不再因 300 条上限丢失旧事件。
- **阶段总结**：`CollaborationMode` 每轮生成 `phase_id`、起止时间、覆盖事件 ID、原始摘要和模型总结，写入 `userData/activity-summaries.jsonl`；启动时恢复上一阶段总结和未总结事件（`seedPending`），下一轮提示词带上一阶段总结。长阶段摘要不再只取最后 60 条，超过 60k 字符时才标注剩余条数。
- **3 天证据保留**：总结成功只调用 `markSummarized()`，不立即删截图；事件保存 `summarizedAt`、`phaseId`、`evidenceExpiresAt`（总结时间 + 3 天）。`removeExpiredEvidence()` 每 3 天保留期到期后删除截图并追加清理更新；未总结截图也按捕获时间 3 天过期。界面增加「已总结」标记，悬停显示截图保留截止时间；过期后点击只提示文字查看。
- **清空联动**：`activity:clear` 现在同时清空 `ActivityTrail` 事件/证据/阶段文件和 `CollaborationMode` 的待总结队列/上一阶段总结，避免清空后下一轮总结继续消费旧数据。
- **测试**：桌面 `node --test` **89/89**、`npm run check` 通过；新增持久化恢复、3 天证据过期、清空持久化文件、阶段记录/上一阶段总结、清空联动、70 条细节摘要测试。
- **注意**：轨迹 JSONL 保存页面正文等本地内容（不提交 Git）；截图仍只在本地，3 天后删除。总结结果继续写入长期记忆。

### 2.23 总结只陈述、不再提问（2026-10-31）

- 协作总结提示词从“最后问一个帮助用户继续工作的问题”改为：只按“本阶段新增进展、仍在继续”陈述事实，不提问、不请求确认/授权、不给下一步建议、不以问句结尾。
- 新增 `stripTrailingQuestion` 兜底：模型偶尔仍以问句收尾时，去掉结尾问题行；如果整段只有问句，则回退为“本阶段没有可陈述的新进展。”。
- 阶段记录、`lastSummary` 和弹窗展示都使用清理后的文本；旧总结不回溯改写。
- **验证**：桌面 `node --test` **92/92**、`npm run check` 通过；新增结尾问句清理和提示词不含“最后问一个问题”的测试。

### 2.24 Agent 缓存、总结流内定位与消息时间（2026-10-31）

- **Agent 缓存**：`Manager.Scan()` 默认返回缓存；新增 `Refresh()`/`InvalidateCache()`。`GET /api/agents` 默认读缓存，`?refresh=1` 或 Agent 面板「重新扫描」强制刷新；新增 `refresh_agents` 工具，`list_agents` 改为读已保存配置。BMA 分派直接走 `BMAURL`，不再为一次调用扫描全部 Agent；CLI/桌面 Agent 先查缓存，目标不存在才刷新一次。
- **总结入口进对话流**：去掉固定悬浮单行，改为在 `#messages` 中按最近一次总结的 `completedAt` 插入「已产生 N 条总结」节点（带自己的时间）；后续消息把它往上顶，下一轮总结更新计数并移到新的结尾。点击仍打开阶段总结弹窗。
- **消息时间**：服务端消息 DTO 增加 `created_at`；桌面和手机每条消息上方显示 `at`/发送时刻。历史恢复、分页加载、实时发送都带时间。
- **验证**：桌面 `node --test` **92/92**、`npm run check` 通过；无头 Edge 桌面验证消息/总结节点顺序为 `10:00 消息 → 10:05 消息 → 10:07 总结 → 10:10 消息`，点击总结弹出阶段总结；手机端 3 条消息显示 `10:00/10:05/10:10`；Go `agenthub` 缓存测试与 BMA 免扫描分派测试通过。
- **缓存实测**：真实 `/api/agents` 第二次请求 **16ms**（缓存），`?refresh=1` **3520ms**（重新扫描），均返回 11 个 Agent；证明调用不再触发全量扫描。

### 2.26 悬浮球自由拖动与移除侧边贴靠 UI（2026-10-31）

- **悬浮球拖动**：移除 `.orb` 的 `-webkit-app-region: drag` 和中心 no-drag 热区，改为 pointer 事件：按下记录 screen 坐标，移动超过 4px 才进入拖动，通过主进程 `widget:drag-start/move/end` 调 `setPosition` 并限制在工作区；未移动则保留点击展开。
- **取消贴边**：主悬浮球和轨迹窗不再调用 `watchDock`/`snapBounds`，只保存自由位置；悬浮球不再显示金色贴边滑轨，轨迹窗不再吸附屏幕边缘。
- **移除侧边小条**：轨迹窗不再收起为 edge-tab；`activity-hide` 隐藏，收起等价于隐藏轨迹窗口，关闭窗口也改为隐藏。旧的 `activityCollapsed` 配置在启动时忽略。
- **验证**：桌面 `node --test` **92/92**、`npm run check` 通过；真实打包 Electron via CDP：拖动后主窗 `y` 从 654→694（x 受右边界 clamp），拖动后点击仍能展开面板；轨迹窗实际尺寸 340×440，`edge-tab` 与 `activity-hide` 均为 hidden。

### 2.27 修复打包版模型配置重启丢失（2026-10-31）

- **根因**：打包版位于工程目录内时，`ServerManager.prepare()` 让 `config.yaml` 沿用工程根，但 `settings.json` 仍放在 Electron userData；换一种启动方式/userData 就会读到另一份模型列表。保存接口本身正常，测试模型确实写入了当时的 userData settings。
- **修复**：只要检测到工程根 `config.yaml`，`settings` 也固定为工程根 `settings.json`，配置与模型列表始终同源；独立安装（无工程根 config）仍使用 userData。
- **验证**：打包版 `/api/settings` 的 `settings_path` 变为工程根 `settings.json`；通过 API 添加测试模型后文件落盘，重启打包版后模型仍在，随后删除测试模型。
- **注意**：旧开发版 userData 里的模型（`.runtime\desktop-userdata\settings.json` 中的“当前生效/OpenDesign”）不会自动迁移到工程根 settings；需要的话单独迁移。桌面测试增至 **93/93**。

### 2.28 手机版模型配置重启验证（2026-10-31）

- 手机版没有 Electron userData 分叉：`android/gosvc` 固定使用 `context.filesDir`，模型设置写入 `<filesDir>/settings.json`，每次 `StartServer(dataDir)` 都打开同一路径。
- 新增 `TestSettingsModelsPersistAcrossRestart`：真实启动 gosvc → 保存模型 → StopServer → 用同一 dataDir 重启 → `/api/settings` 仍能读到该模型；`go test ./android/gosvc` 通过。
- 只有清除应用数据、卸载重装（清数据）才会丢失；普通升级安装和进程重启不会。

### 2.29 轨迹窗补回关闭按钮（2026-10-31）

- 按“不要侧边贴靠 UI”的约定，轨迹窗不再贴边缩成小条，也没有侧边展开按钮；但这导致窗口只能通过托盘/设置隐藏。
- 轨迹窗标题栏补回可见的 `×`（`#activity-hide`），点击只隐藏轨迹窗，不吸附、不生成侧边条；需要时从托管窗口的设置页“记录并展示工作轨迹”或托盘重新显示。
- 打包版已重新构建并重启，8080 正常；桌面测试 **93/93**、`npm run check` 通过。

### 2.30 轨迹窗高度裁剪（2026-10-31）

- 按用户要求只裁剪大小，不做自动移动/避让：`ACTIVITY_SIZE.height` 从 440 降为 **340**，轨迹窗默认尺寸变为 340×340。
- 启动时读取旧 `activityExpandedBounds` 后按 340 高度上限裁剪，并把裁剪结果写回 settings；只改高度，不改变用户保存的 x/y 位置。
- 轨迹窗标题栏有「立即总结」「清空」和 `×`；`×` 只隐藏，贴边收起逻辑由 2.31 接管。

### 2.31 轨迹窗拖出边缘自动收起与侧边收回按钮（2026-11-01）

- 去掉标题栏的手动收起按钮；`watchDock` 在拖动停顿后用 `overflowEdge` 检测窗口超出工作区边缘是否达到自身尺寸 20%，达到就按该方向自动 `setActivityCollapsed` 成贴边小条。
- 展开且贴边时，在轨迹窗左侧外侧显示独立收回按钮；窗口拖回中间、不再贴边时自动隐藏；收起成小条后该按钮也隐藏。
- 收回按钮改为独立小窗口 `activity-handle.html`（32×46），外观复用贴边展开小条的半透明玻璃样式、箭头方向相反；贴在卡片外侧，不占卡片宽度，也不再留透明空列；卡片左侧空间不足时自动换到右侧并旋转箭头。轨迹卡片恢复满宽，仅保留 `data-edge` 用于圆角。
- 按钮跟随轨迹窗移动；收起、拖回中间、隐藏轨迹时自动隐藏，展开且贴边时恢复。点击按钮收起，点击贴边小条展开。
- 贴边小条仍是展开按钮，点击恢复原 340×340；`×` 仍是完全隐藏，不影响贴边。
- 轨迹窗高度上限仍为 340；收起时实际窗口 32×46。主对话面板不贴边。
- 真实 Electron 验证：轨迹窗 `340×340`、面板 `left=0`（无空列）；收回按钮窗口 `32×46` 位于 `x=1546`，轨迹窗 `x=1580`，`outsideCard=true`；点击收回 → 轨迹 32×46、小条出现、按钮窗口隐藏；点击小条 → 轨迹恢复 340×340、按钮窗口恢复显示。桌面检查与测试 **94/94** 通过。

### 2.34 主对话不再背轨迹，总结改走子代理（2026-11-01）

- 去掉主对话每轮自动注入 `activityTrail.chatContext()` 的行为；“你是谁”等普通对话现在只发送原始用户消息。
- 只有 `wantsActivitySummary()` 命中“今天/工作/总结/做了什么/做事”等意图时，桌面端才读取当天轨迹并调用新增的 `POST /api/summarize` 子代理；子代理用独立上下文、无工具、单次 LLM 调用，返回最多 4000 字摘要，再交给主对话。
- `/api/summarize` 不创建会话、不调用工具、不写聊天历史；主对话继续负责最终回复与持久化。
- 新增 `desktop/activity-summary.js` 意图判断与单测、`AssistantApi.summarizeActivity` 单测、Go `TestSummarizeEndpointUsesNoToolsAndSeparateContext`。
- **注意**：本次验证时 OpenDesign 本地代理 `127.0.0.1:3425` 对最小 `/v1/responses` 请求也返回 `HTTP 502 ECONNRESET`，所以“你是谁”仍会显示“卡住了”；这是上游模型代理故障，不是上下文或工具限制。需要恢复对话可临时切到 `deepseek-flash`。

### 2.33 工作轨迹恢复页面正文捕获（2026-11-01）

- **先确认版本**：当前主采集源就是自研 `NativeActivitySource`；`.archive/source-snapshot-20261030.tar.gz` 里的活动链路与当前一致，没有更早的“能读正文”的独立实现可回滚。Screenpipe 只在 `screenpipe-shadow.js` 里做默认关闭的影子评估，不是主链路。
- **根因**：应用以 `--no-sandbox` 启动后是 Low 完整性（RID 4096），`desktopCapturer` 对 Chrome/VS Code 返回空缩略图，UIA 也读不到高完整性窗口正文，于是只剩窗口标题。
- **截图兜底**：新增 `desktop/screen-capture.js`，用 PowerShell `CopyFromScreen` 截取当前窗口/页面区域；`desktopCapturer` 失败时自动回退。
- **OCR 修复**：Windows OCR 原脚本用 `StorageFile.GetFileFromPathAsync`，Low/AppContainer 下返回空流并报 `Invalid pointer`；改为 `InMemoryRandomAccessStream + DataWriter` 后成功。实测打包版对 Chrome 页面读到 523–902 字符，对 VS Code 读到 971 字符。
- **隐私处理**：新增 `desktop/redaction.js`；OCR/UIA 文本入库前脱敏 API Key、Token、密码、中文“密钥”等；命中敏感内容或区域截图未读到文字时丢弃截图证据。实测 Chrome API 页面和 settings.json 页面都只保留脱敏文字，`evidencePath` 为空。
- **性能/超时**：窗口截图 6s、区域截图 16s、OCR 15s、整轮观察 35s；Screenpipe 影子逻辑未变。
- **验证**：桌面 `node --test` **103/103**、`npm run check` 通过；打包版真实采集到 Chrome `Open Design Cloud` 的“API 密钥/接入配置/我的密钥”正文与 VS Code 的 settings.json 页面正文；含敏感内容时截图被丢弃。Go 全量测试与 `go vet` 通过。

### 2.32 修复“卡住了”兜底并注入当天工作轨迹（2026-11-01）

- **诊断**：不是模型连不上。`127.0.0.1:3425` 代理正常，`/v1/models` 同时暴露 `auto/deepseek-v4.1-flash` 与 `opendesign/deepseek-v4.1-flash`，直接调用 `/v1/responses` 返回 200；服务日志中 01:05–01:06 每一步都有 LLM 回复和工具调用，没有连接拒绝/401/超时。用户看到的“我这边有点卡住了”是 `internal/agent/loop.go` 在工具循环跑满 6 步后的兜底。
- **最终回答兜底**：跑满 `maxSteps` 后追加一次不带工具（`Tools` 为空）的请求，提示模型“不要再调用工具，直接用已有结果回答”；只有该请求失败或内容为空才保留原来的“卡住了”。新增 `TestLoopMaxStepsReturnsFinalAnswerWithoutTools`。
- **当天轨迹上下文**：`ActivityTrail.chatContext()` 汇总当天阶段总结与最近 80 条活动（最多 12000 字符）；桌面 `assistant:chat` 自动随请求发送 `context`；Go `chatRequest.Context` 只把上下文附加到本轮 `agentText`，不写入会话原文。新增 `TestChatContextIsInjectedButNotPersisted` 与 `ActivityTrail chatContext` 单测。
- **验证**：`go test ./...`、`go vet ./...` 通过；桌面测试 **96/96**、`npm run check` 通过。真实桌面 E2E 提问“今天做了哪些工作”返回了当天 settings.json/VS Code、Open Design Cloud、FolderView/Weixin/CC Switch 等轨迹摘要，不再出现“卡住了”。

### 2.25 BMA 计划确认由 GFA 自动批准（2026-10-31）

- 根因：GFA 的异步 `delegate_agent` 链路在 BMA 返回 `awaiting_clarify` 后只按 completed 落库，计划确认停在 BMA UI。
- `DelegateAt("bma")` 在 `invokeBMA` 返回后调用 `autoApproveBMAPlan`：仅当问题含“计划/批准开工/计划待批准”且选项含 `approve`/“批准”/“允许开工”时，自动 `AnswerBMA` 并继续等待；同一 `question_id` 只批一次，最多连续处理 4 次。
- 普通 `ask_user`（例如选择文件、补充信息）不自动回答，仍原样返回需要用户处理。
- **验证**：新增 `TestBMAPlanApprovalAnswer` 与 `TestDelegateBMAAutoApprovesPlan`；`go test ./internal/agenthub ./internal/httpapi` 通过，自动批准后任务返回 completed，扫描计数为 0。
- **真实同步链路**：GFA `POST /api/agents/delegate` 触发 BMA `submit_plan`/`ask_user`，BMA 事件显示 `clarify User 提问答复: approve`，随后写文件并 completed。
- **真实异步链路**：GFA `POST /api/jobs` 提交同一类任务，job 从 running → completed；BMA 计划被自动批准，`BMA-PLAN-ASYNC.txt` 写入并读回 `BMA_PLAN_ASYNC_OK`，证明截图里的异步 `delegate_agent` 路径已覆盖。

### 2.21 总结单行入口与弹窗（2026-10-31）

- **聊天记录不再出现总结气泡**：服务端 `internal` 总结请求即使带当前会话 ID，也只借用上下文，不再持久化用户提示或助手总结；`TestInternalChatWithSessionDoesNotPersistSummary` 锁定。
- **主对话入口**：消息列表下方增加单行「已产生 N 条总结，点击查看」，按当前会话从 `activity-summaries.jsonl` 读取阶段总结；点击弹出总结列表，展示时间、覆盖记录数和 Markdown 正文，支持关闭/Escape/点遮罩关闭。
- **兼容旧数据**：历史会话恢复时，用阶段库的总结文本过滤旧版落库的总结助手消息；新总结只来自阶段库，不混入普通气泡。
- **阶段归属**：`CollaborationMode` 的阶段记录增加 `sessionId`；`ActivityTrail.listSummaryPhases(sessionId)` 按会话过滤，旧记录没有 sessionId 时仍可被兼容展示。
- **验证**：桌面 `node --test` **89/89**、`npm run check` 通过；无头 Edge 加载真实 `index.html` + stub IPC，确认单行显示「已产生 2 条总结，点击查看」，点击后弹出 2 条总结，关闭后弹窗隐藏。

### 2.22 完整会话记录：游标分页与向上加载（2026-10-31）

- **服务端**：新增 `SessionRepo.MessagesBefore(sessionID, beforeID, limit)`，按 SQLite `rowid` 倒序取页再反转，保证同一时间戳内也按真实写入顺序；`GET /api/sessions/{id}/messages` 支持 `limit`（默认 50，最大 200）和 `before`（消息 ID）游标，响应消息增加 `id` 字段。
- **桌面端**：首次加载最新 50 条并滚到底部；`#messages` 滚到距顶部 24px 内时用最早消息 ID 加载更早一页，`insertBefore` 只插入旧消息，并用瞬时滚动（临时关闭 `scroll-behavior: smooth`）锚定阅读位置。初始化/加载期间用 `allowOlderLoad` 防误触发。
- **手机端**：Vue 增加 `messageCursor/messageHasMore/messageLoadingMore/suppressOlderScroll`；初次恢复只取最新 50 条，`#messages` 滚到顶部加载更早一页并保持 scrollTop；顶部显示「正在加载更早记录…」。同样防止初次渲染误加载。
- **兼容**：旧客户端不传参数仍取最新 50 条；`before` 游标不存在时返回空页，客户端停止加载。`web/index.html` 已同步到 `android/gosvc/webassets` 与 `desktop/server/web`。
- **验证**：`go test ./...` 全通过（新增 store 分页/游标不存在、HTTP limit/before 测试）；桌面 `node --test` **90/90**、`npm run check` 通过；无头 Edge 桌面与手机两次实测均为：初始 50 条（首条“消息 50”），滚顶后 100 条（首条“消息 0”），scrollTop 分别保持 2357/2683，不再回到顶部。

### 2.33 Super-agent P1 地基（2026-11-01）

计划书：`docs/SuperAgent个人Agent架构改造计划书.md`。本轮只落 P1 的存储、事件接入、路由与 Worker 执行骨架，不接邮件/日历/微信，不做外部发送。

- **加法迁移** `migrations/0007_superagent.sql`：`agent_thread`、`agent_thread_link`、`agent_trigger`、`agent_thread_subscription`、`agent_action_outbox`、`agent_event`；`agent_job` 增加 `thread_id/session_id/parent_trigger_id/profile/priority/result_summary/waiting_kind/waiting_payload_json/version`；`chat_session` 增加 `kind/thread_id/summary`。
- **仓储** `internal/store/superagent.go`：Trigger 按 `(owner, dedupe_key)` 幂等写入并返回原记录；状态迁移使用 CAS；Thread 摘要游标只允许前进；ActionOutbox 每次编辑/审批递增 `version`；EventLog 自增 ID 支持 `after` 分页。`AgentJobRepo` 增加 Worker 视图、`FinishWorker`、等待状态 CAS，通用 Dispatcher 跳过 `agent_id=superagent`。
- **事件层** `internal/superagent`：`Ingest` 完成校验、Thread 解析/创建、去重、事件日志与 `new→queued/ignored` 迁移；确定性 Filter；Router 协议与 `ignore|notify|reply|create_worker|route_to_worker|ask_user`；LLM Router 使用无工具 JSON 决策，失败由 `DeterministicRouter` 回退。
- **Worker**：`WorkerManager` 复用 `agent_job`，最多 3 个并发（`GFA_SUPERAGENT_MAX_WORKERS` 可调），心跳、超时、取消、完成/失败通知；`create_worker` 以 `superagent:<trigger_id>` 幂等创建并订阅 Thread。`internal/agent/runner.go` 抽取 `TurnService`，HTTP 聊天与 Worker 共用会话准备、`agent.Run` 与持久化；会话读取新增 owner 校验。
- **HTTP**：`POST/GET /api/triggers`、`GET /api/threads`、`GET /api/threads/{id}`、`POST/DELETE` 关联、`GET /api/worker-tasks`、取消/追加指令、`/api/action-outbox` CRUD+版本审批、`GET /api/events`（JSON 分页 / SSE + `Last-Event-ID`）、`POST /api/superagent/route`、`GET /api/superagent/health`。旧 `/api/chat`、`/api/jobs`、`/api/outbox` 未改语义。
- **开关**：`GFA_SUPERAGENT=1` 才启动每 2s 的 Router 后台批处理；默认只保留手动路由/API 与 Worker 消费。`GFA_SUPERAGENT_ROUTER=deterministic` 强制不走 LLM Router。外部发送仍必须进入 ActionOutbox 人工审批，本轮没有发送器。
- **验证**：`go test ./...` 全通过、`go vet ./...` 无输出。新增仓储去重/CAS、EventLog 分页、Thread 摘要游标、Outbox 版本冲突、Ingest 去重与 owner 隔离、确定性/LLM Router 解析、Worker 完成/幂等/取消、HTTP 触发器/线程/事件/审批/Worker 路由测试。
- **隔离 E2E（2026-11-01）**：独立新库 `.runtime/e2e-superagent/e2e.db` + 独立 `GFA_CONFIG`/`GFA_SETTINGS` + 端口 18080，迁移 `0001-0007` 全部应用。实测通过：Trigger 写入（201）→ `mode=llm` Router 返回 `create_worker` → Worker 完成并写入独立 `kind=worker` 会话 → Thread 详情/EventLog；重复投递返回原 Trigger；Outbox 重复幂等键 409、旧版本审批 409、批准后 version=1、重复审批 409；Thread 关联；SSE `event: agent`；健康检查计数。E2E 中发现并修复：确定性通知优先显示 Router 内部原因而不是 Trigger 标题（`notifyText` 顺序已调整）。
- **真实模型阻塞**：当前 `settings.json` 指向 `127.0.0.1:3425` OpenDesign 代理，最小 `/v1/responses` 返回 **502 ECONNRESET**；`config.yaml` 的直连 DeepSeek Key 经 `/v1/chat/completions` 返回 **401 invalid**。因此本轮 E2E 使用本地 OpenAI 兼容桩模型（`.runtime/e2e-superagent/mock-llm.js`，端口 18081）验证集成链路，**没有验证真实模型质量**；修复模型配置后应重跑同一脚本。
- **环境注意**：本机 `TEMP` 指向 Go 临时目录时，`agenthub` 的工作目录断言会因 `t.TempDir()` 路径不一致失败；测试时把 `TEMP/TMP` 指到项目内可写目录即可。与业务代码无关。

### 2.35 补丁：单用户单主会话与 Worker 结果回流（2026-11-01）

按 `docs/补丁.md` 的最高优先级落地：唯一主会话 → 创建后台 Worker → 完成后结果与经验回到统一记忆 → 主会话可继续追问。

- **唯一主会话** `migrations/0008_single_main_session.sql` 增加 `kind='main'` 的部分唯一索引；`SessionRepo.GetOrCreateMain` 优先把最近一条普通聊天会话原地升级为 `main`，旧库升级不丢当前上下文；`ListByOwner` 过滤 `worker/router` 会话。`TurnService` 在未显式传会话时默认落到主会话；新增 `GET /api/sessions/main`。桌面 `assistant-api/main/preload/renderer` 与手机页面启动时直接恢复主会话，历史入口只作查看。
- **Worker 生命周期进事件流**：`worker.running/completed/failed/timeout/cancelled/interrupted` 与 `worker.created` 统一写入 `agent_event`，SSE 可直接驱动任务卡；重启恢复把失去执行线程的 Worker 标 `interrupted`，等待中的任务保持暂停。
- **结果回流统一记忆**：Worker 成功完成后，在同一完成流程内更新所属 Thread 摘要（游标只前进）；`WorkerManager.OnResult` 同步调用新增的 `memory.SaveWorkerResult`：先落 SQLite episode（立即可检索），再异步补向量并镜像长期记忆。Worker 原始会话继续保留，但不进入用户会话列表。
- **真实模型 E2E（隔离库 `.runtime/e2e-real`，端口 18082）**：真实 Router 4 秒内返回 `create_worker`，Worker 用真实模型 19 秒完成报价单草稿；执行中强杀进程后重启，任务变为 `interrupted` 且不重复执行；`/api/events` 出现 `worker.running`/`worker.completed`；`/api/sessions/main` 在旧库升级后稳定复用原聊天会话，两次无会话聊天返回同一 `session_id`；Worker 完成后立即用 `/api/memory/search` 命中“Worker 任务结果”，Thread 摘要写入，Worker 会话不出现在 `/api/sessions`；主会话追问“刚才后台任务完成的会议通知说了什么”正确回答“明天上午10点在B201开产品评审会，产品负责人和研发负责人参加”。
- **验证**：`go test ./...` 全部通过、`go vet ./...` 无输出；桌面 `npm run check` 通过，`node --test` **106/106** 通过；新增主会话仓储/HTTP、默认会话、Worker 生命周期事件、Thread 摘要与 OnResult 测试。
- **当前边界**：Worker 结果回流使用 Worker 最终摘要，尚未另起“经验提取 Worker”；历史旧会话仍可打开查看但日常默认主会话；工作轨迹 Episode、邮件/微信 Thread、跨渠道每日简报和桌面任务卡仍待接。

### 2.36 补丁剩余项：活动 Episode、邮件/微信、每日简报、工作台（2026-11-01）

- **迁移** `0009_channels_briefs.sql`：`agent_channel_policy`（来源 allow/deny）、`agent_channel_skip`（仅计数，不保存被拒正文）、`agent_channel_cursor`（IMAP 单调游标）、`agent_daily_brief`（按日期+版本保留，旧版不覆盖）。
- **渠道入口**：`/api/channels/activity/episodes`、`/api/channels/email/messages`、`/api/channels/wechat/messages`。活动片段按日期归入 `activity:<日期>` Thread；邮件按 `thread_key` 归 Thread；日历事件按 `series_id/event_id` 归 `calendar:` Thread；微信默认 deny，用户在“工作台 → 渠道采集”显式允许后才落正文。拒绝只增加当日跳过计数。
- **Thread 增量摘要**：`internal/superagent/summary.go` 维护滚动摘要，按已覆盖 trigger ID 去重，同一 Thread 合并排队；无模型时确定性摘要；摘要游标只前进，并记录 `thread.summarized` 事件和来源 ID。桌面工作片段完成后自动调用活动入口；微信窗口可见文本由桌面网关转发（不读微信数据库、不注入、不绕过客户端）。
- **邮件 IMAP**：`internal/email` 可选只读采集器（新增 go-imap v1 + go-message + x/text），`BODY.PEEK[]` 不标记已读，游标落库；正文/MIME/HTML 转文本和附件名解析有单测；配置在 settings.json `email`，接口 `/api/settings/email`（GET 打码、空密码保留原值）。不实现自动发送。
- **每日简报**：`internal/superagent/brief.go` 按日期汇总活动/邮件/微信 Thread 摘要、Worker、待审批和到期任务，输出 `needs_action/important_changes/background`；已有 Thread 摘要时不再重复喂原始 trigger。`digest.time` 后自动生成首版，新增事件最多每 30 分钟补版；`GET /api/briefs/latest`、`POST /api/briefs/generate`、版本列表与已读标记。主会桌面/手机显示简报卡，桌面“工作台”显示完整内容。
- **桌面工作台**：新增 `workbench.html/js` 与主进程 SSE 转发。主进程连接 `/api/events?stream=1`，通过 IPC `agent:event` 广播；主对话显示待处理条（审批/等待/异常任务）和简报卡，工作台含“待处理 / 进行中 / 今日简报 / 渠道采集”，支持批准/驳回、取消/追加 Worker、生成简报、微信 allow/deny 和邮箱 IMAP 配置。
- **验证**：`go test ./...` 全通过、`go vet ./...` 无输出；桌面 `npm run check` 通过、`node --test` **107/107**。隔离 E2E（端口 18082）：活动/邮件/微信入口分别生成 Thread 摘要（真实模型）；微信先 202 拦截并计数、允许后 201；日简报 v4 长 3143 字，含三类摘要且不再包含被摘要覆盖的原始邮件/微信 trigger；邮箱配置 GET/PUT 不泄漏密码；无待审批时主会话显示 1 个异常 Worker 待处理；Electron CDP 实测主窗简报卡、待处理条、工作台三页内容与 SSE IPC（`trigger.queued` 事件）均正常；手机网页无头 Edge 实测简报卡与主会话消息。新增假 IMAP 服务器测试验证 PollOnce 登录/EXAMINE/UID SEARCH/UID FETCH/游标推进。
- **边界（需真实环境验收）**：没有真实邮箱凭据，IMAP 只做了假服务器与解析单测；微信只验证了桌面可见文本入口，没有个人微信数据库解密/hook/非官方协议；邮件只读，不发送；活动 Episode 目前由现有阶段总结触发，不是独立的后台切片器。

### 2.37 补丁第 13 条：渠道开关、隐私前置与网页采集地基（2026-11-01）

- **渠道与全局开关** `migrations/0010_channel_configs.sql`：六类渠道（activity/email/feishu/dingtalk/wechat_desktop/wechat_mobile）分别保存 `enabled/summary_enabled/index_enabled/brief_enabled/auto_worker_enabled`，默认全关；全局 `router_enabled/worker_enabled/daily_brief_enabled/proactive_notify_enabled` 默认开启。新增 `GET /api/settings/channels`（configs/global/policies/skips）、`PUT /api/settings/channels/config/{channel}`、`PUT /api/settings/channels/global`。
- **开关语义真正生效**：渠道关闭时 `/api/channels/*` 在持久化前返回 202 并只增加跳过计数；`summary_enabled` 控制是否入摘要队列；`brief_enabled` 过滤每日简报来源；`auto_worker_enabled` 与全局 `worker_enabled` 同时放行才允许 Router 创建 Worker，否则降级为通知；`proactive_notify_enabled` 抑制主动通知回调和简报推送；全局 `router_enabled`/`daily_brief_enabled` 控制后台循环。JSON 配置不再靠界面隐藏。
- **手机微信统一过滤**：`/api/inbox/events` 在写旧通知中心前识别 `com.tencent.mm`，按 `wechat_mobile` 渠道开关与来源策略过滤；默认关闭且来源默认拒绝，被拒正文不落库、不留向量，只计数。`index_enabled` 关闭时不生成 embedding。
- **工作台**：渠道页展示每个渠道五项开关、四个全局开关和来源 allow/deny；邮箱页增加多文件夹、发件人、主题、最近天数筛选。新增“网页采集”页。手机设置抽屉同步增加“渠道采集与每日简报”折叠区，可直接开关 `wechat_mobile` 并允许/拒绝具体群名或联系人。
- **邮箱闭环**：IMAP 游标升级为 `UIDVALIDITY + UID`，邮箱重建后自动重置；支持 `Mailboxes`、`Senders`、`SubjectFilters`、`SinceDays` 服务端搜索 + 本地二次校验；继续 `EXAMINE`/`BODY.PEEK[]` 只读，不标记已读、不发送。
- **网页采集服务端**：`POST /api/channels/web/items`、`GET/PUT/DELETE /api/settings/web/sites`。站点配置保存授权 origin/路径、页数/深度/间隔、摘要/索引/简报/自动 Worker/导航/会话开关；未启用站点在入库前 202 拦截。服务端重算规范化正文 SHA-256，按 `web:<site>:<account>:<item>:<hash>` 幂等，同一 Thread 仍可保留内容变化的新版本；网页内容固定 `trusted=false`。
- **桌面 Browser Collector**：新增 `desktop/browser-policy.js`（URL origin/路径/边界）、`browser-extract.js`（Readability + DOM 兜底 + 脱敏 + SHA-256）、`browser-session.js`（按站点独立持久 profile、人工登录接续、登录页探测）、`browser-collector.js`（单站点 single-flight、页数/深度/时长限制、只跟随授权链接）与 `secret-store.js`（Electron safeStorage/DPAPI 加密存储）。启用 `playwright-core` 与 `@mozilla/readability`，使用本机 Edge/Chrome，不下载额外 Chromium。自动登录只允许 HTTPS 登录地址和确定性选择器；每次填充前确认，origin 不匹配、验证码/二次验证或表单变化立即停止并转人工，不重试。
- **微信采集端前置**：桌面主进程缓存 `wechat_desktop` 开关与来源策略，关闭时不再把可见文本发往服务端；所有可见快照固定标记 `incomplete=true`，不伪装成完整逐条消息。手机微信通知仍由服务端在旧通知中心前统一过滤。
- **验证**：Go 全量测试通过；桌面 `node --test` **122/122**、`npm run check` 通过；Playwright + Readability 本地 HTML 冒烟实际返回 `extractedBy=readability`、正文 229 字、链接 1 条；新增渠道开关、手机微信过滤、简报/Worker 门控、网页去重与越权 URL 拒绝、SecretStore 加密/拒绝明文、自动登录 origin 校验与表单填充测试。
- **打包版真实联调**：隔离库（复制真实 `gfa.db` + 独立 `GFA_CONFIG`，端口 18083）迁移只应用 `0010_channel_configs.sql`；默认 email 202 拦截、开启后 201；网页站点保存后 item 201、重复 200、关闭站点 202；关闭站点 `brief_enabled` 后新简报不再引用该 web Thread。
- **打包版工作台 CDP**：Electron 低完整性启动，工作台出现 5 个标签（含“网页采集”），读取到 6 个渠道配置与 4 个全局开关；本地 HTTP 样例站点“立即采集”实际捕获 1 条，重复采集服务端仍只有 1 条 web Trigger。首次运行报 Playwright `mkdtemp EPERM`（低完整性不能写默认 AppData 临时目录），已在 `ensureBrowserCollector` 里把 `TMP/TEMP` 指向 `userData/tmp` 并重新打包验证通过。测试站点已从生产 `settings.json` 删除，示例 HTTP 服务已停止。
- **手机页面**：`web/index.html` 设置抽屉新增渠道折叠区，无头 Edge 390×844 实测渲染 34 个开关、`/api/settings/channels` 返回 6 个渠道配置；页面同步到 `android/gosvc/webassets` 与 `desktop/server/web`。
- **DPAPI 真实写入**：打包版 CDP 调用 `saveBrowserCredential` 保存测试账号，`secrets.json` 仅含 safeStorage 密文，未出现账号或密码明文；随后通过 IPC 删除凭据并删除测试站点。
- **真实站点验收**：打包版 Browser Collector 实际采集百度百科「人工智能」文章（`baike.baidu.com/item/.../9180`），Readability 提取 75,568 字节正文、`complete`；重复采集服务端仍只有 1 条 web Trigger；Thread 摘要由真实模型生成（176 字），当日简报 v3 引用该 `thread:` 来源。原始正文上限从 64KB 提高到 512KB（请求体 1MB），避免长文章被误判超限；测试站点已删除，采集记录保留作为证据。
- **主窗口布局修复**：`pending-strip` 与 `brief-card` 原先作为额外网格项与对话区争用第 2 行，导致简报卡/消息/标题栏重叠。现在两条状态条收进独立 `.agent-strips` 行，面板补 `grid-template-columns: minmax(0, 1fr)`，对话区改为第 3 行；CDP 实测 460×680 下标题栏、简报卡、消息区、输入区互不重叠，长代码消息也不再撑宽面板。
- **边界**：飞书/钉钉连接器按用户要求暂缓；自动登录已具备 DPAPI 加密存储和确定性填充，但尚未在真实 QQ 邮箱/BOSS 页面验收；网页调度游标只在进程内，重启后依赖服务端内容 hash 去重；尚未用真实邮箱与微信账号验收。

**本轮未做（飞书/钉钉按要求暂缓）**：飞书官方 SDK、钉钉 Stream SDK、QQ 邮箱/BOSS 专用适配器、真实邮箱与微信账号验收、手机工作台/动态待办、relay v2 增量同步、ICS 日历、邮件发送器、Worker `route_to_worker` 执行上下文。

## 三、验证记录（2026-10-31 最新）

实际执行并通过：

- `go test ./...`：**全部包通过**；`go vet ./...` 无输出；`gofmt` 已整理本轮修改文件。
- 桌面：`node --test` **92/92 通过**（新增轨迹持久化/3 天证据/清空持久化文件/阶段总结/清空联动/消息分页请求/总结问句清理测试）、`node --check`（29 个 JS 文件）全通过。
- BMA 真实链路：fast 档创建文件并回传结果（`BMA_E2E_OK`）；同会话继续提问返回 `BMA_E2E_OK`；GFA daily 分派 12.3 秒完成（`GFA_BMA_E2E_OK`）；GFA 返回 `awaiting_clarify` 后回复 `yes`，BMA 继续执行并写入 `CLARIFY_OK`。
- BMA 桌面操控：cluster 和 GFA daily 两条路径都在“画图”窗口完成 HID 点击（坐标 959,578），返回 `effect=changed`/`delivery=hid`；daily 路径证明 DomainAgent 已获得 `host_computer_use` 权限。
- Agent 面板澄清 UI：无头 Edge 加载真实 `agents.html` + stub IPC，确认 `awaiting_clarify` 显示问题与两个选项，点击后切换为 `completed` 且澄清区隐藏。
- 总结单行/弹窗 UI：无头 Edge 加载真实 `index.html` + stub IPC，确认「已产生 2 条总结，点击查看」单行显示，点击弹出 2 条阶段总结，关闭/Escape 后隐藏。
- 会话分页 UI：无头 Edge 桌面端（stub preload）与手机端（本地 HTTP mock + 真实 Vue 页面）均验证：初始 50 条、滚顶后 100 条、首条从“消息 50”变为“消息 0”，scrollTop 保持 2357/2683。
- Agent 缓存：无头/HTTP 之外用 Go 测试验证 `Scan` 两次只扫描一次、`Refresh` 再扫描；`DelegateAt("bma")` 使用已保存端点，扫描计数为 0。
- 总结流内定位：无头 Edge 真实 `index.html` 验证节点顺序 `10:00 消息 → 10:05 消息 → 10:07 总结 → 10:10 消息`，点击弹出对应阶段总结；手机端 3 条消息时间显示为 `10:00/10:05/10:10`。
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
