"""金手指 Agent 系统 — Skill 基类"""

from abc import ABC, abstractmethod
from typing import Any

from ..models import (
    SkillManifest,
    SkillState,
    KnowledgeEntry,
    RealmLevel,
    SpiritRoot,
)


class BaseSkill(ABC):
    """技能基类：所有金手指能力的抽象接口"""

    # 子类覆盖
    name: str = ""
    display_name: str = ""
    description: str = ""
    category: str = "utility"
    realm_requirement: RealmLevel = RealmLevel.MORTAL
    tools_required: list[str] = []
    SYSTEM_PROMPT: str = ""

    def __init__(self):
        self.manifest = SkillManifest(
            name=self.name,
            display_name=self.display_name,
            description=self.description,
            category=self.category,
            realm_requirement=self.realm_requirement,
            tools_required=self.tools_required,
        )
        self.state = SkillState.UNLOCKED
        self.level = 0
        self.experience = 0
        self.affinity = 1.0

    # ---- 核心接口 ----

    @abstractmethod
    async def activate(self, context: dict[str, Any]) -> dict[str, Any]:
        """激活技能：执行核心逻辑"""
        ...

    def get_system_prompt(self) -> str:
        """返回此 Skill 的 system prompt 片段"""
        return f"## {self.display_name}\n{self.description}\n"

    def get_knowledge_context(self, query: str) -> str:
        """检索与此 Skill 相关的知识上下文"""
        from ..storage.vector_store import vector_store as vs
        results = vs.search_by_skill(self.name, query, top_k=3)
        if not results:
            return ""
        parts: list[str] = ["[已记录的相关经验]"]
        for r in results:
            parts.append(f"- {r['content'][:300]}")
        return "\n".join(parts) + "\n"

    def add_knowledge(self, entry: KnowledgeEntry):
        """添加知识条目"""
        self.manifest.knowledge.append(entry)

    # ---- 前置检查 ----

    def can_activate(self, host_realm: RealmLevel) -> bool:
        """检查宿主境界是否满足激活条件"""
        if host_realm.value < self.realm_requirement.value:
            return False
        if self.state in (SkillState.SEALED, SkillState.OVERLOADED):
            return False
        return True

    def _calculate_affinity(self, spirit_root: SpiritRoot) -> float:
        """根据灵根计算技能契合度"""
        affinity_map = {
            "metal": ["code_assistant", "data_processing", "logical_analysis"],
            "wood": ["knowledge_absorption", "creative_design", "content_creation"],
            "water": ["knowledge_absorption", "file_operations", "communication"],
            "fire": ["code_assistant", "project_management", "execution"],
            "earth": ["file_operations", "quality_assurance", "research"],
        }
        dominant = spirit_root.dominant
        if self.name in affinity_map.get(dominant, []):
            purity = getattr(spirit_root, dominant, 50) / 100.0
            return 1.0 + purity * 0.5
        return 1.0

    def to_dict(self) -> dict[str, Any]:
        return {
            "name": self.name,
            "display_name": self.display_name,
            "description": self.description,
            "category": self.category,
            "level": self.level,
            "state": self.state.value,
            "knowledge_count": len(self.manifest.knowledge),
            "total_uses": self.manifest.stats.total_uses,
        }
