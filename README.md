# 金手指管家 GoldenFingerAgent

用自然语言（语音优先）就能用的 AI 管家 MVP：记住你提到的人、帮你盯住要做的事、到点提醒你，并在**通知层**替你接住信息、按语义找回历史消息。提供闹钟/随手记/天气/问答等日常小能力。

同一内核兼顾三类非技术用户的使用体验：**老人、普通人、小孩**。设计上「高置信才自动，低置信必反问」，零学习成本。

> 📘 完整文档见 [docs/](./docs/)：
> - [项目大纲与技术报告](./docs/项目大纲与技术报告.md)
> - [API 手册](./docs/API手册.md)
> - [通知通道专题](./docs/通知通道专题.md)
>
> 需求与技术方案见 [docs/archive/需求与技术方案.md](docs/archive/需求与技术方案.md)；竞品分析见 [docs/archive/竞品深调报告.md](docs/archive/竞品深调报告.md)；开发任务清单见 [docs/TODO.md](docs/TODO.md)。

## 功能

### 对话 / 任务 / 记忆
- 多轮自然语言对话、语音输入与播报、工具调用循环。
- 任务创建/确认/提醒/完成/取消；闹钟、随手记、天气、问答。
- 人物画像与事实记忆、向量语义检索；记忆可编辑/一键遗忘（级联硬删）；`GET /api/memory/export.md` 导出 Markdown 档案。
- 情景记忆：Android 持续听写只上报最终文字，保存原话、30 秒转写上下文、时间与来源；向量缺失时仍可按关键词召回。可在首页或通知栏随时开始/停止记录。

### 通知中心（v0.2 新增）
一条统一「通知通道」（安卓旁路监听，不取消原通知）：

| 功能 | 说明 |
|---|---|
| **稍后处理** | 通知一键「10 分钟后/1 小时后/今晚再看」，到点把**原文**弹回 |
| **线索自动接住** | 自动识别取件码/验证码/运单号，结构化入库、一键复制 |
| **语义搜索** | 按意思找回历史通知（"老王上次发我的地址"），无向量时显式降级关键词 |
| **等到了** | 设定联系人+关键词，命中即高优通知 |

LLM 不参与通知捕获（快、省电、稳），仅用于语义检索。详见 [通知通道专题](./docs/通知通道专题.md)。

## 架构（全外接 + 自建编排与规则）

