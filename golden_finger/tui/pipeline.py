"""流水线模式：五域流式执行 + ProgressBar + 右侧面板更新。"""

from __future__ import annotations

import asyncio
import time
from typing import TYPE_CHECKING, Any, Callable

from textual.widgets import RichLog, ProgressBar

from ..logging import log_system, log_step
from .constants import PIPELINE_ICONS

if TYPE_CHECKING:
    from .app import GoldenFingerApp
    from .widgets import TaskPanel, AgentPanel
    from .chat import DebouncedWriter


class PipelineHandler:
    """管理五域流水线执行：分析→执行→校验→沉淀，渲染进度和事件。"""

    def __init__(self, app: GoldenFingerApp) -> None:
        self._app = app

    async def run(self, query: str, log: RichLog, writer: DebouncedWriter) -> object | None:
        assert self._app.harness is not None
        execution_report = None
        self._hide_panels()
        progress_bar = self._app.query_one("#pipeline-progress", ProgressBar)
        progress_bar.add_class("-visible")

        try:
            async for event in self._app.harness.run_query_stream(query):
                domain = str(event.get("domain", ""))
                status = str(event.get("status", ""))

                if status == "started":
                    label = PIPELINE_ICONS.get(domain, domain)
                    await writer.write(f"[bold #cba6f7]⏳ {label}中...[/]")
                    self._app._update_status_text(f"⏳ {label}...")
                    await asyncio.sleep(0)
                elif status == "completed":
                    progress_bar.advance(1)
                    result = await self._handle_domain_completed(event, domain, log, writer)
                    if domain == "execution":
                        execution_report = result
                elif status == "error":
                    await writer.write(f"[#f38ba8]  ✗ {event.get('error', '未知错误')}[/]")
                    self._app._update_status_text("✗ 流水线出错")
                elif status == "tool_call":
                    await self._handle_tool_call(event.get("event", {}), log, writer)
                elif domain == "complete":
                    await writer.write("[bold #cba6f7]━━━ 修炼结束 ━━━[/]")
                    log_step("流水线执行完成", mode="tui_pipeline", status="complete")
                    if execution_report:
                        try:
                            final = self._app.harness.get_final_response(execution_report)
                            if final and final != "（无输出）":
                                await writer.write(f"[bold #94e2d5]◆ 金手指[/]  {final}")
                        except Exception:
                            pass
                    self._app._update_status_text("✦ 就绪")
                await asyncio.sleep(0)
        except Exception as e:
            await writer.write(f"[#f38ba8]走火入魔: {e}[/]")
            self._app._update_status_text("✗ 流水线错误")
            log_system(f"流水线执行异常: {e}", level="ERROR")
        finally:
            progress_bar.remove_class("-visible")

        return execution_report

    async def _handle_domain_completed(
        self, event: dict[str, Any], domain: str, log: RichLog, writer: DebouncedWriter
    ) -> Any:
        if domain == "analysis":
            plan = event.get("plan")
            if plan:
                await writer.write(f"[#a6e3a1]  ✓ 拆解为 {len(plan.tasks)} 个任务[/]")
                try:
                    tp = self._app.query_one("#task-panel")
                    tp.update_tasks(plan)
                    self._show_panel("#task-panel")
                except Exception:
                    pass
        elif domain == "execution":
            report = event.get("report")
            if report:
                await writer.write(
                    f"[#a6e3a1]  ✓ 执行完成 ({report.total_duration_ms}ms)[/]"
                )
                for a in report.anomalies:
                    await writer.write(f"[#f9e2af]    ⚠ {a}[/]")
                try:
                    tp = self._app.query_one("#task-panel")
                    for tid, t_res in report.task_results.items():
                        tp.set_task_status(tid, "completed" if t_res.get("success") else "error")
                except Exception:
                    pass
            return report
        elif domain == "verification":
            ver = event.get("verification")
            if ver:
                color = "#a6e3a1" if ver.overall_pass else "#f38ba8"
                act = "通过" if ver.overall_pass else ver.action
                await writer.write(f"[{color}]  ✓ 校验: {act}[/]")
        elif domain == "persistence":
            await writer.write("[#a6e3a1]  ✓ 经验已沉淀[/]")
        return None

    async def _handle_tool_call(
        self, ev: dict[str, Any], log: RichLog, writer: DebouncedWriter
    ) -> None:
        task_id = ev.get("task_id", "")
        phase = ev.get("phase", "")
        tp = self._safe_panel("task-panel")
        ap = self._safe_panel("agent-panel")

        handler = PHASE_HANDLERS.get(phase)
        if handler:
            await handler(self, ev, task_id, tp, ap, log, writer)

    # ---- 面板管理 ----

    def _show_panel(self, panel_id: str) -> None:
        try:
            self._app.query_one(panel_id).add_class("-visible")
            self._app.query_one("#right-pane").add_class("-visible")
            self._app.query_one("#left-pane").add_class("-has-right")
        except Exception:
            pass

    def _hide_panels(self) -> None:
        for pid in ("#task-panel", "#agent-panel", "#right-pane"):
            try:
                self._app.query_one(pid).remove_class("-visible")
            except Exception:
                pass
        try:
            self._app.query_one("#left-pane").remove_class("-has-right")
        except Exception:
            pass

    def _safe_panel(self, panel_id: str):
        try:
            return self._app.query_one(panel_id)
        except Exception:
            return None


