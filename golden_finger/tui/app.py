"""Golden Finger 终端 TUI — 现代 Chat 风格 (Textual)"""

import asyncio
import os
import time
from datetime import datetime
from typing import Any

from rich.panel import Panel
from textual import work
from textual import events
from textual.app import App, ComposeResult
from textual.binding import Binding
from textual.containers import Container, Horizontal, Vertical
from textual.widgets import RichLog, Static, TextArea, ProgressBar

from ..harness import GoldenFingerHarness
from ..config import config
from ..llm import LLMClient
from ..logging import log_system, log_step

from .constants import (
    INPUT_RECOVERY_TIMEOUT_SEC,
    DOUBLE_PRESS_WINDOW_SEC,
    CHROMADB_WARMUP_TIMEOUT_SEC,
    CHAT_MEMORY_TURN_LIMIT,
    CHAT_LOG_MAX_LINES,
)
from .widgets import StatusBar, TaskPanel, AgentPanel
from .clipboard import copy_to_clipboard
from .commands import CommandHandler
from .chat import ChatHandler, DebouncedWriter
from .pipeline import PipelineHandler


class GoldenFingerApp(App[None]):
    """金手指终端 TUI"""

    CSS_PATH = "tui.tcss"
    AUTO_FOCUS = "#query-input"

    BINDINGS = [
        Binding("ctrl+c", "ctrl_c_action", "复制/退出", show=False),
        Binding("ctrl+s", "save_session", "保存", show=False),
        Binding("ctrl+m", "export_log", "导出", show=False),
        Binding("ctrl+enter", "submit_query", "发送", show=False),
        Binding("escape", "escape_action", "中止/聚焦", show=False),
        Binding("ctrl+up", "history_prev", "", show=False),
        Binding("ctrl+down", "history_next", "", show=False),
    ]

    def __init__(self, monitor_port: int = 0) -> None:
        super().__init__()
        self.monitor_port: int = monitor_port
        self.harness: GoldenFingerHarness | None = None
        self.llm: LLMClient | None = None
        self.chat_memory: list[dict[str, str]] = []
        self.input_history: list[str] = []
        self.history_index: int = -1
        self.current_input: str = ""
        self.is_processing: bool = False
        self._processing_timer: asyncio.Task | None = None
        self._pipeline_mode: bool = False
        self.cli_context_prompt: str = ""
        self._chat_worker: asyncio.Task | None = None
        self._last_ctrl_c_ts: float = 0.0
        self._last_esc_ts: float = 0.0
        self._double_press_window_sec: float = DOUBLE_PRESS_WINDOW_SEC
        self._inline_copy_mode: bool = False

    # ---- Lifecycle ----

    async def on_mount(self) -> None:
        self.harness = GoldenFingerHarness()
        self.llm = LLMClient()
        log_system("TUI 模式启动", mode="tui", model=config.openai_model,
                    llm_provider=config.llm_provider)

        log = self.query_one("#chat-log", RichLog)
        s = self.harness.get_status()
        realm_info = f"{s['realm']} · {s['realm_stage']}"
        spirit = s['spirit_root']['dominant']

        monitor_url = f"http://127.0.0.1:{self.monitor_port}" if self.monitor_port else ""
        welcome_text = (
            f"[#a6adc8]模型:[/] [#89b4fa]{config.openai_model}[/]\n"
            f"[#a6adc8]境界:[/] [#89b4fa]{realm_info}[/]  |  [#a6adc8]灵根:[/] [#89b4fa]{spirit}[/]\n"
        )
        if monitor_url:
            welcome_text += (
                f"[#a6adc8]监控:[/] [#89b4fa]{monitor_url}[/]  "
                f"[dim #6c7086]浏览器打开可实时查看日志[/]\n"
            )
        welcome_text += "\n[dim #6c7086]输入问题开始修炼 · /help 查看指令 · Ctrl+↑↓ 历史 · Ctrl+Enter 发送[/]"

        panel = Panel(
            welcome_text,
            title="✨ [bold #cba6f7]金手指 Agent System 已就绪[/] ✨",
            border_style="#cba6f7",
            expand=False,
            padding=(1, 2),
        )
        log.write(panel)
        log.write("")
        self._update_status()
        self.query_one("#query-input", TextArea).focus()

        self._pipeline_mode = False
        self.cli_context_prompt = self._detect_cli_context()
        self._warm_chromadb(log)

    def _detect_cli_context(self) -> str:
        if os.name == "nt":
            return (
                "当前系统为 Windows。命令执行工具 shell_exec 在本系统下使用 cmd /c。"
                "生成命令时请使用 Windows CMD 兼容语法，避免 bash 专有语法。"
            )
        shell_name = os.environ.get("SHELL", "/bin/sh")
        return (
            f"当前系统为类 Unix，默认 shell 为 {shell_name}。"
            "生成命令时请使用 POSIX shell 兼容语法。"
        )

    async def on_unmount(self) -> None:
        from ..logging import log_manager
        log_manager.shutdown()
        if self.harness:
            await self.harness.close()
        if self.llm:
            await self.llm.close()

    # ---- Layout ----

    def compose(self) -> ComposeResult:
        with Horizontal(id="main-layout"):
            with Vertical(id="left-pane"):
                with Container(id="chat-area"):
                    yield RichLog(
                        id="chat-log",
                        highlight=True,
                        markup=True,
                        wrap=True,
                        auto_scroll=True,
                        max_lines=CHAT_LOG_MAX_LINES,
                    )
                    yield TextArea(
                        "",
                        id="chat-copy-inline",
                        read_only=True,
                    )
                with Container(id="input-bar"):
                    yield Static(
                        "Ctrl+Enter 发送 · Shift+Enter 换行 · Ctrl+↑↓ 历史",
                        id="input-hint",
                    )
                    yield TextArea(
                        id="query-input",
                        language=None,
                        show_line_numbers=False,
                    )
                    yield ProgressBar(
                        id="pipeline-progress",
                        total=4,
                        show_eta=False,
                    )
            with Vertical(id="right-pane"):
                yield TaskPanel(id="task-panel", classes="side-panel")
                yield AgentPanel(id="agent-panel", classes="side-panel")
        yield StatusBar(id="status-bar")

    # ---- Input ----

    def action_submit_query(self) -> None:
        if self.is_processing:
            return

        ta = self._safe_query("#query-input", TextArea)
        if ta is None:
            return

        query = ta.text.strip()
        if not query:
            return

        if not self.input_history or self.input_history[-1] != query:
            self.input_history.append(query)
        self.history_index = len(self.input_history)
        self.current_input = ""

        ta.text = ""
        ta.disabled = True

        self._cancel_processing_timer()
        self._processing_timer = asyncio.create_task(
            self._force_enable_input_after(INPUT_RECOVERY_TIMEOUT_SEC)
        )

        if query.startswith("/"):
            self._chat_worker = self._process_command(query)
            return

        log = self.query_one("#chat-log", RichLog)
        log.write(f"[bold #f9e2af]▸ 宿主[/]  {query}")

        self.is_processing = True
        self._set_processing_ui(True)
        log_step(
            f"收到查询: {query[:80]}",
            mode="tui_chat" if not self._pipeline_mode else "tui_pipeline",
            query=query[:200],
        )
        self._chat_worker = self._handle_chat(query)

    @work(exclusive=False)
    async def _process_command(self, query: str) -> None:
        try:
            await CommandHandler(self).handle(query)
        finally:
            self._cancel_processing_timer()
            ta = self._safe_query("#query-input", TextArea)
            if ta:
                ta.disabled = False
                ta.focus()

    async def _force_enable_input_after(self, seconds: float) -> None:
        await asyncio.sleep(seconds)
        try:
            ta = self.query_one("#query-input", TextArea)
            ta.disabled = False
            ta.focus()
            self.is_processing = False
            self._set_processing_ui(False)
        except Exception:
            pass

    def _cancel_processing_timer(self) -> None:
        if self._processing_timer and not self._processing_timer.done():
            self._processing_timer.cancel()
            self._processing_timer = None

    # ---- Chat / Pipeline ----

    @work(exclusive=False)
    async def _handle_chat(self, query: str) -> None:
        try:
            log = self.query_one("#chat-log", RichLog)
            writer = DebouncedWriter(log)
            if self._pipeline_mode:
                await PipelineHandler(self).run(query, log, writer)
            else:
                await ChatHandler(self).run(query, log, writer)
        except asyncio.CancelledError:
            try:
                log = self.query_one("#chat-log", RichLog)
                log.write("[#f9e2af]⏹ 已中止当前对话[/]")
            except Exception:
                pass
            self._update_status_text("⏹ 已中止")
            log_step("用户中止对话", mode="tui_chat", status="cancelled")
        except Exception as e:
            try:
                self.query_one("#chat-log", RichLog).write(f"[bold #f38ba8]✗ 出错: {e}[/]")
            except Exception:
                pass
            log_system(f"对话处理异常: {e}", level="ERROR")
        finally:
            self.is_processing = False
            self._chat_worker = None
            self._cancel_processing_timer()
            try:
                ta = self.query_one("#query-input", TextArea)
                ta.disabled = False
                ta.focus()
            except Exception:
                pass
            try:
                self._set_processing_ui(False)
            except Exception:
                pass

    # ---- ChromaDB 预热 ----

    @work(exclusive=False)
    async def _warm_chromadb(self, log: RichLog) -> None:
        status = self.query_one("#status-info", Static)
        status.update(" ⏳ 初始化向量数据库...")

        try:
            import os as _os
            if "HF_ENDPOINT" not in _os.environ:
                _os.environ["HF_ENDPOINT"] = "https://hf-mirror.com"

            from ..storage.vector_store import vector_store
            from pathlib import Path

            chroma_db = Path(config.memory_dir) / "chroma.sqlite3"
            if not (chroma_db.exists() and chroma_db.stat().st_size > 0):
                log.write(
                    "[dim #6c7086]⏳ 向量数据库初始化中..."
                    " 首次运行需下载嵌入模型 (~79MB)，请耐心等待[/]"
                )

            loop = asyncio.get_event_loop()
            await asyncio.wait_for(
                loop.run_in_executor(None, vector_store.warm_up),
                timeout=CHROMADB_WARMUP_TIMEOUT_SEC,
            )

            count: int = vector_store.count()
            log.write(f"[#a6e3a1]✓ 向量数据库就绪 ({count} 条记忆)[/]")
            log.write("")
            log_system("向量数据库就绪", count=count)
        except asyncio.TimeoutError:
            log.write("[#f9e2af]⚠ 向量数据库初始化超时 (下载过慢)[/]")
            log.write(
                "[dim #6c7086]  可手动下载 ONNX 模型到 ChromaDB 缓存目录，"
                "或设置 HF_ENDPOINT 环境变量[/]"
            )
            log.write("")
            log_system("向量数据库初始化超时", level="WARNING")
        except Exception as e:
            log.write(f"[#f9e2af]⚠ 向量数据库初始化失败: {str(e)[:200]}[/]")
            log.write("[dim #6c7086]  技能匹配功能暂不可用，其他功能正常[/]")
            log.write("")
            log_system(f"向量数据库初始化失败: {str(e)[:200]}", level="ERROR")
        finally:
            self._update_status()

    # ---- Paste ----

    def on_paste(self, event: events.Paste) -> None:
        focused = self.focused
        if focused is None or getattr(focused, "id", "") != "query-input":
            return
        lines = event.text.strip().split("\n")
        if len(lines) > 10:
            try:
                self.query_one("#chat-log", RichLog).write(
                    f"[dim #6c7086]📋 粘贴了 {len(lines)} 行 ({len(event.text.strip())} 字符)[/]"
                )
            except Exception:
                pass

    # ---- Copy Mode ----

    def action_ctrl_c_action(self) -> None:
        if self._inline_copy_mode:
            self._copy_inline_selection()
            return

        now = time.monotonic()
        if now - self._last_ctrl_c_ts <= self._double_press_window_sec:
            self.exit()
            return
        self._last_ctrl_c_ts = now
        self.action_copy_mode()
        self._update_status_text("鼠标选择文字后 Ctrl+C 复制（自动返回）| Esc 返回 | Ctrl+C×2 退出")

    def action_copy_mode(self) -> None:
        self._set_inline_copy_mode(not self._inline_copy_mode)

    def _set_inline_copy_mode(self, enabled: bool) -> None:
        self._inline_copy_mode = enabled
        log = self.query_one("#chat-log", RichLog)
        copy_area = self.query_one("#chat-copy-inline", TextArea)
        if enabled:
            content: str = "\n".join(str(line) for line in log.lines) or "（暂无日志）"
            copy_area.text = content
            copy_area.add_class("-visible")
            log.add_class("-hidden")
            copy_area.focus()
        else:
            copy_area.remove_class("-visible")
            log.remove_class("-hidden")

    def _copy_inline_selection(self) -> None:
        copy_area = self.query_one("#chat-copy-inline", TextArea)
        selected = ""
        try:
            selected = str(getattr(copy_area, "selected_text", "") or "")
        except Exception:
            selected = ""
        payload = selected.strip() or (copy_area.text or "")
        if not payload.strip():
            return
        copy_to_clipboard(payload)
        self._update_status_text("已复制到剪贴板")
        self._set_inline_copy_mode(False)
        self.action_focus_input()

    # ---- Escape ----

    def action_escape_action(self) -> None:
        if self._inline_copy_mode:
            self._set_inline_copy_mode(False)
            self.action_focus_input()
            return

        now = time.monotonic()
        second_press = now - self._last_esc_ts <= self._double_press_window_sec
        self._last_esc_ts = now

        if not second_press:
            self.action_focus_input()
            if self.is_processing:
                self._update_status_text("再按一次 Esc 中止当前对话")
            return

        if not self.is_processing:
            self.action_focus_input()
            return

        try:
            if self._chat_worker is not None and hasattr(self._chat_worker, "cancel"):
                self._chat_worker.cancel()
            self.is_processing = False
            self._cancel_processing_timer()
            self._set_processing_ui(False)
            ta = self.query_one("#query-input", TextArea)
            ta.disabled = False
            ta.focus()
        except Exception:
            pass

    # ---- History ----

    def action_history_prev(self) -> None:
        if not self.input_history:
            return
        ta = self._safe_query("#query-input", TextArea)
        if ta is None:
            return
        if self.history_index == len(self.input_history):
            self.current_input = ta.text
        if self.history_index > 0:
            self.history_index -= 1
            ta.text = self.input_history[self.history_index]

    def action_history_next(self) -> None:
        if not self.input_history:
            return
        ta = self._safe_query("#query-input", TextArea)
        if ta is None:
            return
        if self.history_index < len(self.input_history) - 1:
            self.history_index += 1
            ta.text = self.input_history[self.history_index]
        elif self.history_index == len(self.input_history) - 1:
            self.history_index = len(self.input_history)
            ta.text = self.current_input

    # ---- Session I/O ----

    async def _save_session(self, name: str = "") -> None:
        log = self.query_one("#chat-log", RichLog)
        ts: str = datetime.now().strftime("%Y%m%d_%H%M%S")
        filename: str = f"session_{name or ts}.txt"
        path = config.logs_dir / filename
        try:
            content: str = "\n".join(str(line) for line in log.lines)
            path.write_text(content, encoding="utf-8")
            log.write(f"[#a6e3a1]✓ 会话已保存: {path}[/]")
            log_system("会话已保存", path=str(path))
        except Exception as e:
            log.write(f"[#f38ba8]✗ 保存失败: {e}[/]")
            log_system(f"会话保存失败: {e}", level="ERROR")

    async def _export_log(self) -> None:
        log = self.query_one("#chat-log", RichLog)
        ts: str = datetime.now().strftime("%Y%m%d_%H%M%S")
        path = config.logs_dir / f"export_{ts}.md"
        try:
            content: str = "\n".join(str(line) for line in log.lines)
            path.write_text(content, encoding="utf-8")
            log.write(f"[#a6e3a1]✓ 日志已导出: {path}[/]")
            log_system("日志已导出", path=str(path))
        except Exception as e:
            log.write(f"[#f38ba8]✗ 导出失败: {e}[/]")
            log_system(f"日志导出失败: {e}", level="ERROR")

    def action_save_session(self) -> None:
        self.run_worker(self._save_session(), exclusive=False)

    def action_export_log(self) -> None:
        self.run_worker(self._export_log(), exclusive=False)

    def action_focus_input(self) -> None:
        ta = self._safe_query("#query-input", TextArea)
        if ta is not None:
            ta.focus()

    # ---- Status Bar ----

    def _update_status_text(self, text: str) -> None:
        try:
            self.query_one("#status-info", Static).update(f" {text}")
        except Exception:
            pass

    def _set_processing_ui(self, active: bool) -> None:
        """Toggle processing CSS class and update status bar."""
        try:
            si = self.query_one("#status-info", Static)
            if active:
                si.add_class("processing")
            else:
                si.remove_class("processing")
        except Exception:
            pass
        self._update_status()

    def _update_status(self) -> None:
        s: dict[str, Any] = self.harness.get_status() if self.harness else {}
        label = "⚡ 处理中" if self.is_processing else "✦ 就绪"
        mode = "五域流水线" if self._pipeline_mode else "Chat"
        tasks = s.get("total_tasks", 0)
        realm = s.get("realm", "凡人")
        stage = s.get("realm_stage", "初阶")
        status = self.query_one("#status-info", Static)
        parts = [
            f" {label}",
            f"  |  {realm} · {stage}",
            f"  |  {config.openai_model}",
            f"  |  模式: {mode}",
            f"  |  已完成 {tasks} 任务",
        ]
        if self.monitor_port:
            parts.append(f"  |  📊 http://127.0.0.1:{self.monitor_port}")
        else:
            parts.append("  |  Ctrl+C 选中复制 | Ctrl+C×2 退出")
        status.update("".join(parts))

    # ---- Utility ----

    def _safe_query(self, widget_id: str, expect_type: type | None = None) -> Any:
        try:
            w = self.query_one(widget_id)
            if expect_type is not None and not isinstance(w, expect_type):
                return None
            return w
        except Exception:
            return None
