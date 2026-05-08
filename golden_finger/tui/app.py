"""Golden Finger 终端 TUI — 现代 Chat 风格 (Textual)"""

import asyncio
import json
from datetime import datetime
from typing import Any

from rich.text import Text as RichText
from textual import work
from textual.app import App, ComposeResult
from textual.binding import Binding
from textual.containers import Container
from textual.widgets import Input, RichLog, Static

from ..harness import GoldenFingerHarness
from ..config import config
from ..llm import LLMClient
from ..tools.builtin import BUILTIN_TOOLS
from ..domain_execution import ToolExecutionGuard


class StatusBar(Static):
    """底部状态栏"""

    def compose(self) -> ComposeResult:
        yield Static(id="status-info")


class GoldenFingerApp(App[None]):
    """金手指终端 TUI — 现代 Chat 风格"""

    CSS_PATH = "tui.tcss"
    AUTO_FOCUS = "#query-input"

    BINDINGS = [
        Binding("ctrl+d", "quit", "退出", show=False),
        Binding("ctrl+c", "quit", "", show=False),
        Binding("ctrl+s", "save_session", "保存", show=False),
        Binding("ctrl+m", "export_log", "导出", show=False),
        Binding("escape", "focus_input", "聚焦输入", show=False),
        Binding("up", "history_prev", "", show=False),
        Binding("down", "history_next", "", show=False),
    ]

    def __init__(self) -> None:
        super().__init__()
        self.harness: GoldenFingerHarness | None = None
        self.llm: LLMClient | None = None
        self.input_history: list[str] = []
        self.history_index: int = -1
        self.current_input: str = ""
        self.is_processing: bool = False
        self._pipeline_mode: bool = False

    # ---- Lifecycle ----

    async def on_mount(self) -> None:
        self.harness = GoldenFingerHarness()
        self.llm = LLMClient()
        log = self.query_one("#chat-log", RichLog)
        s = self.harness.get_status()
        log.write("[bold #c084fc]✦ 金手指 Agent System 已就绪[/]")
        log.write(
            f"[dim #6b7280]模型: {config.openai_model}"
            f"  |  境界: {s['realm']}·{s['realm_stage']}[/]"
        )
        log.write(
            "[dim #6b7280]输入问题开始修炼  ·  /help 查看指令  ·  ↑↓ 历史[/]"
        )
        log.write("")
        self._update_status()
        self.query_one("#query-input", Input).focus()

        # 后台预热向量数据库（首次运行需下载 ONNX 模型 ~79MB）
        self._pipeline_mode = False
        self._warm_chromadb(log)

    # ---- ChromaDB 预热 ----

    @work(exclusive=False)
    async def _warm_chromadb(self, log: RichLog) -> None:
        """后台预热向量数据库（首次需下载 ONNX 嵌入模型 ~79MB）"""
        status = self.query_one("#status-info", Static)
        status.update(" ⏳ 初始化向量数据库...")

        try:
            import os
            if "HF_ENDPOINT" not in os.environ:
                os.environ["HF_ENDPOINT"] = "https://hf-mirror.com"

            from ..storage.vector_store import vector_store

            log.write(
                "[dim #6b7280]⏳ 向量数据库初始化中..."
                " 首次运行需下载嵌入模型 (~79MB)，请耐心等待[/]"
            )

            loop = asyncio.get_event_loop()
            # 最多等待 120 秒，超时则跳过
            await asyncio.wait_for(
                loop.run_in_executor(None, vector_store.warm_up),
                timeout=120,
            )

            count: int = vector_store.count()
            log.write(f"[#34d399]✓ 向量数据库就绪 ({count} 条记忆)[/]")
            log.write("")
        except asyncio.TimeoutError:
            log.write(
                "[#fbbf24]⚠ 向量数据库初始化超时 (下载过慢)[/]"
            )
            log.write(
                "[dim #6b7280]  可手动下载 ONNX 模型到 ChromaDB 缓存目录，"
                "或设置 HF_ENDPOINT 环境变量[/]"
            )
            log.write("")
        except Exception as e:
            log.write(
                f"[#fbbf24]⚠ 向量数据库初始化失败: {str(e)[:200]}[/]"
            )
            log.write(
                "[dim #6b7280]  技能匹配功能暂不可用，其他功能正常[/]"
            )
            log.write("")
        finally:
            self._update_status()

    async def on_unmount(self) -> None:
        if self.harness:
            await self.harness.close()
        if self.llm:
            await self.llm.close()

    # ---- Layout ----

    def compose(self) -> ComposeResult:
        with Container(id="chat-area"):
            yield RichLog(
                id="chat-log",
                highlight=True,
                markup=True,
                wrap=True,
                auto_scroll=True,
                max_lines=10_000,
            )
        with Container(id="input-bar"):
            yield Input(
                placeholder="宿主> 输入问题或指令...",
                id="query-input",
            )
        yield StatusBar(id="status-bar")

    # ---- Input ----

    async def on_input_submitted(self, event: Input.Submitted) -> None:
        query: str = event.value.strip()
        if not query:
            return

        if not self.input_history or self.input_history[-1] != query:
            self.input_history.append(query)
        self.history_index = len(self.input_history)
        self.current_input = ""

        inp = self.query_one("#query-input", Input)
        inp.value = ""
        inp.disabled = True

        if query.startswith("/"):
            await self._handle_command(query)
            inp.disabled = False
            inp.focus()
            return

        log = self.query_one("#chat-log", RichLog)
        log.write(f"[bold #fbbf24]宿主>[/] {query}")

        self.is_processing = True
        self._update_status()
        self._handle_chat(query)

    # ---- Slash Commands ----

    async def _handle_command(self, cmd: str) -> None:
        log = self.query_one("#chat-log", RichLog)
        parts: list[str] = cmd.split(maxsplit=1)
        action: str = parts[0].lower()
        arg: str = parts[1] if len(parts) > 1 else ""

        if action == "/help":
            log.write("[bold #c084fc]✦ 可用指令[/]")
            for item in [
                "/help            显示此帮助",
                "/status          显示宿主状态",
                "/clear           清空对话",
                "/save  [name]    保存会话",
                "/export          导出对话日志",
                "/pipeline        切换至完整五域流水线",
                "",
                "Ctrl+S           保存会话",
                "Ctrl+M           导出日志",
                "↑/↓              历史命令",
                "Esc              聚焦输入框",
                "Ctrl+D           退出",
            ]:
                log.write(f"[dim #8b949e]  {item}[/]")
            log.write("")
        elif action == "/status":
            assert self.harness is not None
            s: dict[str, Any] = self.harness.get_status()
            log.write("[bold #c084fc]✦ 宿主状态[/]")
            log.write(f"  [dim]灵魂印记:[/] {s['soul_mark'][:12]}")
            log.write(
                f"  [dim]修炼境界:[/] {s['realm']} {s['realm_stage']}"
                f" ({s['realm_progress']})"
            )
            log.write(f"  [dim]已完成:[/] {s['total_tasks']} 任务")
            log.write(f"  [dim]修炼时间:[/] {s['total_time_min']} 分钟")
            log.write(f"  [dim]Skill:[/] {len(s['skills'])} 个")
            log.write(f"  [dim]LLM:[/] {config.openai_model}")
            log.write("")
        elif action == "/clear":
            log.clear()
        elif action == "/save":
            await self._save_session(arg)
        elif action == "/export":
            await self._export_log()
        elif action == "/pipeline":
            self._pipeline_mode = not self._pipeline_mode
            mode_status: str = "已启用" if self._pipeline_mode else "已关闭"
            log.write(f"[#fbbf24]五域流水线模式: {mode_status}[/]")
        else:
            log.write(f"[#ef4444]未知指令: {action}，输入 /help 查看[/]")

    # ---- Chat Handler ----

    @work(exclusive=False)
    async def _handle_chat(self, query: str) -> None:
        log = self.query_one("#chat-log", RichLog)
        try:
            if self._pipeline_mode:
                await self._run_pipeline(query, log)
            else:
                await self._chat_loop(query, log)
        except Exception as e:
            log.write(f"[bold #ef4444]✗ 出错: {e}[/]")
        finally:
            self.is_processing = False
            self._update_status()
            inp = self.query_one("#query-input", Input)
            inp.disabled = False
            inp.focus()

    async def _chat_loop(self, query: str, log: RichLog) -> None:
        """LLM ↔ Tool 循环（每轮一次 LLM 调用）"""
        assert self.llm is not None

        messages: list[dict[str, Any]] = [
            {
                "role": "system",
                "content": (
                    "你是金手指(GoldenFinger)，一个运行在终端TUI中的AI编程助手。"
                    "使用中文回复。可以调用工具：读文件、写文件、执行命令、搜索网页。"
                    "代码用 ``` 语法高亮。回复简洁，重点突出。"
                ),
            },
            {"role": "user", "content": query},
        ]
        tool_schemas: list[dict[str, Any]] = [
            t.to_openai_schema() for t in BUILTIN_TOOLS.values()
        ]
        max_rounds: int = 6

        for round_num in range(max_rounds):
            label: str = "金手指>" if round_num == 0 else "  …>"
            log.write(f"[bold #6ee7b7]{label}[/] ")

            try:
                resp: dict[str, Any] = await self.llm.chat(
                    messages=messages,
                    tools=tool_schemas,
                    max_tokens=4096,
                )
            except Exception as e:
                log.write(f"[#ef4444]LLM 调用失败: {e}[/]")
                return

            text: str = self.llm.extract_text(resp)
            tool_calls: list[dict[str, Any]] = self.llm.extract_tool_calls(resp)

            # 显示文本
            if text:
                log.write(RichText(text))
            if not tool_calls:
                log.write("")
                break

            log.write("")  # 换行

            # 构建 assistant 消息
            assistant_msg: dict[str, Any] = {
                "role": "assistant",
                "content": text or None,
            }
            if tool_calls:
                assistant_msg["tool_calls"] = tool_calls
            messages.append(assistant_msg)

            # 执行工具
            for tc in tool_calls:
                func: dict[str, Any] = tc.get("function", {})
                tool_name: str = str(func.get("name", ""))
                params_str: str = func.get("arguments", "{}")
                params: dict[str, Any] = {}
                try:
                    params = json.loads(params_str)
                except json.JSONDecodeError:
                    pass

                preview: str = ", ".join(
                    f"{k}={str(v)[:40]!r}" for k, v in params.items()
                )
                log.write(f"[#fbbf24]  ⚙ {tool_name}({preview})[/]")

                tool_log = await ToolExecutionGuard.execute_tool(tool_name, params)

                if tool_log.success and tool_log.result is not None:
                    result_preview: str = (
                        str(tool_log.result)[:250].replace("\n", " ")
                    )
                    log.write(
                        f"[#34d399]    ✓ {tool_log.duration_ms}ms"
                        f" | {result_preview}[/]"
                    )
                elif tool_log.success:
                    log.write(f"[#34d399]    ✓ {tool_log.duration_ms}ms[/]")
                else:
                    log.write(f"[#ef4444]    ✗ {tool_log.error}[/]")

                result_content: str = (
                    json.dumps(tool_log.result, ensure_ascii=False)
                    if tool_log.success and tool_log.result
                    else (str(tool_log.error) or "(无输出)")
                )
                messages.append({
                    "role": "tool",
                    "tool_call_id": str(tc.get("id", "tool")),
                    "content": result_content,
                })

        log.write("")

    async def _run_pipeline(self, query: str, log: RichLog) -> None:
        """完整五域流水线（/pipeline 模式）"""
        assert self.harness is not None
        # 缓存执行报告，避免在 complete 阶段重复执行流水线
        execution_report = None
        try:
            async for event in self.harness.run_query_stream(query):
                domain: str = str(event.get("domain", ""))
                status: str = str(event.get("status", ""))

                if status == "started":
                    icons: dict[str, str] = {
                        "analysis": "🔮 天机推演",
                        "execution": "⚡ 施法执行",
                        "verification": "🔍 验道校验",
                        "persistence": "📝 刻碑沉淀",
                    }
                    log.write(
                        f"[bold #c084fc]{icons.get(domain, domain)}中...[/]"
                    )
                elif status == "completed":
                    if domain == "analysis":
                        plan = event.get("plan")
                        if plan is not None:
                            log.write(
                                f"[#34d399]  ✓ 拆解为 {len(plan.tasks)} 个任务[/]"
                            )
                    elif domain == "execution":
                        report = event.get("report")
                        if report is not None:
                            execution_report = report
                            log.write(
                                f"[#34d399]  ✓ 执行完成 "
                                f"({report.total_duration_ms}ms)[/]"
                            )
                            for a in report.anomalies:
                                log.write(f"[#fbbf24]    ⚠ {a}[/]")
                    elif domain == "verification":
                        ver = event.get("verification")
                        if ver is not None:
                            color: str = (
                                "#34d399" if ver.overall_pass else "#ef4444"
                            )
                            act: str = "通过" if ver.overall_pass else ver.action
                            log.write(f"[{color}]  ✓ 校验: {act}[/]")
                    elif domain == "persistence":
                        log.write("[#34d399]  ✓ 经验已沉淀[/]")
                elif status == "error":
                    log.write(
                        f"[#ef4444]  ✗ {event.get('error', '未知错误')}[/]"
                    )
                elif status == "tool_call":
                    ev: dict[str, Any] = event["event"]
                    if ev["phase"] == "tool_start":
                        ps: str = ", ".join(
                            f"{k}={str(v)[:40]!r}"
                            for k, v in ev.get("params", {}).items()
                        )
                        log.write(
                            f"[#fbbf24]    ⚙ {ev['tool_name']}({ps})[/]"
                        )
                    elif ev["phase"] == "tool_end":
                        if ev.get("error"):
                            log.write(
                                f"[#ef4444]      ✗ {str(ev['error'])[:200]}[/]"
                            )
                        else:
                            pv: str = (
                                str(ev.get("result", ""))[:150].replace("\n", " ")
                            )
                            log.write(
                                f"[#34d399]      ✓ "
                                f"{ev.get('duration_ms', 0)}ms | {pv}[/]"
                            )
                elif domain == "complete":
                    log.write(
                        "[bold #c084fc]━━━ 修炼结束 ━━━[/]"
                    )
                    # 从缓存的执行报告提取最终回复，不再重复执行流水线
                    if execution_report:
                        try:
                            final_text: str = self.harness.get_final_response(execution_report)
                            if final_text and final_text != "（无输出）":
                                log.write(
                                    f"[bold #6ee7b7]金手指>[/] {final_text}"
                                )
                        except Exception:
                            pass
                await asyncio.sleep(0)
        except Exception as e:
            log.write(f"[#ef4444]走火入魔: {e}[/]")

    # ---- History ----

    def action_history_prev(self) -> None:
        if not self.input_history:
            return
        inp = self.query_one("#query-input", Input)
        if self.history_index == len(self.input_history):
            self.current_input = inp.value
        if self.history_index > 0:
            self.history_index -= 1
            inp.value = self.input_history[self.history_index]
            inp.action_end()

    def action_history_next(self) -> None:
        if not self.input_history:
            return
        inp = self.query_one("#query-input", Input)
        if self.history_index < len(self.input_history) - 1:
            self.history_index += 1
            inp.value = self.input_history[self.history_index]
            inp.action_end()
        elif self.history_index == len(self.input_history) - 1:
            self.history_index = len(self.input_history)
            inp.value = self.current_input
            inp.action_end()

    # ---- Session I/O ----

    async def _save_session(self, name: str = "") -> None:
        log = self.query_one("#chat-log", RichLog)
        ts: str = datetime.now().strftime("%Y%m%d_%H%M%S")
        filename: str = f"session_{name or ts}.txt"
        path = config.logs_dir / filename
        try:
            content: str = "\n".join(str(line) for line in log.lines)
            path.write_text(content, encoding="utf-8")
            log.write(f"[#34d399]✓ 会话已保存: {path}[/]")
        except Exception as e:
            log.write(f"[#ef4444]✗ 保存失败: {e}[/]")

    async def _export_log(self) -> None:
        log = self.query_one("#chat-log", RichLog)
        ts: str = datetime.now().strftime("%Y%m%d_%H%M%S")
        path = config.logs_dir / f"export_{ts}.md"
        try:
            content: str = "\n".join(str(line) for line in log.lines)
            path.write_text(content, encoding="utf-8")
            log.write(f"[#34d399]✓ 日志已导出: {path}[/]")
        except Exception as e:
            log.write(f"[#ef4444]✗ 导出失败: {e}[/]")

    def action_save_session(self) -> None:
        self.run_worker(self._save_session(), exclusive=False)

    def action_export_log(self) -> None:
        self.run_worker(self._export_log(), exclusive=False)

    def action_focus_input(self) -> None:
        self.query_one("#query-input", Input).focus()

    # ---- Status Bar ----

    def _update_status(self) -> None:
        s: dict[str, Any] = (
            self.harness.get_status() if self.harness else {}
        )
        label: str = "⚡ 处理中" if self.is_processing else "✦ 就绪"
        mode: str = "流水线" if self._pipeline_mode else "Chat"
        status = self.query_one("#status-info", Static)
        status.update(
            f" {label} | "
            f"{s.get('realm', '凡人')}·{s.get('realm_stage', '初阶')} | "
            f"{config.openai_model} | "
            f"模式: {mode} | "
            f"Ctrl+D 退出 · ↑↓ 历史 · /help 指令"
        )
