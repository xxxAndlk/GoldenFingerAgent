"""金手指 Agent 系统 — 知识汲取术

帮助用户拆解学习目标、规划学习路径、解答知识问题。
"""

from typing import Any

from .base import BaseSkill
from ..models import HostProfile, RealmLevel


class KnowledgeAbsorption(BaseSkill):
    """知识汲取术：学习辅助与知识管理"""

    name = "knowledge_absorption"
    display_name = "知识汲取术"
    description = "拆解学习目标为知识图谱，规划修炼路径，解答知识疑问。适合「帮我学习」「解释一下」「规划学习」类问题。"
    category = "knowledge"
    realm_requirement = RealmLevel.MORTAL
    tools_required = ["web_search", "file_write", "file_read"]

    SYSTEM_PROMPT = """你是「知识汲取术」——金手指系统的知识学习 Skill。

你的职责：
1. 将复杂的学习目标拆解为结构化的知识脉络（主脉/支脉/穴位）
2. 根据用户当前水平推荐最优学习路径
3. 用通俗易懂的方式解释概念
4. 标注学习重点和常见难点（瓶颈穴位）

回答格式：
- 先给出知识脉络概览
- 再逐点展开讲解
- 最后给出下一步建议和预计修炼时间

每次回答末尾，用一行总结核心收获：
[修炼收获]: <一句话核心收获>
"""

    async def activate(self, context: dict[str, Any]) -> dict[str, Any]:
        query = context.get("query", "")
        host_profile = context.get("host_profile")

        return {
            "skill_name": self.name,
            "system_prompt": self.SYSTEM_PROMPT,
            "context_hint": self._build_context_hint(query, host_profile),
        }

    def _build_context_hint(self, query: str, host_profile: HostProfile | None) -> str:
        """根据宿主画像构建个性化上下文提示"""
        if not host_profile:
            return ""

        parts: list[str] = []
        realm = host_profile.realm
        if realm != RealmLevel.MORTAL:
            parts.append(f"宿主当前境界: {realm.display}，请用合适的深度讲解。")

        interests = host_profile.interest_tags
        if interests:
            parts.append(f"宿主兴趣领域: {', '.join(interests[:5])}")

        # 灵根偏好学习方式
        root = host_profile.spirit_root
        if root:
            dominant = root.dominant
            style_map = {
                "metal": "偏好逻辑推理和系统性学习，可多用结构化框架",
                "wood": "偏好创造性思维，可多用类比和发散联想",
                "water": "偏好语言表达，可多用故事和案例",
                "fire": "偏好快速实践，可直接给行动步骤",
                "earth": "偏好扎实基础，可多解释底层原理",
            }
            parts.append(style_map.get(dominant, ""))

        return "\n".join(parts)
