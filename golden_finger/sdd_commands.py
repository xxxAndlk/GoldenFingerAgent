"""金手指 Agent 系统 — SDD 工作流命令

融合 spec-kit 和 OpenSpec 的规格驱动开发工作流。

spec-kit 命令 (结构化 SDD):
  /constitution   — 创建/更新项目宪章
  /specify        — 从用户描述生成功能规格
  /clarify        — 结构化澄清未明确的需求
  /plan           — 生成技术实现计划
  /tasks          — 将计划分解为有序任务清单
  /implement      — 按 TDD 方式执行所有任务
  /analyze        — 跨制品一致性分析

OpenSpec 命令 (轻量迭代):
  /propose        — 生成变更提案 (proposal + specs + design + tasks)
  /apply          — 逐任务执行变更清单
  /archive        — 归档已完成变更，更新主规格
"""

from __future__ import annotations

import json
import logging
import os
import re
from datetime import datetime
from pathlib import Path
from typing import Any

from .llm import LLMClient

logger = logging.getLogger("golden_finger.sdd")

# .goldenfinger 目录路径
GOLDENFINGER_DIR = Path(".goldenfinger")
SPECS_DIR = GOLDENFINGER_DIR / "specs"
CHANGES_DIR = GOLDENFINGER_DIR / "changes"
CHANGES_ACTIVE = CHANGES_DIR / "active"
CHANGES_ARCHIVE = CHANGES_DIR / "archive"
MEMORY_DIR = GOLDENFINGER_DIR / "memory"
TEMPLATES_DIR = GOLDENFINGER_DIR / "templates"


# ============================================================
# 工具函数
# ============================================================

def _ensure_dirs() -> None:
    """确保所有 SDD 目录存在"""
    for d in [SPECS_DIR, CHANGES_ACTIVE, CHANGES_ARCHIVE, MEMORY_DIR, TEMPLATES_DIR]:
        d.mkdir(parents=True, exist_ok=True)


def _load_template(name: str) -> str:
    """加载模板文件"""
    path = TEMPLATES_DIR / name
    if path.exists():
        return path.read_text(encoding="utf-8")
    return ""


def _parse_json(text: str) -> dict[str, Any]:
    """从 LLM 回复中提取 JSON"""
    try:
        return json.loads(text)
    except json.JSONDecodeError:
        pass
    match = re.search(r'```(?:json)?\s*(.*?)\s*```', text, re.DOTALL)
    if match:
        try:
            return json.loads(match.group(1))
        except json.JSONDecodeError:
            pass
    return {}


def _next_spec_num(base_dir: Path, prefix: str = "") -> int:
    """获取下一个规格编号"""
    if not base_dir.exists():
        return 1
    nums = []
    pattern = re.compile(rf"^{re.escape(prefix)}(\d+)-")
    for d in base_dir.iterdir():
        if d.is_dir():
            m = pattern.match(d.name)
            if m:
                nums.append(int(m.group(1)))
    return max(nums, default=0) + 1


# ============================================================
# /constitution — 项目宪章
# ============================================================

CONSTITUTION_PROMPT = """你是一位软件工程专家。请根据项目上下文创建项目宪章（Constitution）。

项目宪章是指导所有开发决策的核心原则文档。

返回 JSON：
{{
  "principles": [
    {{"name": "原则名称", "description": "详细描述", "rationale": "为什么重要"}}
  ],
  "workflow": "推荐的开发工作流描述",
  "quality_standards": "代码质量标准",
  "test_standards": "测试标准",
  "security_standards": "安全标准"
}}

项目上下文: {context}
"""


