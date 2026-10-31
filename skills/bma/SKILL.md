---
name: bma
description: BlockMemoryAgent 桌面版的架构、开发、部署、HTTP 接口与金手指调度方式（含端口、启动器、任务编排、插件和技能）
---

# BlockMemoryAgent（BMA）技能

## 是什么

BMA 是一个 **上下文外部化 + 记忆隔离** 的 Go 多 Agent 编排系统：

- 单一 MetaAgent ReAct 主循环，通过 `call_sub_agent` 异步派发子 Agent；
- 子 Agent 拥有独立上下文，结果通过 Mailbox 回灌父 Agent；
- 会话历史/事件/日志落 PostgreSQL，Redis 保存工作区与快照，pgvector 用于块记忆；
- Web UI（Vue 3）+ TUI（bubbletea）双入口，SSE 推送实时事件；
- 支持插件（MCP / Docker / 服务 / Claude/Codex 插件包）、经验技能自进化、DAG 定时任务。

GFA（金手指管家）把 BMA 当作本机专属 Agent：通过 BMA 原生 HTTP API 创建会话、轮询结果，不直接修改 BMA 的内部数据库。

## 本机桌面版布局

本机当前安装目录是 `D:\WebApp\BMA-Desktop`（可用 `GFA_BMA_HOME` 或 `GFA_BMA_PATH` 覆盖）：

```text
BMA-Desktop\
  BMA-Desktop.exe          # WebView2 桌面壳
  BMA.cmd / BMA.vbs        # 桌面入口
  .env                     # 端口、LLM Key、BMA_API_TOKEN
  app\
    current.json           # 版本指针（current / previous）
    runtime.json           # 启动器写入的 pid / 版本 / 端口（运行后才有）
    launch.ps1             # 单实例启动器：读 current.json + .env，必要时启动 bma-server
    versions\<版本>\bin\bma-server.exe
    versions\<版本>\web\dist
  config\                  # config.yaml / roles.yaml / models.json / plugins.yaml / skills.yaml / soul.md / user_profile.md
  plugins.d\
  workspace\               # Agent 默认工作目录
  logs\
```

- 当前 `.env` 的 `HTTP_ADDR=127.0.0.1:10011`，健康检查：`GET http://127.0.0.1:10011/api/health`。
- 默认安装脚本使用 `10010`，并行安装会写不同端口；**不要假设端口固定**，以 `.env` 的 `HTTP_ADDR` 为准。
- `.env` 里的 `BMA_API_TOKEN` 在 `http.auth_enabled: true` 时作为 `Authorization: Bearer <token>` 使用；本机当前默认关闭鉴权。

## 启动、停止、升级

优先级：BMA 桌面版启动器 > 直接运行 bma-server.exe。

```powershell
# 只启动后端，不打开浏览器（GFA 自动拉起用的方式）
powershell -NoProfile -ExecutionPolicy Bypass -File D:\WebApp\BMA-Desktop\app\launch.ps1 -NoBrowser -Quiet

# 查看状态 / 停止 / 重启 / 回滚
D:\WebData\github\BlockMemoryAgent\bma-app.ps1 status  -Target D:\WebApp\BMA-Desktop
D:\WebData\github\BlockMemoryAgent\bma-app.ps1 stop    -Target D:\WebApp\BMA-Desktop
D:\WebData\github\BlockMemoryAgent\bma-app.ps1 restart -Target D:\WebApp\BMA-Desktop
D:\WebData\github\BlockMemoryAgent\bma-app.ps1 rollback -Target D:\WebApp\BMA-Desktop
```

- `launch.ps1` 是单实例语义：已在跑就直接返回；未跑才启动 `app\versions\<current>\bin\bma-server.exe`，随后等待健康检查。
- GFA 的 BMA 集成会自动发现 `D:\WebApp\BMA-Desktop\app\launch.ps1`，分派前若 BMA 不在线就调用它启动，并等待健康检查。
- 升级/安装会保留用户数据（`.env`、`config\`、`workspace\`、`logs\`），不要手工覆盖这些目录。

## 给 GFA 的调度方式

优先走 BMA 原生 HTTP API：

```http
POST http://127.0.0.1:10011/api/sessions
Content-Type: application/json

{"goal":"任务目标与验收标准","work_dir":"D:\\path\\to\\project","gear":"daily"}
```

- `goal` 必填，建议写清：背景、涉及文件/目录、期望产物、验收标准、禁止事项。
- `work_dir` 可选；不填用 BMA 默认 `workspace/`，填了必须存在且是文件夹。
- `gear` 可选：`fast`（轻任务直达）、`daily`（DomainAgent 执行，默认）、`cluster`（MetaAgent 多子 Agent 编排，大任务）。
- 响应只代表「已受理」，真正的结果要轮询：

```http
GET  /api/sessions/{id}          # 状态 + result
GET  /api/sessions/{id}/tree     # 子 Agent 树
POST /api/sessions/{id}/message  # 继续对话
POST /api/sessions/{id}/stop     # 软停止（可续跑）
POST /api/sessions/{id}/cancel   # 取消
POST /api/sessions/{id}/interrupt
POST /api/sessions/{id}/enqueue
POST /api/sessions/{id}/gear     # {"gear":"fast|daily|cluster"}
POST /api/sessions/{id}/model    # 会话级模型覆盖
POST /api/sessions/{id}/thinking
POST /api/sessions/{id}/workdir
```

GFA 当前实现（`internal/agenthub/bma.go`）：

- 自动发现桌面安装目录并读取 `.env` 的端口/Token；
- 分派时先探 `/api/health`，不在线则调用 `app\launch.ps1 -NoBrowser -Quiet`；
- 用 `POST /api/sessions` 建会话（`gear=daily`、传 `work_dir`），每 1.5 秒轮询；
- 单次派发最长 30 分钟，连续 5 分钟无状态推进则放弃；
- 结果原样回传，失败状态如实标注，不伪造成功。

**安排任务的建议**：一个会话只放一个可验收目标；跨项目/跨阶段拆成多个会话或使用 BMA 的 DAG/子 Agent；长任务先选 `cluster`，短小改动用 `daily`，纯文档问答用 `fast`；验收标准要可检查（文件路径、命令输出、测试结果）；需要改动用户代码时明确 `work_dir`，不要让它自由选目录。

## 开发与修改 BMA

源码仓库：`D:\WebData\github\BlockMemoryAgent`

```bash
# 环境：Go 1.25，命令必须带 GOTOOLCHAIN=local
GOTOOLCHAIN=local go test ./...
GOTOOLCHAIN=local go vet ./...

