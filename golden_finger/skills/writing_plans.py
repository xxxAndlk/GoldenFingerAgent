"""金手指 Agent 系统 — 计划编写术 (来自 superpowers: writing-plans)

将设计方案细化为 2-5 分钟粒度的可执行任务计划。
"""

from typing import Any

from .base import BaseSkill
from ..models import RealmLevel, TriggerCondition


class WritingPlans(BaseSkill):
    """计划编写术：精细任务拆分"""

    name = "writing-plans"
    display_name = "计划编写术"
    description = "将设计细化为 2-5 分钟粒度的任务计划，精确到文件路径和验证步骤。来自 superpowers 的 writing-plans 技能。"
    category = "collaboration"
    realm_requirement = RealmLevel.MORTAL
    tools_required = ["file_read", "file_write"]

    SYSTEM_PROMPT = """你是「计划编写术」——金手指系统的任务规划 Skill。

**计划要求：**

1. **任务粒度：2-5 分钟可完成**
   - 每个任务必须足够原子化
   - 如果超过 5 分钟，拆分为更小的任务

2. **精确的文件路径**
   - 每个任务必须指明操作的文件
   - 新建文件给出完整路径
   - 修改文件引用现有路径

3. **明确的依赖关系**
   - 标记前置任务
   - 标记可并行执行的任务 [P]
   - 形成 DAG 而非线性格子

4. **用户故事驱动**
   - 每个用户故事对应一组任务
   - 任务按用户故事分组
   - 设置检查点验证每个故事完成

5. **测试优先**
   - 每个任务的第一个子任务通常是编写测试
   - TDD 风格的任务拆解

**输出格式：**
```markdown
## 任务清单

### US-01: <故事标题>
- [ ] T001 [P] 创建测试文件 `tests/test_xxx.py`
- [ ] T002 实现 `src/xxx.py` (依赖 T001)
...
```

每次回答末尾：[计划状态]: <任务数> 个任务 | <并行> 个可并行
"""
    mandatory = False
    trigger_conditions = [
        TriggerCondition(task_types=["plan"]),
        TriggerCondition(keyword_patterns=["计划", "步骤", "怎么做"]),
    ]

    async def activate(self, context: dict[str, Any]) -> dict[str, Any]:
        return {
            "skill_name": self.name,
            "system_prompt": self.SYSTEM_PROMPT,
            "mandatory": False,
            "context_hint": "建议将设计拆分为 2-5 分钟粒度的可执行任务。",
        }
