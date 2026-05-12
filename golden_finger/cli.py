"""金手指 Agent 系统 — CLI 入口

支持：
- 无参数：启动 FastAPI 服务器 + 打开浏览器（Web TUI）
- --status：终端显示宿主状态
- <query>：单次命令模式
"""

import asyncio
import os
import signal
import socket
import sys
import time
import webbrowser
from pathlib import Path

from rich.console import Console
from rich.panel import Panel
from rich.table import Table
from rich.markdown import Markdown

from .harness import GoldenFingerHarness
from .config import config

# Windows 控制台 UTF-8 编码修复
import io
if sys.platform == "win32":
    sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")
    sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding="utf-8", errors="replace")

console = Console(force_terminal=True)

BANNER = """
╔══════════════════════════════════════════════════════════╗
║                    ✦ 金 手 指 ✦                          ║
║               Golden Finger Agent System                ║
║            ── 像小说主角一样，持续修炼成长 ──             ║
╚══════════════════════════════════════════════════════════╝
"""


def print_banner():
    console.print(BANNER, style="bold yellow")


def print_status(harness: GoldenFingerHarness):
    """显示系统状态"""
    status = harness.get_status()

    host_text = f"""
[bold cyan]灵魂印记:[/bold cyan] {status['soul_mark']}
[bold cyan]修炼境界:[/bold cyan] {status['realm']} {status['realm_stage']}
[bold cyan]境界进度:[/bold cyan] {status['realm_progress']}
[bold cyan]已完成任务:[/bold cyan] {status['total_tasks']}
[bold cyan]总修炼时间:[/bold cyan] {status['total_time_min']} 分钟
"""
    console.print(Panel(host_text.strip(), title="[bold]宿主画像[/bold]", border_style="cyan"))

    root = status["spirit_root"]
    root_text = f"""
金灵根(逻辑):  {_bar(root['metal'])}
木灵根(创造):  {_bar(root['wood'])}
水灵根(沟通):  {_bar(root['water'])}
火灵根(行动):  {_bar(root['fire'])}
土灵根(持久):  {_bar(root['earth'])}
主灵根: [bold yellow]{root['dominant']}[/bold yellow]
"""
    console.print(Panel(root_text.strip(), title="[bold]灵根属性[/bold]", border_style="green"))

    skill_table = Table(title="已安装 Skill", border_style="blue")
    skill_table.add_column("Skill", style="cyan")
    skill_table.add_column("类别", style="yellow")
    skill_table.add_column("等级", justify="center")
    skill_table.add_column("使用次数", justify="right")
    skill_table.add_column("知识条目", justify="right")

    for s in status["skills"]:
        skill_table.add_row(
            s["display_name"],
            s["category"],
            str(s["level"]),
            str(s["total_uses"]),
            str(s["knowledge_count"]),
        )

    console.print(skill_table)

    sys_text = f"""
向量记忆: {status['vector_memories']} 条
LLM 提供商: {status['llm_provider']}
数据目录: {status['data_dir']}
"""
    console.print(Panel(sys_text.strip(), title="[bold]系统信息[/bold]", border_style="dim"))


def _bar(value: float, width: int = 20) -> str:
    """灵根属性进度条"""
    filled = int(value / 100 * width)
    empty = width - filled
    bar_color = "green" if value > 60 else "yellow" if value > 30 else "red"
    return f"[{bar_color}]{'#' * filled}{'-' * empty}[/{bar_color}] {value:.0f}"


def _find_free_port() -> int:
    """找到一个可用的端口"""
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def _frontend_dist() -> str:
    """找到前端构建产物目录"""
    # 检查几个可能的位置
    candidates = [
        os.path.join(os.path.dirname(__file__), "..", "frontend", "dist"),
        os.path.join(os.getcwd(), "frontend", "dist"),
    ]
    for c in candidates:
        p = os.path.abspath(c)
        if os.path.isdir(p):
            return p
    return ""


