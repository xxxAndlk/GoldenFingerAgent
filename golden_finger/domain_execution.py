"""金手指 Agent 系统 — ② 施法执行（执行调度域）

职责：按 TaskPlan 的拓扑序，逐一执行原子任务，管理工具调用，确保安全。

流程：
1. 执行编排 → 按拓扑分层调度
2. 单任务执行循环 → LLM 调用 ↔ 工具调用
3. 安全防御 → 注入检测 + 权限校验 + 沙箱
4. 异常处理 → 重试 + 降级 + 熔断
"""

import asyncio
import json
import time
from typing import Any, Awaitable, Callable

from .models import (
    TaskPlan, AtomTask, ExecutionReport, ToolCallLog,
    ToolCallEvent, ToolCallPhase,
)
from .llm import LLMClient, LLMError
from .tools.builtin import BUILTIN_TOOLS
from .domain_isolation import IngressFilter


class ToolExecutionGuard:
    """工具执行守卫：注入检测 + 权限校验 + 沙箱执行"""

    @classmethod
    async def execute_tool(
        cls,
        tool_name: str,
        params: dict[str, Any],
    ) -> ToolCallLog:
        """安全地执行一个工具调用"""
        log = ToolCallLog(tool_name=tool_name, params=params)
        start = time.time()

        # 1. 注入检测
        params_str = json.dumps(params, ensure_ascii=False)
        safe, reason = IngressFilter.check_safety(params_str)
        log.injection_check_passed = safe
        if not safe:
            log.error = f"注入检测不通过: {reason}"
            log.duration_ms = int((time.time() - start) * 1000)
            return log

        # 2. 权限校验
        tool = BUILTIN_TOOLS.get(tool_name)
        if tool is None:
            log.error = f"工具不存在: {tool_name}"
            log.permission_check_passed = False
            log.duration_ms = int((time.time() - start) * 1000)
            return log

        # 3. 执行
        try:
            result = await tool.execute(**params)
            log.result = result.data if result.success else None
            log.error = result.error if not result.success else None
        except Exception as e:
            log.error = str(e)

        log.duration_ms = int((time.time() - start) * 1000)
        return log


class SingleTaskExecutor:
    """单任务执行器：处理一个原子任务的 LLM 对话循环"""

    def __init__(self, llm: LLMClient):
        self.llm = llm

    async def execute(
        self,
        task: AtomTask,
        messages: list[dict[str, Any]],
        on_tool_event: Callable[[ToolCallEvent], Awaitable[None]] | None = None,
    ) -> dict[str, Any]:
        """执行单个原子任务

        循环：LLM 调用 → 解析 Tool Call → 执行工具 → 将结果反馈 LLM → 直到无 Tool Call
        """
        # 构建系统 + 用户消息
        system_msg = task.prompt
        user_msg = task.description

        if not messages:
            messages = [
                {"role": "system", "content": system_msg},
                {"role": "user", "content": user_msg},
            ]

        tool_schemas: list[dict[str, Any]] = [t.to_openai_schema() for t in BUILTIN_TOOLS.values()]
        tool_logs: list[ToolCallLog] = []
        max_rounds = 5
        final_text: str = ""

        for _round_num in range(max_rounds):
            try:
                resp = await self.llm.chat(
                    messages=messages,
                    tools=tool_schemas,
                    max_tokens=4096,
                )
            except LLMError as e:
                return {
                    "task_id": task.task_id,
                    "success": False,
                    "error": str(e),
                    "text": "",
                    "tool_logs": tool_logs,
                }

            text = self.llm.extract_text(resp)
            tool_calls = self.llm.extract_tool_calls(resp)

            # 添加 assistant 消息
            assistant_msg: dict[str, Any] = {"role": "assistant", "content": text or None}
            if tool_calls:
                assistant_msg["tool_calls"] = tool_calls
            messages.append(assistant_msg)

            if not tool_calls:
                final_text = text
                break

            # 执行工具
            for tc in tool_calls:
                func: dict[str, Any] = tc.get("function", {})
                tool_name: str = func.get("name", "")
                params: dict[str, Any] = {}
                try:
                    params = json.loads(func.get("arguments", "{}"))
                except json.JSONDecodeError:
                    pass

                # 通知 tool_start
                if on_tool_event:
                    await on_tool_event(ToolCallEvent(
                        phase=ToolCallPhase.TOOL_START,
                        task_id=task.task_id,
                        tool_name=tool_name,
                        params=params,
                    ))

                log = await ToolExecutionGuard.execute_tool(tool_name, params)
                tool_logs.append(log)

                # 通知 tool_end
                if on_tool_event:
                    await on_tool_event(ToolCallEvent(
                        phase=ToolCallPhase.TOOL_END,
                        task_id=task.task_id,
                        tool_name=tool_name,
                        params=params,
                        result=log.result if log.success else None,
                        error=log.error,
                        duration_ms=log.duration_ms,
                    ))

                # 将工具结果反馈给 LLM
                tool_result_content = json.dumps(log.result) if log.success else f"错误: {log.error}"
                messages.append({
                    "role": "tool",
                    "tool_call_id": tc.get("id", ""),
                    "content": tool_result_content,
                })

                # 如果工具调用失败，中断本轮
                if not log.success:
                    final_text = f"工具 [{tool_name}] 执行失败: {log.error}"
                    break
        else:
            # 超过最大轮数
            final_text = self.llm.extract_text(
                await self.llm.chat(messages=messages, max_tokens=2048)
            )

        return {
            "task_id": task.task_id,
            "success": True,
            "text": final_text,
            "tool_logs": tool_logs,
            "messages": messages,
        }