照抄 [pi agent](https://github.com/earendil-works/pi) 的分层，用 Go 自研精简版：

| 本项目 | pi 包 | 职责 |
|---|---|---|
| `internal/llm` | `packages/ai` | 统一 LLM API（OpenAI 兼容客户端 + 模型目录 + 测试脚本客户端） |
| `internal/agent` | `packages/agent` | 工具调用循环、会话状态、工具注册、系统提示词组装 |
| `internal/httpapi` | `packages/protocol` | JSON wire 类型 + HTTP 传输 |
| `internal/store` | `packages/session-backends` | SQLite 持久化（纯 Go，CGO_ENABLED=0；含聊天会话） |
| `internal/nlu` `memory` `task` `scheduler` `compliance` `intent` `inbox` | *(自建"大脑")* | 抽取、记忆、任务状态机、澄清、合规、意图、通知中心 |

**能外接全外接**：LLM 走 OpenAI 兼容 API（DeepSeek/Moonshot/通义/豆包换 `base_url` 即可）；ASR/TTS/推送/短信/天气全部接口 + Stub，接真服务即插即用。调度为进程内 DB tick 循环（`dedupe_key` 幂等），预留 Temporal Cloud 替换。

### 关键产品规则（均有测试覆盖）

- **置信度分级**：任务 ≥0.85 自动（可撤销）/ 0.6–0.85 反问 / <0.6 丢弃；人物消歧 <0.8 反问
- **意图≠事实（P3）**：intent 只提醒"别忘了"，fact 才触发行程类提前量提醒
- **相对时间 100% 归一化**：Go 规则解析器（`internal/nlu/timecn`）+ LLM 兜底（硬校验 RFC3339/未来时间/recurrence 格式）
- **任务 8 态状态机**：`draft→pending_confirm→scheduled→notified→done/snoozed/cancelled/expired`，CAS 迁移
- **免打扰 22:00–7:00**：低级别顺延到窗口结束；成人高优可突破；**儿童夜间一律静默**
- **分级升级**：App→推送→短信/语音（≤3 级），非紧急推送 ≤3 条/日，超出并入 11:00 每日盘点
- **记忆可治理**：查看/编辑/删除/一键遗忘（级联硬删向量）、事实可溯源、评价性标签 0 条自动、儿童仅事实型、监护人同意
- **工具结果视为数据非指令**：工具载荷包裹固定前缀，参数在入口校验
- **意图三层分级**（借鉴 OpenClaw）：时间型 / 事件型 standing intent（确定性关键词、冷却 24h、预算 3 次、90 天过期）/ 愿望型（只记记忆）
- **通知中心降级显式**：embedder 缺失时搜索 `mode=keyword` 如实回传，不静默失效；线索/匹配走纯规则
- **记忆人可读、无黑盒**：`GET /api/memory/export.md` 一键导出 Markdown 记忆档案

### 日志与可追溯（开发期，仅服务端）

- 全链路日志输出在**服务端终端**，请求 ID 贯穿：`[http]`/`[chat]`/`[agent]`（含真实报错）/`[tool]`/`[task]`/`[sched]`/`[memory]`/`[settings]`。
- 每条管家回复的「处理过程」仅在 URL 带 `?debug` 时显示。

## 快速开始

```bash
# 1. 建表（无需 docker；SQLite 单文件 data/gfa.db，自动创建）
go run ./cmd/migrate

# 2. 配置 LLM（OpenAI Responses 协议；config.yaml 的 llm 段可直接填 api_key）
#    也可启动后在页面右上角「⚙️ 设置」里填（存 settings.json，立即生效、全设备通用）
go run ./cmd/server
# 打开 http://localhost:8080
```

- 对话模型走 **OpenAI Responses 协议**（`POST {base_url}/responses`）；key 支持 `config.yaml` 直填、`api_key_env`、`GFA_LLM_API_KEY` 三种方式，页面设置优先级最高。
- 页面「⚙️ 设置」预置 DeepSeek V4.1 Flash（`deepseek-flash`）与 DeepSeek V4 Pro（`deepseek-v4-pro`）。
- 向量模型走 OpenAI 兼容端点（`POST {base_url}/embeddings`，自建网关带 `/v1` 前缀）；key 用 `embedder.api_key` 或 `GFA_EMBED_API_KEY`。
- **联网搜索（Firecrawl Search API）**：key 填 `config.yaml` 的 `search.api_key`（或 `GFA_SEARCH_API_KEY`），不配置时自动回落 dev stub，CI 不依赖外网。
- `settings.json` 含密钥，**不要提交**（已在 .gitignore）。

### Windows 桌面挂件

先按上面步骤启动本机 Go 服务，再运行桌面端：

```bash
cd desktop
npm install
npm start
```

桌面小球常驻置顶，点击展开对话框。协作模式默认关闭，只有用户主动授权后才每 30 秒读取一次活动应用与窗口标题，每 15 分钟写入记忆并主动总结；不读取屏幕像素、文件内容或键盘输入。`npm run dist` 可生成免安装 Windows 目录包。

### 试试这些话

- 「帮我记一下我车位在 B2」→ 记忆面板出现事实；问「我车位在哪」→ 能答
- 「20 分钟后叫我吃药」→ 闹钟入列
- 「明早 7 点提醒我去医院」→ 时间归一化为明日 07:00
- 「以后张阿姨来电话时提醒我问她女儿」→ 事件提醒入列
- 点顶部「📮 收件箱」→ 语义搜索历史通知、查看取件码/验证码、添加「等到了」
- 安卓：授予通知访问权后，微信/短信通知可一键「10 分钟后再看」，到点原文弹回

## 测试

```bash
go vet ./...          # 静态检查
go test ./...         # 全量（SQLite 内存库，无需外部数据库）
```

测试策略：时间归一化表驱动、置信度边界、状态机全迁移矩阵、DND 策略、记忆冲突/遗忘级联、脚本化 LLM 端到端（`llm.ScriptedClient` + 假时钟）。通知中心覆盖线索正则、watch 匹配、snooze 到期、去重、向量/关键词降级与八端点契约共 26 例。CI 不打真实 LLM。

## 目录结构

```
cmd/server, cmd/migrate        # 装配与迁移入口
internal/llm/                  # 统一 LLM API（openai Responses 客户端 + mock）
internal/agent/                # pi 式工具循环 + 会话 + 工具
internal/nlu/                  # 抽取/评分/澄清 + timecn 时间归一化
internal/memory/               # 人物画像/事实/备忘 + 检索 + 上下文注入
internal/task/                 # 8 态状态机 + intent/fact 提醒模板
internal/inbox/                # 通知中心：线索正则/watch/snooze/语义搜索
internal/scheduler/            # DB tick 调度 + DND/升级/盘点
internal/settings/             # 运行时模型设置（settings.json）
internal/store/                # SQLite（modernc.org/sqlite）仓储 + 应用层余弦
internal/compliance/           # 监护人同意/内容过滤/事实类型门控
internal/extsvc/               # 外接服务接口 + stub
internal/httpapi/              # HTTP API + 静态页
migrations/                    # SQL 迁移（0001–0004）
web/                           # Vue3 单页（无构建）：聊天 + 记忆 + 收件箱 + 设置
android/                       # 安卓 App：口令唤醒 + 情景记忆 + 无障碍 + 通知监听
desktop/                       # Windows 桌面挂件：置顶小球 + 对话框 + 授权协作扫描
docs/                          # 项目文档（大纲/技术报告、API、通知通道）
```

## 后续

- **联调补证（当前）**：Web 收件箱 runtime/截图；Android `gradle assembleDebug` + 真机（通知访问权、各 ROM 保活）。
- **v0.3 家庭安全**：屏幕内诈骗行为链守卫、就医/用药流程卡片、子女端（微信服务号/小程序先行）。
- **v1.0**：家庭多角色协同、指标回流、跨品牌安全纽带闭环。

## 向量维度注意

`fact.embedding` / `episode.embedding` / `inbox_event.embedding` 为定长 1024 维 BLOB（小端 float32），需与 `config.yaml` 的 `embedder.dim` 一致（默认 `bge-m3`）。更换 embedding 模型且维度不同时，需新迁移 + 重嵌入。

## 已知未验证项

- Web 收件箱的浏览器 runtime 探针与截图（子环境浏览器桥故障），待联调补跑。
- Android gradle 编译与真机（通知访问授权、各 ROM 保活、event_id 回查链路），当前仅静态自检。
- 无障碍/通知监听的应用商店审核政策、家庭付费意愿仍属风险/假设，需真机与用户访谈验证。
