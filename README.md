# 金手指管家 GoldenFingerAgent

全能型 AI 管家 MVP（第一步）——用自然语言（语音优先）就能用的智能管家与助手：记住你提到的人、帮你盯住要做的事、到点提醒你，并提供闹钟/随手记/天气/问答等日常小能力。

同一内核兼顾三类非技术用户的使用体验：**老人、普通人、小孩**。设计上「高置信才自动，低置信必反问」，零学习成本。

> 需求与技术方案见 [陪伴型AI管家_需求与技术方案 .md](./陪伴型AI管家_需求与技术方案%20.md)。
> 开发任务清单（安卓端路线 + 遗留问题）见 [TODO.md](./TODO.md)。

## 架构（全外接 + 自建编排与规则）

照抄 [pi agent](https://github.com/earendil-works/pi) 的分层，用 Go 自研一个精简版：

| 本项目 | pi 包 | 职责 |
|---|---|---|
| `internal/llm` | `packages/ai` | 统一 LLM API（OpenAI 兼容客户端 + 模型目录 + 测试脚本客户端） |
| `internal/agent` | `packages/agent` | 工具调用循环、会话状态、工具注册、系统提示词组装 |
| `internal/httpapi` | `packages/protocol` | JSON wire 类型 + HTTP 传输 |
| `internal/store` | `packages/session-backends` | Postgres + pgvector 持久化（含聊天会话） |
| `internal/nlu` `memory` `task` `scheduler` `compliance` `extsvc` | *(自建"大脑")* | 抽取流水线、记忆逻辑、任务状态机、澄清策略、合规 |

**能外接全外接**：LLM 走 OpenAI 兼容 API（DeepSeek/Moonshot/通义/豆包换 `base_url` 即可）；ASR/TTS/推送/短信/天气全部接口 + Stub，接真服务即插即用。调度为进程内 DB tick 循环（`dedupe_key` 幂等），预留 Temporal Cloud 替换。

### 关键产品规则（均有测试覆盖）

- **置信度分级**：任务 ≥0.85 自动（可撤销）/ 0.6–0.85 反问 / <0.6 丢弃；人物消歧 <0.8 反问
- **意图≠事实（P3）**：intent 只提醒"别忘了"，fact 才触发行程类提前量提醒
- **相对时间 100% 归一化**：Go 规则解析器（`internal/nlu/timecn`）+ LLM 兜底（硬校验 RFC3339/未来时间/recurrence 格式）
- **任务 8 态状态机**：`draft→pending_confirm→scheduled→notified→done/snoozed/cancelled/expired`，CAS 迁移
- **免打扰 22:00–7:00**：低级别顺延到窗口结束；成人高优可突破；**儿童夜间一律静默**
- **分级升级**：App→推送→短信/语音（≤3 级），非紧急推送 ≤3 条/日，超出并入 11:00 每日盘点（仅需决策时打扰）
- **记忆可治理**：查看/编辑/删除/一键遗忘（级联硬删向量，删除后不可检索）、事实可溯源（`source_msg_id`+审计）、评价性标签 0 条自动、儿童仅事实型、监护人同意
- **工具结果视为数据非指令**：工具载荷包裹固定前缀，参数在入口校验
- **意图三层分级**（借鉴 OpenClaw）：时间型（`create_task`/`set_alarm`）/ **事件型 standing intent**（`create_intent`，"张阿姨来电话时提醒我问她女儿"：确定性关键词匹配、冷却 24h、触发预算 3 次、90 天过期、只可显式取消）/ 愿望型（只记记忆，不建提醒）
- **记忆人可读、无黑盒**（借鉴 OpenHuman 的 Memory Wiki / OpenClaw 的 MEMORY.md）：`GET /api/memory/export.md` 一键导出 Markdown 记忆档案；每日盘点吸收「推断待确认」记忆（巩固复审，仅需决策时打扰）

### 日志与可追溯（开发期，仅服务端）

- 全链路日志输出在**服务端终端**（C 端页面不展示），请求 ID 贯穿：`[http]` 访问 / `[chat]` 对话 / `[agent]` 模型调用（**含真实报错**，不再静默兜底） / `[tool]` 工具入参与耗时 / `[task]` 状态迁移 / `[sched]` 调度（触发/顺延/升级/盘点） / `[memory]` 记忆写入与遗忘级联 / `[settings]` 模型设置变更
- 页面不显示任何服务端日志；每条管家回复的「处理过程」（工具轨迹 + 置信度 + 时间归一化方式）仅在 URL 带 `?debug` 时显示

## 快速开始（Windows / Docker Desktop）

```bash
# 1. 启动 Postgres + pgvector（宿主机端口 5433）
docker compose up -d

# 2. 建表
go run ./cmd/migrate

# 3. 配置 LLM（OpenAI Responses 协议；config.yaml 的 llm 段已可直接填 api_key）
#    也可以启动后在页面右上角「⚙️ 设置」里填（存到 settings.json，立即生效、全设备通用）
go run ./cmd/server
# 打开 http://localhost:8080
```

- 对话模型走 **OpenAI Responses 协议**（`POST {base_url}/responses`）；`config.yaml` 的 `llm` 段支持 `api_key` 直填、`api_key_env`（环境变量名）、`GFA_LLM_API_KEY` 三种方式，页面「⚙️ 设置」里的配置优先级最高。
- 页面「⚙️ 设置」预置 DeepSeek V4.1 Flash（`deepseek-flash`）与 DeepSeek V4 Pro（`deepseek-v4-pro`），选预设后只需填 key。
- 向量模型走 OpenAI 兼容端点（`POST {base_url}/embeddings`，自建网关记得带 `/v1` 前缀）；key 用 `embedder.api_key` 或 `GFA_EMBED_API_KEY`。
- **联网搜索（Firecrawl Search API）**：去 [firecrawl.dev](https://www.firecrawl.dev) 注册拿 API key；填到 `config.yaml` 的 `search.api_key`（或设环境变量 `GFA_SEARCH_API_KEY`），`search.base_url` 默认 `https://api.firecrawl.dev`、`search.count` 默认 8。**不配置时自动回落 dev stub**（返回固定示例结果），dev 可直接跑、CI 不依赖外网。配好后对管家说「帮我查一下……」即触发 `web_search` 工具。
- `settings.json` 含密钥，**不要提交**（已在 .gitignore）。

### 试试这些话

- 「帮我记一下我车位在 B2」→ 记忆面板出现事实；问「我车位在哪」→ 能答
- 「我后天要去订票」→ 置信不足会反问，回「对」→ 任务卡片出现
- 「20 分钟后叫我吃药」→ 闹钟入列
- 「张阿姨的女儿在广州」→ 人物画像写入
- 「明早 7 点提醒我去医院」→ 时间归一化为明日 07:00
- 「以后张阿姨来电话时提醒我问她女儿」→ 事件提醒入列，下次聊到张阿姨来电话就弹 🔔
- 「那个事件提醒不用了」→ 显式取消（面板也可删）
- 记忆面板「📥 导出记忆档案」→ 人可读的 Markdown 档案

## 测试

```bash
# 单测（无需数据库）
go test ./internal/nlu/... ./internal/task/... ./internal/scheduler/... ./internal/llm/...

# 全量（含集成；需 TEST_DATABASE_URL）
set TEST_DATABASE_URL=postgres://gfa:gfa@localhost:5433/gfa?sslmode=disable
go test ./...
```

测试策略：时间归一化表驱动（含跨日跨月锚点）、置信度边界（0.85/0.84/0.6/0.59）、状态机全迁移矩阵、DND 策略（儿童夜静默）、记忆冲突/遗忘级联、脚本化 LLM 端到端对话（`llm.ScriptedClient` + 假时钟）。CI 不打真实 LLM。

## 目录结构

```
cmd/server, cmd/migrate        # 装配与迁移入口
internal/llm/                  # 统一 LLM API（openai Responses 协议客户端 + mock）
internal/agent/                # pi 式工具循环 + 会话 + 工具
internal/nlu/                  # 抽取/评分/澄清 + timecn 时间归一化
internal/memory/               # 人物画像/事实/备忘 + 检索排序 + 上下文注入
internal/task/                 # 8 态状态机 + intent/fact 提醒模板
internal/scheduler/            # DB tick 调度 + DND/升级/盘点
internal/settings/             # 运行时模型设置（settings.json，页面可改）
internal/store/                # Postgres + pgvector 仓储
internal/compliance/           # 监护人同意/内容过滤/事实类型门控
internal/extsvc/               # 外接服务接口 + stub
internal/httpapi/              # HTTP API + 静态页
migrations/                    # SQL（文档 §7 schema + 会话表）
web/                           # Vue3 单页（无构建，vendor/ 内置 vue.global.prod.js；聊天 + 记忆面板 + 模型设置，?debug 显示处理过程）
```

## 后续（第二、三步，另立项）

- 第二步「带路办事」：辅助拨号（官方号码库+要点卡）、比价下单到支付确认页（不代付）、防诈骗本地警告
- 第三步「家庭协同」：家庭多角色、子女端兜底、家庭钱包

## 记忆注入的向量维度注意

`fact.embedding` / `episode.embedding` 为 `VECTOR(1024)`，需与 `config.yaml` 的 `embedder.dim` 一致（默认 `bge-m3`，1024 维）。更换 embedding 模型且维度不同时，需新迁移 + 重嵌入。