# 依赖与本地运行
make up        # docker compose：PostgreSQL + Redis
make migrate   # 应用 migrations/*.sql；全新库也可由 bootstrap 自动建表
make run       # 构建 web/dist 并 go run ./backend（默认 127.0.0.1:10010）

# 打包与安装
powershell -File dist.ps1
powershell -File bma-app.ps1 pack
powershell -File bma-app.ps1 update -Target D:\WebApp\BMA-Desktop
```

关键配置文件（缺一不可，严格启动）：

- `config/config.yaml`：服务端口、鉴权、Agent 默认目录、DAG；
- `config/roles.yaml`：meta_agent / domain_agent / lightweight_model 等角色绑定；
- `config/models.json`：模型连接参数（Key 从 `.env` 插值）；
- `config/skills.yaml`、`config/soul.md`、`config/user_profile.md`、`config/plugins.yaml`。

修改代码的约束：

- 新建表/字段带 owner 列；底账 append-only，剪枝只改视图或归档标记；
- 会话/事件/日志的持久化语义不能被改成内存 `go func()`；
- 工具调用统一走 Registry，才能继承审批、白名单、事件流与前端卡片；
- 改 BMA 前端后必须重建 `web/dist`，否则 Web/桌面仍加载旧页面。

## 插件与技能（BMA 自身）

- 插件：`config/plugins.yaml` + `plugins.d/`；kind 包括 `mcp`（stdio/HTTP/docker）、`service`（Docker 长驻 HTTP）、`bundle`（Claude/Codex 插件包）。管理 API：`GET /api/plugins`、`POST /api/plugins/{id}/enable|disable`、`POST /api/plugins/reload`。
- 技能：`config/skills.yaml`、约定目录 `.claude/skills/<name>/SKILL.md` 以及自进化技能 `config/skills_learned/`；`load_skill` 按需加载全文，配套脚本放在技能目录 `scripts/`。
- BMA 的“技能携带工具”允许把会话中跑通的小脚本固化为技能配套命令，但仍需经过沙箱与审批。

## 部署要点

- PostgreSQL 14+（pgvector）与 Redis 7+ 必须可达；常见端口 5432 / 6380。
- `.env` 至少配置一个模型 Key、`POSTGRES_DSN`、`REDIS_ADDR`；Docker 自带 Redis 时不要忘记密码。
- 默认只绑回环；对局域网开放时同时开启鉴权并配置 TLS 反代。
- 检查入口：`/api/health`（进程就绪）与 `/api/capabilities`（llm/postgres/redis/embed/plugins/workdir/docker 逐项诊断）。
- 安装/升级用 `bma-app.ps1 update`，健康门禁失败会回滚；不要手工删 `app\versions` 或覆盖 `config\`/`.env`。

## 常见故障

| 现象 | 排查 |
|---|---|
| GFA 显示 BMA 未运行 | 检查 `D:\WebApp\BMA-Desktop\.env` 的 `HTTP_ADDR`；手动跑 `app\launch.ps1 -NoBrowser -Quiet`；看 `logs\` |
| `/api/health` 通但会话 401 | `http.auth_enabled: true` 时必须带 `Authorization: Bearer <BMA_API_TOKEN>`；GFA 会从 `.env` 读取 token |
| 启动报 Redis 认证失败 | `.env` 的 `REDIS_PASSWORD` 与 compose 一致 |
| 模型调用 401/404 | 在 BMA「模型」页检查角色绑定与 Key；启动不校验模型连通性 |
| 端口冲突 | 改 `.env` 的 `HTTP_ADDR` 并重启；GFA 会重新读取 `.env` |
| Docker 不可用 | `/api/capabilities` 的 docker 项会给出原因；插件启用依赖 Docker |
| 升级后数据丢失风险 | 升级前备份 `config\`、`workspace\`、`.env`；不要用 dist 模板覆盖现有配置 |

## 安全边界

- BMA 会读写 `work_dir` 内文件并执行命令；分发任务前确认目录和验收标准。
- 不在技能/日志中写入 API Key、Token、用户隐私内容。
- 不直接修改 BMA 的 PostgreSQL 业务表来“修结果”；通过 API 或 BMA 自己的迁移/工具处理。
- 用户明确要求时才执行会修改其他项目的任务。
