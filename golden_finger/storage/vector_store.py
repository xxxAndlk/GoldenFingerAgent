"""金手指 Agent 系统 — ChromaDB 向量存储

用于 Skill 知识的语义检索。
"""

from typing import Any

from ..config import config


class VectorStore:
    """ChromaDB 向量存储封装"""

    def __init__(self):
        self._client = None
        self._collection = None

    def _ensure_init(self):
        if self._client is not None:
            return self._collection
        import chromadb
        self._client = chromadb.PersistentClient(
            path=str(config.memory_dir)
        )
        self._collection = self._client.get_or_create_collection(
            name="golden_finger_skills",
            metadata={"hnsw:space": "cosine"}
        )
        return self._collection

    def warm_up(self):
        """预初始化向量数据库（首次调用时下载 ONNX 嵌入模型）"""
        self._ensure_init()

    def add_skill_knowledge(
        self,
        skill_name: str,
        entries: list[dict[str, Any]],
    ):
        """向向量库添加 Skill 知识条目

        Args:
            skill_name: Skill 名称
            entries: [{"content": "...", "metadata": {...}}, ...]
        """
        if not entries:
            return
        ids: list[str] = []
        documents: list[str] = []
        metadatas: list[dict[str, Any]] = []
        for i, entry in enumerate(entries):
            ids.append(f"{skill_name}_{i}_{hash(entry['content']) % 100000}")
            documents.append(str(entry["content"]))
            meta: dict[str, Any] = dict(entry.get("metadata", {}))
            meta["skill_name"] = skill_name
            metadatas.append(meta)

        col = self._ensure_init()
        assert col is not None
        col.add(
            ids=ids,
            documents=documents,
            metadatas=metadatas,  # pyright: ignore[reportArgumentType]
        )

    def search_skills(
        self,
        query: str,
        top_k: int = 5,
    ) -> list[dict[str, Any]]:
        """搜索最匹配的 Skill 知识

        Returns:
            [{"skill_name": ..., "content": ..., "distance": ...}, ...]
        """
        col = self._ensure_init()
        assert col is not None
        results = col.query(
            query_texts=[query],
            n_results=top_k,
        )
        items: list[dict[str, Any]] = []
        if results["ids"] and results["ids"][0]:
            ids_list: list[str] = results["ids"][0]
            metadatas_list: list[Any] = results["metadatas"][0] if results["metadatas"] else []
            documents_list: list[str] = results["documents"][0] if results["documents"] else []
            distances_list: list[float] = results["distances"][0] if results["distances"] else []
            for i, doc_id in enumerate(ids_list):
                meta = metadatas_list[i] if i < len(metadatas_list) else {}
                dist = distances_list[i] if i < len(distances_list) else 0.0
                items.append({
                    "skill_name": meta.get("skill_name", ""),
                    "content": documents_list[i] if i < len(documents_list) else "",
                    "distance": dist,
                })
        return items

    def search_by_skill(
        self,
        skill_name: str,
        query: str,
        top_k: int = 3,
    ) -> list[dict[str, Any]]:
        """在指定 Skill 中搜索"""
        col = self._ensure_init()
        assert col is not None
        results = col.query(
            query_texts=[query],
            n_results=top_k,
            where={"skill_name": skill_name},
        )
        items: list[dict[str, Any]] = []
        if results["ids"] and results["ids"][0]:
            ids_list: list[str] = results["ids"][0]
            documents_list: list[str] = results["documents"][0] if results["documents"] else []
            distances_list: list[float] = results["distances"][0] if results["distances"] else []
            for i, doc_id in enumerate(ids_list):
                dist = distances_list[i] if i < len(distances_list) else 0.0
                items.append({
                    "content": documents_list[i] if i < len(documents_list) else "",
                    "distance": dist,
                })
        return items

    def delete_skill(self, skill_name: str):
        """删除某个 Skill 的所有知识"""
        try:
            col = self._ensure_init()
            assert col is not None
            col.delete(where={"skill_name": skill_name})
        except Exception:
            pass

    def count(self) -> int:
        try:
            col = self._ensure_init()
            assert col is not None
            return col.count()
        except Exception:
            return 0


# 全局实例
vector_store = VectorStore()