# ---- 阶段分发（替代 if/elif 链）----

type _PH = Callable[
    [PipelineHandler, dict[str, Any], str, object, object, RichLog, "DebouncedWriter"],
    None,
]


async def _ph_task_publish(
    h: PipelineHandler, ev: dict[str, Any], task_id: str, tp: Any, ap: Any,
    log: RichLog, writer: DebouncedWriter,
) -> None:
    h._show_panel("#agent-panel")
    msg = f"[#89b4fa]📌 发布任务[/] {task_id} → {ev.get('to_agent', '')}"
    await writer.write(f"    {msg}")
    if ap:
        ap.write_log(msg)


async def _ph_task_claim(
    h: PipelineHandler, ev: dict[str, Any], task_id: str, tp: Any, ap: Any,
    log: RichLog, writer: DebouncedWriter,
) -> None:
    h._show_panel("#agent-panel")
    if tp and task_id:
        tp.set_task_status(task_id, "running")
    msg = f"[#f9e2af]👤 领取任务[/] {ev.get('to_agent', '')} ({ev.get('message', '')})"
    await writer.write(f"    {msg}")
    if ap:
        ap.write_log(msg)


async def _ph_agent_start(
    h: PipelineHandler, ev: dict[str, Any], task_id: str, tp: Any, ap: Any,
    log: RichLog, writer: DebouncedWriter,
) -> None:
    h._show_panel("#agent-panel")
    msg = f"[#94e2d5]🚀 {ev.get('from_agent', '')}[/] {ev.get('message', '')}"
    await writer.write(f"    {msg}")
    if ap:
        ap.write_log(msg)


async def _ph_agent_end(
    h: PipelineHandler, ev: dict[str, Any], task_id: str, tp: Any, ap: Any,
    log: RichLog, writer: DebouncedWriter,
) -> None:
    h._show_panel("#agent-panel")
    msg = f"[#a6e3a1]🏁 {ev.get('from_agent', '')}[/] {ev.get('message', '')}"
    await writer.write(f"    {msg}")
    if ap:
        ap.write_log(msg)


async def _ph_mail_send(
    h: PipelineHandler, ev: dict[str, Any], task_id: str, tp: Any, ap: Any,
    log: RichLog, writer: DebouncedWriter,
) -> None:
    h._show_panel("#agent-panel")
    msg = (
        f"[#cba6f7]✉️ {ev.get('from_agent', '')} → {ev.get('to_agent', '')}[/]"
        f" {ev.get('message', '')}"
    )
    await writer.write(f"    {msg}")
    if ap:
        ap.write_log(msg)


async def _ph_mail_recv(
    h: PipelineHandler, ev: dict[str, Any], task_id: str, tp: Any, ap: Any,
    log: RichLog, writer: DebouncedWriter,
) -> None:
    h._show_panel("#agent-panel")
    msg = (
        f"[#89b4fa]📥 {ev.get('from_agent', '')} → {ev.get('to_agent', '')}[/]"
        f" {ev.get('message', '')}"
    )
    await writer.write(f"    {msg}")
    if ap:
        ap.write_log(msg)


