"""金手指 Agent 系统 — 系统调试术 (来自 superpowers: systematic-debugging)

4 阶段根因分析：复现 → 定位 → 修复 → 验证。
"""

from typing import Any

from .base import BaseSkill
from ..models import RealmLevel, TriggerCondition


class SystematicDebugging(BaseSkill):
    """系统调试术：结构化根因分析"""

    name = "systematic-debugging"
    display_name = "系统调试术"
    description = "4 阶段根因分析法。错误发生时自动触发，强制走完复现→定位→修复→验证全流程，避免随机猜测。来自 superpowers 的 systematic-debugging 技能。"
    category = "debugging"
    realm_requirement = RealmLevel.MORTAL
    tools_required = ["file_read", "file_write", "shell_exec"]

    SYSTEM_PROMPT = """你是「系统调试术」——金手指系统的调试 Skill。

**4 阶段根因分析流程：**

**Phase 1: 复现 (Reproduce)**
- 首先确认错误现象
- 收集完整的错误信息和堆栈
- 记录复现步骤
- 确定错误是否稳定复现

**Phase 2: 定位 (Locate)**
- 从错误堆栈逐层追踪
- 使用 root-cause-tracing 技术
- 检查最近的代码变更
- 缩小问题范围到最小可复现单元
- **不要猜测，要验证**

**Phase 3: 修复 (Fix)**
- 编写失败的回归测试
- 实施修复
- 确认回归测试通过
- 使用 defense-in-depth 策略（多层防御）

**Phase 4: 验证 (Verify)**
- 确认原始问题已解决
- 检查修复是否引入新问题
- 确认所有相关测试通过
- 记录修复过程和根因

**规则：**
- 禁止盲目尝试（不理解的修复不可合入）
- 修复前必须复现
- 修复后必须有测试验证

每次回答末尾：
[调试阶段]: <Phase 1-4> | <进度描述>
"""
    mandatory = True
    trigger_conditions = [
        TriggerCondition(task_types=["debug"], context_states=["error"]),
        TriggerCondition(keyword_patterns=["报错", "错误", "bug", "fix", "修", "失败", "异常"]),
    ]

    async def activate(self, context: dict[str, Any]) -> dict[str, Any]:
        return {
            "skill_name": self.name,
            "system_prompt": self.SYSTEM_PROMPT,
            "mandatory": True,
            "gate": "DEBUG_4_PHASE",
            "context_hint": "请先复现错误，收集完整的错误信息，再进行分析和修复。",
        }
