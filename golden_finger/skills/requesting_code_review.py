"""金手指 Agent 系统 — 代码审查请求术 (来自 superpowers: requesting-code-review)

实现完成后按计划进行自我审查，严重问题分级。
"""

from typing import Any

from .base import BaseSkill
from ..models import RealmLevel, TriggerCondition


class RequestingCodeReview(BaseSkill):
    """代码审查请求术：实现后的自审查"""

    name = "requesting-code-review"
    display_name = "代码审查请求术"
    description = "实现完成后自动进行自我审查。对照 plan.md 检查规格合规性，按严重程度分级问题，严重问题阻断进度。来自 superpowers 的 requesting-code-review 技能。"
    category = "collaboration"
    realm_requirement = RealmLevel.MORTAL
    tools_required = ["file_read", "file_write"]

    SYSTEM_PROMPT = """你是「代码审查请求术」——金手指系统的代码审查 Skill。

**审查流程：**

1. **规格合规审查**
   - 逐项对照 spec.md 中的验收条件
   - 检查是否有遗漏的功能点
   - 验证非功能需求是否满足

2. **代码质量审查**
   - 代码可读性（命名、注释、结构）
   - 错误处理是否完善
   - 性能是否有明显问题
   - 安全是否有隐患

3. **测试审查**
   - 测试覆盖是否充分
   - 边界情况是否覆盖
   - 测试是否真正有意义（非占位符）

**问题分级：**

| 级别 | 定义 | 行动 |
|------|------|------|
| 🔴 Critical | 安全问题、功能缺失、测试失败 | **必须修复**，阻断合入 |
| 🟡 Major | 性能问题、代码异味、缺乏测试 | 应在合入前修复 |
| 🔵 Minor | 命名建议、风格问题、文档补充 | 建议修复，不阻断 |

**审查输出：**
```markdown
## 审查报告
- 审查范围: <文件列表>
- 总体评价: PASS / NEEDS_FIX / REJECT
- 🔴 Critical: N 个
- 🟡 Major: N 个
- 🔵 Minor: N 个
```

每次回答末尾：[审查结论]: PASS/NEEDS_FIX/REJECT
"""
    mandatory = True
    trigger_conditions = [
        TriggerCondition(context_states=["after_implementation"]),
    ]

    async def activate(self, context: dict[str, Any]) -> dict[str, Any]:
        return {
            "skill_name": self.name,
            "system_prompt": self.SYSTEM_PROMPT,
            "mandatory": True,
            "gate": "CODE_REVIEW_REQUIRED",
            "context_hint": "实现已完成，请进行自我审查（对照 spec 检查规格合规性和代码质量）。",
        }
