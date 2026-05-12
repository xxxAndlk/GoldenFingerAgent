"""TUI 独立组件：状态栏、任务面板、子代理面板。"""

from typing import Any

from textual.containers import Container
from textual.widgets import RichLog, Static


class StatusBar(Static):
    """底部状态栏，支持文本和 renderable 两种模式。"""

    def compose(self) -> Container:
        yield Static(id="status-info")


class TaskPanel(Static):
    """右上角任务计划面板。显示流水线 DAG 任务状态。"""

    def __init__(self, **kwargs: Any):
        super().__init__(**kwargs)
        self.tasks: list[dict[str, Any]] = []

    def compose(self) -> Container:
        yield Static("🎯 任务计划", classes="panel-title")
        yield RichLog(id="task-log", classes="panel-content", wrap=True, auto_scroll=True)

    def update_tasks(self, plan: Any) -> None:
        from ..models import TaskPlan
        self.tasks = []
        log = self.query_one("#task-log", RichLog)
        log.clear()
        if not plan or not isinstance(plan, TaskPlan) or not plan.tasks:
            log.write("[dim #6c7086]暂无任务[/]")
            return
        for task in plan.tasks:
            self.tasks.append({
                "id": task.task_id,
                "desc": task.description,
                "status": "pending",
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
        icons: dict[str, str] = {
            "pending": "⏳", "running": "▶", "completed": "✓", "error": "✗",
        }
        colors: dict[str, str] = {
            "pending": "dim #6c7086",
            "running": "#f9e2af",
            "completed": "#a6e3a1",
            "error": "#f38ba8",
        }
        for i, t in enumerate(self.tasks):
            status = t["status"]
            icon = icons.get(status, "-")
            color = colors.get(status, "dim")
            log.write(f"[{icon}] [{color}]{i + 1}. {t['desc']}[/]")


class AgentPanel(Static):
    """右下角子代理/工具协同面板。显示实时执行日志。"""

    def compose(self) -> Container:
        yield Static("🤖 子Agent/工具 协同", classes="panel-title")
        yield RichLog(id="agent-log", classes="panel-content", wrap=True, auto_scroll=True)

    def write_log(self, text: str) -> None:
        self.query_one("#agent-log", RichLog).write(text)