async def cmd_constitution(llm: LLMClient, context: str = "") -> str:
    """创建或更新项目宪章"""
    _ensure_dirs()

    prompt = CONSTITUTION_PROMPT.format(context=context or "新项目")
    resp = await llm.chat(
        messages=[{"role": "user", "content": prompt}],
        max_tokens=2000,
    )
    data = _parse_json(llm.extract_text(resp))

    # 生成宪章内容
    parts = [
        "# GoldenFinger 项目宪章\n",
        f"> 最后更新: {datetime.now().strftime('%Y-%m-%d %H:%M')}\n",
        "## 核心原则\n",
    ]
    for i, p in enumerate(data.get("principles", []), 1):
        parts.append(f"### {i}. {p.get('name', '原则')}")
        parts.append(f"**描述:** {p.get('description', '')}")
        parts.append(f"**理由:** {p.get('rationale', '')}")
        parts.append("")

    if data.get("workflow"):
        parts.append(f"## 开发工作流\n{data['workflow']}\n")
    if data.get("quality_standards"):
        parts.append(f"## 代码质量标准\n{data['quality_standards']}\n")
    if data.get("test_standards"):
        parts.append(f"## 测试标准\n{data['test_standards']}\n")
    if data.get("security_standards"):
        parts.append(f"## 安全标准\n{data['security_standards']}\n")

    content = "\n".join(parts)
    path = MEMORY_DIR / "constitution.md"
    path.write_text(content, encoding="utf-8")

    logger.info(f"项目宪章已更新: {path}")
    return content


# ============================================================
# /specify — 功能规格生成
# ============================================================

SPECIFY_PROMPT = """你是一位产品经理专家。请根据用户描述创建功能规格文档。

返回 JSON：
{{
  "feature_name": "英文功能名（kebab-case）",
  "overview": "功能概述",
  "user_stories": [
    {{"title": "故事标题", "role": "角色", "want": "想要什么", "benefit": "以便什么",
      "acceptance": ["验收条件1", "验收条件2"]}}
  ],
  "functional_requirements": [
    {{"name": "需求名", "description": "描述", "priority": "P0/P1/P2", "depends_on": "依赖"}}
  ],
  "non_functional": {{"performance": "", "security": "", "compatibility": ""}},
  "boundaries": ["不做什么"],
  "clarification_needed": ["需要澄清的问题"]
}}

用户描述: {query}"""


async def cmd_specify(llm: LLMClient, query: str) -> dict[str, Any]:
    """生成功能规格文档"""
    _ensure_dirs()

    prompt = SPECIFY_PROMPT.format(query=query)
    resp = await llm.chat(
        messages=[{"role": "user", "content": prompt}],
        max_tokens=3000,
    )
    data = _parse_json(llm.extract_text(resp))

    feature_name = data.get("feature_name", "feature")
    spec_num = _next_spec_num(SPECS_DIR)
    spec_dir = SPECS_DIR / f"{spec_num:03d}-{feature_name}"
    spec_dir.mkdir(parents=True, exist_ok=True)

    # 生成 spec.md
    parts = [
        f"# Spec: {feature_name}\n",
        f"## 概述\n{data.get('overview', query)}\n",
        "## 用户故事\n",
    ]
    for i, us in enumerate(data.get("user_stories", []), 1):
        parts.append(f"### US-{i:02d}: {us.get('title', '')}")
        parts.append(f"**作为** {us.get('role', '用户')}")
        parts.append(f"**我想要** {us.get('want', '')}")
        parts.append(f"**以便** {us.get('benefit', '')}")
        parts.append("\n**验收条件:**")
        for ac in us.get("acceptance", []):
            parts.append(f"- [ ] {ac}")
        parts.append("")

    parts.append("## 功能需求\n")
    for fr in data.get("functional_requirements", []):
        parts.append(f"### {fr.get('name', '')}")
        parts.append(f"- **优先级:** {fr.get('priority', 'P1')}")
        parts.append(f"- **依赖:** {fr.get('depends_on', '无')}")
        parts.append(f"- **描述:** {fr.get('description', '')}")
        parts.append("")

    nf = data.get("non_functional", {})
    if nf:
        parts.append("## 非功能需求\n")
        for k, v in nf.items():
            if v:
                parts.append(f"- **{k}:** {v}")
        parts.append("")

    boundaries = data.get("boundaries", [])
    if boundaries:
        parts.append("## 边界与约束\n")
        for b in boundaries:
            parts.append(f"- {b}")
        parts.append("")

    clarifications = data.get("clarification_needed", [])
    if clarifications:
        parts.append("## 待澄清问题\n")
        for c in clarifications:
            parts.append(f"- [ ] {c}")
        parts.append("")

    content = "\n".join(parts)
    (spec_dir / "spec.md").write_text(content, encoding="utf-8")

    logger.info(f"规格已生成: {spec_dir}/spec.md")
    return {
        "status": "created",
        "spec_dir": str(spec_dir),
        "feature_name": feature_name,
        "content": content,
    }