async def _ph_subtask_schedule(
    h: PipelineHandler, ev: dict[str, Any], task_id: str, tp: Any, ap: Any,
    log: RichLog, writer: DebouncedWriter,
) -> None:
    h._show_panel("#agent-panel")
    if tp and task_id:
        tp.set_task_status(task_id, "running")
    msg = f"[#f9e2af]🧵 异步派发[/] {task_id}"
    await writer.write(f"    {msg}")
    if ap:
        ap.write_log(msg)


async def _ph_subtask_result(
    h: PipelineHandler, ev: dict[str, Any], task_id: str, tp: Any, ap: Any,
    log: RichLog, writer: DebouncedWriter,
) -> None:
    h._show_panel("#agent-panel")
    if tp and task_id:
        msg_text = ev.get("message", "")
        tp.set_task_status(task_id, "error" if "失败" in msg_text else "completed")
    msg = f"[#a6e3a1]📬 异步回收[/] {task_id} {ev.get('message', '')}"
    await writer.write(f"    {msg}")
    if ap:
        ap.write_log(msg)


async def _ph_task_close(
    h: PipelineHandler, ev: dict[str, Any], task_id: str, tp: Any, ap: Any,
    log: RichLog, writer: DebouncedWriter,
) -> None:
    h._show_panel("#agent-panel")
    if tp and task_id:
        tp.set_task_status(task_id, "completed")
    msg = f"[#a6e3a1]🔒 任务关闭[/] {task_id}"
    await writer.write(f"    {msg}")
    if ap:
        ap.write_log(msg)


async def _ph_task_force_close(
    h: PipelineHandler, ev: dict[str, Any], task_id: str, tp: Any, ap: Any,
    log: RichLog, writer: DebouncedWriter,
) -> None:
    h._show_panel("#agent-panel")
    if tp and task_id:
        tp.set_task_status(task_id, "error")
    msg = f"[#f38ba8]🛑 强制关闭[/] {task_id} {ev.get('message', '')}"
    await writer.write(f"    {msg}")
    if ap:
        ap.write_log(msg)


async def _ph_tool_start(
    h: PipelineHandler, ev: dict[str, Any], task_id: str, tp: Any, ap: Any,
    log: RichLog, writer: DebouncedWriter,
) -> None:
    h._show_panel("#agent-panel")
    if tp and task_id:
        tp.set_task_status(task_id, "running")
    ps = ", ".join(f"{k}={str(v)[:40]!r}" for k, v in ev.get("params", {}).items())
    msg = f"[#f9e2af]⚙ {ev['tool_name']}({ps})[/]"
    await writer.write(f"    {msg}")
    if ap:
        ap.write_log(msg)


async def _ph_tool_end(
    h: PipelineHandler, ev: dict[str, Any], task_id: str, tp: Any, ap: Any,
    log: RichLog, writer: DebouncedWriter,
) -> None:
    if ev.get("error"):
        if tp and task_id:
            tp.set_task_status(task_id, "error")
        msg = f"[#f38ba8]✗ {str(ev['error'])[:200]}[/]"
    else:
        pv = str(ev.get("result", ""))[:150].replace("\n", " ")
        msg = f"[#a6e3a1]✓ {ev.get('duration_ms', 0)}ms | {pv}[/]"
    await writer.write(f"      {msg}")
    if ap:
        ap.write_log(msg)


PHASE_HANDLERS: dict[str, _PH] = {
    "task_publish": _ph_task_publish,
    "task_claim": _ph_task_claim,
    "agent_start": _ph_agent_start,
    "agent_end": _ph_agent_end,
    "mail_send": _ph_mail_send,
    "mail_recv": _ph_mail_recv,
    "subtask_schedule": _ph_subtask_schedule,
    "subtask_result": _ph_subtask_result,
    "task_close": _ph_task_close,
    "task_force_close": _ph_task_force_close,
    "tool_start": _ph_tool_start,
    "tool_end": _ph_tool_end,
}