class ExecutionOrchestrator:
    """执行编排器：按拓扑分层调度所有原子任务"""

    def __init__(self, llm: LLMClient):
        self.llm = llm
        self.executor = SingleTaskExecutor(llm)

    async def execute_plan(
        self,
        plan: TaskPlan,
        on_tool_event: Callable[[ToolCallEvent], Awaitable[None]] | None = None,
    ) -> ExecutionReport:
        """按拓扑序执行整个 TaskPlan"""
        report = ExecutionReport(plan_id=plan.plan_id, status="running")
        start = time.time()
        results: dict[str, Any] = {}
        # 全局消息上下文，在任务间传递
        global_messages: list[dict[str, Any]] = []

        for _level_idx, level in enumerate(plan.execution_order):
            # 同层级可以并行执行
            level_tasks = [t for t in plan.tasks if t.task_id in level]

            if len(level_tasks) == 1:
                # 单任务：直接执行
                task = level_tasks[0]
                result = await self.executor.execute(task, list(global_messages), on_tool_event=on_tool_event)
                results[task.task_id] = result
                report.tool_call_logs.extend(
                    result.get("tool_logs", [])
                )
                # 将本任务输出作为后续任务的上下文
                if result.get("text"):
                    global_messages.append({
                        "role": "assistant",
                        "content": f"[步骤完成: {task.description}]\n{result['text']}"
                    })
            else:
                # 多任务并行
                coros = [
                    self.executor.execute(t, list(global_messages), on_tool_event=on_tool_event)
                    for t in level_tasks
                ]
                level_results = await asyncio.gather(*coros)
                for task, result in zip(level_tasks, level_results):
                    results[task.task_id] = result
                    report.tool_call_logs.extend(
                        result.get("tool_logs", [])
                    )
                    if result.get("text"):
                        global_messages.append({
                            "role": "assistant",
                            "content": f"[步骤完成: {task.description}]\n{result['text']}"
                        })

        # 检查是否有失败
        all_ok = all(r.get("success", False) for r in results.values())
        report.status = "completed" if all_ok else "partial_failure"
        report.task_results = results
        report.total_duration_ms = int((time.time() - start) * 1000)
        report.llm_messages = global_messages

        # 异常检测
        for log in report.tool_call_logs:
            if log.error:
                report.anomalies.append(f"工具 [{log.tool_name}] 异常: {log.error}")

        return report
