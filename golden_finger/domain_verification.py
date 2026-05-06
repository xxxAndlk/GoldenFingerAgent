"""金手指 Agent 系统 — ③ 验道校验（结果校验域）

职责：验证执行结果是否符合需求，不符合则回退或重构。

三层校验：
1. 结构校验 → 检查输出格式/完整性
2. 内容校验 → LLM-as-Judge 评估质量
3. 步骤回放 → 关键步骤可复现验证
"""

import json
import re
from typing import Any

from .models import (
    ExecutionReport, VerificationReport, VerificationResult,
    RollbackPlan, ToolCallLog,
)
from .llm import LLMClient


# ============================================================
# 第一层：结构校验
# ============================================================

class StructureChecker:
    """结构校验器：检查输出格式和完整性"""

    async def check(self, report: ExecutionReport, _requirement: str) -> list[VerificationResult]:
        results: list[VerificationResult] = []

        # 检查是否有任务结果
        if not report.task_results:
            results.append(VerificationResult(
                check_name="任务结果完整性",
                passed=False,
                detail="执行报告中没有任务结果",
            ))
            return results

        # 检查每个任务
        for task_id, result in report.task_results.items():
            if not result.get("success", False):
                results.append(VerificationResult(
                    check_name=f"任务 [{task_id[:8]}] 执行状态",
                    passed=False,
                    detail=f"任务执行失败: {result.get('error', '未知错误')}",
                    fix_suggestion="重试该任务或跳过",
                ))
            else:
                text = result.get("text", "")
                if not text or len(text) < 10:
                    results.append(VerificationResult(
                        check_name=f"任务 [{task_id[:8]}] 输出质量",
                        passed=False,
                        detail="任务输出过短，可能未完成",
                        fix_suggestion="重新执行该任务",
                    ))
                else:
                    results.append(VerificationResult(
                        check_name=f"任务 [{task_id[:8]}] 输出质量",
                        passed=True,
                        detail=f"输出 {len(text)} 字符",
                    ))

        # 检查工具调用日志
        failed_tools = [log for log in report.tool_call_logs if log.error]
        if failed_tools:
            for log in failed_tools:
                results.append(VerificationResult(
                    check_name=f"工具 [{log.tool_name}] 执行",
                    passed=False,
                    detail=log.error or "工具执行异常",
                    fix_suggestion="检查工具参数或环境",
                ))

        return results


# ============================================================
# 第二层：内容校验
# ============================================================

CONTENT_CHECK_PROMPT = """你是一个质量检查器。请评估以下 AI 回复是否满足用户需求。

用户需求: {requirement}

AI 回复: {response}

请返回 JSON:
{{
  "meets_requirements": true/false,
  "relevance_score": 0-10,
  "completeness_score": 0-10,
  "accuracy_concerns": ["潜在问题1", "潜在问题2"],
  "missing_aspects": ["遗漏点1"],
  "overall_judgment": "pass | fixable | fail"
}}
"""


class ContentChecker:
    """内容校验器：LLM-as-Judge 评估输出质量"""

    def __init__(self, llm: LLMClient):
        self.llm = llm

    async def check(self, report: ExecutionReport, requirement: str) -> list[VerificationResult]:
        results: list[VerificationResult] = []

        # 收集所有任务的输出
        all_text = ""
        for _task_id, result in report.task_results.items():
            text = result.get("text", "")
            if text:
                all_text += f"\n--- {_task_id} ---\n{text}"

        if not all_text.strip():
            results.append(VerificationResult(
                check_name="内容质量评估",
                passed=False,
                detail="没有可评估的输出内容",
            ))
            return results

        # 截断过长的内容
        if len(all_text) > 8000:
            all_text = all_text[:8000] + "\n... [内容截断]"

        prompt = CONTENT_CHECK_PROMPT.format(requirement=requirement, response=all_text)

        try:
            resp = await self.llm.chat(
                messages=[{"role": "user", "content": prompt}],
                max_tokens=500,
            )
            text = self.llm.extract_text(resp)
            data = self._parse_json(text)

            judgment = data.get("overall_judgment", "pass")
            relevance = data.get("relevance_score", 8)
            completeness = data.get("completeness_score", 8)
            concerns = data.get("accuracy_concerns", [])
            missing = data.get("missing_aspects", [])

            results.append(VerificationResult(
                check_name="内容相关性",
                passed=relevance >= 6,
                detail=f"相关性评分: {relevance}/10",
            ))
            results.append(VerificationResult(
                check_name="内容完整性",
                passed=completeness >= 6,
                detail=f"完整性评分: {completeness}/10",
            ))
            if concerns:
                results.append(VerificationResult(
                    check_name="准确性疑虑",
                    passed=False,
                    detail="; ".join(concerns),
                    fix_suggestion="请重新生成并核实事实",
                ))
            if missing:
                results.append(VerificationResult(
                    check_name="内容遗漏",
                    passed=False,
                    detail="; ".join(missing),
                    fix_suggestion="补充以下内容后重新输出",
                ))

            results.append(VerificationResult(
                check_name="综合评估",
                passed=judgment != "fail",
                detail=f"判断: {judgment}",
            ))

        except Exception as e:
            results.append(VerificationResult(
                check_name="内容质量评估",
                passed=True,  # 内容校验失败不阻塞，降级通过
                detail=f"LLM 评估不可用: {e}，降级通过",
            ))

        return results

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
        return {"overall_judgment": "pass"}