# ============================================================
# /clarify — 需求澄清
# ============================================================

CLARIFY_PROMPT = """你是一位需求分析师。请审查以下功能规格，找出模糊或不完整的地方，提出澄清问题。

对每个问题，提供：
- 问题所属的用户故事或功能需求
- 具体的问题
- 为什么这个问题重要
- 可能的选项（如有）

返回 JSON：
{{
  "questions": [
    {{"scope": "US-01", "question": "具体问题", "importance": "为什么重要", "options": ["选项A", "选项B"]}}
  ],
  "overall_assessment": "总体评估",
  "recommendation": "建议（继续/需要澄清/需要重新定义）"
}}

当前规格:
{spec_content}"""


async def cmd_clarify(llm: LLMClient, spec_content: str) -> dict[str, Any]:
    """澄清规格中的模糊点"""
    prompt = CLARIFY_PROMPT.format(spec_content=spec_content[:5000])
    resp = await llm.chat(
        messages=[{"role": "user", "content": prompt}],
        max_tokens=2000,
    )
    data = _parse_json(llm.extract_text(resp))
    return data


# ============================================================
# /plan — 技术实现计划
# ============================================================

PLAN_PROMPT = """你是一位技术架构师。请基于功能规格生成技术实现计划。

返回 JSON：
{{
  "tech_stack": {{"language": "", "framework": "", "database": "", "key_dependencies": []}},
  "architecture": "架构设计描述",
  "phases": [
    {{"name": "阶段名", "tasks": ["任务1", "任务2"]}}
  ],
  "api_endpoints": [
    {{"method": "GET/POST/PUT/DELETE", "path": "/api/...", "description": ""}}
  ],
  "risks": [
    {{"risk": "风险描述", "impact": "高/中/低", "probability": "高/中/低", "mitigation": "缓解措施"}}
  ],
  "test_strategy": "测试策略描述"
}}

规格内容:
{spec_content}

技术上下文（如有）:
{tech_context}"""


async def cmd_plan(llm: LLMClient, spec_content: str, tech_context: str = "") -> dict[str, Any]:
    """生成技术实现计划"""
    _ensure_dirs()

    prompt = PLAN_PROMPT.format(
        spec_content=spec_content[:4000],
        tech_context=tech_context or "无特定技术栈偏好",
    )
    resp = await llm.chat(
        messages=[{"role": "user", "content": prompt}],
        max_tokens=3000,
    )
    data = _parse_json(llm.extract_text(resp))

    # 尝试保存到对应的 spec 目录
    spec_dirs = sorted(SPECS_DIR.glob("*/spec.md"), key=os.path.getmtime, reverse=True)
    target_dir = spec_dirs[0].parent if spec_dirs else SPECS_DIR

    # 生成 plan.md
    parts = [
        "# 技术实现计划\n",
        f"## 技术栈\n",
    ]
    ts = data.get("tech_stack", {})
    parts.append(f"- **语言/框架:** {ts.get('language', '未指定')}")
    parts.append(f"- **框架:** {ts.get('framework', '无')}")
    parts.append(f"- **数据库:** {ts.get('database', '无')}")
    deps = ts.get("key_dependencies", [])
    if deps:
        parts.append(f"- **关键依赖:** {', '.join(deps)}")
    parts.append("")

    parts.append(f"## 架构设计\n{data.get('architecture', '待补充')}\n")

    parts.append("## 实现路径\n")
    for i, phase in enumerate(data.get("phases", []), 1):
        parts.append(f"### Phase {i}: {phase.get('name', '')}")
        for task in phase.get("tasks", []):
            parts.append(f"- [ ] {task}")
        parts.append("")

    endpoints = data.get("api_endpoints", [])
    if endpoints:
        parts.append("## 接口合约\n")
        parts.append("| 方法 | 路径 | 描述 |")
        parts.append("|------|------|------|")
        for ep in endpoints:
            parts.append(f"| {ep.get('method', 'GET')} | {ep.get('path', '')} | {ep.get('description', '')} |")
        parts.append("")

    risks = data.get("risks", [])
    if risks:
        parts.append("## 风险评估\n")
        parts.append("| 风险 | 影响 | 概率 | 缓解措施 |")
        parts.append("|------|------|------|----------|")
        for r in risks:
            parts.append(
                f"| {r.get('risk', '')} | {r.get('impact', '')} | "
                f"{r.get('probability', '')} | {r.get('mitigation', '')} |"
            )
        parts.append("")

    if data.get("test_strategy"):
        parts.append(f"## 测试策略\n{data['test_strategy']}\n")

    content = "\n".join(parts)
    (target_dir / "plan.md").write_text(content, encoding="utf-8")

    logger.info(f"技术计划已生成: {target_dir}/plan.md")
    return {"status": "created", "plan_dir": str(target_dir), "content": content}


