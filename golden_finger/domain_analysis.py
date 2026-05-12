"""金手指 Agent 系统 — ① 天机推演（分析规划域）

职责：把用户的任意问题，变成可执行的原子任务 DAG。

流程：
1. 意图分类 → 复杂度评估
2. 任务拆解 → DAG 生成
3. Skill 匹配 → 检索已有功法
4. 提示词组合 → 注入画像 + 约束
5. 返回 TaskPlan
"""

import logging
import os
from typing import Any

from .models import (
    TaskPlan, AtomTask, TaskComplexity,
    HostProfile, RealmLevel,
)
from .llm import LLMClient
from .skills.registry import skill_registry
from .skill_trigger import trigger_engine, TriggerContext, DomainTagger, ContextState
from .domain_isolation import EgressAnonymizer
from .utils import parse_json

logger = logging.getLogger("golden_finger.domain_analysis")


# ============================================================
# 意图分类器
# ============================================================

INTENT_CLASSIFY_PROMPT = """分析用户输入，返回 JSON：

{{
  "complexity": "simple_qa | skill_single | skill_chain | long_running",
  "domain": "知识学习 | 编程技术 | 文件操作 | 日常咨询 | 复合任务",
  "estimated_steps": 1-10,
  "summary": "任务简述"
}}

用户输入: {query}"""


class IntentClassifier:
    """意图分类器：评估问题复杂度"""

    def __init__(self, llm: LLMClient):
        self.llm = llm

    async def classify(self, query: str) -> dict[str, Any]:
        """分类用户意图"""
        prompt = INTENT_CLASSIFY_PROMPT.format(query=query)

        try:
            resp = await self.llm.chat(
                messages=[{"role": "user", "content": prompt}],
                max_tokens=300,
            )
            text = self.llm.extract_text(resp)
            result = parse_json(text, default={"complexity": "simple_qa", "summary": text[:50]})
            return result
        except Exception:
            return {
                "complexity": "simple_qa",
                "domain": "日常咨询",
                "estimated_steps": 1,
                "summary": query[:50],
            }


# ============================================================
# 任务拆解器
# ============================================================

TASK_DECOMPOSE_PROMPT = """你需要将用户的复杂需求拆解为原子任务列表。每个原子任务是一个独立的、可执行的小步骤。

返回 JSON 格式：
{{
  "tasks": [
    {{
      "description": "任务描述",
      "depends_on": [],  // 依赖的前置任务索引列表（从0开始）
      "skill_hint": "",  // 适合的 Skill 类型提示（knowledge/code/file/general）
      "needs_tools": [],  // 需要的工具列表
      "dispatch_mode": "sync | async",  // 同步/异步执行建议
      "chief_agent": "chief-xxx",  // 该步骤负责人(主任agent)
      "agent_kind": "reusable | ephemeral"  // 子agent类型建议
    }}
  ]
}}

规则：
- 每个任务必须足够原子化，能被单次 LLM 调用完成
- 依赖关系形成 DAG，不要有循环依赖
- 最多拆成 8 个原子任务
- 如果任务简单，可以只有 1 个任务

用户需求: {query}"""


