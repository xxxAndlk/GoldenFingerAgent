"""Golden Finger 终端 TUI — 现代 Chat 风格 (Textual)"""

import asyncio
import json
import os
import time
from datetime import datetime
from typing import Any, Optional

from rich.text import Text as RichText
from textual import work
from textual import events
from textual.app import App, ComposeResult
from textual.binding import Binding
from textual.containers import Container
from textual.widgets import Input, RichLog, Static
from textual.strip import Strip


class VisibleInput(Input):
    """Input with forced white-on-black text — bypasses CSS cascade entirely."""

    def render_line(self, y: int) -> Strip:
        strip = super().render_line(y)
        from rich.style import Style
        return strip.apply_style(Style(color="#ffffff", bgcolor="#000000"))

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
    """金手指终端 TUI"""

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
        Binding("ctrl+shift+v", "paste_multiline", "粘贴", show=False),
    ]

    def __init__(self) -> None:
        super().__init__()
        self.harness: GoldenFingerHarness | None = None
        self.llm: LLMClient | None = None
        self.input_history: list[str] = []
        self.history_index: int = -1
        self.current_input: str = ""
        self.is_processing: bool = False
        self._processing_timer: Optional[asyncio.Task] = None
        self._pasted_lines: int = 0
        self._pipeline_mode: bool = False

    # ---- Lifecycle ----

    async def on_mount(self) -> None:
        self.harness = GoldenFingerHarness()
        self.llm = LLMClient()
        log = self.query_one("#chat-log", RichLog)
        s = self.harness.get_status()
        realm_info = f"{s['realm']} · {s['realm_stage']}"
        spirit = s['spirit_root']['dominant']
        log.write("[bold #c084fc]╔══════════════════════════════════════════════╗[/]")
        log.write("[bold #c084fc]║    ✦ 金手指 Agent System 已就绪 ✦            ║[/]")
        log.write("[bold #c084fc]╟──────────────────────────────────────────────╢[/]")
        log.write(f"[bold #c084fc]║[/]  [dim #8b949e]模型: {config.openai_model}[/]")
        log.write(f"[bold #c084fc]║[/]  [dim #8b949e]境界: {realm_info}  |  灵根: {spirit}[/]")
        log.write(f"[bold #c084fc]║[/]  [dim #545d68]输入问题开始修炼  ·  /help 查看指令  ·  ↑↓ 历史[/]")
        log.write("[bold #c084fc]╚══════════════════════════════════════════════╝[/]")
        log.write("")
        self._update_status()
        inp = self.query_one("#query-input", Input)
        # Force high-contrast text via inline styles (highest CSS priority)
        self._force_input_colors(inp)
        inp.focus()

        self._pipeline_mode = False
        self._warm_chromadb(log)

    @staticmethod
    def _force_input_colors(inp: Input) -> None:
        """Force white-on-black text on input widget for all terminal types."""
        inp.styles.color = "#ffffff"
        inp.styles.background = "#000000"

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
            from pathlib import Path

            # Only show init message if ChromaDB is not already cached
            chroma_db = Path(config.memory_dir) / "chroma.sqlite3"
            if not (chroma_db.exists() and chroma_db.stat().st_size > 0):
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
            yield VisibleInput(
                placeholder="宿主> 输入问题或指令...",
                id="query-input",
            )
        yield StatusBar(id="status-bar")

    # ---- Input ----

    async def on_input_submitted(self, event: Input.Submitted) -> None:
        query: str = event.value.strip()
        if not query:
            return

        # 如果是粘贴的多行数据，直接作为查询发送
        if self._pasted_lines > 1:
            self._pasted_lines = 0

        if not self.input_history or self.input_history[-1] != query:
            self.input_history.append(query)
        self.history_index = len(self.input_history)
        self.current_input = ""

        inp = self.query_one("#query-input", Input)
        inp.value = ""
        inp.disabled = True

        # 安全锁：30 秒后强制恢复输入框
        self._cancel_processing_timer()
        self._processing_timer = asyncio.create_task(
            self._force_enable_input_after(45)
        )

        if query.startswith("/"):
            await self._handle_command(query)
            self._cancel_processing_timer()
            inp.disabled = False
            self._force_input_colors(inp)
            inp.focus()
            return

        log = self.query_one("#chat-log", RichLog)
        log.write(f"[bold #fbbf24]▸ 宿主[/]  {query}")

        self.is_processing = True
        self._update_status()
        self._handle_chat(query)

    async def _force_enable_input_after(self, seconds: float) -> None:
        """超时保护：强制恢复输入框"""
        await asyncio.sleep(seconds)
        try:
            inp = self.query_one("#query-input", Input)
            inp.disabled = False
            self._force_input_colors(inp)
            inp.focus()
            self.is_processing = False
        except Exception:
            pass

    def _cancel_processing_timer(self) -> None:
        """取消超时保护定时器"""
        if self._processing_timer and not self._processing_timer.done():
            self._processing_timer.cancel()
            self._processing_timer = None

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
        try:
            log = self.query_one("#chat-log", RichLog)
            if self._pipeline_mode:
                await self._run_pipeline(query, log)
            else:
                await self._chat_loop(query, log)
        except Exception as e:
            try:
                log.write(f"[bold #ef4444]✗ 出错: {e}[/]")
            except Exception:
                pass
        finally:
            self.is_processing = False
            self._cancel_processing_timer()
            # 恢复输入框 — 放最前面，确保执行
            try:
                inp = self.query_one("#query-input", Input)
                inp.disabled = False
                self._force_input_colors(inp)
                inp.focus()
            except Exception:
                pass
            try:
                self._update_status()
            except Exception:
                pass

    # ---- Paste / Clipboard ----

    def on_paste(self, event: events.Paste) -> None:
        """处理粘贴事件：多行文本折叠并显示行数提示"""
        # RichLog 或只读区域粘贴时不拦截
        focused = self.focused
        if focused is None or not isinstance(focused, Input):
            return

        text = event.text
        if not text:
            return

        # 统计有效行数（去掉首尾空行）
        stripped = text.strip()
        if not stripped:
            return

        lines = stripped.split("\n")
        line_count = len(lines)

        if line_count > 1:
            # 多行 → 合并为单行，通知用户
            self._pasted_lines = line_count
            single_line = " ".join(line.strip() for line in lines if line.strip())
            # 替换粘贴事件文本
            event.text = single_line
            # 在聊天日志中提示
            try:
                log = self.query_one("#chat-log", RichLog)
                log.write(
                    f"[dim #545d68]📋 粘贴了 {line_count} 行数据"
                    f"（{len(stripped)} 字符），已合并为一行[/]"
                )
            except Exception:
                pass
        else:
            self._pasted_lines = 0

    def action_paste_multiline(self) -> None:
        """Ctrl+Shift+V: 多行粘贴模式 — 打开临时编辑器"""
        try:
            inp = self.query_one("#query-input", Input)
            current = inp.value
            # 使用环境变量 EDITOR 或 notepad 打开
            import subprocess
            import tempfile
            editor = os.environ.get("EDITOR", "notepad.exe")
            with tempfile.NamedTemporaryFile(
                mode="w", suffix=".txt", delete=False, encoding="utf-8"
            ) as f:
                if current:
                    f.write(current)
                f.flush()
                subprocess.call([editor, f.name])
                with open(f.name, encoding="utf-8") as rf:
                    inp.value = rf.read().strip()
                os.unlink(f.name)
        except Exception:
            pass

    async def _chat_loop(self, query: str, log: RichLog) -> None:
        """LLM ↔ Tool 循环（每轮一次 LLM 调用，含思考日志与 loading 状态）"""
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
        total_tokens: dict[str, int] = {"input": 0, "output": 0}

        for round_num in range(max_rounds):
            # ── 轮次分隔 ──
            if round_num > 0:
                log.write(f"[dim #444c56]── 第{round_num + 1}轮 ──[/]")

            label: str = "◆ 金手指" if round_num == 0 else "  ◇"
            log.write(f"[bold #6ee7b7]{label}[/]  ")

            # › 思考中
            self._update_status_text("› 思考中...")
            await asyncio.sleep(0)  # 刷新 UI

            t_start: float = time.time()
            try:
                resp: dict[str, Any] = await self.llm.chat(
                    messages=messages,
                    tools=tool_schemas,
                    max_tokens=4096,
                )
            except Exception as e:
                log.write(f"[#ef4444]✗ LLM 调用失败: {e}[/]")
                self._update_status_text("✗ 调用失败")
                return

            elapsed: float = time.time() - t_start

            # ── 推理/思考日志 ──
            reasoning: str = self.llm.extract_reasoning(resp)
            if reasoning:
                # 截断过长推理，保留关键部分
                if len(reasoning) > 800:
                    reasoning_show = reasoning[:800]
                    log.write(
                        f"[dim #6b7280]  › {reasoning_show}[/]"
                    )
                    log.write(
                        f"[dim #3d444d]     ... (推理共 {len(reasoning)} 字符，已截断)[/]"
                    )
                else:
                    log.write(f"[dim #6b7280]  › {reasoning}[/]")

            text: str = self.llm.extract_text(resp)
            tool_calls: list[dict[str, Any]] = self.llm.extract_tool_calls(resp)
            usage: dict[str, int] = self.llm.extract_usage(resp)

            total_tokens["input"] += usage.get("input", 0)
            total_tokens["output"] += usage.get("output", 0)

            # ── 显示回复 ──
            if text:
                log.write(RichText(text))

            if not tool_calls and not text:
                log.write("[dim #6b7280](空响应)[/]")

            if not tool_calls:
                log.write(
                    f"[dim #484f58]  ⏱ {elapsed:.1f}s"
                    f"  ↑{usage.get('input', 0)} ↓{usage.get('output', 0)} tok[/]"
                )
                log.write("")
                break

            # ── 工具调用 ──
            assistant_msg: dict[str, Any] = {
                "role": "assistant",
                "content": text or None,
            }
            if tool_calls:
                assistant_msg["tool_calls"] = tool_calls
            messages.append(assistant_msg)

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

                # ⚙ 执行工具
                log.write(f"[#fbbf24]  ⚙ {tool_name}({preview})[/]")
                self._update_status_text(f"⚙ 执行 {tool_name}...")
                await asyncio.sleep(0)

                t_tool: float = time.time()
                tool_log = await ToolExecutionGuard.execute_tool(tool_name, params)
                tool_elapsed: float = time.time() - t_tool

                if tool_log.success and tool_log.result is not None:
                    result_preview: str = (
                        str(tool_log.result)[:200].replace("\n", " ")
                    )
                    log.write(
                        f"[#34d399]    ✓ {tool_elapsed:.1f}s"
                        f"  |  {result_preview}[/]"
                    )
                elif tool_log.success:
                    log.write(f"[#34d399]    ✓ {tool_elapsed:.1f}s[/]")
                else:
                    log.write(f"[#ef4444]    ✗ {tool_elapsed:.1f}s  |  {tool_log.error}[/]")

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

        # ── 总结用量 ──
        if total_tokens["input"] > 0:
            log.write(
                f"[dim #484f58]── 总计 ↑{total_tokens['input']}"
                f" ↓{total_tokens['output']} tok ──[/]"
            )
        self._update_status_text("✦ 就绪")
        log.write("")

    async def _run_pipeline(self, query: str, log: RichLog) -> None:
        """完整五域流水线（/pipeline 模式，含 loading 状态）"""
        assert self.harness is not None
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
                    label = icons.get(domain, domain)
                    log.write(f"[bold #c084fc]⏳ {label}中...[/]")
                    self._update_status_text(f"⏳ {label}...")
                    await asyncio.sleep(0)
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
                    self._update_status_text("✗ 流水线出错")
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
                    if execution_report:
                        try:
                            final_text: str = self.harness.get_final_response(execution_report)
                            if final_text and final_text != "（无输出）":
                                log.write(
                                    f"[bold #6ee7b7]◆ 金手指[/]  {final_text}"
                                )
                        except Exception:
                            pass
                    self._update_status_text("✦ 就绪")
                await asyncio.sleep(0)
        except Exception as e:
            log.write(f"[#ef4444]走火入魔: {e}[/]")
            self._update_status_text("✗ 流水线错误")

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

    def _update_status_text(self, text: str) -> None:
        """快速更新状态栏文字"""
        try:
            status = self.query_one("#status-info", Static)
            status.update(f" {text}")
        except Exception:
            pass

    def _update_status(self) -> None:
        s: dict[str, Any] = (
            self.harness.get_status() if self.harness else {}
        )
        if self.is_processing:
            label = "⚡ 处理中"
        else:
            label = "✦ 就绪"
        mode = "五域流水线" if self._pipeline_mode else "Chat"
        tasks = s.get("total_tasks", 0)
        realm = s.get("realm", "凡人")
        stage = s.get("realm_stage", "初阶")
        status = self.query_one("#status-info", Static)
        status.update(
            f" {label}"
            f"  |  {realm} · {stage}"
            f"  |  {config.openai_model}"
            f"  |  模式: {mode}"
            f"  |  已完成 {tasks} 任务"
            f"  |  Ctrl+D 退出"
        )