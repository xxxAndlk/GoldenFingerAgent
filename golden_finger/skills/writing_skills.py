"""金手指 Agent 系统 — 技能编写术 (来自 superpowers: writing-skills)

创建和修改 Skill 的最佳实践指南。
"""

from typing import Any

from .base import BaseSkill
from ..models import RealmLevel, TriggerCondition


class WritingSkills(BaseSkill):
    """技能编写术：创建新技能的元技能"""

    name = "writing-skills"
    display_name = "技能编写术"
    description = "创建新 Skill 的元技能。当 GapAnalyzer 发现技能缺口时推荐使用，提供 Skill 编写的最佳实践。来自 superpowers 的 writing-skills 技能。"
    category = "meta"
    realm_requirement = RealmLevel.QI_REFINING
    tools_required = ["file_read", "file_write"]

    SYSTEM_PROMPT = (
        "你是「技能编写术」——金手指系统的元 Skill。\n"
        "\n"
        "**Skill 编写规范：**\n"
        "\n"
        "1. **命名规范**\n"
        "   - name: 英文小写，下划线分隔，如 `test-driven-development`\n"
        "   - display_name: 中文 4-6 字，以'术'结尾，如 `TDD 驱动开发术`\n"
        "   - 文件放在 `golden_finger/skills/` 目录，与 name 对应\n"
        "\n"
        "2. **必需属性**\n"
        "   - name, display_name, description: 清晰描述 Skill 功能\n"
        "   - category: testing / debugging / collaboration / meta / domain\n"
        "   - realm_requirement: 最低境界要求\n"
        "   - tools_required: 依赖的工具列表\n"
        "   - SYSTEM_PROMPT: 注入到 LLM 的行为指令\n"
        "\n"
        "3. **触发条件设计**\n"
        "   - task_types: 什么类型的任务触发\n"
        "   - context_states: 什么上下文状态触发\n"
        "   - keyword_patterns: 用户输入中匹配哪些关键词\n"
        "   - 触发条件要精确，避免误触发\n"
        "\n"
        "4. **SYSTEM_PROMPT 编写要点**\n"
        "   - 清晰的角色定义\n"
        "   - 具体的执行步骤\n"
        "   - 明确的禁止行为\n"
        "   - 每轮回复末尾的状态标记\n"
        "\n"
        "5. **激活逻辑**\n"
        "   - activate() 方法检查前置条件\n"
        "   - 条件不满足时返回 skip=True\n"
        "   - 返回的 context_hint 作为用户可见的触发说明\n"
        "\n"
        "每次回答末尾：[技能状态]: <SKILL_CREATED/SKILL_UPDATED>\n"
    )
    mandatory = False
    trigger_conditions = [
        TriggerCondition(
            task_types=["plan"],
            keyword_patterns=["新技能", "创建技能", "skill", "自动化", "工作流"],
        ),
    ]

    async def activate(self, context: dict[str, Any]) -> dict[str, Any]:
        return {
            "skill_name": self.name,
            "system_prompt": self.SYSTEM_PROMPT,
            "mandatory": False,
            "context_hint": "创建新技能时请遵循 Skill 编写规范。",
        }
