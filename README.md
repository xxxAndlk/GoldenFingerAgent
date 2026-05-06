# 金手指 Agent 系统

> 像小说主角的金手指一样，带领你持续修炼成长。

一个**本地优先、持续进化**的个人 Agent 系统。接收你的任意问题，自行拆解、规划、执行、校验，并将每一次经验沉淀为可复用的「功法」——越用越强。

---

## 目录

- [架构概览](#架构概览)
- [快速开始](#快速开始)
- [配置模型](#配置模型)
- [使用方式](#使用方式)
- [项目结构](#项目结构)
- [五域详解](#五域详解)
- [内置 Skill](#内置-skill)
- [内置工具](#内置工具)
- [安全模型](#安全模型)
- [开发指南](#开发指南)
- [相关文档](#相关文档)
- [后续规划](#后续规划)

---

## 架构概览

```
用户问题
    │
    ▼
┌──────────┐   ┌──────────┐   ┌──────────┐   ┌──────────┐
│① 天机推演 │──▶│② 施法执行 │──▶│③ 验道校验 │──▶│④ 刻碑沉淀 │
│ 分析规划  │   │ 执行调度  │   │ 结果校验  │   │ 持久进化  │
└──────────┘   └──────────┘   └──────────┘   └────┬─────┘
     │                                             │
     └──────────── ⑤ 内外结界（贯穿全程） ◀──────────┘
```

| 域 | 网文隐喻 | 职责 |
|----|----------|------|
| ① 天机推演 | 分析规划 | 意图分类 → 任务拆解 DAG → Skill 匹配 → 提示词组装 |
| ② 施法执行 | 执行调度 | 拓扑编排 → LLM ↔ Tool 循环 → 安全防御 → 异常处理 |
| ③ 验道校验 | 结果校验 | 结构校验 → 内容校验 → 步骤回放 → 回退/重构 |
| ④ 刻碑沉淀 | 持久进化 | 执行摘要 → Skill 缺口分析 → Skill 更新/新建 → 画像进化 |
| ⑤ 内外结界 | 隔离安全 | 三级数据分级 → 出境脱敏 → 入境过滤 → 画像演化 |

### 技术栈

| 层级 | 选型 |
|------|------|
| LLM 调用 | `httpx` 直调 OpenAI / Anthropic API |
| Web 后端 | `FastAPI` + `uvicorn` + SSE 流式推送 |
| Web 前端 | `Vue 3` + `TypeScript` + `Vite` + `marked` |
| 终端 TUI | `Textual` + `Rich` |
| 数据模型 | `pydantic` v2 |
| 向量存储 | `ChromaDB`（嵌入式，零配置） |
| 关系存储 | `SQLite3`（Python 标准库） |
| 配置管理 | `python-dotenv` + 环境变量 |
| 异步 | `asyncio` |

---

## 快速开始

### 环境要求

- Python 3.10+
- Node.js 18+（前端构建）
- Windows 10+ / Linux / macOS

### 1. 安装

```bash
# 克隆或进入项目目录
cd GoldenFinger

# 安装 Python 依赖
pip install -r requirements.txt

# 安装为可执行命令
pip install -e .
```

### 2. 构建前端（Web 模式需要）

```bash
cd frontend
npm install
npm run build
cd ..
```

### 3. 配置 API Key

```bash
# 复制配置模板
cp .env.example .env

# 编辑 .env 文件，填入你的 API Key
```

**.env 示例：**

```env
# 使用 OpenAI（推荐新手）
OPENAI_API_KEY=sk-you...here
OPENAI_BASE_URL=https://api.openai.com/v1
OPENAI_MODEL=gpt-4o

# 使用 Anthropic Claude
# ANTHROPIC_API_KEY=sk-ant...here
# ANTHROPIC_MODEL=claude-sonnet-4-6

# 选择使用的提供商：openai / anthropic
GOLDEN_FINGER_LLM_PROVIDER=openai

# 数据存储目录（默认 ./data）
GOLDEN_FINGER_DATA_DIR=./data

# 日志级别
GOLDEN_FINGER_LOG_LEVEL=INFO
```

### 4. 启动

```bash
# 终端 TUI 模式（默认，推荐）
gfa

# Web 界面模式（自动打开浏览器）
gfa --web

# 单次命令模式
gfa "帮我写一个Python脚本读取CSV文件并统计"

# 查看宿主状态
gfa --status
```

---

## 配置模型

### OpenAI

支持所有 OpenAI 兼容 API（包括国内代理）：

```env
GOLDEN_FINGER_LLM_PROVIDER=openai
OPENAI_API_KEY=sk-your-key
OPENAI_BASE_URL=https://api.openai.com/v1    # 官方
# OPENAI_BASE_URL=https://your-proxy.com/v1  # 代理
OPENAI_MODEL=gpt-4o
```

### Anthropic Claude

```env
GOLDEN_FINGER_LLM_PROVIDER=anthropic
ANTHROPIC_API_KEY=***
ANTHROPIC_MODEL=claude-sonnet-4-6
```

### 使用国内大模型 API

如果你使用国内代理（如 DeepSeek、通义千问等提供 OpenAI 兼容接口）：

```env
GOLDEN_FINGER_LLM_PROVIDER=openai
OPENAI_API_KEY=sk-you...-key
OPENAI_BASE_URL=https://api.deepseek.com
OPENAI_MODEL=deepseek-chat
```

> 只要 API 兼容 OpenAI 的 `/chat/completions` 端点即可使用。

---

## 使用方式

### 终端 TUI 模式（默认）

```bash
gfa
```

进入 Textual 终端界面，直接输入问题，系统自动走完整五域回路：

```
宿主> 帮我学习Python装饰器，从入门到进阶

🔮 天机推演中...
  ✓ 拆解为 4 个原子任务
    [knowledge_absorption] 解释装饰器基础概念
    [code_assistant] 编写装饰器示例代码
    ...
⚡ 施法执行中...
  ⚙ web_search(...) → ✓ (245ms)
  ⚙ file_write(...) → ✓ (12ms)
  ✓ 执行完成 (3200ms)
🔍 验道校验中...
  ✓ 校验结果: 通过
📝 刻碑沉淀中...
  ✓ 经验已沉淀

━━━ 修炼结束 ━━━
[最终回答...]
```

### Web 界面模式

```bash
gfa --web
```

自动打开浏览器，展示 Vue 3 构建的可视化界面：

- 实时 SSE 流式展示五域流水线进度
- 工具调用详情（参数/结果/耗时）
- Markdown 渲染最终回答
- 顶部显示宿主画像状态

### 单次命令模式

```bash
gfa "帮我写一个Python脚本读取CSV文件并统计"
```

直接在终端输出 Markdown 格式的回答。

### 查看状态

```bash
gfa --status
```

显示宿主画像（灵根属性、修炼境界、Skill 列表、系统信息）。

### 终端交互命令

| 快捷键 | 说明 |
|--------|------|
| `Ctrl+D` | 退出系统 |
| `Ctrl+E` | 导出日志 |

---

## 项目结构

```
GoldenFinger/
├── golden_finger/               # Python 主包
│   ├── __init__.py              # 包信息
│   ├── __main__.py              # python -m golden_finger 入口
│   ├── models.py                # 所有 Pydantic 数据模型
│   ├── config.py                # 配置管理
│   ├── llm.py                   # LLM 抽象层（OpenAI + Anthropic）
│   ├── harness.py               # 主调度器（五域编排）
│   ├── api.py                   # FastAPI 后端（REST + SSE）
│   ├── cli.py                   # CLI 入口
│   │
│   ├── domain_analysis.py       # ① 天机推演
│   ├── domain_execution.py      # ② 施法执行
│   ├── domain_verification.py   # ③ 验道校验
│   ├── domain_persistence.py    # ④ 刻碑沉淀
│   ├── domain_isolation.py      # ⑤ 内外结界
│   │
│   ├── skills/                  # Skill 系统
│   │   ├── base.py              #   BaseSkill 基类
│   │   ├── registry.py          #   Skill 注册表
│   │   ├── knowledge.py         #   知识汲取术
│   │   ├── code_assistant.py    #   代码辅助术
│   │   └── file_operations.py   #   文件操作术
│   │
│   ├── tools/                   # 工具系统
│   │   ├── base.py              #   BaseTool 基类
│   │   ├── builtin.py           #   内置工具
│   │   └── sandbox.py           #   沙箱执行器
│   │
│   ├── tui/                     # 终端 TUI
│   │   ├── app.py               #   Textual 应用
│   │   └── tui.tcss             #   TUI 样式
│   │
│   └── storage/                 # 存储层
│       ├── sqlite_store.py      #   SQLite 存储
│       └── vector_store.py      #   ChromaDB 向量存储
│
├── frontend/                    # Vue 3 前端
│   ├── src/
│   │   ├── App.vue              #   主组件（五域流水线可视化）
│   │   ├── main.ts              #   入口
│   │   ├── style.css            #   样式
│   │   └── composables/
│   │       └── useSSE.ts        #   SSE 流式接收
│   ├── index.html
│   ├── package.json
│   ├── vite.config.ts
│   └── tsconfig.json
│
├── data/                        # 运行时数据（自动创建）
│   ├── golden_finger.db         #   SQLite 数据库
│   ├── skills/                  #   Skill 定义
│   ├── memory/                  #   ChromaDB 持久化
│   └── logs/                    #   执行日志
│
├── pyproject.toml               # 项目配置（gfa 命令入口）
├── requirements.txt             # 依赖清单
├── .env.example                 # 环境变量模板
├── README.md                    # 本文件
├── 产品架构.md                   # 产品架构文档（推荐阅读）
├── 金手指Agent产品架构v2.md      # 远景规划
├── 金手指Agent系统设计文档.md      # v1.0 设计文档
└── 金手指Agent实施路线图.md       # 实施路线图
```

---

## 五域详解

### ① 天机推演 — 分析规划

将用户任意问题变为可执行的原子任务 DAG。

```
用户问题 → 意图分类 → 任务拆解(DAG) → Skill匹配(向量检索) → 提示词组装 → TaskPlan
```

**关键组件：**
- `IntentClassifier`：评估问题复杂度（simple_qa / skill_single / skill_chain / long_running）
- `TaskDecomposer`：LLM 驱动的任务拆解，支持依赖关系 DAG
- `SkillMatcher`：关键词匹配已有 Skill
- `PromptComposer`：注入宿主上下文 + Skill 知识 + 安全约束
- `PlanGenerator`：聚合入口，返回完整 `TaskPlan`

### ② 施法执行 — 执行调度

按拓扑序执行 TaskPlan，管理 LLM ↔ Tool 交互循环。

```
TaskPlan → 拓扑分层调度 → [LLM调用 → 解析ToolCall → 安全检查 → 工具执行] × N → ExecutionReport
```

**安全防御：**
1. 提示词注入检测（正则 + 规则）
2. 权限校验（工具白名单 + 参数范围）
3. 沙箱执行（路径限制 + 命令过滤）
4. 结果校验（格式 + 敏感信息泄漏扫描）

**关键组件：**
- `ExecutionOrchestrator`：按拓扑分层调度，同层任务并行执行
- `SingleTaskExecutor`：单任务 LLM ↔ Tool 循环（最多 8 轮）
- `ToolExecutionGuard`：注入检测 → 权限校验 → 工具执行

### ③ 验道校验 — 结果校验

三层校验模型确保执行结果质量。

```
三层校验：
  第一层(结构校验)：输出格式/完整性 → Schema匹配
  第二层(内容校验)：内容质量 → LLM-as-Judge 评估
  第三层(步骤回放)：关键操作 → 可复现验证

失败处理：重试 → 回退 → 重构 → 请求用户澄清
```

**关键组件：**
- `StructureChecker`：检查输出格式、完整性、工具调用异常
- `ContentChecker`：LLM-as-Judge 评估相关性、完整性、准确性
- `ReplayChecker`：验证关键工具操作是否生效
- `RollbackEngine`：生成回退计划（清理副作用）

### ④ 刻碑沉淀 — 持久进化

将执行经验转化为可复用的 Skill 知识，让系统越用越强。

```
ExecutionReport → 执行摘要生成 → Skill缺口分析 → Skill更新/新建 → 画像更新
```

**关键组件：**
- `ExecutionSummarizer`：LLM 生成结构化摘要（做了什么→怎么做的→遇到什么问题→怎么解决）
- `GapAnalyzer`：检测 Skill 缺失和能力边界
- `SkillUpdater`：追加知识条目 + 更新向量库 + 调整统计
- `ProfileUpdater`：更新灵根属性 + 累计修炼时间 + 检测境界突破

### ⑤ 内外结界 — 隔离安全

贯穿全流程的横向切面。

**数据三级分级：**

| 等级 | 名称 | 范围 | 出境规则 |
|------|------|------|----------|
| Level 0 | 紫府级 | 真实姓名/坐标/收入/健康原始值 | 禁止出境 |
| Level 1 | 灵台级 | 能力坐标/学习偏好/技能熟练度 | 脱敏后内部使用 |
| Level 2 | 识海级 | 境界等级/通用标签/匿名ID | 可出境 |

**关键组件：**
- `EgressAnonymizer`：PII 脱敏（手机号/身份证/邮箱/银行卡/IP）+ 坐标模糊化
- `IngressFilter`：XSS/SQL注入/命令注入/路径穿越检测
- `ProfileEvolver`：从交互中提取画像更新信号

---

## 内置 Skill

| Skill | 显示名称 | 适用场景 | 所需工具 |
|-------|----------|----------|----------|
| `knowledge_absorption` | 知识汲取术 | 学习、解释概念、规划学习路径 | web_search, file_write, file_read |
| `code_assistant` | 代码辅助术 | 编程、调试、代码优化 | file_read, file_write, shell_exec |
| `file_operations` | 文件操作术 | 文件管理、批量处理、文档操作 | file_read, file_write, shell_exec |

系统会自动根据你的问题匹配最合适的 Skill。没有匹配的 Skill 时，会在刻碑沉淀阶段自动创建新 Skill。

---

## 内置工具

| 工具 | 说明 | 安全限制 |
|------|------|----------|
| `file_read` | 读取文件内容 | 仅允许访问工作目录和 data 目录 |
| `file_write` | 写入文件 | 路径限制 + 内容注入检测 |
| `shell_exec` | 执行 Shell 命令 | 危险命令检测（rm -rf / 等），超时控制 |
| `web_search` | 搜索网页 | DuckDuckGo，无需 API Key |

---

## 安全模型

### 出境安全

所有发往 LLM API 的数据经过脱敏处理：
- 手机号 / 身份证 / 邮箱 → 替换为占位符
- 坐标 → 模糊化至 100m 精度
- 仅传递识海级数据（境界、通用标签）

### 入境安全

所有外部返回内容经过过滤：
- XSS / SQL 注入 / 命令注入检测
- 路径穿越检测
- 模板注入检测

### 执行沙箱

- 文件操作：仅允许访问当前工作目录、data 目录、系统临时目录
- Shell 命令：正则检测 10+ 种危险模式
- 超时控制：工具执行 60s 超时，LLM 调用 120s 超时

---

## 开发指南

### 添加新 Skill

在 `golden_finger/skills/` 下创建新文件：

```python
from .base import BaseSkill
from ..models import RealmLevel

class MySkill(BaseSkill):
    name = "my_skill"
    display_name = "我的技能"
    description = "技能描述"
    category = "knowledge"
    realm_requirement = RealmLevel.MORTAL
    tools_required = ["web_search"]

    SYSTEM_PROMPT = """你的 system prompt..."""

    async def activate(self, context: dict) -> dict:
        return {
            "skill_name": self.name,
            "system_prompt": self.SYSTEM_PROMPT,
            "context_hint": "",
        }
```

然后在 `harness.py` 的 `_init_skills()` 中注册：

```python
from .skills.my_skill import MySkill
skill_registry.register(MySkill())
```

### 添加新工具

在 `golden_finger/tools/builtin.py` 中添加：

```python
class MyTool(BaseTool):
    name = "my_tool"
    description = "工具描述"
    parameters = {
        "param1": {"type": "string", "description": "参数说明"}
    }

    async def execute(self, param1: str = "") -> ToolResult:
        # 实现逻辑
        return ToolResult(success=True, data="结果")
```

然后在 `BUILTIN_TOOLS` 字典中注册。

### 运行测试

```bash
# 完整导入验证
python -c "import golden_finger; print('OK')"

# 状态检查
gfa --status

# 单次测试（需要配置 API Key）
gfa "你好，请简单介绍自己"
```

---

## 相关文档

| 文档 | 说明 |
|------|------|
| [产品架构.md](./产品架构.md) | 产品架构总结（推荐阅读） |
| [金手指Agent产品架构v2.md](./金手指Agent产品架构v2.md) | 远景规划（目标架构设计） |
| [金手指Agent系统设计文档.md](./金手指Agent系统设计文档.md) | v1.0 设计文档（历史参考） |
| [金手指Agent实施路线图.md](./金手指Agent实施路线图.md) | 四阶段实施路线图 |

---

## 后续规划

**近期 (v0.2)：**
- [ ] 前端交互增强（对话历史、Skill 管理面板）
- [ ] 多轮对话记忆上下文
- [ ] 更多内置 Skill

**中期 (v0.3)：**
- [ ] 知识图谱可视化
- [ ] 修炼进度面板
- [ ] 境界突破动画

**远期 (v1.0+)：**
- [ ] 插件市场（第三方 Skill 发布/安装）
- [ ] 移动端适配
- [ ] 语音交互
- [ ] 多宿主协同（宗门/社群系统）
- [ ] 因果推演 Skill（长期规划与预测）

---

## License

MIT
