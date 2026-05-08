"""金手指 Agent 系统 — 配置管理"""

import os
from pathlib import Path

from dotenv import load_dotenv


# 加载 .env 文件
_ENV_FILE = Path(os.environ.get("GOLDEN_FINGER_ENV", ".env"))
if _ENV_FILE.exists():
    load_dotenv(_ENV_FILE, override=True)


class Config:
    """全局配置单例"""

    def __init__(self):
        # 数据目录
        self.data_dir = Path(
            os.environ.get("GOLDEN_FINGER_DATA_DIR", "./data")
        ).resolve()

        # LLM 配置
        self.llm_provider = os.environ.get("GOLDEN_FINGER_LLM_PROVIDER", "openai")

        # OpenAI
        self.openai_api_key = os.environ.get("OPENAI_API_KEY", "")
        self.openai_base_url = os.environ.get("OPENAI_BASE_URL", "https://api.openai.com/v1")
        self.openai_model = os.environ.get("OPENAI_MODEL", "gpt-4o")

        # Anthropic
        self.anthropic_api_key = os.environ.get("ANTHROPIC_API_KEY", "")
        self.anthropic_model = os.environ.get("ANTHROPIC_MODEL", "claude-sonnet-4-6")

        # 日志
        self.log_level = os.environ.get("GOLDEN_FINGER_LOG_LEVEL", "INFO")

        # 超时与重试
        self.llm_timeout_sec = int(os.environ.get("GOLDEN_FINGER_LLM_TIMEOUT", "120"))
        self.llm_max_retries = int(os.environ.get("GOLDEN_FINGER_LLM_RETRIES", "2"))
        self.tool_timeout_sec = int(os.environ.get("GOLDEN_FINGER_TOOL_TIMEOUT", "60"))

        # 沙箱
        self.sandbox_enabled = os.environ.get("GOLDEN_FINGER_SANDBOX", "1") == "1"

        # 确保目录存在
        self._ensure_dirs()

    def _ensure_dirs(self):
        """确保运行时目录存在"""
        dirs = [
            self.data_dir,
            self.data_dir / "skills",
            self.data_dir / "memory",
            self.data_dir / "logs",
        ]
        for d in dirs:
            d.mkdir(parents=True, exist_ok=True)

    @property
    def host_profile_path(self) -> Path:
        return self.data_dir / "host_profile.json"

    @property
    def skills_dir(self) -> Path:
        return self.data_dir / "skills"

    @property
    def memory_dir(self) -> Path:
        return self.data_dir / "memory"

    @property
    def logs_dir(self) -> Path:
        return self.data_dir / "logs"

    def is_configured(self) -> bool:
        """检查是否至少配置了一个 LLM 提供商"""
        if self.llm_provider == "openai":
            return bool(self.openai_api_key)
        elif self.llm_provider == "anthropic":
            return bool(self.anthropic_api_key)
        return False

    def display(self) -> str:
        """显示当前配置（隐藏敏感信息）"""
        lines = [
            f"LLM 提供商: {self.llm_provider}",
            f"OpenAI 模型: {self.openai_model}",
            f"Anthropic 模型: {self.anthropic_model}",
            f"数据目录: {self.data_dir}",
            f"沙箱模式: {'开启' if self.sandbox_enabled else '关闭'}",
            f"日志级别: {self.log_level}",
        ]
        return "\n".join(lines)


# 全局配置实例
config = Config()