def run_server(port: int | None = None):
    """启动 FastAPI 服务器 + 打开浏览器"""
    import uvicorn

    print_banner()

    if not config.is_configured():
        console.print("\n[bold red]⚠ LLM API Key 未配置！[/bold red]")
        console.print("请复制 .env.example 为 .env 并填入你的 API Key")
        console.print("[dim]支持 OpenAI (OPENAI_API_KEY) 和 Anthropic (ANTHROPIC_API_KEY)[/dim]\n")
        sys.exit(1)

    if port is None:
        port = _find_free_port()

    # 挂载静态文件
    static_dir = _frontend_dist()
    from .api import app, setup_static

    if static_dir:
        setup_static(app, static_dir)
        console.print(f"[dim]前端目录: {static_dir}[/]")
    else:
        console.print("[yellow]⚠ 未找到前端构建产物，仅 API 服务可用[/]")
        console.print("[dim]请在 frontend/ 目录执行 npm run build[/]")

    url = f"http://127.0.0.1:{port}"
    console.print(f"\n[bold green]✦ 金手指系统正在觉醒...[/]")
    console.print(f"[dim]本地地址: {url}[/]")
    console.print(f"[dim]按 Ctrl+C 停止服务[/]\n")

    # 延迟打开浏览器，等服务器启动
    def _open_browser():
        time.sleep(1.5)
        webbrowser.open(url)

    import threading
    threading.Thread(target=_open_browser, daemon=True).start()

    uvicorn.run(
        app,
        host="127.0.0.1",
        port=port,
        log_level="warning",
    )


def run_tui():
    """启动终端 TUI 模式（同时启动后台 HTTP 监控服务）"""
    from .tui.app import GoldenFingerApp

    if not config.is_configured():
        console.print("[bold red]⚠ LLM API Key 未配置！[/bold red]")
        console.print("请复制 .env.example 为 .env 并填入你的 API Key")
        console.print("[dim]支持 OpenAI (OPENAI_API_KEY) 和 Anthropic (ANTHROPIC_API_KEY)[/dim]\n")
        sys.exit(1)

    # 后台启动监控 HTTP 服务
    monitor_port = _find_free_port()
    _start_monitor_server(monitor_port)

    console.print(f"\n[dim]📊 监控大屏: http://127.0.0.1:{monitor_port}[/]")
    console.print("[dim]   在浏览器中打开上述地址，可实时观察系统日志[/]\n")

    app = GoldenFingerApp(monitor_port=monitor_port)
    app.run()


def _start_monitor_server(port: int):
    """在后台线程中启动监控 HTTP 服务（供 TUI 模式使用）"""
    import threading
    import uvicorn
    from .api import app, setup_static

    static_dir = _frontend_dist()
    if static_dir:
        setup_static(app, static_dir)

    def _serve():
        uvicorn.run(
            app,
            host="127.0.0.1",
            port=port,
            log_level="warning",
        )

    t = threading.Thread(target=_serve, daemon=True, name="monitor-server")
    t.start()


async def run_single_query(query: str):
    """单次命令模式"""
    if not config.is_configured():
        console.print("[bold red]⚠ LLM API Key 未配置！[/bold red]")
        console.print("请复制 .env.example 为 .env 并填入你的 API Key")
        sys.exit(1)

    harness = GoldenFingerHarness()
    console.print(f"[dim]正在处理: {query}[/]")

    try:
        result = await harness.run_query(query)
        if result.get("error"):
            console.print(f"[red]错误: {result['error']}[/red]")
        else:
            final_text = harness.get_final_response(result["execution"])
            console.print(Markdown(final_text))
    except Exception as e:
        console.print(f"[red]错误: {e}[/red]")

    await harness.close()


def main():
    """CLI 入口"""
    # 初始化日志系统
    from .logging import log_manager
    log_manager.setup()

    import argparse

    parser = argparse.ArgumentParser(
        description="金手指 Agent 系统 - Golden Finger Agent System"
    )
    parser.add_argument(
        "query", nargs="?", type=str, default=None,
        help="直接输入问题（否则启动终端 TUI）"
    )
    parser.add_argument(
        "--status", "-s", action="store_true",
        help="显示宿主状态"
    )
    parser.add_argument(
        "--web", "-w", action="store_true",
        help="启动 Web TUI 模式（浏览器）"
    )
    parser.add_argument(
        "--port", "-p", type=int, default=None,
        help="Web 模式指定端口（默认自动分配）"
    )

    args = parser.parse_args()

    if args.status:
        harness = GoldenFingerHarness()
        print_status(harness)
        asyncio.run(harness.close())
    elif args.query:
        asyncio.run(run_single_query(args.query))
    elif args.web:
        run_server(port=args.port)
    else:
        run_tui()


if __name__ == "__main__":
    main()
