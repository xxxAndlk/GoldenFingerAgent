"""Golden Finger 终端 TUI — 现代 Chat 风格 (Textual)"""

import asyncio
import contextlib
import json
import os
import sys
import time
from datetime import datetime
from typing import Any, Optional

from rich.text import Text as RichText
from textual import work
from textual import events
from textual.app import App, ComposeResult
from textual.binding import Binding
from textual.containers import Container, Horizontal, Vertical
from textual.screen import ModalScreen
from textual.widgets import Input, RichLog, Static, TextArea

from ..harness import GoldenFingerHarness
from ..config import config
from ..llm import LLMClient
from ..tools.builtin import BUILTIN_TOOLS
from ..domain_execution import ToolExecutionGuard
from ..logging import log_system, log_step, log_api_request, log_api_response


class CopyLogScreen(ModalScreen[None]):
    """复制模式：在只读文本区域中可选择并复制聊天记录。"""

    BINDINGS = [
        Binding("escape", "close", "关闭", show=False),
        Binding("ctrl+c", "copy_to_clipboard", "复制", show=False),
    ]

    def __init__(self, content: str) -> None:
        super().__init__()
        self._content = content

    def compose(self) -> ComposeResult:
        with Container(id="copy-log-modal"):
            yield TextArea(
                self._content,
                read_only=True,
                id="copy-log-text",
            )

    def on_mount(self) -> None:
        self.query_one("#copy-log-text", TextArea).focus()

    def action_close(self) -> None:
        self.dismiss()

    def action_copy_to_clipboard(self) -> None:
        """复制当前选择内容（无选择时复制全部）到系统剪贴板。"""
        text_area = self.query_one("#copy-log-text", TextArea)
        selected = ""
        try:
            selected = str(getattr(text_area, "selected_text", "") or "")
        except Exception:
            selected = ""
        payload = selected.strip() or self._content
        if not payload.strip():
            return
        self._copy_text(payload)
        self.notify("已复制到剪贴板", title="Copy")

    @staticmethod
    def _copy_text(text: str) -> None:
        # 优先走 Windows clip，失败再回退 tkinter
        try:
            if os.name == "nt":
                import subprocess
                subprocess.run(["clip"], input=text, text=True, check=True)
                return
        except Exception:
            pass
        try:
            import tkinter as tk
            root = tk.Tk()
            root.withdraw()
            root.clipboard_clear()
            root.clipboard_append(text)
            root.update()
            root.destroy()
        except Exception:
            pass


class StatusBar(Static):
    """底部状态栏"""

    def compose(self) -> ComposeResult:
        yield Static(id="status-info")


class TaskPanel(Static):
    """右上角：任务计划面板"""
    def __init__(self, **kwargs):
        super().__init__(**kwargs)
        self.tasks: list[dict[str, Any]] = []

    def compose(self) -> ComposeResult:
        yield Static("🎯 任务计划", classes="panel-title")
        yield RichLog(id="task-log", classes="panel-content", wrap=True, auto_scroll=True)

    def update_tasks(self, plan: Any) -> None:
        self.tasks = []
        log = self.query_one("#task-log", RichLog)
        log.clear()
        if not plan or not plan.tasks:
            log.write("[dim #6c7086]暂无任务[/]")
            return
        
        for i, task in enumerate(plan.tasks):
            self.tasks.append({
                "id": task.task_id,
                "desc": task.description,
                "status": "pending"
            })
        self._render_tasks()

    def set_task_status(self, task_id: str, status: str) -> None:
        changed = False
        for t in self.tasks:
            if t["id"] == task_id and t["status"] != status:
                t["status"] = status
                changed = True
        if changed:
            self._render_tasks()

    def _render_tasks(self) -> None:
        log = self.query_one("#task-log", RichLog)
        log.clear()
        for i, t in enumerate(self.tasks):
            status = t["status"]
            if status == "pending":
                icon = "[dim]⏳[/]"
                color = "dim #6c7086"
            elif status == "running":
                icon = "[#f9e2af]▶[/]"
                color = "#f9e2af"
            elif status == "completed":
                icon = "[#a6e3a1]✓[/]"
                color = "#a6e3a1"
            elif status == "error":
                icon = "[#f38ba8]✗[/]"
                color = "#f38ba8"
            else:
                icon = "[dim]-[/]"
                color = "dim"
            
            log.write(f"{icon} [{color}]{i+1}. {t['desc']}[/]")


