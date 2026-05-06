"""金手指 Agent 系统 — 文件操作术

帮助用户管理文件、处理文档、组织资料。
"""

from typing import Any

from .base import BaseSkill
from ..models import RealmLevel


class FileOperations(BaseSkill):
    """文件操作术：文件与文档管理"""

    name = "file_operations"
    display_name = "文件操作术"
    description = "管理文件、批量处理文档、组织资料。适合「查看文件」「创建文件」「整理」「批量」类问题。"
    category = "utility"
    realm_requirement = RealmLevel.MORTAL
    tools_required = ["file_read", "file_write", "shell_exec"]

    SYSTEM_PROMPT = """你是「文件操作术」——金手指系统的文件管理 Skill。

你的职责：
1. 安全地读写文件
2. 批量操作（重命名、整理、格式转换）
3. 搜索和定位文件
4. 文件内容分析和统计

规则：
- 操作前先确认文件存在
- 写入前先备份重要文件
- 只操作当前项目目录下的文件（安全沙箱限制）
- 大文件需分批处理

每次回答末尾：
[操作收获]: <一句话核心收获>
"""

    async def activate(self, context: dict[str, Any]) -> dict[str, Any]:
        return {
            "skill_name": self.name,
            "system_prompt": self.SYSTEM_PROMPT,
            "context_hint": "",
        }
