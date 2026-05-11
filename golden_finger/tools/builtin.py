"""金手指 Agent 系统 — 内置工具

file_read / file_write / shell_exec / web_search
"""

import asyncio
import re
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

    @staticmethod
    def _unwrap_outer_quotes(cmd: str) -> str:
        """去掉整串包裹引号。"""
        if len(cmd) >= 2 and cmd[0] == "'" and cmd[-1] == "'":
            return cmd[1:-1].strip()
        if len(cmd) >= 2 and cmd[0] == '"' and cmd[-1] == '"':
            return cmd[1:-1].strip()
        return cmd

    @staticmethod
    def _normalize_windows_command(command: str) -> str:
        """归一化 Windows 命令，避免模型输出的重复包装导致执行失败。"""
        cmd = command.strip()
        if not cmd:
            return cmd

        # 去掉重复的 cmd /c 前缀（本工具会统一再包一层）
        cmd = re.sub(r"^\s*cmd(?:\.exe)?\s*/c\s+", "", cmd, flags=re.IGNORECASE)
        return ShellExecTool._unwrap_outer_quotes(cmd)

    @staticmethod
    def _normalize_posix_command(command: str) -> str:
        """归一化 Linux/macOS 命令，避免 shell 包装和跨平台误用导致失败。"""
        cmd = command.strip()
        if not cmd:
            return cmd

        # 去掉重复 shell 包装（本工具会统一用 sh -c）
        cmd = re.sub(r"^\s*(?:sh|bash|zsh)\s+-[lc]\s+", "", cmd, flags=re.IGNORECASE)
        # 去掉 Windows 包装
        cmd = re.sub(r"^\s*cmd(?:\.exe)?\s*/c\s+", "", cmd, flags=re.IGNORECASE)
        cmd = ShellExecTool._unwrap_outer_quotes(cmd)

        # 轻量兼容：将常见 Windows dir 命令映射到 ls
        if re.match(r"^\s*dir(?:\s|$)", cmd, flags=re.IGNORECASE):
            has_b = bool(re.search(r"(^|\s)/b(\s|$)", cmd, flags=re.IGNORECASE))
            has_a = bool(re.search(r"(^|\s)/a(\s|$)", cmd, flags=re.IGNORECASE))
            # 提取路径参数（忽略 /x 形式开关）
            parts = [p for p in cmd.split()[1:] if not p.startswith("/")]
            target = " ".join(parts).strip() or "."
            ls_flags = []
            if has_b:
                ls_flags.append("-1")
            if has_a:
                ls_flags.append("-a")
            flag_str = (" " + " ".join(ls_flags)) if ls_flags else ""
            cmd = f"ls{flag_str} {target}".strip()

        return cmd

    @staticmethod
    def _normalize_command_for_platform(command: str) -> str:
        if sys.platform == "win32":
            return ShellExecTool._normalize_windows_command(command)
        return ShellExecTool._normalize_posix_command(command)

    @staticmethod
    def _rewrite_command_after_failure(command: str, err_output: str) -> str | None:
        """失败后重写命令（仅一次），用于处理明显的跨平台语法不匹配。"""
        c = command.strip()
        e = (err_output or "").lower()
        if not c:
            return None

        if sys.platform == "win32":
            # 例：'ls' 不是内部或外部命令
            if ("不是内部或外部命令" in e) or ("is not recognized as an internal or external command" in e):
                if re.match(r"^\s*ls(?:\s|$)", c):
                    # ls -> dir /b
                    rest = c[2:].strip()
                    return f"dir /b {rest}".strip()
                if re.match(r"^\s*pwd\s*$", c):
                    return "cd"
            return None

        # POSIX：例 'dir /b /a' 残留，重写为 ls
        if ("not found" in e) or ("illegal option" in e) or ("invalid option" in e):
            if re.match(r"^\s*dir(?:\s|$)", c, flags=re.IGNORECASE):
                return ShellExecTool._normalize_posix_command(c)
            if re.match(r"^\s*cd\s*$", c):
                return "pwd"
        return None

    async def execute(self, command: str = "") -> ToolResult:  # pyright: ignore[reportIncompatibleMethodOverride]
        try:
            command = self._normalize_command_for_platform(command)
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
                # 补充一层：失败后二次重写重试（仅一次）
                rewritten = self._rewrite_command_after_failure(command, err_output)
                if rewritten and rewritten != command:
                    rewritten = Sandbox.check_shell_command(rewritten)
                    if sys.platform == "win32":
                        retry_shell = ["cmd", "/c", rewritten]
                    else:
                        retry_shell = ["sh", "-c", rewritten]

                    retry_proc = await asyncio.create_subprocess_exec(
                        *retry_shell,
                        stdout=asyncio.subprocess.PIPE,
                        stderr=asyncio.subprocess.PIPE,
                        cwd=str(config.data_dir),
                    )
                    try:
                        retry_stdout, retry_stderr = await asyncio.wait_for(
                            retry_proc.communicate(), timeout=config.tool_timeout_sec
                        )
                    except asyncio.TimeoutError:
                        retry_proc.kill()
                        return ToolResult(success=False, error="命令执行超时（重试）")

                    retry_out = retry_stdout.decode("utf-8", errors="replace")
                    retry_err = retry_stderr.decode("utf-8", errors="replace")
                    if retry_proc.returncode == 0:
                        return ToolResult(
                            success=True,
                            data=retry_out or "命令执行成功（重试后无输出）",
                        )

                    return ToolResult(
                        success=False,
                        data={
                            "stdout": output,
                            "stderr": err_output,
                            "returncode": proc.returncode,
                            "rewritten_command": rewritten,
                            "retry_stdout": retry_out,
                            "retry_stderr": retry_err,
                            "retry_returncode": retry_proc.returncode,
                        },
                        error=f"命令返回非零退出码: {proc.returncode}（已重试）",
                    )

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
