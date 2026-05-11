"""金手指 Agent 系统 — 头脑风暴术 (来自 superpowers: brainstorming)

苏格拉底式需求澄清，任何编码前自动触发。
"""

from typing import Any

from .base import BaseSkill
from ..models import RealmLevel, TriggerCondition


class Brainstorming(BaseSkill):
    """头脑风暴术：编码前的需求澄清"""

    name = "brainstorming"
    display_name = "头脑风暴术"
    description = "苏格拉底式需求澄清。任何代码编写之前自动触发，通过提问帮助用户明确需求、探索替代方案，产出设计文档。来自 superpowers 的 brainstorming 技能。"
    category = "collaboration"
    realm_requirement = RealmLevel.MORTAL
    tools_required = ["file_write"]

    SYSTEM_PROMPT = """你是「头脑风暴术」——金手指系统的需求澄清 Skill。

**你的职责：在编写任何代码之前，帮助用户明确需求。**

**工作流：**

1. **理解意图**
   - 用户真正想达成什么？
   - 当前面临什么约束？
   - 期望的结果是什么样？

2. **探索方案**
   - 提出 2-3 种可行的解决方案
   - 对比各方案的优缺点
   - 评估复杂度和风险

3. **明确范围**
   - 确定核心功能和边界
   - 明确不做什么（避免范围蔓延）
   - 确定验收条件

4. **产出设计摘要**
   - 将讨论结果保存为设计文档
   - 包含选定方案和关键决策理由

**规则：**
- 用提问引导，不要直接给出方案
- 每次最多提 3 个问题
- 确认用户认可后再进入下一步

每次回答末尾：[头脑风暴]: <当前阶段>
"""
    mandatory = True
    trigger_conditions = [
        TriggerCondition(task_types=["plan"], context_states=["clean_slate"]),
        TriggerCondition(keyword_patterns=["设计", "方案", "架构", "怎么", "如何", "重构"]),
    ]

    async def activate(self, context: dict[str, Any]) -> dict[str, Any]:
        return {
            "skill_name": self.name,
            "system_prompt": self.SYSTEM_PROMPT,
            "mandatory": True,
            "gate": "BRAINSTORMING_REQUIRED",
            "context_hint": "请在编码前先澄清需求，探索替代方案。",
        }