# ============================================================
# /tasks — 任务分解
# ============================================================

TASKS_PROMPT = """你是一位项目经理。请根据技术计划将工作分解为可执行的任务清单。

每个任务必须：
- 有具体的文件路径
- 2-15 分钟可完成
- 标记可并行执行的任务 [P]

返回 JSON：
{{
  "user_stories": [
    {{
      "story": "US-01: 名称",
      "tasks": [
        {{"id": "T001", "description": "任务描述", "file": "路径", "parallel": true/false,
          "depends_on": [], "estimated_min": 5}}
      ]
    }}
  ],
  "execution_order": [["T001", "T003"], ["T002"]],
  "checkpoints": [{{"name": "检查点", "after_tasks": ["T002"]}}]
}}

技术计划:
{plan_content}

规格:
{spec_content}"""


async def cmd_tasks(llm: LLMClient, plan_content: str, spec_content: str = "") -> dict[str, Any]:
    """生成任务清单"""
    _ensure_dirs()

    prompt = TASKS_PROMPT.format(
        plan_content=plan_content[:3000],
        spec_content=spec_content[:2000] if spec_content else "无",
    )
    resp = await llm.chat(
        messages=[{"role": "user", "content": prompt}],
        max_tokens=3000,
    )
    data = _parse_json(llm.extract_text(resp))

    spec_dirs = sorted(SPECS_DIR.glob("*/plan.md"), key=os.path.getmtime, reverse=True)
    target_dir = spec_dirs[0].parent if spec_dirs else SPECS_DIR

    parts = ["# 任务清单\n", "## 依赖关系\n"]
    eo = data.get("execution_order", [])
    for i, level in enumerate(eo):
        parts.append(f"- Level {i} (并行): {', '.join(level)}")
    parts.append("")

    for us in data.get("user_stories", []):
        parts.append(f"### {us.get('story', '')}")
        for task in us.get("tasks", []):
            p_flag = " [P]" if task.get("parallel") else ""
            deps = f" (依赖: {', '.join(task.get('depends_on', []))})" if task.get("depends_on") else ""
            parts.append(
                f"- [ ] {task.get('id', '')}{p_flag} {task.get('description', '')}"
                f"{deps} — {task.get('estimated_min', 5)}min"
            )
            if task.get("file"):
                parts.append(f"  - 文件: `{task['file']}`")
        parts.append("")

    cps = data.get("checkpoints", [])
    if cps:
        parts.append("## 检查点\n")
        for cp in cps:
            parts.append(f"### {cp.get('name', '')}")
            parts.append(f"- 在任务 {', '.join(cp.get('after_tasks', []))} 后检查")
            parts.append("")

    content = "\n".join(parts)
    (target_dir / "tasks.md").write_text(content, encoding="utf-8")

    logger.info(f"任务清单已生成: {target_dir}/tasks.md")
    return {"status": "created", "tasks_dir": str(target_dir), "content": content}


# ============================================================
# /implement — 执行实现（委托给 Harness 五域流水线）
# ============================================================

async def cmd_implement(
    llm: LLMClient,
    tasks_content: str,
    harness=None,  # GoldenFingerHarness
) -> dict[str, Any]:
    """按 tasks.md 执行实现——委托给五域流水线"""
    if harness is None:
        return {"status": "error", "error": "Harness 不可用"}

    # 解析任务清单
    tasks = []
    for line in tasks_content.split("\n"):
        match = re.match(r'- \[ \] (\S+).*?— (\d+)min', line)
        if match:
            tasks.append({"id": match.group(1), "est_min": int(match.group(2))})

    results = []
    for task in tasks:
        query = f"执行任务 {task['id']}: 参考 tasks.md 中的描述完成此任务。遵循 TDD 流程。"
        result = await harness.run_query(query)
        results.append({"task_id": task["id"], "result": result})

    return {"status": "completed", "tasks_executed": len(tasks), "results": results}


