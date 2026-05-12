"""金手指 Agent 系统 — Skill 注册表

管理所有 Skill 的注册、发现和匹配。
"""

import time
from typing import Any

from .base import BaseSkill
from ..models import RealmLevel
from ..storage.vector_store import vector_store


class SkillRegistry:
    """Skill 注册表：管理所有已安装的 Skill"""

    def __init__(self):
        self._skills: dict[str, BaseSkill] = {}
        self._search_cache: dict[str, tuple[float, list[dict[str, Any]]]] = {}
        self._cache_ttl: float = 30.0  # 搜索缓存 30 秒

    def register(self, skill: BaseSkill) -> None:
        """注册一个 Skill"""
        self._skills[skill.name] = skill
        self._search_cache.clear()  # 注册新 skill 时清缓存

    def unregister(self, skill_name: str) -> None:
        """移除一个 Skill"""
        self._skills.pop(skill_name, None)
        self._search_cache.clear()

    def get(self, name: str) -> BaseSkill | None:
        """获取 Skill"""
        return self._skills.get(name)

    def list_all(self) -> list[BaseSkill]:
        """列出所有 Skill"""
        return list(self._skills.values())

    def list_unlocked(self, host_realm: RealmLevel) -> list[BaseSkill]:
        """列出已解锁的 Skill"""
        return [
            s for s in self._skills.values()
            if s.can_activate(host_realm)
        ]

    def search(self, query: str, top_k: int = 5) -> list[dict[str, Any]]:
        """搜索最匹配的 Skill

        先用名称/描述关键词匹配，再用向量检索。
        结果包含 30 秒缓存以避免重复计算。
        """
        now = time.monotonic()
        cache_key = f"{query}:{top_k}"
        if cache_key in self._search_cache:
            ts, cached = self._search_cache[cache_key]
            if now - ts < self._cache_ttl:
                return cached

        # 1. 关键词匹配
        query_lower = query.lower()
        keyword_matches: list[tuple[BaseSkill, int]] = []
        for skill in self._skills.values():
            score = 0
            if skill.name.lower() in query_lower:
                score += 3
            if skill.display_name.lower() in query_lower:
                score += 2
            for word in query_lower.split():
                if word in skill.description.lower():
                    score += 1
                if word in skill.category.lower():
                    score += 1
            if score > 0:
                keyword_matches.append((skill, score))

        keyword_matches.sort(key=lambda x: x[1], reverse=True)

        # 2. 向量检索补充
        vector_matches = vector_store.search_skills(query, top_k=top_k)

        # 3. 合并结果（不返回完整的 BaseSkill 对象，避免内存浪费）
        results: list[dict[str, Any]] = []
        seen: set[str] = set()

        for skill, score in keyword_matches[:top_k]:
            seen.add(skill.name)
            results.append({
                "skill_name": skill.name,
                "display_name": skill.display_name,
                "description": skill.description,
                "match_reason": f"关键词匹配 (分数: {score})",
            })

        for vm in vector_matches:
            name = vm["skill_name"]
            if name not in seen and name in self._skills:
                seen.add(name)
                skill = self._skills[name]
                results.append({
                    "skill_name": name,
                    "display_name": skill.display_name,
                    "description": skill.description,
                    "match_reason": f"语义匹配 (相似度: {1 - vm['distance']:.2f})",
                    "content": vm["content"][:200],
                })

        results = results[:top_k]
        self._search_cache[cache_key] = (now, results)
        return results

    def get_tools_for_skills(self, skill_names: list[str]) -> list[str]:
        """获取一组 Skill 需要的所有工具"""
        tools: list[str] = []
        for name in skill_names:
            skill = self._skills.get(name)
            if skill:
                tools.extend(skill.tools_required)
        return list(set(tools))


# 全局注册表
skill_registry = SkillRegistry()
