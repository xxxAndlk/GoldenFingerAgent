"""金手指 Agent 系统 — 沙箱执行器

对 Shell 命令和文件操作进行安全限制。
"""

import os
import re
from pathlib import Path

from ..config import config


class SandboxError(Exception):
    """沙箱拦截错误"""
    pass


class Sandbox:
    """执行沙箱：检测和阻止危险操作"""

    # Shell 注入检测模式
    DANGEROUS_SHELL_PATTERNS: list[str] = [
        r"rm\s+-rf\s+/",           # 删除根目录
        r"mkfs\.",                  # 格式化
        r"dd\s+if=",               # 磁盘直接写入
        r">\s*/dev/sd",            # 写入块设备
        r"chmod\s+777\s+/",        # 权限修改根目录
        r":\(\)\s*\{\s*:\|:&\s*\};:",  # fork bomb
        r"wget\s+.*\|\s*sh",       # 管道执行远程脚本
        r"curl\s+.*\|\s*sh",       # 同上
        r"\.\./\.\./\.\./",        # 路径穿越（过多层级）
        r"eval\s+",                 # eval 命令
        r"exec\s+",                 # exec 命令
        r"__import__\s*\(",         # Python __import__
        r"subprocess",              # subprocess 模块
        r"os\.system",             # os.system
        r"os\.popen",              # os.popen
    ]

    # 危险文件路径（不允许触碰）
    FORBIDDEN_PATHS: list[str] = [
        "/etc/passwd",
        "/etc/shadow",
        "/etc/hosts",
        "/etc/ssh",
        "~/.ssh",
        "C:\\Windows",
        "C:\\Windows\\System32",
        os.path.expanduser("~/.ssh"),
        os.path.expanduser("~/.gnupg"),
    ]

    # 允许的文件读写目录
    ALLOWED_DIRS: list[Path] = []

    @classmethod
    def init_allowed_dirs(cls):
        cls.ALLOWED_DIRS = [
            config.data_dir.resolve(),
            Path.cwd().resolve(),
        ]
        # 添加系统临时目录
        import tempfile
        cls.ALLOWED_DIRS.append(Path(tempfile.gettempdir()).resolve())

    @classmethod
    def check_shell_command(cls, command: str) -> str:
        """检查 Shell 命令是否安全

        Returns:
            净化后的命令，或抛出 SandboxError
        """
        if not config.sandbox_enabled:
            return command

        # 检查危险模式
        for pattern in cls.DANGEROUS_SHELL_PATTERNS:
            if re.search(pattern, command, re.IGNORECASE):
                raise SandboxError(f"检测到危险命令模式: {pattern}")

        # 限制命令长度
        if len(command) > 8000:
            raise SandboxError("命令过长，拒绝执行")

        return command

    @classmethod
    def check_file_path(cls, file_path: str, must_exist: bool = False) -> Path:
        """检查文件路径是否在允许范围内

        Args:
            file_path: 要操作的文件路径
            must_exist: 如果是读操作，文件必须已存在

        Returns:
            解析后的绝对路径

        Raises:
            SandboxError: 路径不在允许范围内
        """
        if not config.sandbox_enabled:
            return Path(file_path).resolve()

        if not cls.ALLOWED_DIRS:
            cls.init_allowed_dirs()

        path = Path(file_path).resolve()

        # 如果文件必须存在，先检查
        if must_exist and not path.exists():
            raise SandboxError(f"文件不存在: {path}")

        # 检查是否在禁止路径中
        for forbidden in cls.FORBIDDEN_PATHS:
            fp = Path(forbidden).expanduser().resolve()
            try:
                path.relative_to(fp)
                raise SandboxError(f"禁止访问路径: {forbidden}")
            except ValueError:
                pass  # 不在禁止路径内

        # 检查是否在允许目录中
        in_allowed = False
        for allowed in cls.ALLOWED_DIRS:
            try:
                path.relative_to(allowed)
                in_allowed = True
                break
            except ValueError:
                continue

        if not in_allowed:
            raise SandboxError(f"路径不在允许范围内: {path}")

        return path

    @classmethod
    def check_content_injection(cls, content: str) -> bool:
        """检查内容是否包含注入攻击

        Returns:
            True 表示安全，False 表示检测到注入
        """
        if not content:
            return True

        # 检测常见注入签名
        injection_signatures = [
            r"<script.*?>",           # XSS
            r"javascript:",           # JS 协议
            r"data:text/html",        # data URI
            r"\{\{.*?\}\}",          # Jinja2 模板语法
            r"\$\{.*?\}",            # 模板注入（排除代码示例中的正常使用）
            r"DROP\s+TABLE\b",        # SQL DROP（完整词边界）
            r"UNION\s+SELECT\b",      # SQL UNION（完整词边界）
        ]

        for sig in injection_signatures:
            if re.search(sig, content, re.IGNORECASE):
                return False

        return True
