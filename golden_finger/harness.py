"""金手指 Agent 系统 — Harness 主调度器

编排五域流水线：天机推演 → 施法执行 → 验道校验 → 刻碑沉淀
内外结界贯穿全程。
"""

import asyncio
import logging
from typing import Any, AsyncGenerator

from .models import HostProfile, ExecutionReport, ToolCallEvent
from .config import config
from .llm import LLMClient
from .domain_analysis import PlanGenerator
from .domain_execution import ExecutionOrchestrator
from .domain_verification import VerificationEngine
from .domain_persistence import PersistenceEngine
from .skills.registry import skill_registry
from .skills.knowledge import KnowledgeAbsorption
from .skills.code_assistant import CodeAssistant
from .skills.file_operations import FileOperations
from .storage.sqlite_store import SQLiteStore
from .storage.vector_store import vector_store

logger = logging.getLogger("golden_finger")


class GoldenFingerHarness:
    """金手指系统主 Harness：五域编排器"""

    def __init__(self):
        self.llm = LLMClient()
        self.store = SQLiteStore()

        # 五域引擎
        self.planner = PlanGenerator(self.llm)
        self.executor = ExecutionOrchestrator(self.llm)
        self.verifier = VerificationEngine(self.llm)
        self.persister = PersistenceEngine(self.llm, self.store)

        # 宿主画像
        self.host_profile: HostProfile | None = None

        # 初始化
        self._init_skills()
        self._load_or_create_profile()

    def _init_skills(self):
        """注册内置 Skill"""
        skill_registry.register(KnowledgeAbsorption())
        skill_registry.register(CodeAssistant())
        skill_registry.register(FileOperations())
        logger.info(f"已注册 {len(skill_registry.list_all())} 个 Skill")

    def _load_or_create_profile(self):
        """加载或创建宿主画像"""
        self.host_profile = self.store.load_host_profile()
        if self.host_profile is None:
            self.host_profile = HostProfile()
            self.store.save_host_profile(self.host_profile)
            logger.info("创建新宿主画像 — 欢迎觉醒")
        else:
            logger.info(f"加载宿主画像 — {self.host_profile.realm.display}")

    async def run_query(self, query: str) -> dict[str, Any]:
        """执行一次完整的五域流水线"""
        result: dict[str, Any] = {
            "query": query,
            "plan": None,
            "execution": None,
            "verification": None,
            "persistence": None,
            "error": None,
        }

        try:
            # ① 天机推演
            plan = await self.planner.generate_plan(query, self.host_profile)
            result["plan"] = plan

            # ② 施法执行
            report = await self.executor.execute_plan(plan)
            result["execution"] = report

            # ③ 验道校验
            verification = await self.verifier.verify(report, query)
            result["verification"] = verification

            # ④ 刻碑沉淀
            if self.host_profile:
                persist_result = await self.persister.persist(
                    plan, report, verification, self.host_profile
                )
                result["persistence"] = persist_result

                # 重新加载画像以获取更新后的数据
                self.host_profile = self.store.load_host_profile()

            return result

        except Exception as e:
            logger.error(f"流水线执行异常: {e}", exc_info=True)
            result["error"] = str(e)
            return result

    async def run_query_stream(self, query: str) -> AsyncGenerator[dict[str, Any], None]:
        """流式执行五域流水线，每步产出中间结果"""
        profile = self.host_profile

        # ① 天机推演
        yield {"domain": "analysis", "status": "started", "message": "正在推演天机..."}
        try:
            plan = await self.planner.generate_plan(query, profile)
            yield {"domain": "analysis", "status": "completed", "plan": plan}
        except Exception as e:
            yield {"domain": "analysis", "status": "error", "error": str(e)}
            return

        # ② 施法执行
        yield {"domain": "execution", "status": "started", "message": "正在施法执行..."}
        exec_task: asyncio.Task[ExecutionReport] | None = None
        try:
            tool_events: asyncio.Queue[ToolCallEvent] = asyncio.Queue()

            async def _on_tool_event(event: ToolCallEvent) -> None:
                await tool_events.put(event)

            exec_task = asyncio.ensure_future(
                self.executor.execute_plan(plan, on_tool_event=_on_tool_event)
            )

            while not exec_task.done():
                try:
                    tool_event = await asyncio.wait_for(tool_events.get(), timeout=0.1)
                    yield {
                        "domain": "execution",
                        "status": "tool_call",
                        "event": {
                            "phase": tool_event.phase.value,
                            "task_id": tool_event.task_id,
                            "tool_name": tool_event.tool_name,
                            "params": tool_event.params,
                            "result": tool_event.result,
                            "error": tool_event.error,
                            "duration_ms": tool_event.duration_ms,
                        },
                    }
                except asyncio.TimeoutError:
                    continue

            report = exec_task.result()
            yield {"domain": "execution", "status": "completed", "report": report}
        except Exception as e:
            if exec_task is not None and not exec_task.done():
                exec_task.cancel()
            yield {"domain": "execution", "status": "error", "error": str(e)}
            return

        # ③ 验道校验
        yield {"domain": "verification", "status": "started", "message": "正在验道校验..."}
        try:
            verification = await self.verifier.verify(report, query)
            yield {"domain": "verification", "status": "completed", "verification": verification}
        except Exception as e:
            yield {"domain": "verification", "status": "error", "error": str(e)}
            return

        # ④ 刻碑沉淀
        yield {"domain": "persistence", "status": "started", "message": "正在刻碑沉淀..."}
        try:
            if profile:
                persist_result = await self.persister.persist(
                    plan, report, verification, profile
                )
                self.host_profile = self.store.load_host_profile()
                yield {"domain": "persistence", "status": "completed", "result": persist_result}
        except Exception as e:
            yield {"domain": "persistence", "status": "error", "error": str(e)}

        yield {"domain": "complete", "status": "done"}

    def get_status(self) -> dict[str, Any]:
        """获取系统状态"""
        profile = self.host_profile
        skills = skill_registry.list_all()
        vector_count = vector_store.count()

        return {
            "host_id": profile.host_id if profile else "N/A",
            "soul_mark": profile.soul_mark[:12] if profile else "N/A",
            "realm": profile.realm.display if profile else "N/A",
            "realm_stage": profile.realm_stage.value if profile else "N/A",
            "realm_progress": f"{profile.realm_progress:.1f}%" if profile else "N/A",
            "total_tasks": profile.total_tasks_completed if profile else 0,
            "total_time_min": profile.total_cultivation_time if profile else 0,
            "spirit_root": {
                "dominant": profile.spirit_root.dominant if profile else "N/A",
                "metal": profile.spirit_root.metal if profile else 0,
                "wood": profile.spirit_root.wood if profile else 0,
                "water": profile.spirit_root.water if profile else 0,
                "fire": profile.spirit_root.fire if profile else 0,
                "earth": profile.spirit_root.earth if profile else 0,
            },
            "skills": [s.to_dict() for s in skills],
            "vector_memories": vector_count,
            "data_dir": str(config.data_dir),
            "llm_provider": config.llm_provider,
        }

    async def close(self):
        """清理资源"""
        await self.llm.close()
        self.store.close()

    @staticmethod
    def get_final_response(report: ExecutionReport) -> str:
        """从执行报告中提取最终回复文本"""
        texts: list[str] = []
        for _task_id, result in report.task_results.items():
            text = result.get("text", "")
            if text:
                texts.append(text)
        return "\n\n".join(texts) if texts else "（无输出）"
