"""金手指 Agent 系统 — ④ 刻碑沉淀（持久进化域）

职责：将执行经验转化为可复用的 Skill 知识，让系统越用越强。

流程：
1. 执行摘要生成
2. Skill 缺口分析
3. Skill 更新 / 新建
4. 宿主画像更新
5. 知识图谱更新
"""

import json
import re
from typing import Any

from .models import (
    ExecutionReport, VerificationReport, TaskPlan,
    ExecutionSummary, GapReport, SkillManifest,
    KnowledgeEntry, HostProfile, now_iso,
)
from .llm import LLMClient
from .skills.registry import skill_registry
from .storage.vector_store import vector_store
from .storage.sqlite_store import SQLiteStore


# ============================================================
# 执行摘要生成
# ============================================================

SUMMARY_PROMPT = """请总结以下 AI 任务的执行过程。返回 JSON：

{
  "what_was_done": "完成了什么（一句话）",
  "how_it_was_done": "怎么做的（关键步骤，2-4句）",
  "problems_encountered": "遇到了什么问题",
  "solutions_found": "如何解决的",
  "success_pattern": "可复用的成功模式",
  "pitfalls": "需要避免的坑",
  "prompt_improvement": "提示词改进建议（如有）",
  "tools_used": ["工具1", "工具2"],
  "new_skills_needed": ["需要的但不存在的新Skill名称"]
}

用户需求: {query}

执行结果:
{results}
"""


class ExecutionSummarizer:
    """执行摘要生成器"""

    def __init__(self, llm: LLMClient):
        self.llm = llm

    async def summarize(
        self,
        report: ExecutionReport,
        query: str,
    ) -> ExecutionSummary:
        """生成执行摘要"""
        # 收集任务结果文本
        results_text = ""
        for _task_id, result in report.task_results.items():
            text = result.get("text", "")
            if text:
                results_text += f"\n[{_task_id}]: {text[:500]}"

        if not results_text.strip():
            # 无有效输出时生成基础摘要
            return ExecutionSummary(
                execution_id=report.execution_id,
                original_query=query,
                what_was_done=f"处理了: {query[:100]}",
                tools_used=[log.tool_name for log in report.tool_call_logs],
            )

        prompt = SUMMARY_PROMPT.format(query=query, results=results_text[:3000])

        try:
            resp = await self.llm.chat(
                messages=[{"role": "user", "content": prompt}],
                max_tokens=800,
            )
            text = self.llm.extract_text(resp)
            data = self._parse_json(text)

            return ExecutionSummary(
                execution_id=report.execution_id,
                original_query=query,
                what_was_done=data.get("what_was_done", ""),
                how_it_was_done=data.get("how_it_was_done", ""),
                problems_encountered=data.get("problems_encountered", ""),
                solutions_found=data.get("solutions_found", ""),
                success_pattern=data.get("success_pattern", ""),
                pitfalls=data.get("pitfalls", ""),
                prompt_improvement=data.get("prompt_improvement", ""),
                tools_used=data.get("tools_used", []),
                new_skills_needed=data.get("new_skills_needed", []),
            )
        except Exception:
            return ExecutionSummary(
                execution_id=report.execution_id,
                original_query=query,
                what_was_done=f"处理了: {query[:100]}",
                tools_used=[log.tool_name for log in report.tool_call_logs],
            )

    @staticmethod
    def _parse_json(text: str) -> dict[str, Any]:
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


# ============================================================
# Skill 缺口分析
# ============================================================

class GapAnalyzer:
    """Skill 缺口分析器"""

    def analyze(
        self,
        plan: TaskPlan,
        _report: ExecutionReport,
        summary: ExecutionSummary,
    ) -> GapReport:
        """分析 Skill 缺口"""
        missing: list[dict[str, str]] = []
        deficient: list[dict[str, str]] = []

        # 检查未匹配到 Skill 的任务
        for task in plan.tasks:
            if task.is_new_skill_needed:
                missing.append({
                    "task_description": task.description,
                    "suggested_skill_name": self._infer_skill_name(task.description),
                })

        # 检查执行中多次失败的工具调用
        failed_tools = [log for log in _report.tool_call_logs if log.error]
        if failed_tools:
            for task in plan.tasks:
                if task.matched_skill:
                    deficient.append({
                        "skill_name": task.matched_skill,
                        "issue": "工具调用存在失败",
                        "suggestion": "需补充工具使用的知识条目",
                    })

        suggestions: list[str] = []
        for m in missing:
            suggestions.append(f"建议新建 Skill: {m['suggested_skill_name']} 用于 '{m['task_description']}'")
        for d in deficient:
            suggestions.append(f"建议优化 Skill [{d['skill_name']}]: {d['suggestion']}")

        return GapReport(
            missing_skills=missing,
            deficient_skills=deficient,
            suggestions=suggestions,
        )

    @staticmethod
    def _infer_skill_name(description: str) -> str:
        """从描述推断 Skill 名称"""
        keywords_map = {
            "搜索": "web_search_skill",
            "文件": "file_management",
            "图片": "image_processing",
            "视频": "video_processing",
            "邮件": "email_automation",
            "日程": "calendar_management",
            "翻译": "translation_skill",
            "数据分析": "data_analysis",
            "报告": "report_generation",
        }
        for kw, name in keywords_map.items():
            if kw in description:
                return name
        return "custom_skill"


# ============================================================
# Skill 更新 / 创建
# ============================================================

