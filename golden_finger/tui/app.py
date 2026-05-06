"""Golden Finger 终端 TUI — Textual 应用"""

import asyncio
import os
from datetime import datetime

from textual.app import App, ComposeResult
from textual.binding import Binding
from textual.containers import Container
from textual.widgets import Footer, Header, Input, Markdown, RichLog

from ..harness import GoldenFingerHarness
from ..config import config


class GoldenFingerApp(App):
    """金手指终端 TUI"""

    CSS_PATH = "tui.tcss"

    BINDINGS = [
        Binding("ctrl+d", "quit", "退出", show=True),
        Binding("ctrl+c", "quit", "", show=False),
        Binding("ctrl+e", "export_log", "导出日志", show=True),
    ]

    def __init__(self):
        super().__init__()
        self.harness: GoldenFingerHarness | None = None

    async def on_mount(self) -> None:
        self.harness = GoldenFingerHarness()
        log = self.query_one("#execution-log", RichLog)
        log.write("[bold yellow]✦ 金手指 Agent System 已就绪[/]")
        log.write(f"[dim]LLM: {config.llm_provider}[/]")
        log.write(f"[dim]数据: {config.data_dir}[/]")
        log.write("")
        log.write("[dim]输入问题开始修炼，Ctrl+D 退出，Ctrl+E 导出日志[/]")
        self._update_title()

    async def on_unmount(self) -> None:
        if self.harness:
            await self.harness.close()

    def compose(self) -> ComposeResult:
        yield Header(show_clock=True)
        yield RichLog(id="execution-log", highlight=True, markup=True, wrap=True)
        yield Markdown(id="final-output")
        with Container(id="input-area"):
            yield Input(
                placeholder="宿主> 输入你的问题，按 Enter 开始修炼...",
                id="query-input",
            )
        yield Footer()

    async def on_input_submitted(self, event: Input.Submitted) -> None:
        query = event.value.strip()
        if not query:
            return

        inp = self.query_one("#query-input", Input)
        inp.disabled = True
        inp.value = ""

        log = self.query_one("#execution-log", RichLog)
        log.clear()
        log.write(f"[bold]宿主>[/] {query}")
        log.write("")

        md = self.query_one("#final-output", Markdown)
        md.update("")

        asyncio.create_task(self._run_pipeline(query))

    async def _run_pipeline(self, query: str) -> None:
        assert self.harness is not None, "Harness not ready"

        log = self.query_one("#execution-log", RichLog)
        md = self.query_one("#final-output", Markdown)

        try:
            async for event in self.harness.run_query_stream(query):
                domain = event.get("domain", "")
                status = event.get("status", "")

                if domain == "analysis":
                    if status == "started":
                        log.write("[bold yellow]🔮 天机推演中...[/]")
                    elif status == "completed":
                        plan = event.get("plan")
                        if plan:
                            log.write(f"[yellow]  ✓ 拆解为 {len(plan.tasks)} 个原子任务[/]")
                            for lidx, level in enumerate(plan.execution_order):
                                tasks_in_level = [
                                    t for t in plan.tasks if t.task_id in level
                                ]
                                descs = ", ".join(
                                    f"[{t.matched_skill or 'general'}]{t.description[:50]}[/]"
                                    for t in tasks_in_level
                                )
                                log.write(f"    [dim]层级 {lidx + 1}: {descs}[/]")
                    elif status == "error":
                        log.write(f"[red]  ✗ 天机推演失败: {event['error']}[/]")

                elif domain == "execution":
                    if status == "started":
                        log.write("[bold cyan]⚡ 施法执行中...[/]")
                    elif status == "tool_call":
                        ev = event["event"]
                        phase = ev["phase"]
                        if phase == "tool_start":
                            params_str = ", ".join(
                                f"{k}={str(v)[:60]!r}"
                                for k, v in ev.get("params", {}).items()
                            )
                            log.write(
                                f"[yellow]    ⚙ [bold]{ev['tool_name']}[/]({params_str})[/]"
                            )
                        elif phase == "tool_end":
                            duration = ev.get("duration_ms", 0)
                            err = ev.get("error")
                            if err:
                                log.write(
                                    f"[red]      ✗ 失败 ({duration}ms): {str(err)[:200]}[/]"
                                )
                            else:
                                result = ev.get("result")
                                preview = str(result)[:200].replace("\n", " ") if result else "(无输出)"
                                log.write(
                                    f"[green]      ✓ ({duration}ms): {preview}[/]"
                                )
                    elif status == "completed":
                        report = event.get("report")
                        if report:
                            log.write(
                                f"[green]  ✓ 执行完成 ({report.total_duration_ms}ms)[/]"
                            )
                            for a in report.anomalies:
                                log.write(f"[yellow]    ⚠ {a}[/]")
                    elif status == "error":
                        log.write(f"[red]  ✗ 执行失败: {event['error']}[/]")

                elif domain == "verification":
                    if status == "started":
                        log.write("[bold magenta]🔍 验道校验中...[/]")
                    elif status == "completed":
                        ver = event.get("verification")
                        if ver:
                            s = "green" if ver.overall_pass else "red"
                            action = "通过" if ver.overall_pass else f"未通过 → {ver.action}"
                            log.write(f"[{s}]  ✓ 校验结果: {action}[/]")
                            if not ver.overall_pass:
                                for c in (
                                    ver.structure_checks
                                    + ver.content_checks
                                    + ver.replay_checks
                                ):
                                    if not c.passed:
                                        log.write(
                                            f"[red]    ✗ {c.check_name}: {c.detail}[/]"
                                        )
                    elif status == "error":
                        log.write(f"[red]  ✗ 校验失败: {event['error']}[/]")

                elif domain == "persistence":
                    if status == "started":
                        log.write("[bold green]📝 刻碑沉淀中...[/]")
                    elif status == "completed":
                        log.write("[green]  ✓ 经验已沉淀[/]")
                    elif status == "error":
                        log.write(f"[yellow]  ⚠ 沉淀异常: {event['error']}[/]")

                elif domain == "complete":
                    log.write("")
                    log.write("[bold cyan]━━━ 修炼结束 ━━━[/]")
                    try:
                        result = await self.harness.run_query(query)
                        if not result.get("error") and result.get("execution"):
                            text = self.harness.get_final_response(result["execution"])
                            if text and text != "（无输出）":
                                md.update(text)
                    except Exception:
                        pass

                await asyncio.sleep(0)

        except Exception as e:
            log.write(f"[red]走火入魔: {e}[/]")
        finally:
            inp = self.query_one("#query-input", Input)
            inp.disabled = False
            inp.focus()
            self._update_title()

    def _update_title(self) -> None:
        if not self.harness:
            return
        s = self.harness.get_status()
        self.sub_title = (
            f"✦ {s.get('soul_mark', 'N/A')[:8]} | "
            f"{s.get('realm', 'N/A')} {s.get('realm_stage', '')} | "
            f"已完成: {s.get('total_tasks', 0)}"
        )
