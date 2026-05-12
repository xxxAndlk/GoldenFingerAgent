"""弹窗屏幕：帮助、历史搜索、设置。"""

from textual.app import ComposeResult
from textual.binding import Binding
from textual.containers import Container
from textual.screen import ModalScreen
from textual.widgets import Static, TextArea, Button


class HelpScreen(ModalScreen[None]):
    """帮助弹窗：显示快捷键和命令列表。"""

    BINDINGS = [
        Binding("escape", "close", "关闭", show=False),
    ]

    def compose(self) -> ComposeResult:
        with Container(id="help-modal"):
            yield Static(
                "✦ 帮助 / 快捷键\n\n"
                "基础命令:\n"
                "  /help            显示此帮助\n"
                "  /status          显示宿主状态\n"
                "  /clear           清空对话\n"
                "  /save  [name]    保存会话\n"
                "  /export          导出对话日志\n"
                "  /copy            打开可选择复制视图\n"
                "  /pipeline        切换至完整五域流水线\n\n"
                "快捷键:\n"
                "  Ctrl+S           保存会话\n"
                "  Ctrl+M           导出日志\n"
                "  Ctrl+C           开启文字选择 → 选中后 Ctrl+C 复制\n"
                "  Ctrl+C×2 (1s内)  退出\n"
                "  Ctrl+Enter       发送消息\n"
                "  Shift+Enter      换行\n"
                "  Ctrl+Up/Down     历史命令\n"
                "  Esc×2            中止当前对话\n\n"
                "按 Esc 关闭",
                id="help-text",
            )

    def action_close(self) -> None:
        self.dismiss()


class HistorySearchScreen(ModalScreen[str | None]):
    """历史命令搜索弹窗。"""

    BINDINGS = [
        Binding("escape", "close", "关闭", show=False),
        Binding("ctrl+enter", "select", "选择", show=False),
    ]

    def __init__(self, history: list[str]) -> None:
        super().__init__()
        self._history = history

    def compose(self) -> ComposeResult:
        with Container(id="history-modal"):
            yield Static("🔍 搜索历史命令（输入关键词过滤）", id="history-title")
            yield TextArea("", id="history-search", show_line_numbers=False)
            yield TextArea(
                "\n".join(reversed(self._history[-50:])),
                id="history-list",
                read_only=True,
                show_line_numbers=False,
            )

    def on_mount(self) -> None:
        self.query_one("#history-search", TextArea).focus()

    def action_close(self) -> None:
        self.dismiss(None)

    def action_select(self) -> None:
        selected = self.query_one("#history-list", TextArea).selected_text.strip()
        if selected:
            self.dismiss(selected)
