"""金手指 Agent 系统 — 内置工具

file_read / file_write / shell_exec / web_search
"""

import asyncio
import sys

import httpx

from ..config import config
from .base import BaseTool, ToolResult
from .sandbox import Sandbox, SandboxError


class FileReadTool(BaseTool):
    name = "file_read"
    description = "读取文件内容。输入文件路径，返回文件文本内容。"
    parameters = {
        "file_path": {
            "type": "string",
            "description": "要读取的文件路径（绝对路径或相对路径）"
        }
    }

    async def execute(self, file_path: str = "") -> ToolResult:  # pyright: ignore[reportIncompatibleMethodOverride]
        try:
            path = Sandbox.check_file_path(file_path, must_exist=True)
            content = path.read_text(encoding="utf-8", errors="replace")
            # 大文件截断
            if len(content) > 50000:
                content = content[:50000] + "\n\n... [文件过大，已截断]"
            return ToolResult(success=True, data=content)
        except SandboxError as e:
            return ToolResult(success=False, error=str(e))
        except Exception as e:
            return ToolResult(success=False, error=f"读取失败: {e}")


class FileWriteTool(BaseTool):
    name = "file_write"
    description = "写入内容到文件。输入文件路径和要写入的内容，返回写入结果。"
    parameters = {
        "file_path": {
            "type": "string",
            "description": "要写入的文件路径"
        },
        "content": {
            "type": "string",
            "description": "要写入的文本内容"
        }
    }

    async def execute(self, file_path: str = "", content: str = "") -> ToolResult:  # pyright: ignore[reportIncompatibleMethodOverride]
        try:
            path = Sandbox.check_file_path(file_path)
            # 检查内容注入
            if not Sandbox.check_content_injection(content):
                return ToolResult(success=False, error="内容包含不安全代码，已拦截")
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(content, encoding="utf-8")
            return ToolResult(success=True, data=f"已写入 {len(content)} 字符到 {path}")
        except SandboxError as e:
            return ToolResult(success=False, error=str(e))
        except Exception as e:
            return ToolResult(success=False, error=f"写入失败: {e}")


class ShellExecTool(BaseTool):
    name = "shell_exec"
    description = "执行 Shell 命令。输入要执行的命令，返回标准输出和错误输出。"
    parameters = {
        "command": {
            "type": "string",
            "description": "要执行的 Shell 命令"
        }
    }

    async def execute(self, command: str = "") -> ToolResult:  # pyright: ignore[reportIncompatibleMethodOverride]
        try:
            command = Sandbox.check_shell_command(command)
            # 根据平台选择 shell
            if sys.platform == "win32":
                shell_cmd = ["cmd", "/c", command]
            else:
                shell_cmd = ["sh", "-c", command]

            proc = await asyncio.create_subprocess_exec(
                *shell_cmd,
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.PIPE,
                cwd=str(config.data_dir),
            )
            try:
                stdout, stderr = await asyncio.wait_for(
                    proc.communicate(), timeout=config.tool_timeout_sec
                )
            except asyncio.TimeoutError:
                proc.kill()
                return ToolResult(success=False, error="命令执行超时")

            output = stdout.decode("utf-8", errors="replace")
            err_output = stderr.decode("utf-8", errors="replace")

            if proc.returncode != 0:
                return ToolResult(
                    success=False,
                    data={"stdout": output, "stderr": err_output, "returncode": proc.returncode},
                    error=f"命令返回非零退出码: {proc.returncode}"
                )

            return ToolResult(success=True, data=output or "命令执行成功（无输出）")
        except SandboxError as e:
            return ToolResult(success=False, error=str(e))
        except Exception as e:
            return ToolResult(success=False, error=f"执行失败: {e}")


class WebSearchTool(BaseTool):
    name = "web_search"
    description = "搜索网页内容。输入搜索关键词，返回搜索结果摘要。"
    parameters = {
        "query": {
            "type": "string",
            "description": "搜索关键词"
        }
    }

    async def execute(self, query: str = "") -> ToolResult:  # pyright: ignore[reportIncompatibleMethodOverride]
        try:
            # 使用 DuckDuckGo 的 HTML 版本（无需 API Key）
            url = "https://html.duckduckgo.com/html/"
            async with httpx.AsyncClient(timeout=15) as client:
                resp = await client.post(url, data={"q": query})
                if resp.status_code != 200:
                    return ToolResult(success=False, error=f"搜索请求失败: HTTP {resp.status_code}")

                # 简单提取搜索结果
                html = resp.text
                results = self._parse_ddg_results(html)
                if not results:
                    return ToolResult(success=True, data="未找到相关结果")
                return ToolResult(success=True, data="\n\n".join(results[:5]))

        except httpx.TimeoutException:
            return ToolResult(success=False, error="搜索请求超时")
        except Exception as e:
            return ToolResult(success=False, error=f"搜索失败: {e}")

    @staticmethod
    def _parse_ddg_results(html: str) -> list[str]:
        """简单解析 DuckDuckGo HTML 结果"""
        import re
        results: list[str] = []
        # 匹配结果片段
        snippets = re.findall(r'class="result__snippet"[^>]*>(.*?)</a>', html, re.DOTALL)
        titles = re.findall(r'class="result__title"[^>]*>.*?<a[^>]*>(.*?)</a>', html, re.DOTALL)
        for i, (title, snippet) in enumerate(zip(titles, snippets)):
            title_text = re.sub(r'<[^>]+>', '', title).strip()
            snippet_text = re.sub(r'<[^>]+>', '', snippet).strip()
            results.append(f"{i+1}. {title_text}\n   {snippet_text}")
        return results


# 内置工具注册表
BUILTIN_TOOLS: dict[str, BaseTool] = {
    "file_read": FileReadTool(),
    "file_write": FileWriteTool(),
    "shell_exec": ShellExecTool(),
    "web_search": WebSearchTool(),
}