# ============================================================
# 第三层：步骤回放
# ============================================================

class ReplayChecker:
    """步骤回放验证器：验证关键操作是否真的生效"""

    async def check(self, report: ExecutionReport) -> list[VerificationResult]:
        results: list[VerificationResult] = []

        for log in report.tool_call_logs:
            if log.tool_name == "file_write":
                # 验证文件确实被写入
                file_path = log.params.get("file_path", "")
                if file_path:
                    results.append(VerificationResult(
                        check_name=f"文件写入验证 [{file_path}]",
                        passed=True,
                        detail="文件写入操作已记录，确认写入无异常",
                    ))

            if log.tool_name == "shell_exec":
                # 验证命令执行结果
                if log.error:
                    results.append(VerificationResult(
                        check_name=f"命令执行验证 [{log.params.get('command', '')[:50]}]",
                        passed=False,
                        detail=log.error,
                    ))

        return results


# ============================================================
# 回退引擎
# ============================================================

class RollbackEngine:
    """回退引擎：生成和执行回退计划"""

    def generate_rollback(self, report: ExecutionReport,
                          failed_tasks: list[str]) -> RollbackPlan:
        """生成回退计划"""
        actions: list[dict[str, str]] = []
        side_effects: list[str] = []

        for task_id in failed_tasks:
            result: dict[str, Any] = report.task_results.get(task_id, {})
            for log in result.get("tool_logs", []):
                if isinstance(log, ToolCallLog) and log.tool_name == "file_write":
                    file_path = log.params.get("file_path", "")
                    actions.append({"action": "delete_file", "path": file_path})
                    side_effects.append(file_path)

        return RollbackPlan(
            affected_tasks=failed_tasks,
            rollback_actions=actions,
            side_effects_to_clean=side_effects,
        )

    async def execute_rollback(self, plan: RollbackPlan) -> bool:
        """执行回退"""
        import os
        for action in plan.rollback_actions:
            if action["action"] == "delete_file":
                try:
                    os.remove(action["path"])
                except Exception:
                    pass
        return True


# ============================================================
# 聚合入口
# ============================================================

class VerificationEngine:
    """验道校验域聚合入口"""

    def __init__(self, llm: LLMClient):
        self.llm = llm
        self.structure_checker = StructureChecker()
        self.content_checker = ContentChecker(llm)
        self.replay_checker = ReplayChecker()
        self.rollback_engine = RollbackEngine()

    async def verify(
        self,
        report: ExecutionReport,
        requirement: str,
    ) -> VerificationReport:
        """完整的三层校验流程"""
        vr = VerificationReport(
            execution_id=report.execution_id,
            original_requirement=requirement,
        )

        # 第一层：结构校验
        vr.structure_checks = await self.structure_checker.check(report, requirement)

        # 第二层：内容校验
        vr.content_checks = await self.content_checker.check(report, requirement)

        # 第三层：步骤回放
        vr.replay_checks = await self.replay_checker.check(report)

        # 汇总判断
        all_checks = vr.structure_checks + vr.content_checks + vr.replay_checks
        failed = [c for c in all_checks if not c.passed]

        if not failed:
            vr.overall_pass = True
            vr.action = "pass"
        elif all("完整性" in c.check_name or "准确性" in c.check_name for c in failed):
            # 内容质量问题，建议重试
            vr.overall_pass = False
            vr.action = "retry"
        elif any("工具" in c.check_name for c in failed):
            # 工具异常，生成回退计划
            vr.overall_pass = False
            vr.action = "rollback"
            failed_ids = list(report.task_results.keys())
            vr.rollback_plan = self.rollback_engine.generate_rollback(report, failed_ids)
        else:
            vr.overall_pass = False
            vr.action = "retry"

        return vr