# ============================================================
# /propose — 变更提案 (OpenSpec)
# ============================================================

PROPOSE_PROMPT = """你是一位软件工程师。请根据用户描述的变更需求生成轻量级变更提案。

返回 JSON：
{{
  "change_id": "CHG-XXX",
  "title": "变更标题",
  "motivation": "为什么做这个变更",
  "scope_included": ["包含的变更1", "包含的变更2"],
  "scope_excluded": ["明确不做的内容"],
  "affected_files": ["文件1", "文件2"],
  "breaking_change": true/false,
  "design_summary": "方案概述",
  "acceptance_criteria": ["验收标准1", "验收标准2"],
  "tasks": [
    {{"id": "T001", "desc": "任务描述", "file": "文件路径", "parallel": false, "est_min": 5}}
  ]
}}

用户变更描述: {query}"""


async def cmd_propose(llm: LLMClient, query: str) -> dict[str, Any]:
    """生成变更提案"""
    _ensure_dirs()

    prompt = PROPOSE_PROMPT.format(query=query)
    resp = await llm.chat(
        messages=[{"role": "user", "content": prompt}],
        max_tokens=3000,
    )
    data = _parse_json(llm.extract_text(resp))

    change_num = _next_spec_num(CHANGES_ACTIVE, "CHG-")
    change_id = f"CHG-{change_num:03d}"
    change_dir = CHANGES_ACTIVE / change_id
    change_dir.mkdir(parents=True, exist_ok=True)

    # proposal.md
    parts = [
        f"# 变更提案: {data.get('title', query)}\n",
        f"**Change ID:** {change_id}",
        f"**日期:** {datetime.now().strftime('%Y-%m-%d %H:%M')}",
        "",
        f"## 动机\n{data.get('motivation', '')}\n",
        "## 范围\n",
        "### 包含\n",
    ]
    for item in data.get("scope_included", []):
        parts.append(f"- {item}")
    parts.append("\n### 不包含\n")
    for item in data.get("scope_excluded", []):
        parts.append(f"- {item}")
    parts.append("")

    parts.append(f"\n## 影响分析\n")
    parts.append(f"- **破坏性变更:** {'是' if data.get('breaking_change') else '否'}")
    affected = data.get("affected_files", [])
    if affected:
        parts.append(f"- **影响文件:** {', '.join(affected)}")
    parts.append("")

    parts.append(f"## 设计方案\n{data.get('design_summary', '待补充')}\n")

    ac = data.get("acceptance_criteria", [])
    if ac:
        parts.append("## 验收标准\n")
        for c in ac:
            parts.append(f"- [ ] {c}")
        parts.append("")

    (change_dir / "proposal.md").write_text("\n".join(parts), encoding="utf-8")

    # design.md
    (change_dir / "design.md").write_text(
        f"# 设计文档: {data.get('title', query)}\n\n"
        f"## 问题陈述\n{data.get('motivation', '')}\n\n"
        f"## 方案描述\n{data.get('design_summary', '待补充')}\n",
        encoding="utf-8",
    )

    # tasks.md
    task_parts = ["# 任务清单\n", "## 依赖关系\n"]
    task_parts.append("所有任务按顺序执行\n")
    for task in data.get("tasks", []):
        p_flag = " [P]" if task.get("parallel") else ""
        task_parts.append(
            f"- [ ] {task.get('id', '')}{p_flag} {task.get('desc', '')}"
            f" — {task.get('est_min', 5)}min"
        )
        if task.get("file"):
            task_parts.append(f"  - 文件: `{task['file']}`")
    (change_dir / "tasks.md").write_text("\n".join(task_parts), encoding="utf-8")

    logger.info(f"变更提案已生成: {change_dir}")
    return {
        "status": "created",
        "change_id": change_id,
        "change_dir": str(change_dir),
        "proposal": (change_dir / "proposal.md").read_text(encoding="utf-8"),
    }