class TaskDecomposer:
    """任务拆解器：将复杂问题拆解为原子任务 DAG"""

    def __init__(self, llm: LLMClient):
        self.llm = llm

    async def decompose(self, query: str, complexity: str) -> tuple[list[AtomTask], list[list[str]]]:
        """拆解任务，返回原子任务列表 + 拓扑分层执行顺序"""
        if complexity == "simple_qa":
            # 简单问答，单个任务
            task = AtomTask(
                description=query,
                prompt=query,
                safety_level=2,
            )
            return [task], [[task.task_id]]

        prompt = TASK_DECOMPOSE_PROMPT.format(query=query)

        try:
            resp = await self.llm.chat(
                messages=[{"role": "user", "content": prompt}],
                max_tokens=2000,
            )
            text = self.llm.extract_text(resp)
            data = parse_json(text, default={"tasks": [{"description": query}]})
        except Exception:
            task = AtomTask(description=query, prompt=query)
            return [task], [[task.task_id]]

        # 构建 AtomTask 列表
        raw_tasks = data.get("tasks", [{"description": query}])
        atom_tasks: list[AtomTask] = []
        id_map: dict[int, str] = {}  # 索引 → task_id

        for i, raw in enumerate(raw_tasks):
            task = AtomTask(
                description=raw.get("description", f"步骤 {i+1}"),
                prompt=raw.get("description", query),
                safety_level=self._infer_safety_level(raw),
                dispatch_mode=str(raw.get("dispatch_mode", "sync")).lower(),
                chief_agent=str(raw.get("chief_agent", "chief-general")),
                agent_kind=str(raw.get("agent_kind", "reusable")).lower(),
            )
            atom_tasks.append(task)
            id_map[i] = task.task_id

        # 填充依赖关系
        for i, raw in enumerate(raw_tasks):
            deps = raw.get("depends_on", [])
            for dep_idx in deps:
                if isinstance(dep_idx, int) and dep_idx in id_map:
                    atom_tasks[i].depends_on.append(id_map[dep_idx])

        # 归一化调度字段，防止 LLM 返回非法值
        for task in atom_tasks:
            if task.dispatch_mode not in {"sync", "async"}:
                task.dispatch_mode = "sync"
            if task.agent_kind not in {"reusable", "ephemeral"}:
                task.agent_kind = "reusable"

        # 拓扑排序生成执行顺序
        execution_order = self._topological_sort(atom_tasks)

        return atom_tasks, execution_order

    @staticmethod
    def _topological_sort(tasks: list[AtomTask]) -> list[list[str]]:
        """拓扑排序：返回按层级分组的 task_id 列表"""
        from collections import deque

        in_degree: dict[str, int] = {t.task_id: len(t.depends_on) for t in tasks}
        children: dict[str, list[str]] = {t.task_id: [] for t in tasks}
        for t in tasks:
            for dep in t.depends_on:
                children.setdefault(dep, []).append(t.task_id)

        queue = deque([tid for tid, deg in in_degree.items() if deg == 0])
        order: list[list[str]] = []
        while queue:
            level = list(queue)
            order.append(level)
            queue.clear()
            for tid in level:
                for child in children.get(tid, []):
                    in_degree[child] -= 1
                    if in_degree[child] == 0:
                        queue.append(child)

        return order

    def _infer_safety_level(self, raw: dict[str, Any]) -> int:
        """根据任务类型推断安全等级"""
        desc = raw.get("description", "").lower()
        needs = raw.get("needs_tools", [])
        if any(t in needs for t in ["file_write", "shell_exec", "web_search"]):
            return 2
        if any(kw in desc for kw in ["密码", "密钥", "隐私", "账号"]):
            return 0
        return 1


# ============================================================
# Skill 匹配器
# ============================================================

class SkillMatcher:
    """Skill 匹配器：为每个原子任务匹配最合适的 Skill"""

    async def match(self, task: AtomTask) -> str | None:
        """为单个任务匹配 Skill"""
        results = skill_registry.search(task.description, top_k=1)
        if results:
            return str(results[0]["skill_name"])
        return None

    async def match_all(self, tasks: list[AtomTask]) -> None:
        """为所有任务批量匹配 Skill"""
        for task in tasks:
            skill_name = await self.match(task)
            task.matched_skill = skill_name
            if skill_name is None:
                task.is_new_skill_needed = True


# ============================================================
# 提示词组合器
# ============================================================

