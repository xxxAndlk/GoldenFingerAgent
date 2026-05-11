"""金手指 Agent 系统 — 代码审查响应术 (来自 superpowers: receiving-code-review)

处理审查反馈的循环流程，逐条响应审查意见。
"""

from typing import Any

from .base import BaseSkill
from ..models import RealmLevel, TriggerCondition


class ReceivingCodeReview(BaseSkill):
    """代码审查响应术：反馈处理循环"""

    name = "receiving-code-review"
    display_name = "代码审查响应术"
    description = "收到审查意见后的处理流程。逐条分析反馈、实施修改、确认修改已被接受。来自 superpowers 的 receiving-code-review 技能。"
    category = "collaboration"
    realm_requirement = RealmLevel.MORTAL
    tools_required = ["file_read", "file_write", "shell_exec"]

    SYSTEM_PROMPT = """你是「代码审查响应术」——金手指系统的审查反馈处理 Skill。

**处理流程：**

1. **理解反馈**
   - 逐条阅读审查意见
   - 确认理解了修改意图
   - 对不理解的内容请求澄清

2. **分类处理**
   - 🔴 Critical: 立即修复
   - 🟡 Major: 评估影响后修复
   - 🔵 Minor: 快速调整

3. **实施修改**
   - 按优先级从高到低处理
   - 每次修改后运行测试
   - 保持变更原子化

4. **确认修复**
   - 回复每一条审查意见
   - 说明修改内容或解释不修改的理由
   - 请求重新审查

**规则：**
- 不要防御性回复（审查是协作不是攻击）
- 不理解就问，不要假装理解
- 每次修改都运行测试确保不引入回归

每次回答末尾：[审查响应]: <已处理>/<总意见> | <待处理>
"""
    mandatory = True
    trigger_conditions = [
        TriggerCondition(task_types=["review"]),
        TriggerCondition(keyword_patterns=["审查意见", "review", "修改建议"]),
    ]

    async def activate(self, context: dict[str, Any]) -> dict[str, Any]:
        return {
            "skill_name": self.name,
            "system_prompt": self.SYSTEM_PROMPT,
            "mandatory": True,
            "gate": "REVIEW_RESPONSE",
            "context_hint": "请逐条处理审查意见，按优先级修复。",
        }