# ============================================================
# /apply — 应用变更
# ============================================================

async def cmd_apply(
    llm: LLMClient,
    change_id: str,
    harness=None,
) -> dict[str, Any]:
    """执行变更清单（委托给五域流水线）"""
    _ensure_dirs()

    change_dir = CHANGES_ACTIVE / change_id
    if not change_dir.exists():
        return {"status": "error", "error": f"变更 {change_id} 不存在"}

    tasks_path = change_dir / "tasks.md"
    if not tasks_path.exists():
        return {"status": "error", "error": "tasks.md 不存在"}

    tasks_content = tasks_path.read_text(encoding="utf-8")

    if harness is None:
        return {"status": "error", "error": "Harness 不可用，无法执行 /apply"}

    result = await cmd_implement(llm, tasks_content, harness)
    return result


# ============================================================
# /archive — 归档变更
# ============================================================

async def cmd_archive(change_id: str) -> dict[str, Any]:
    """归档已完成变更"""
    _ensure_dirs()

    src = CHANGES_ACTIVE / change_id
    if not src.exists():
        return {"status": "error", "error": f"变更 {change_id} 不存在"}

    dst = CHANGES_ARCHIVE / change_id
    # 移动目录
    import shutil
    shutil.move(str(src), str(dst))

    # 更新主规格（将变更的 specs 合并到 specs/ 目录）
    spec_files = list(dst.glob("*.md"))
    if spec_files:
        archive_spec_dir = SPECS_DIR / f"archive-{change_id}"
        archive_spec_dir.mkdir(parents=True, exist_ok=True)
        for sf in spec_files:
            dest = archive_spec_dir / sf.name
            dest.write_text(sf.read_text(encoding="utf-8"), encoding="utf-8")

    logger.info(f"变更已归档: {change_id} -> {dst}")
    return {"status": "archived", "change_id": change_id, "archive_dir": str(dst)}


# ============================================================
# /analyze — 跨制品一致性分析
# ============================================================

ANALYZE_PROMPT = """你是一位质量保证专家。请审查以下规格制品，检查一致性。

检查维度：
1. spec.md ↔ plan.md 一致性（所有功能需求是否有对应的实现计划？）
2. plan.md ↔ tasks.md 一致性（所有计划阶段是否有对应的任务？）
3. 是否有遗漏的功能需求？
4. 是否有计划了但没有对应需求的任务（范围蔓延）？

返回 JSON：
{{
  "overall_score": 0-100,
  "issues": [
    {{"severity": "critical/major/minor", "artifacts": ["spec.md", "plan.md"], "description": ""}}
  ],
  "missing_coverage": ["未覆盖的需求"],
  "scope_creep": ["范围蔓延的任务"],
  "recommendation": "总体建议"
}}

规格文件:
{artifacts}"""


async def cmd_analyze(llm: LLMClient, spec_dir: str | None = None) -> dict[str, Any]:
    """跨制品一致性分析"""
    if spec_dir:
        target = Path(spec_dir)
    else:
        spec_dirs = sorted(SPECS_DIR.glob("*/"), key=os.path.getmtime, reverse=True)
        target = spec_dirs[0] if spec_dirs else SPECS_DIR

    # 收集所有制品
    artifacts = {}
    for fname in ["spec.md", "plan.md", "tasks.md"]:
        fpath = target / fname
        if fpath.exists():
            artifacts[fname] = fpath.read_text(encoding="utf-8")[:2000]

    if not artifacts:
        return {"status": "error", "error": "没有找到规格制品"}

    artifacts_text = "\n\n".join(
        f"=== {name} ===\n{content}" for name, content in artifacts.items()
    )

    prompt = ANALYZE_PROMPT.format(artifacts=artifacts_text)
    resp = await llm.chat(
        messages=[{"role": "user", "content": prompt}],
        max_tokens=2000,
    )
    data = _parse_json(llm.extract_text(resp))
    return {
        "status": "analyzed",
        "spec_dir": str(target),
        "overall_score": data.get("overall_score", 0),
        "issues": data.get("issues", []),
        "missing_coverage": data.get("missing_coverage", []),
        "scope_creep": data.get("scope_creep", []),
        "recommendation": data.get("recommendation", ""),
    }
