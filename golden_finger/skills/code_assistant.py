"""金手指 Agent 系统 — 代码辅助术

帮助用户编写、调试、优化代码。
"""

from typing import Any

from .base import BaseSkill
from ..models import RealmLevel


class CodeAssistant(BaseSkill):
    """代码辅助术：编程帮助"""

    name = "code_assistant"
    display_name = "代码辅助术"
    description = "辅助编写、调试、优化代码。支持 Python、JS、Shell 等常见语言。适合「帮我写」「这段代码」「报错」「优化」类问题。"
    category = "knowledge"
    realm_requirement = RealmLevel.MORTAL
    tools_required = ["file_read", "file_write", "shell_exec"]

    SYSTEM_PROMPT = """你是「代码辅助术」——金手指系统的编程 Skill。

你的职责：
1. 编写清晰、可运行的代码
2. 分析和修复代码错误
3. 优化代码性能和可读性
4. 解释代码逻辑

规则：
- 代码优先使用 Python（跨平台兼容）
- 提供完整可运行的示例
- 标注关键步骤和注意事项
- Shell 命令需注明 Linux/Windows 差异

每次回答末尾：
[代码收获]: <一句话核心收获>
"""

    async def activate(self, context: dict[str, Any]) -> dict[str, Any]:
        query = context.get("query", "")

        return {
            "skill_name": self.name,
            "system_prompt": self.SYSTEM_PROMPT,
            "context_hint": self._build_context_hint(query),
        }

    def _build_context_hint(self, query: str) -> str:
        import sys
        hints = [f"运行平台: {sys.platform}"]
        if "windows" in sys.platform.lower():
            hints.append("Shell 命令使用 cmd 语法或 PowerShell")
        else:
            hints.append("Shell 命令使用 bash 语法")
        return "\n".join(hints)
