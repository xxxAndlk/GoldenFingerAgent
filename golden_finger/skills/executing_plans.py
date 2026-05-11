"""金手指 Agent 系统 — 计划执行术 (来自 superpowers: executing-plans)

按计划批量执行任务，设置人工检查点。
"""

from typing import Any

from .base import BaseSkill
from ..models import RealmLevel, TriggerCondition


class ExecutingPlans(BaseSkill):
    """计划执行术：批量执行 + 检查点"""

    name = "executing-plans"
    display_name = "计划执行术"
    description = "按 plan.md 批量执行任务，每个检查点暂停等待人工确认。来自 superpowers 的 executing-plans 技能。"
    category = "collaboration"
    realm_requirement = RealmLevel.MORTAL
    tools_required = ["file_read", "file_write", "shell_exec"]

    SYSTEM_PROMPT = """你是「计划执行术」——金手指系统的批量执行 Skill。

**执行规则：**

1. **按依赖顺序执行**
   - 遵循 tasks.md 中定义的任务顺序
   - 依赖未完成的绝不开始

2. **检查点机制**
   - 每个用户故事完成后暂停
   - 展示完成情况并等待确认
   - 用户确认后继续下一阶段

3. **并行任务处理**
   - 标记 [P] 的任务可并行执行
   - 使用 SubagentDispatcher 分派子代理

4. **失败处理**
   - 任务失败时立即暂停
   - 展示错误并询问如何处理（跳过/重试/终止）
   - 记录失败原因用于改进

5. **进度追踪**
   - 始终显示当前进度（已完成/总数）
   - 记录每个任务的执行时间

每次回答末尾：[执行进度]: <已完成>/<总数> | <当前任务>
"""
    mandatory = False
    trigger_conditions = [
        TriggerCondition(task_types=["code"], context_states=["clean_slate"]),
        TriggerCondition(keyword_patterns=["按计划", "执行", "开始实现"]),
    ]

    async def activate(self, context: dict[str, Any]) -> dict[str, Any]:
        return {
            "skill_name": self.name,
            "system_prompt": self.SYSTEM_PROMPT,
            "mandatory": False,
            "context_hint": "建议按计划批量执行，每个检查点暂停确认。",
        }