class SkillUpdater:
    """Skill 更新器"""

    def update_existing_skill(self, skill_name: str, summary: ExecutionSummary):
        """更新已有 Skill 的知识"""
        skill = skill_registry.get(skill_name)
        if not skill:
            return

        # 添加知识条目
        entry = KnowledgeEntry(
            source_execution=summary.execution_id,
            context=summary.what_was_done,
            pattern=summary.success_pattern,
            pitfalls=summary.pitfalls,
            prompt_template=summary.prompt_improvement,
        )
        skill.add_knowledge(entry)

        # 更新向量库
        if summary.success_pattern:
            vector_store.add_skill_knowledge(skill_name, [{
                "content": f"成功模式: {summary.success_pattern}\n避免: {summary.pitfalls}",
                "metadata": {
                    "source_execution": summary.execution_id,
                    "type": "execution_learning",
                }
            }])

        # 更新 Skill 统计
        skill.manifest.stats.total_uses += 1
        if not summary.problems_encountered:
            skill.manifest.stats.success_count += 1

    def create_new_skill_from_summary(
        self,
        summary: ExecutionSummary,
        gap: dict[str, str],
    ) -> SkillManifest | None:
        """从执行摘要创建新 Skill"""
        skill_name = gap.get("suggested_skill_name", "custom_skill")
        task_desc = gap.get("task_description", "")

        manifest = SkillManifest(
            name=skill_name,
            display_name=skill_name.replace("_", " ").title(),
            description=task_desc,
            category="utility",
            version="0.1.0",
            created_from=summary.execution_id,
            knowledge=[
                KnowledgeEntry(
                    source_execution=summary.execution_id,
                    context=task_desc,
                    pattern=summary.success_pattern,
                    pitfalls=summary.pitfalls,
                )
            ],
        )

        # 初始化向量知识
        vector_store.add_skill_knowledge(skill_name, [{
            "content": f"Skill: {skill_name}\n用途: {task_desc}\n成功模式: {summary.success_pattern}",
            "metadata": {"skill_name": skill_name, "type": "skill_definition"}
        }])

        return manifest


# ============================================================
# 宿主画像更新
# ============================================================

class ProfileUpdater:
    """宿主画像更新器"""

    def __init__(self, store: SQLiteStore):
        self.store = store

    def update_profile(
        self,
        profile: HostProfile,
        summary: ExecutionSummary,
    ):
        """根据本次执行更新宿主画像"""
        # 累计修炼时间（按任务数和平均耗时估算）
        profile.total_cultivation_time += 1
        profile.total_tasks_completed += 1

        # 如果有新工具使用，略微提升相关灵根
        tools_used = summary.tools_used
        if "shell_exec" in tools_used or "code" in str(tools_used):
            profile.spirit_root.metal = min(100, profile.spirit_root.metal + 0.5)
        if "web_search" in tools_used:
            profile.spirit_root.water = min(100, profile.spirit_root.water + 0.3)
        if "file_write" in tools_used:
            profile.spirit_root.earth = min(100, profile.spirit_root.earth + 0.2)

        # 检查境界突破
        self._check_realm_breakthrough(profile)

        # 持久化
        self.store.save_host_profile(profile)

    def _check_realm_breakthrough(self, profile: HostProfile):
        """检查是否满足突破条件"""
        thresholds = {
            0: 5,    # 凡人 → 练气: 5个任务
            1: 20,   # 练气 → 筑基: 20个任务
            2: 50,   # 筑基 → 金丹: 50个任务
            3: 150,  # 金丹 → 元婴: 150个任务
            4: 400,  # 元婴 → 化神: 400个任务
        }

        current = profile.realm.value
        if current in thresholds and profile.total_tasks_completed >= thresholds[current]:
            if current < 8:  # 最高境界
                profile.realm = type(profile.realm)(current + 1)
                profile.realm_progress = 0
                profile.breakthrough_history.append({
                    "from_realm": current,
                    "to_realm": current + 1,
                    "at_tasks": profile.total_tasks_completed,
                    "timestamp": now_iso(),
                })


# ============================================================
# 聚合入口
# ============================================================

class PersistenceEngine:
    """刻碑沉淀域聚合入口"""

    def __init__(self, llm: LLMClient, store: SQLiteStore):
        self.llm = llm
        self.store = store
        self.summarizer = ExecutionSummarizer(llm)
        self.gap_analyzer = GapAnalyzer()
        self.skill_updater = SkillUpdater()
        self.profile_updater = ProfileUpdater(store)

    async def persist(
        self,
        plan: TaskPlan,
        report: ExecutionReport,
        verification: VerificationReport,
        host_profile: HostProfile,
    ) -> dict[str, Any]:
        """完整的刻碑沉淀流程"""

        # 1. 生成执行摘要
        summary = await self.summarizer.summarize(report, plan.original_query)

        # 2. Skill 缺口分析
        gaps = self.gap_analyzer.analyze(plan, report, summary)

        # 3. 更新已有 Skill
        for task in plan.tasks:
            if task.matched_skill:
                self.skill_updater.update_existing_skill(task.matched_skill, summary)

        # 4. 创建新 Skill（如果有缺口）
        new_skills: list[SkillManifest] = []
        for gap in gaps.missing_skills:
            manifest = self.skill_updater.create_new_skill_from_summary(summary, gap)
            if manifest:
                new_skills.append(manifest)
                self.store.save_skill_meta(manifest)

        # 5. 更新宿主画像
        self.profile_updater.update_profile(host_profile, summary)

        # 6. 持久化
        self.store.save_execution_report(report, plan.original_query)
        self.store.save_execution_summary(summary)
        self.store.log_event("persistence", "consolidation_complete",
                             f"摘要: {summary.what_was_done[:100]}")

        return {
            "summary": summary,
            "gaps": gaps,
            "new_skills": new_skills,
            "host_profile_updated": True,
        }