class AgentPanel(Static):
    """右下角：子 Agent 执行状态面板"""
    def compose(self) -> ComposeResult:
        yield Static("🤖 子Agent/工具 协同", classes="panel-title")
        yield RichLog(id="agent-log", classes="panel-content", wrap=True, auto_scroll=True)

    def write_log(self, text: str) -> None:
        log = self.query_one("#agent-log", RichLog)
        log.write(text)


class GoldenFingerApp(App[None]):
    """金手指终端 TUI"""

    CSS_PATH = "tui.tcss"
    AUTO_FOCUS = "#query-input"

    BINDINGS = [
        Binding("ctrl+c", "ctrl_c_action", "复制/退出", show=False),
        Binding("ctrl+s", "save_session", "保存", show=False),
        Binding("ctrl+m", "export_log", "导出", show=False),
        Binding("ctrl+shift+c", "copy_mode", "复制模式", show=False),
        Binding("escape", "escape_action", "中止/聚焦", show=False),
        Binding("up", "history_prev", "", show=False),
        Binding("down", "history_next", "", show=False),
        Binding("ctrl+shift+v", "paste_multiline", "粘贴", show=False),
    ]

    def __init__(self) -> None:
        super().__init__()
        self.harness: GoldenFingerHarness | None = None
        self.llm: LLMClient | None = None
        self.chat_memory: list[dict[str, str]] = []
        self.chat_memory_turn_limit: int = 10
        self.chat_memory_level2_threshold: int = 12  # 消息条数阈值（超过触发 L2）
        self.chat_memory_level3_threshold: int = 20  # 消息条数阈值（超过触发 L3）
        self.chat_memory_keep_recent: int = 8
        self.input_history: list[str] = []
        self.history_index: int = -1
        self.current_input: str = ""
        self.is_processing: bool = False
        self._processing_timer: Optional[asyncio.Task] = None
        self._pasted_lines: int = 0
        self._pipeline_mode: bool = False
        self.cli_context_prompt: str = ""
        self._chat_worker: Any = None
        self._last_ctrl_d_ts: float = 0.0
        self._last_ctrl_c_ts: float = 0.0
        self._last_esc_ts: float = 0.0
        self._double_press_window_sec: float = 1.2
        self._inline_copy_mode: bool = False

    # ---- Lifecycle ----

    async def on_mount(self) -> None:
        from rich.panel import Panel
        self.harness = GoldenFingerHarness()
        self.llm = LLMClient()
        log_system("TUI 模式启动", mode="tui", model=config.openai_model,
                    llm_provider=config.llm_provider)
        log = self.query_one("#chat-log", RichLog)
        s = self.harness.get_status()
        realm_info = f"{s['realm']} · {s['realm_stage']}"
        spirit = s['spirit_root']['dominant']
        
        welcome_text = (
            f"[#a6adc8]模型:[/] [#89b4fa]{config.openai_model}[/]\n"
            f"[#a6adc8]境界:[/] [#89b4fa]{realm_info}[/]  |  [#a6adc8]灵根:[/] [#89b4fa]{spirit}[/]\n\n"
            f"[dim #6c7086]输入问题开始修炼 · /help 查看指令 · ↑↓ 历史[/]"
        )
        panel = Panel(
            welcome_text,
            title="✨ [bold #cba6f7]金手指 Agent System 已就绪[/] ✨",
            border_style="#cba6f7",
            expand=False,
            padding=(1, 2)
        )
        log.write(panel)
        log.write("")
        self._update_status()
        inp = self.query_one("#query-input", Input)
        inp.focus()

        self._pipeline_mode = False
        self.cli_context_prompt = self._detect_cli_context()
        self._warm_chromadb(log)

    def _detect_cli_context(self) -> str:
        """检测当前命令行上下文，供提示词约束命令生成。"""
        if os.name == "nt":
            # 当前内置 shell_exec 在 win32 下使用 cmd /c 执行
            return (
                "当前系统为 Windows。命令执行工具 shell_exec 在本系统下使用 cmd /c。"
                "生成命令时请使用 Windows CMD 兼容语法，避免 bash 专有语法。"
            )
        shell_name = os.environ.get("SHELL", "/bin/sh")
        return (
            f"当前系统为类 Unix，默认 shell 为 {shell_name}。"
            "生成命令时请使用 POSIX shell 兼容语法。"
        )

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
                    "[dim #6c7086]⏳ 向量数据库初始化中..."
                    " 首次运行需下载嵌入模型 (~79MB)，请耐心等待[/]"
                )

            loop = asyncio.get_event_loop()
            # 最多等待 120 秒，超时则跳过
            await asyncio.wait_for(
                loop.run_in_executor(None, vector_store.warm_up),
                timeout=120,
            )

            count: int = vector_store.count()
            log.write(f"[#a6e3a1]✓ 向量数据库就绪 ({count} 条记忆)[/]")
            log.write("")
            log_system("向量数据库就绪", count=count)
        except asyncio.TimeoutError:
            log.write(
                "[#f9e2af]⚠ 向量数据库初始化超时 (下载过慢)[/]"
            )
            log.write(
                "[dim #6c7086]  可手动下载 ONNX 模型到 ChromaDB 缓存目录，"
                "或设置 HF_ENDPOINT 环境变量[/]"
            )
            log.write("")
            log_system("向量数据库初始化超时", level="WARNING")
        except Exception as e:
            log.write(
                f"[#f9e2af]⚠ 向量数据库初始化失败: {str(e)[:200]}[/]"
            )
            log.write(
                "[dim #6c7086]  技能匹配功能暂不可用，其他功能正常[/]"
            )
            log.write("")
            log_system(f"向量数据库初始化失败: {str(e)[:200]}", level="ERROR")
        finally:
            self._update_status()

    async def on_unmount(self) -> None:
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
                        max_lines=10_000,
                    )
                    yield TextArea(
                        "",
                        id="chat-copy-inline",
                        read_only=True,
                    )
                with Container(id="input-bar"):
                    yield Input(
                        placeholder="宿主> 输入问题或指令...",
                        id="query-input",
                    )
            with Vertical(id="right-pane"):
                yield TaskPanel(id="task-panel", classes="side-panel")
                yield AgentPanel(id="agent-panel", classes="side-panel")
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
            inp.focus()
            return

        log = self.query_one("#chat-log", RichLog)
        log.write(f"[bold #f9e2af]▸ 宿主[/]  {query}")

        self.is_processing = True
        self._update_status()
        log_step(f"收到查询: {query[:80]}", mode="tui_chat" if not self._pipeline_mode else "tui_pipeline",
                  query=query[:200])
        self._chat_worker = self._handle_chat(query)

    async def _force_enable_input_after(self, seconds: float) -> None:
        """超时保护：强制恢复输入框"""
        await asyncio.sleep(seconds)
        try:
            inp = self.query_one("#query-input", Input)
            inp.disabled = False
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
            log.write("[bold #cba6f7]✦ 可用指令[/]")
            for item in [
                "/help            显示此帮助",
                "/status          显示宿主状态",
                "/clear           清空对话",
                "/save  [name]    保存会话",
                "/export          导出对话日志",
                "/copy            打开可选择复制视图",
                "/pipeline        切换至完整五域流水线",
                "",
                "Ctrl+S           保存会话",
                "Ctrl+M           导出日志",
                "Ctrl+C           当前页复制模式（鼠标可选）",
                "Ctrl+C×2         退出",
                "复制模式内 Ctrl+C 复制",
                "↑/↓              历史命令",
                "Esc×2            中止当前对话",
            ]:
                log.write(f"[dim #a6adc8]  {item}[/]")
            log.write("")
        elif action == "/status":
            assert self.harness is not None
            s: dict[str, Any] = self.harness.get_status()
            log.write("[bold #cba6f7]✦ 宿主状态[/]")
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
            self.chat_memory.clear()
            log.write("[dim #6c7086]已清空对话与会话记忆上下文[/]")
        elif action == "/save":
            await self._save_session(arg)
        elif action == "/export":
            await self._export_log()
        elif action == "/copy":
            self.action_copy_mode()
        elif action == "/pipeline":
            self._pipeline_mode = not self._pipeline_mode
            mode_status: str = "已启用" if self._pipeline_mode else "已关闭"
            log.write(f"[#f9e2af]五域流水线模式: {mode_status}[/]")
            log_system(f"流水线模式切换: {mode_status}")
        else:
            log.write(f"[#f38ba8]未知指令: {action}，输入 /help 查看[/]")

    # ---- Chat Handler ----

    @work(exclusive=False)
    async def _handle_chat(self, query: str) -> None:
        try:
            log = self.query_one("#chat-log", RichLog)
            if self._pipeline_mode:
                await self._run_pipeline(query, log)
            else:
                await self._chat_loop(query, log)
        except asyncio.CancelledError:
            try:
                log = self.query_one("#chat-log", RichLog)
                log.write("[#f9e2af]⏹ 已中止当前对话[/]")
            except Exception:
                pass
            self._update_status_text("⏹ 已中止")
            log_step("用户中止对话", mode="tui_chat", status="cancelled")
            return
        except Exception as e:
            try:
                log.write(f"[bold #f38ba8]✗ 出错: {e}[/]")
            except Exception:
                pass
            log_system(f"对话处理异常: {e}", level="ERROR")
        finally:
            self.is_processing = False
            self._chat_worker = None
            self._cancel_processing_timer()
            # 恢复输入框 — 放最前面，确保执行
            try:
                inp = self.query_one("#query-input", Input)
                inp.disabled = False
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
                    f"[dim #6c7086]📋 粘贴了 {line_count} 行数据"
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
        await self._compress_chat_memory_if_needed(log)

        messages: list[dict[str, Any]] = [
            {
                "role": "system",
                "content": (
                    "你是金手指(GoldenFinger)，一个运行在终端TUI中的AI编程助手。"
                    "使用中文回复。可以调用工具：读文件、写文件、执行命令、搜索网页。"
                    "代码用 ``` 语法高亮。回复简洁，重点突出。"
                    f"{self.cli_context_prompt}"
                ),
            },
        ]
        messages.extend(self.chat_memory)
        messages.append({"role": "user", "content": query})
        tool_schemas: list[dict[str, Any]] = [
            t.to_openai_schema() for t in BUILTIN_TOOLS.values()
        ]
        max_rounds: int = 6
        total_tokens: dict[str, int] = {"input": 0, "output": 0}
        final_text_for_memory: str = ""

        for round_num in range(max_rounds):
            # ── 轮次分隔 ──
            if round_num > 0:
                log.write(f"[dim #585b70]── 第{round_num + 1}轮 ──[/]")

            label: str = "◆ 金手指" if round_num == 0 else "  ◇"
            log.write(f"[bold #94e2d5]{label}[/]  ")

            # › 思考中
            self._update_status_text("› 思考中...")
            await asyncio.sleep(0)  # 刷新 UI

            t_start: float = time.time()
            thinking_task = asyncio.create_task(
                self._emit_thinking_trace(log, t_start)
            )
            try:
                resp: dict[str, Any] = await self.llm.chat(
                    messages=messages,
                    tools=tool_schemas,
                    max_tokens=4096,
                )
            except Exception as e:
                thinking_task.cancel()
                with contextlib.suppress(asyncio.CancelledError):
                    await thinking_task
                log.write(f"[#f38ba8]✗ LLM 调用失败: {e}[/]")
                self._update_status_text("✗ 调用失败")
                log_system(f"LLM 调用失败 (chat loop): {e}", level="ERROR")
                return
            else:
                thinking_task.cancel()
                with contextlib.suppress(asyncio.CancelledError):
                    await thinking_task

            elapsed: float = time.time() - t_start

            # ── 推理/思考日志 ──
            reasoning: str = self.llm.extract_reasoning(resp)
            if reasoning:
                # 截断过长推理，保留关键部分
                if len(reasoning) > 800:
                    reasoning_show = reasoning[:800]
                    log.write(
                        f"[dim #6c7086]  › {reasoning_show}[/]"
                    )
                    log.write(
                        f"[dim #45475a]     ... (推理共 {len(reasoning)} 字符，已截断)[/]"
                    )
                else:
                    log.write(f"[dim #6c7086]  › {reasoning}[/]")

            text: str = self.llm.extract_text(resp)
            tool_calls: list[dict[str, Any]] = self.llm.extract_tool_calls(resp)
            usage: dict[str, int] = self.llm.extract_usage(resp)

            total_tokens["input"] += usage.get("input", 0)
            total_tokens["output"] += usage.get("output", 0)

            # ── 显示回复 ──
            if text:
                log.write(RichText(text))
                final_text_for_memory = text

            if not tool_calls and not text:
                log.write("[dim #6c7086](空响应)[/]")

            if not tool_calls:
                log.write(
                    f"[dim #585b70]  ⏱ {elapsed:.1f}s"
                    f"  ↑{usage.get('input', 0)} ↓{usage.get('output', 0)} tok[/]"
                )
                log.write("")
                break

            # ── 工具调用 ──
            assistant_msg: dict[str, Any] = {
                "role": "assistant",
                "content": text or None,
            }
            if reasoning:
                # thinking 模式下必须把 reasoning_content 原样回传给下一轮 API
                assistant_msg["reasoning_content"] = reasoning
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
                log.write(f"[#f9e2af]  ⚙ {tool_name}({preview})[/]")
                self._update_status_text(f"⚙ 执行 {tool_name}...")
                await asyncio.sleep(0)

                t_tool: float = time.time()
                log_step(f"工具调用开始: {tool_name}", domain="execution",
                          status="tool_start", tool_name=tool_name,
                          params_preview=preview, round=round_num + 1)
                tool_log = await ToolExecutionGuard.execute_tool(tool_name, params)
                tool_elapsed: float = time.time() - t_tool

                if tool_log.success and tool_log.result is not None:
                    result_preview: str = (
                        str(tool_log.result)[:200].replace("\n", " ")
                    )
                    log.write(
                        f"[#a6e3a1]    ✓ {tool_elapsed:.1f}s"
                        f"  |  {result_preview}[/]"
                    )
                    log_step(f"工具调用完成: {tool_name}", domain="execution",
                              status="tool_end", tool_name=tool_name,
                              duration_ms=int(tool_elapsed * 1000), success=True)
                elif tool_log.success:
                    log.write(f"[#a6e3a1]    ✓ {tool_elapsed:.1f}s[/]")
                    log_step(f"工具调用完成: {tool_name}", domain="execution",
                              status="tool_end", tool_name=tool_name,
                              duration_ms=int(tool_elapsed * 1000), success=True)
                else:
                    log.write(f"[#f38ba8]    ✗ {tool_elapsed:.1f}s  |  {tool_log.error}[/]")
                    log_step(f"工具调用失败: {tool_name}", domain="execution",
                              status="tool_end", tool_name=tool_name,
                              duration_ms=int(tool_elapsed * 1000), success=False,
                              error=str(tool_log.error)[:200])

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
                f"[dim #585b70]── 总计 输入：{total_tokens['input']}"
                f" 输出：{total_tokens['output']} tokens ──[/]"
            )
        log_step(f"对话轮次完成", mode="tui_chat", rounds=round_num + 1,
                  total_input_tokens=total_tokens["input"],
                  total_output_tokens=total_tokens["output"])
        self._remember_chat_turn(query, final_text_for_memory)
        self._update_status_text("✦ 就绪")
        log.write("")

    def _remember_chat_turn(self, user_query: str, assistant_text: str) -> None:
        """记录会话短期记忆，用于连续对话上下文。"""
        if user_query.strip():
            self.chat_memory.append({"role": "user", "content": user_query.strip()})
        if assistant_text.strip():
            self.chat_memory.append({"role": "assistant", "content": assistant_text.strip()})

        max_messages = self.chat_memory_turn_limit * 2
        if len(self.chat_memory) > max_messages:
            self.chat_memory = self.chat_memory[-max_messages:]

    async def _compress_chat_memory_if_needed(self, log: RichLog) -> None:
        """三级上下文压缩：
        L1: 不压缩；
        L2: 用大模型总结工具调用过程并回填；
        L3: 触发规则化工具式压缩（保留关键摘要+近期对话）。
        """
        if not self.chat_memory:
            return

        message_count = len(self.chat_memory)

        # L1：不压缩
        if message_count <= self.chat_memory_level2_threshold:
            return

        # L2：模型压缩（无上下文）
        if message_count <= self.chat_memory_level3_threshold:
            await self._compress_chat_memory_level2(log)
            return

        # L3：按需触发“工具式压缩”
        await self._compress_chat_memory_level3(log)

    async def _compress_chat_memory_level2(self, log: RichLog) -> None:
        """二级压缩：使用独立压缩提示，不携带历史上下文。"""
        if self.llm is None:
            return
        if len(self.chat_memory) <= self.chat_memory_keep_recent:
            return

        compress_part = self.chat_memory[:-self.chat_memory_keep_recent]
        keep_part = self.chat_memory[-self.chat_memory_keep_recent:]
        transcript = self._format_memory_transcript(compress_part)
        if not transcript.strip():
            return

        prompt = (
            "你是“上下文压缩器”。请在不依赖外部上下文的前提下，"
            "压缩以下对话，重点提取：\n"
            "1) 用户目标与约束\n"
            "2) 关键决策与结论\n"
            "3) 工具调用过程与结果（成功/失败）\n"
            "4) 尚未完成事项\n"
            "输出要求：简洁中文，最多 10 条，每条不超过 50 字。"
        )

        try:
            resp = await self.llm.chat(
                messages=[
                    {"role": "system", "content": prompt},
                    {"role": "user", "content": transcript[:12000]},
                ],
                max_tokens=600,
            )
            summary = self.llm.extract_text(resp).strip()
            if not summary:
                return

            self.chat_memory = [
                {
                    "role": "assistant",
                    "content": (
                        "【上下文压缩摘要-L2】\n"
                        f"{summary}"
                    ),
                },
                *keep_part,
            ]
            log.write("[dim #6c7086]🧠 已执行二级上下文压缩（模型摘要）[/]")
            log_system("L2 上下文压缩完成", level="L2", kept_recent=self.chat_memory_keep_recent)
        except Exception:
            # 压缩失败时不影响主流程
            pass

    async def _compress_chat_memory_level3(self, log: RichLog) -> None:
        """三级压缩：按需触发规则化压缩（工具式清理），进一步瘦身上下文。"""
        if len(self.chat_memory) <= self.chat_memory_keep_recent:
            return

        # 先做一次 L2（若可用）以提炼关键信息
        await self._compress_chat_memory_level2(log)

        # 再做规则化收敛：仅保留压缩摘要 + 最近对话
        anchor = None
        for msg in self.chat_memory:
            if "上下文压缩摘要" in msg.get("content", ""):
                anchor = msg
                break

        tail = self.chat_memory[-self.chat_memory_keep_recent:]
        compact_header = {
            "role": "assistant",
            "content": "【上下文压缩摘要-L3】已执行工具式压缩：保留关键摘要与近期对话。",
        }
        self.chat_memory = [compact_header]
        if anchor is not None and anchor is not compact_header:
            self.chat_memory.append(anchor)
        self.chat_memory.extend(tail)
        log.write("[dim #6c7086]🧠 已执行三级上下文压缩（按需工具式压缩）[/]")
        log_system("L3 上下文压缩完成", level="L3")

    @staticmethod
    def _format_memory_transcript(messages: list[dict[str, str]]) -> str:
        """将历史消息转换为可压缩文本。"""
        lines: list[str] = []
        for m in messages:
            role = m.get("role", "unknown")
            content = (m.get("content", "") or "").strip()
            if not content:
                continue
            lines.append(f"[{role}] {content}")
        return "\n".join(lines)

    async def _emit_thinking_trace(self, log: RichLog, started_at: float) -> None:
        """在 LLM 返回前输出有限节奏的思考进度，避免刷屏。"""
        phases = [
            "解析问题",
            "规划方案",
            "组织回复",
        ]
        index = 0
        log.write("[dim #6c7086]  › 思考开始：正在解析你的问题...[/]")
        try:
            while True:
                await asyncio.sleep(1.2)
                elapsed = time.time() - started_at
                dots = "." * ((index % 3) + 1)

                # 前三次输出对应阶段；之后降低频率，避免无限刷屏
                if index < len(phases):
                    phase = phases[index]
                    log.write(
                        f"[dim #6c7086]  › 思考中{dots} {phase}（{elapsed:.1f}s）[/]"
                    )
                elif index % 5 == 0:
                    log.write(
                        f"[dim #6c7086]  › 思考中{dots} 深度推理（{elapsed:.1f}s）[/]"
                    )
                index += 1
        except asyncio.CancelledError:
            elapsed = time.time() - started_at
            log.write(
                f"[dim #585b70]  › 思考结束，开始输出结果（{elapsed:.1f}s）[/]"
            )
            return

    def _show_panel(self, panel_id: str) -> None:
        """显示指定的右侧面板，并联动显示右侧容器"""
        try:
            panel = self.query_one(panel_id)
            panel.add_class("-visible")
            self.query_one("#right-pane").add_class("-visible")
            self.query_one("#left-pane").add_class("-has-right")
        except Exception:
            pass

    def _hide_panels(self) -> None:
        """隐藏所有右侧面板及容器"""
        try:
            self.query_one("#task-panel").remove_class("-visible")
            self.query_one("#agent-panel").remove_class("-visible")
            self.query_one("#right-pane").remove_class("-visible")
            self.query_one("#left-pane").remove_class("-has-right")
        except Exception:
            pass

    async def _run_pipeline(self, query: str, log: RichLog) -> None:
        """完整五域流水线（/pipeline 模式，含 loading 状态）"""
        assert self.harness is not None
        execution_report = None
        
        # 每次新提问时，隐藏右侧面板
        self._hide_panels()
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
                    log.write(f"[bold #cba6f7]⏳ {label}中...[/]")
                    self._update_status_text(f"⏳ {label}...")
                    await asyncio.sleep(0)
                elif status == "completed":
                    if domain == "analysis":
                        plan = event.get("plan")
                        if plan is not None:
                            log.write(
                                f"[#a6e3a1]  ✓ 拆解为 {len(plan.tasks)} 个任务[/]"
                            )
                            try:
                                tp = self.query_one("#task-panel", TaskPanel)
                                tp.update_tasks(plan)
                                self._show_panel("#task-panel")
                            except Exception:
                                pass
                    elif domain == "execution":
                        report = event.get("report")
                        if report is not None:
                            execution_report = report
                            log.write(
                                f"[#a6e3a1]  ✓ 执行完成 "
                                f"({report.total_duration_ms}ms)[/]"
                            )
                            for a in report.anomalies:
                                log.write(f"[#f9e2af]    ⚠ {a}[/]")
                            try:
                                tp = self.query_one("#task-panel", TaskPanel)
                                for tid, t_res in report.task_results.items():
                                    if t_res.get("success"):
                                        tp.set_task_status(tid, "completed")
                                    else:
                                        tp.set_task_status(tid, "error")
                            except Exception:
                                pass
                    elif domain == "verification":
                        ver = event.get("verification")
                        if ver is not None:
                            color: str = (
                                "#a6e3a1" if ver.overall_pass else "#f38ba8"
                            )
                            act: str = "通过" if ver.overall_pass else ver.action
                            log.write(f"[{color}]  ✓ 校验: {act}[/]")
                    elif domain == "persistence":
                        log.write("[#a6e3a1]  ✓ 经验已沉淀[/]")
                elif status == "error":
                    log.write(
                        f"[#f38ba8]  ✗ {event.get('error', '未知错误')}[/]"
                    )
                    self._update_status_text("✗ 流水线出错")
                elif status == "tool_call":
                    ev: dict[str, Any] = event["event"]
                    task_id = ev.get("task_id", "")
                    try:
                        agent_panel = self.query_one("#agent-panel", AgentPanel)
                        task_panel = self.query_one("#task-panel", TaskPanel)
                    except Exception:
                        agent_panel = None
                        task_panel = None

                    phase = ev.get("phase", "")
                    if phase in {"task_publish", "task_claim"}:
                        self._show_panel("#agent-panel")
                        if phase == "task_publish":
                            msg = (
                                f"[#89b4fa]📌 发布任务[/] "
                                f"{ev.get('task_id','')} → {ev.get('to_agent','')}"
                            )
                        else:
                            if task_panel and task_id:
                                task_panel.set_task_status(task_id, "running")
                            msg = (
                                f"[#f9e2af]👤 领取任务[/] "
                                f"{ev.get('to_agent','')} ({ev.get('message','')})"
                            )
                        log.write(f"    {msg}")
                        if agent_panel:
                            agent_panel.write_log(msg)
                    elif phase in {"agent_start", "agent_end"}:
                        self._show_panel("#agent-panel")
                        icon = "🚀" if phase == "agent_start" else "🏁"
                        color = "#94e2d5" if phase == "agent_start" else "#a6e3a1"
                        msg = (
                            f"[{color}]{icon} {ev.get('from_agent','')}[/] "
                            f"{ev.get('message','')}"
                        )
                        log.write(f"    {msg}")
                        if agent_panel:
                            agent_panel.write_log(msg)
                    elif phase in {"mail_send", "mail_recv"}:
                        self._show_panel("#agent-panel")
                        icon = "✉️" if phase == "mail_send" else "📥"
                        color = "#cba6f7" if phase == "mail_send" else "#89b4fa"
                        msg = (
                            f"[{color}]{icon} {ev.get('from_agent','')} → "
                            f"{ev.get('to_agent','')}[/] {ev.get('message','')}"
                        )
                        log.write(f"    {msg}")
                        if agent_panel:
                            agent_panel.write_log(msg)
                    elif phase in {"subtask_schedule", "subtask_result"}:
                        self._show_panel("#agent-panel")
                        if phase == "subtask_schedule":
                            if task_panel and task_id:
                                task_panel.set_task_status(task_id, "running")
                            msg = f"[#f9e2af]🧵 异步派发[/] {task_id}"
                        else:
                            if task_panel and task_id:
                                msg_text = ev.get("message", "")
                                if "失败" in msg_text:
                                    task_panel.set_task_status(task_id, "error")
                                else:
                                    task_panel.set_task_status(task_id, "completed")
                            msg = f"[#a6e3a1]📬 异步回收[/] {task_id} {ev.get('message','')}"
                        log.write(f"    {msg}")
                        if agent_panel:
                            agent_panel.write_log(msg)
                    elif phase in {"task_close", "task_force_close"}:
                        self._show_panel("#agent-panel")
                        if task_panel and task_id:
                            if phase == "task_close":
                                task_panel.set_task_status(task_id, "completed")
                            else:
                                task_panel.set_task_status(task_id, "error")
                        msg = (
                            f"[#a6e3a1]🔒 任务关闭[/] {task_id}"
                            if phase == "task_close"
                            else f"[#f38ba8]🛑 强制关闭[/] {task_id} {ev.get('message','')}"
                        )
                        log.write(f"    {msg}")
                        if agent_panel:
                            agent_panel.write_log(msg)
                    elif phase == "tool_start":
                        self._show_panel("#agent-panel")
                        if task_panel and task_id:
                            task_panel.set_task_status(task_id, "running")

                        ps: str = ", ".join(
                            f"{k}={str(v)[:40]!r}"
                            for k, v in ev.get("params", {}).items()
                        )
                        msg = f"[#f9e2af]⚙ {ev['tool_name']}({ps})[/]"
                        log.write(f"    {msg}")
                        if agent_panel:
                            agent_panel.write_log(msg)
                    elif phase == "tool_end":
                        if ev.get("error"):
                            if task_panel and task_id:
                                task_panel.set_task_status(task_id, "error")
                            msg = f"[#f38ba8]✗ {str(ev['error'])[:200]}[/]"
                        else:
                            pv: str = (
                                str(ev.get("result", ""))[:150].replace("\n", " ")
                            )
                            msg = f"[#a6e3a1]✓ {ev.get('duration_ms', 0)}ms | {pv}[/]"
                        
                        log.write(f"      {msg}")
                        if agent_panel:
                            agent_panel.write_log(msg)
                elif domain == "complete":
                    log.write(
                        "[bold #cba6f7]━━━ 修炼结束 ━━━[/]"
                    )
                    log_step("流水线执行完成", mode="tui_pipeline", status="complete")
                    if execution_report:
                        try:
                            final_text: str = self.harness.get_final_response(execution_report)
                            if final_text and final_text != "（无输出）":
                                log.write(
                                    f"[bold #94e2d5]◆ 金手指[/]  {final_text}"
                                )
                        except Exception:
                            pass
                    self._update_status_text("✦ 就绪")
                await asyncio.sleep(0)
        except Exception as e:
            log.write(f"[#f38ba8]走火入魔: {e}[/]")
            self._update_status_text("✗ 流水线错误")
            log_system(f"流水线执行异常: {e}", level="ERROR")

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
            log.write(f"[#a6e3a1]✓ 会话已保存: {path}[/]")
            log_system(f"会话已保存", path=str(path))
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
            log_system(f"日志已导出", path=str(path))
        except Exception as e:
            log.write(f"[#f38ba8]✗ 导出失败: {e}[/]")
            log_system(f"日志导出失败: {e}", level="ERROR")

    def action_save_session(self) -> None:
        self.run_worker(self._save_session(), exclusive=False)

    def action_export_log(self) -> None:
        self.run_worker(self._export_log(), exclusive=False)

    def action_focus_input(self) -> None:
        self.query_one("#query-input", Input).focus()

    def action_request_quit(self) -> None:
        """Ctrl+D 连按两次退出。"""
        now = time.monotonic()
        if now - self._last_ctrl_d_ts <= self._double_press_window_sec:
            self.exit()
            return
        self._last_ctrl_d_ts = now
        self._update_status_text("再按一次 Ctrl+D 退出")

    def action_ctrl_c_action(self) -> None:
        """Ctrl+C：复制模式内复制；普通模式单击开复制、双击退出。"""
        if self._inline_copy_mode:
            self._copy_inline_selection()
            return

        now = time.monotonic()
        if now - self._last_ctrl_c_ts <= self._double_press_window_sec:
            self.exit()
            return
        self._last_ctrl_c_ts = now
        self.action_copy_mode()
        self._update_status_text("复制模式已开启：鼠标选择文本后 Ctrl+C 复制；Esc 返回输入")

    def action_escape_action(self) -> None:
        """Esc 连按两次：处理中则中止；否则聚焦输入框。"""
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
            inp = self.query_one("#query-input", Input)
            inp.disabled = False
            inp.focus()
        except Exception:
            pass

    def action_copy_mode(self) -> None:
        """在当前页面切换复制模式（支持鼠标选择与 Ctrl+C）。"""
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
        CopyLogScreen._copy_text(payload)
        self._update_status_text("已复制到剪贴板")

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
            f"  |  Ctrl+C 复制 / Ctrl+C×2 退出"
        )
