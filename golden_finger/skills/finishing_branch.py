"""金手指 Agent 系统 — 分支完成术 (来自 superpowers: finishing-a-development-branch)

所有任务完成后的分支收尾工作流：验证 → 合并/PR → 清理。
"""

from typing import Any

from .base import BaseSkill
from ..models import RealmLevel, TriggerCondition


class FinishingBranch(BaseSkill):
    """分支完成术：合并决策与清理"""

    name = "finishing-a-development-branch"
    display_name = "分支完成术"
    description = "开发分支完成后的收尾工作流。验证测试→选择合并策略→清理 worktree。来自 superpowers 的 finishing-a-development-branch 技能。"
    category = "collaboration"
    realm_requirement = RealmLevel.MORTAL
    tools_required = ["shell_exec"]

    SYSTEM_PROMPT = """你是「分支完成术」——金手指系统的分支收尾 Skill。

**收尾流程：**

1. **最终验证**
   - 运行全部测试套件
   - 确认所有检查点达成
   - 检查是否有未提交的变更

2. **合并策略选择**
   - **合并 (Merge)**: 标准合入，保留完整历史
   - **创建 PR**: 推送到远程，创建 Pull Request
   - **保留分支**: 暂时不合入，保留给后续工作
   - **丢弃分支**: 实验性工作，不再需要

3. **执行合并**
   - 切换到主分支
   - 合并功能分支
   - 确认无冲突
   - 推送（如果需要）

4. **清理**
   - 删除 worktree（如果使用）
   - 删除本地功能分支（可选）
   - 更新项目文档

**规则：**
- 合入前必须所有测试通过
- 合入前必须代码审查通过
- 不要 force push 到共享分支

每次回答末尾：[分支状态]: <VALIDATED/MERGED/PR_CREATED/DISCARDED>
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
            "gate": "BRANCH_FINISH",
            "context_hint": "所有任务已完成。请进行最终验证并选择合并策略。",
        }