class PromptComposer:
    """提示词组合器：为每个任务拼装完整提示词"""

    def __init__(self, llm: LLMClient):
        self.llm = llm

    async def compose(
        self,
        task: AtomTask,
        host_profile: HostProfile | None,
        query: str,
    ) -> str:
        """为单个任务组装完整提示词"""
        parts: list[str] = []

        # 1. Skill system prompt
        if task.matched_skill:
            skill = skill_registry.get(task.matched_skill)
            if skill:
                parts.append(skill.SYSTEM_PROMPT.strip())

        # 2. 宿主上下文（脱敏版）
        if host_profile:
            host_ctx = EgressAnonymizer.anonymize_host_context(host_profile)
            parts.append(f"\n[宿主信息]\n境界: {host_ctx['realm']} {host_ctx['realm_stage']}")
            if host_ctx["interests"]:
                parts.append(f"兴趣: {', '.join(host_ctx['interests'])}")

        # 3. Skill 相关知识
        if task.matched_skill:
            skill = skill_registry.get(task.matched_skill)
            if skill:
                knowledge = skill.get_knowledge_context(query)
                if knowledge:
                    parts.append(knowledge)

        # 4. 安全约束
        parts.append("\n[安全约束]")
        parts.append("- 不要输出任何 PII（个人身份信息）")
        parts.append("- 不要执行危险命令（rm -rf /, fork bomb 等）")
        parts.append("- 文件操作限制在当前工作目录")
        if os.name == "nt":
            parts.append("- 如需生成命令，使用 Windows CMD 兼容语法（shell_exec 在 win32 下为 cmd /c）")
        else:
            parts.append("- 如需生成命令，使用 POSIX shell 兼容语法")

        # 5. 用户原始问题
        parts.append(f"\n[用户问题]\n{task.description}")

        return "\n\n".join(parts)


# ============================================================
# 规划生成器（聚合入口）
# ============================================================

class PlanGenerator:
    """天机推演域聚合入口"""

    def __init__(self, llm: LLMClient):
        self.llm = llm
        self.classifier = IntentClassifier(llm)
        self.decomposer = TaskDecomposer(llm)
        self.matcher = SkillMatcher()
        self.composer = PromptComposer(llm)

    async def generate_plan(
        self,
        query: str,
        host_profile: HostProfile | None = None,
    ) -> TaskPlan:
        """完整的天机推演流程（含 Skill 自动触发）"""
        # 1. 意图分类
        intent = await self.classifier.classify(query)
        complexity = TaskComplexity(intent.get("complexity", "simple_qa"))

        # 2. 任务拆解
        tasks, execution_order = await self.decomposer.decompose(query, complexity.value)

        # 3. Skill 自动触发评估 (superpowers 概念)
        realm = host_profile.realm if host_profile else RealmLevel.MORTAL
        domain_tags = DomainTagger.tag(query)
        task_type = DomainTagger.infer_task_type(query)
        is_multi = len(tasks) > 1

        trigger_ctx = TriggerContext(
            query_text=query,
            task_type=task_type,
            context_state=ContextState.CLEAN_SLATE,
            domain_tags=domain_tags,
            realm=realm,
            task_count=len(tasks),
            is_parallel=any(len(level) > 1 for level in execution_order),
        )
        trigger_result = trigger_engine.evaluate(trigger_ctx)
        mandatory_skills = [s[0] for s in trigger_result.mandatory_skills]
        recommended_skills = [s[0] for s in trigger_result.recommended_skills]

        logger.info(
            f"Skill 触发: 强制={mandatory_skills}, 推荐={recommended_skills}"
        )

        # 4. Skill 匹配（强制触发的 Skill 优先匹配）
        await self.matcher.match_all(tasks)

        # 对于没有匹配到 skill 的任务，尝试用强制触发的 skill 补充
        for task in tasks:
            if task.matched_skill is None and mandatory_skills:
                task.matched_skill = mandatory_skills[0]
            if task.dispatch_mode == "sync" and is_multi and not task.depends_on:
                # 只有无依赖的任务才自动切换为异步
                task.dispatch_mode = "async"

        # 5. 提示词组合
        for task in tasks:
            task.prompt = await self.composer.compose(task, host_profile, query)

        # 6. 构建 TaskPlan
        plan = TaskPlan(
            original_query=query,
            complexity=complexity,
            tasks=tasks,
            execution_order=execution_order,
            estimated_duration_ms=len(tasks) * 5000,
            host_context_snapshot=(
                EgressAnonymizer.anonymize_host_context(host_profile)
                if host_profile else {}
            ),
        )

        return plan
