"""金手指 Agent 系统 — Git Worktree 术 (来自 superpowers: using-git-worktrees)

为并行开发创建隔离的 Git worktree 工作空间。
"""

from typing import Any
import subprocess
import os

from .base import BaseSkill
from ..models import RealmLevel, TriggerCondition


class UsingGitWorktrees(BaseSkill):
    """Git Worktree 术：隔离并行开发"""

    name = "using-git-worktrees"
    display_name = "Git Worktree 术"
    description = "创建隔离的 Git worktree 分支进行并行开发，避免上下文污染。来自 superpowers 的 using-git-worktrees 技能。"
    category = "collaboration"
    realm_requirement = RealmLevel.QI_REFINING
    tools_required = ["shell_exec"]

    SYSTEM_PROMPT = """你是「Git Worktree 术」——金手指系统的 Git 隔离开发 Skill。

**使用场景：**
- 多个独立功能并行开发
- 需要隔离的试验性修改
- 避免上下文污染

**操作流程：**

1. **创建 worktree**
   ```bash
   git worktree add ../project-feat-xxx -b feat/xxx
   ```
   - worktree 目录放在项目父目录
   - 分支名遵循 `feat/<描述>` 或 `fix/<描述>`

2. **初始化工作空间**
   - 在 worktree 中安装依赖
   - 验证测试基线通过
   - 确认环境可用

3. **在隔离空间工作**
   - 所有文件变更在 worktree 内
   - 不影响主工作区
   - 可以安全地切换/放弃

4. **清理**
   ```bash
   git worktree remove ../project-feat-xxx
   git branch -d feat/xxx  # 如果不需要
   ```

**规则：**
- worktree 命名前缀: `feat/`, `fix/`, `exp/`
- 完成后必须清理 worktree
- 不要在 worktree 中修改共享配置

每次回答末尾：[Worktree]: <分支名> | <状态>
"""
    mandatory = False
    trigger_conditions = [
        TriggerCondition(context_states=["multi_task"], domain_tags=["infra", "cli"]),
        TriggerCondition(keyword_patterns=["并行开发", "多分支", "worktree"]),
    ]

    async def activate(self, context: dict[str, Any]) -> dict[str, Any]:
        # 检查是否在 git 仓库中
        try:
            subprocess.run(
                ["git", "rev-parse", "--git-dir"],
                capture_output=True, check=True,
                timeout=5,
            )
        except Exception:
            return {"skill_name": self.name, "skip": True, "reason": "不在 Git 仓库中"}

        return {
            "skill_name": self.name,
            "system_prompt": self.SYSTEM_PROMPT,
            "mandatory": False,
            "context_hint": "建议创建 Git worktree 进行隔离开发。",
        }
