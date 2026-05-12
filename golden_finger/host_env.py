"""宿主环境检测与持久化记忆。

每次启动时自动检测本机环境（Python 路径、OS、Shell、工作区），
持久化到 data/host_profile.json，并在系统提示词中注入这些信息，
确保 LLM 总能使用正确的 Python 解释器和路径。
"""

from __future__ import annotations

import json
import os
import platform
import sys
from pathlib import Path
from typing import Any

from .config import config


class HostEnvironment:
    """宿主环境信息，持久化到 host_profile.json。"""

    def __init__(self) -> None:
        self._profile: dict[str, Any] = {}
        self._loaded = False

    # ---- 加载 / 保存 ----

    def load(self) -> dict[str, Any]:
        """加载持久化画像，若不存在则自动检测。"""
        if self._loaded:
            return self._profile

        path = config.host_profile_path
        if path.exists():
            try:
                self._profile = json.loads(path.read_text(encoding="utf-8"))
                # 补全可能缺失的字段
                self._fill_missing()
                self._loaded = True
                return self._profile
            except Exception:
                pass

        self._profile = self._detect()
        self._loaded = True
        return self._profile

    def save(self) -> None:
        """持久化当前环境画像。"""
        self.load()
        self._profile["_updated_at"] = self._now_iso()
        self._ensure_memories()
        config.host_profile_path.parent.mkdir(parents=True, exist_ok=True)
        config.host_profile_path.write_text(
            json.dumps(self._profile, ensure_ascii=False, indent=2),
            encoding="utf-8",
        )

    # ---- 检测 ----

    @staticmethod
    def _detect() -> dict[str, Any]:
        cwd = Path.cwd().resolve()
        python_exe = sys.executable

        # 找到实际可用的 python 命令
        python_cmd = "python"
        if sys.platform == "win32":
            python_cmd = python_exe  # 直接用绝对路径最可靠

        return {
            "os": sys.platform,
            "os_name": platform.system(),
            "os_version": platform.version(),
            "hostname": platform.node(),
            "python_exe": python_exe,
            "python_cmd": python_cmd,
            "python_version": sys.version.split()[0],
            "cwd": str(cwd),
            "workspace_root": str(cwd),
            "data_dir": str(config.data_dir.resolve()),
            "shell": os.environ.get("SHELL", os.environ.get("COMSPEC", "cmd.exe")),
            "shell_type": "cmd" if sys.platform == "win32" else "bash",
            "home_dir": str(Path.home()),
            "encoding": sys.getdefaultencoding(),
            "_created_at": HostEnvironment._now_iso(),
        }

    def _fill_missing(self) -> None:
        """补全旧画像中可能缺失的字段。"""
        defaults = self._detect()
        for key, value in defaults.items():
            if key not in self._profile:
                self._profile[key] = value

    def _ensure_memories(self) -> None:
        """确保关键环境信息保存在持久化记忆中，方便检索。"""
        p = self._profile
        memories: dict[str, Any] = p.setdefault("memories", {})

        # 运行环境记忆
        memories.setdefault("python_exe", {
            "value": p.get("python_cmd", sys.executable),
            "desc": "Python 解释器绝对路径，执行 Python 命令时必须使用此路径",
            "_saved_at": self._now_iso(),
        })
        memories.setdefault("shell_type", {
            "value": p.get("shell_type", "cmd"),
            "desc": "Shell 类型：cmd=Windows CMD 语法，bash=POSIX shell 语法",
            "_saved_at": self._now_iso(),
        })
        memories.setdefault("os_name", {
            "value": p.get("os_name", platform.system()),
            "desc": f"操作系统: {p.get('os_name', '')} {p.get('os_version', '')}",
            "_saved_at": self._now_iso(),
        })
        memories.setdefault("encoding", {
            "value": p.get("encoding", "utf-8"),
            "desc": "系统默认编码",
            "_saved_at": self._now_iso(),
        })

        # 工作区记忆
        memories.setdefault("workspace_root", {
            "value": p.get("workspace_root", ""),
            "desc": "项目根目录，所有文件操作基于此路径",
            "_saved_at": self._now_iso(),
        })
        memories.setdefault("home_dir", {
            "value": p.get("home_dir", ""),
            "desc": "用户主目录",
            "_saved_at": self._now_iso(),
        })
        memories.setdefault("data_dir", {
            "value": p.get("data_dir", ""),
            "desc": "数据存储目录 (logs/memory/skills)",
            "_saved_at": self._now_iso(),
        })

        # 重要提醒记忆
        memories.setdefault("python_cmd_reminder", {
            "value": f"执行 Python 命令时使用: {p.get('python_cmd', sys.executable)}",
            "desc": "Python 命令执行提醒",
            "_saved_at": self._now_iso(),
        })
        memories.setdefault("shell_cmd_reminder", {
            "value": "Windows 上使用 cmd /c 包装命令，不要使用 bash 专有语法",
            "desc": "Shell 命令执行提醒",
            "_saved_at": self._now_iso(),
        })
        memories.setdefault("path_reminder", {
            "value": f"文件路径使用 {p.get('workspace_root', '')} 下的相对路径或绝对路径",
            "desc": "文件路径使用规范",
            "_saved_at": self._now_iso(),
        })

    @staticmethod
    def _now_iso() -> str:
        from datetime import datetime, timezone
        return datetime.now(timezone.utc).isoformat()

    # ---- 系统提示词片段 ----

    def get_prompt_context(self) -> str:
        """生成注入系统提示词的环境上下文。"""
        p = self.load()

        lines = [
            f"【运行环境】",
            f"- 操作系统: {p.get('os_name', '')} ({p.get('os', '')})",
            f"- Python 解释器: {p.get('python_cmd', 'python')}",
            f"- Python 版本: {p.get('python_version', '')}",
            f"- Shell: {p.get('shell_type', 'bash')} ({p.get('shell', '')})",
            f"- 编码: {p.get('encoding', 'utf-8')}",
            f"",
            f"【工作区】",
            f"- 项目根目录: {p.get('workspace_root', '')}",
            f"- 当前目录: {p.get('cwd', '')}",
            f"- 数据目录: {p.get('data_dir', '')}",
            f"- 用户主目录: {p.get('home_dir', '')}",
            f"",
            f"【重要提醒】",
            f"- 执行 Python 命令时，请使用: {p.get('python_cmd', 'python')}",
            f"- 执行 Shell 命令时，{'Windows CMD 语法 (cmd /c ...)' if p.get('shell_type') == 'cmd' else '使用 POSIX shell 语法'}",
            f"- 文件路径使用: {p.get('workspace_root', '')} 下的相对路径或绝对路径",
        ]

        return "\n".join(lines)

    # ---- 持久化记忆 ----

    def remember(self, key: str, value: Any) -> None:
        """存储一条持久化记忆（如用户偏好、常用路径）。"""
        self.load()
        memories: dict[str, Any] = self._profile.setdefault("memories", {})
        memories[key] = {"value": value, "_saved_at": self._now_iso()}
        self.save()

    def recall(self, key: str) -> Any | None:
        """读取一条持久化记忆。"""
        self.load()
        memories: dict[str, Any] = self._profile.get("memories", {})
        entry = memories.get(key)
        return entry.get("value") if isinstance(entry, dict) else None

    def list_memories(self) -> dict[str, Any]:
        """列出所有持久化记忆。"""
        self.load()
        return self._profile.get("memories", {})

    def forget(self, key: str) -> bool:
        """删除一条记忆。"""
        self.load()
        memories: dict[str, Any] = self._profile.setdefault("memories", {})
        if key in memories:
            del memories[key]
            self.save()
            return True
        return False


# 全局实例
host_env = HostEnvironment()
