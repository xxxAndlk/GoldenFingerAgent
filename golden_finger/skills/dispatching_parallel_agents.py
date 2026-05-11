"""金手指 Agent 系统 — 并行子代理调度术 (来自 superpowers: dispatching-parallel-agents)

将多个独立任务并发分派给子代理执行，两阶段审查（规格合规 + 代码质量）。
"""

from typing import Any

from .base import BaseSkill
from ..models import RealmLevel, TriggerCondition


class DispatchingParallelAgents(BaseSkill):
    """并行子代理调度术：并发执行 + 两阶段审查"""

    name = "dispatching-parallel-agents"
    display_name = "并行子代理调度术"
    description = "多任务场景自动启用。将独立任务并发分派给子代理，每个子代理执行完进行两阶段审查（规格合规 + 代码质量）。来自 superpowers 的 dispatching-parallel-agents + subagent-driven-development 技能。"
    category = "collaboration"
    realm_requirement = RealmLevel.QI_REFINING
    tools_required = ["file_read", "file_write", "shell_exec"]

    SYSTEM_PROMPT = """你是「并行子代理调度术」——金手指系统的并行执行 Skill。

**调度规则：**

1. **识别并行机会**
   - 分析 tasks.md 中标记 [P] 的任务
   - 确认任务之间无数据依赖
   - 每批并行任务数不超过 5 个

2. **子代理分派**
   - 每个独立任务创建一个 ephemeral 子代理
   - 子代理只获得任务相关的最小上下文
   - 使用 AgentMailbox 进行代理间通信

3. **两阶段审查**
   - **第一阶段：规格合规检查**
     - 输出是否符合 spec.md 的要求？
     - 是否遗漏验收条件？
   - **第二阶段：代码质量检查**
     - 代码风格是否符合规范？
     - 是否有安全隐患？
     - 测试是否充分？

4. **结果汇聚**
   - 所有子代理完成后统一聚合结果
   - 处理代理间的冲突（如修改同一文件）
   - 生成合并报告

5. **失败隔离**
   - 一个子代理失败不影响其他
   - 失败任务自动标记为需要人工介入

每次回答末尾：[子代理状态]: <运行中>/<总数> | <成功>/<失败>
"""
    mandatory = True
    trigger_conditions = [
        TriggerCondition(context_states=["multi_task"]),
    ]

    async def activate(self, context: dict[str, Any]) -> dict[str, Any]:
        task_count = context.get("task_count", 1)
        if task_count <= 1:
            return {"skill_name": self.name, "skip": True, "reason": "单任务无需子代理调度"}
        return {
            "skill_name": self.name,
            "system_prompt": self.SYSTEM_PROMPT,
            "mandatory": True,
            "gate": "PARALLEL_DISPATCH",
            "context_hint": f"检测到 {task_count} 个任务，建议启用并行子代理调度。",
        }
