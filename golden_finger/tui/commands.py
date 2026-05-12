"""Slash 命令处理：/help /status /clear /save /export /copy /pipeline。"""

from __future__ import annotations

from typing import TYPE_CHECKING

from textual.widgets import RichLog

from ..config import config

if TYPE_CHECKING:
    from .app import GoldenFingerApp


class CommandHandler:
    """处理 /slash 命令，委托给主 App 执行交互操作。"""

    def __init__(self, app: GoldenFingerApp) -> None:
        self._app = app

    async def handle(self, cmd: str) -> None:
        log = self._app.query_one("#chat-log", RichLog)
        parts = cmd.split(maxsplit=1)
        action = parts[0].lower()
        arg = parts[1] if len(parts) > 1 else ""

        if action == "/help":
            await self._show_help(log)
        elif action == "/status":
            await self._show_status(log)
        elif action == "/clear":
            log.clear()
            self._app.chat_memory.clear()
            log.write("[dim #6c7086]已清空对话与会话记忆上下文[/]")
        elif action == "/save":
            await self._app._save_session(arg)
        elif action == "/export":
            await self._app._export_log()
        elif action == "/copy":
            self._app.action_copy_mode()
        elif action == "/pipeline":
            self._app._pipeline_mode = not self._app._pipeline_mode
            status = "已启用" if self._app._pipeline_mode else "已关闭"
            log.write(f"[#f9e2af]五域流水线模式: {status}[/]")
            from ..logging import log_system
            log_system(f"流水线模式切换: {status}")
        else:
            log.write(f"[#f38ba8]未知指令: {action}，输入 /help 查看[/]")

    async def _show_help(self, log: RichLog) -> None:
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
            "Ctrl+C           开启文字选择 → 选中后 Ctrl+C 复制（自动返回）",
            "Ctrl+C×2 (1s内)  退出",
            "Ctrl+Enter       发送消息",
            "Shift+Enter      换行",
            "Ctrl+Up/Down     历史命令",
            "Esc×2            中止当前对话",
        ]:
            log.write(f"[dim #a6adc8]  {item}[/]")
        log.write("")

    async def _show_status(self, log: RichLog) -> None:
        assert self._app.harness is not None
        s = self._app.harness.get_status()
        log.write("[bold #cba6f7]✦ 宿主状态[/]")
        log.write(f"  [dim]灵魂印记:[/] {s.get('soul_mark', '')[:12]}")
        log.write(
            f"  [dim]修炼境界:[/] {s.get('realm', '')} {s.get('realm_stage', '')}"
            f" ({s.get('realm_progress', '')})"
        )
        log.write(f"  [dim]已完成:[/] {s.get('total_tasks', 0)} 任务")
        log.write(f"  [dim]修炼时间:[/] {s.get('total_time_min', 0)} 分钟")
        log.write(f"  [dim]Skill:[/] {len(s.get('skills', []))} 个")
        log.write(f"  [dim]LLM:[/] {config.openai_model}")
        log.write("")
