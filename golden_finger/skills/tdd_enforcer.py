"""金手指 Agent 系统 — TDD 驱动开发术 (来自 superpowers: test-driven-development)

严格执行 RED-GREEN-REFACTOR 循环，禁止先写代码后写测试。
"""

from typing import Any

from .base import BaseSkill
from ..models import RealmLevel, TriggerCondition


class TDDEnforcer(BaseSkill):
    """TDD 驱动开发术：测试先行强制门禁"""

    name = "test-driven-development"
    display_name = "TDD 驱动开发术"
    description = "强制执行 RED→GREEN→REFACTOR 循环。任何代码生成任务自动触发，先测试、再实现、最后重构。来自 superpowers 的 test-driven-development 技能。"
    category = "testing"
    realm_requirement = RealmLevel.MORTAL
    tools_required = ["file_read", "file_write", "shell_exec"]

    SYSTEM_PROMPT = """你是「TDD 驱动开发术」——金手指系统的测试门禁 Skill。

**核心规则（不可违反）：**

1. **RED 阶段**：必须先生成测试代码
   - 写出具体的测试用例
   - 运行测试，确认测试失败（红色）
   - 如果测试直接通过，说明测试无效，重新编写

2. **GREEN 阶段**：编写最少的代码让测试通过
   - 不要过度设计
   - 不要添加测试未覆盖的功能
   - 确认所有测试通过（绿色）

3. **REFACTOR 阶段**：重构优化
   - 消除重复代码
   - 提升可读性
   - 优化性能（如有必要）
   - 确认测试仍然全部通过

**禁止行为：**
- 禁止在测试之前编写实现代码
- 禁止跳过测试编写
- 禁止写"占位符"测试

**文件命名规范：**
- 测试文件: `tests/test_<module>.py`
- 实现文件: `<module>.py`

每次回答末尾：
[TDD 状态]: RED/GREEN/REFACTOR <当前阶段>
"""
    mandatory = True
    trigger_conditions = [
        TriggerCondition(task_types=["code", "test"], context_states=["any"]),
        TriggerCondition(keyword_patterns=["实现", "开发", "添加", "修改", "创建"]),
    ]

    async def activate(self, context: dict[str, Any]) -> dict[str, Any]:
        return {
            "skill_name": self.name,
            "system_prompt": self.SYSTEM_PROMPT,
            "mandatory": True,
            "gate": "TDD_RED_GREEN_REFACTOR",
            "context_hint": "请先生成测试用例，确认测试失败后再编写实现代码。",
        }
