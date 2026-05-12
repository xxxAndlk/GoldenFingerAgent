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
from collections import defaultdict
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
            tool_failed = False
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

                # 如果工具调用失败，中断本轮和外层循环
                if not log.success:
                    final_text = f"工具 [{tool_name}] 执行失败: {log.error}"
                    tool_failed = True
                    break

            if tool_failed:
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


class AgentMailbox:
    """Agent 邮件系统：用于 leader/主任/子 agent 间消息同步。"""

    def __init__(self):
        self._boxes: dict[str, list[dict[str, Any]]] = defaultdict(list)

    def send(self, from_agent: str, to_agent: str, subject: str, content: str) -> dict[str, Any]:
        mail = {
            "from": from_agent,
            "to": to_agent,
            "subject": subject,
            "content": content,
            "timestamp": int(time.time() * 1000),
        }
        self._boxes[to_agent].append(mail)
        return mail

    def recv_all(self, agent: str) -> list[dict[str, Any]]:
        mails = list(self._boxes.get(agent, []))
        self._boxes[agent].clear()
        return mails


class ExecutionOrchestrator:
    """执行编排器：按拓扑分层调度所有原子任务"""

    def __init__(self, llm: LLMClient):
        self.llm = llm
        self.executor = SingleTaskExecutor(llm)
        self.mailbox = AgentMailbox()

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
        running_async_tasks: dict[str, asyncio.Task[dict[str, Any]]] = {}

        async def emit(event: ToolCallEvent) -> None:
            if on_tool_event:
                await on_tool_event(event)

        async def publish_task(task: AtomTask) -> None:
            task.protocol_status = "published"
            await emit(ToolCallEvent(
                phase=ToolCallPhase.TASK_PUBLISH,
                task_id=task.task_id,
                tool_name="task_protocol",
                from_agent="leader",
                to_agent=task.chief_agent,
                message=f"发布任务: {task.description[:80]}",
            ))

        async def claim_task(task: AtomTask, sub_agent: str) -> None:
            task.protocol_status = "claimed"
            await emit(ToolCallEvent(
                phase=ToolCallPhase.TASK_CLAIM,
                task_id=task.task_id,
                tool_name="task_protocol",
                from_agent=task.chief_agent,
                to_agent=sub_agent,
                message=f"领取任务 ({task.agent_kind}/{task.dispatch_mode})",
            ))

        async def close_task(task: AtomTask, success: bool, err: str = "") -> None:
            task.protocol_status = "closed" if success else "failed_closed"
            await emit(ToolCallEvent(
                phase=ToolCallPhase.TASK_CLOSE if success else ToolCallPhase.TASK_FORCE_CLOSE,
                task_id=task.task_id,
                tool_name="task_protocol",
                from_agent=task.chief_agent,
                to_agent="leader",
                error=err or None,
                message="任务关闭" if success else f"任务异常关闭: {err[:120]}",
            ))

        async def run_with_agent(task: AtomTask, messages: list[dict[str, Any]]) -> dict[str, Any]:
            sub_agent = f"{task.chief_agent}-sub-{task.task_id[:4]}"
            await claim_task(task, sub_agent)
            await emit(ToolCallEvent(
                phase=ToolCallPhase.AGENT_START,
                task_id=task.task_id,
                tool_name="agent_dispatch",
                from_agent=task.chief_agent,
                to_agent=sub_agent,
                message=f"子Agent启动 ({task.agent_kind})",
            ))

            # 有上下文复用 agent：注入主管对任务要求；即用即删 agent：给出简短清洁目标
            work_messages = list(messages)
            if task.agent_kind == "reusable":
                work_messages.append({
                    "role": "system",
                    "content": (
                        f"你是 {sub_agent}，由 {task.chief_agent} 指派。"
                        "复用现有上下文，优先延续之前决策，完成当前任务。"
                    ),
                })
            else:
                work_messages = [{
                    "role": "system",
                    "content": (
                        f"你是临时执行Agent {sub_agent}。"
                        "仅根据当前任务最小化执行，输出简洁结果，不携带冗余上下文。"
                    ),
                }, {"role": "user", "content": task.description}]

            result = await self.executor.execute(task, work_messages, on_tool_event=on_tool_event)

            mail = self.mailbox.send(
                from_agent=sub_agent,
                to_agent=task.chief_agent,
                subject=f"任务{task.task_id[:6]}结果",
                content=result.get("text", "")[:500],
            )
            await emit(ToolCallEvent(
                phase=ToolCallPhase.MAIL_SEND,
                task_id=task.task_id,
                tool_name="mailbox",
                from_agent=sub_agent,
                to_agent=task.chief_agent,
                message=f"{mail['subject']}",
            ))
            await emit(ToolCallEvent(
                phase=ToolCallPhase.AGENT_END,
                task_id=task.task_id,
                tool_name="agent_dispatch",
                from_agent=sub_agent,
                to_agent=task.chief_agent,
                message="子Agent结束",
            ))
            return result

        for _level_idx, level in enumerate(plan.execution_order):
            # 同层级可以并行执行
            level_tasks = [t for t in plan.tasks if t.task_id in level]
            for task in level_tasks:
                await publish_task(task)

            # 先收集并合并上一阶段异步结果（依赖已满足前）
            if running_async_tasks:
                done_keys = [k for k, v in running_async_tasks.items() if v.done()]
                for tid in done_keys:
                    r = await running_async_tasks[tid]
                    running_async_tasks.pop(tid, None)
                    results[tid] = r
                    report.tool_call_logs.extend(r.get("tool_logs", []))
                    if r.get("text"):
                        global_messages.append({
                            "role": "assistant",
                            "content": f"[异步步骤完成]\n{r['text']}",
                        })

            if len(level_tasks) == 1:
                # 单任务：直接执行
                task = level_tasks[0]
                if task.dispatch_mode == "async":
                    await emit(ToolCallEvent(
                        phase=ToolCallPhase.SUBTASK_SCHEDULE,
                        task_id=task.task_id,
                        tool_name="async_dispatch",
                        from_agent="leader",
                        to_agent=task.chief_agent,
                        message="异步派发子任务",
                    ))
                    running_async_tasks[task.task_id] = asyncio.create_task(
                        run_with_agent(task, list(global_messages))
                    )
                else:
                    result = await run_with_agent(task, list(global_messages))
                    results[task.task_id] = result
                    report.tool_call_logs.extend(result.get("tool_logs", []))
                    mails = self.mailbox.recv_all(task.chief_agent)
                    for mail in mails:
                        await emit(ToolCallEvent(
                            phase=ToolCallPhase.MAIL_RECV,
                            task_id=task.task_id,
                            tool_name="mailbox",
                            from_agent=mail["from"],
                            to_agent=mail["to"],
                            message=f"收到邮件: {mail['subject']}",
                        ))
                    if result.get("text"):
                        global_messages.append({
                            "role": "assistant",
                            "content": f"[步骤完成: {task.description}]\n{result['text']}"
                        })
                    await close_task(task, bool(result.get("success", False)), result.get("error", ""))
            else:
                # 多任务并行
                sync_tasks = [t for t in level_tasks if t.dispatch_mode != "async"]
                async_tasks = [t for t in level_tasks if t.dispatch_mode == "async"]

                for task in async_tasks:
                    await emit(ToolCallEvent(
                        phase=ToolCallPhase.SUBTASK_SCHEDULE,
                        task_id=task.task_id,
                        tool_name="async_dispatch",
                        from_agent="leader",
                        to_agent=task.chief_agent,
                        message="异步派发子任务",
                    ))
                    running_async_tasks[task.task_id] = asyncio.create_task(
                        run_with_agent(task, list(global_messages))
                    )

                coros = [run_with_agent(t, list(global_messages)) for t in sync_tasks]
                level_results = await asyncio.gather(*coros) if coros else []
                for task, result in zip(sync_tasks, level_results):
                    results[task.task_id] = result
                    report.tool_call_logs.extend(result.get("tool_logs", []))
                    mails = self.mailbox.recv_all(task.chief_agent)
                    for mail in mails:
                        await emit(ToolCallEvent(
                            phase=ToolCallPhase.MAIL_RECV,
                            task_id=task.task_id,
                            tool_name="mailbox",
                            from_agent=mail["from"],
                            to_agent=mail["to"],
                            message=f"收到邮件: {mail['subject']}",
                        ))
                    if result.get("text"):
                        global_messages.append({
                            "role": "assistant",
                            "content": f"[步骤完成: {task.description}]\n{result['text']}"
                        })
                    await close_task(task, bool(result.get("success", False)), result.get("error", ""))

        # 收敛所有未完成异步任务（任务关闭协议）
        if running_async_tasks:
            done = await asyncio.gather(*running_async_tasks.values(), return_exceptions=True)
            for tid, r in zip(list(running_async_tasks.keys()), done):
                task = next((t for t in plan.tasks if t.task_id == tid), None)
                if isinstance(r, Exception):
                    results[tid] = {
                        "task_id": tid,
                        "success": False,
                        "error": str(r),
                        "text": "",
                        "tool_logs": [],
                    }
                    if task:
                        await close_task(task, False, str(r))
                    continue
                results[tid] = r
                report.tool_call_logs.extend(r.get("tool_logs", []))
                if r.get("text"):
                    global_messages.append({
                        "role": "assistant",
                        "content": f"[异步步骤完成]\n{r['text']}",
                    })
                if task:
                    await emit(ToolCallEvent(
                        phase=ToolCallPhase.SUBTASK_RESULT,
                        task_id=tid,
                        tool_name="async_dispatch",
                        from_agent=task.chief_agent,
                        to_agent="leader",
                        message=f"异步结果回收: {'成功' if r.get('success') else '失败'}",
                    ))
                    await close_task(task, bool(r.get("success", False)), r.get("error", ""))

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
