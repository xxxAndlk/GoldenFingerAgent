"""金手指 Agent 系统 — 工具基类"""

from abc import ABC, abstractmethod
from typing import Any


class ToolResult:
    """工具执行结果"""

    def __init__(self, success: bool, data: Any = None, error: str = ""):
        self.success = success
        self.data = data
        self.error = error

    def __repr__(self):
        if self.success:
            return f"ToolResult(ok, {self.data!r})"
        return f"ToolResult(err, {self.error!r})"


class BaseTool(ABC):
    """工具基类"""

    name: str = ""
    description: str = ""
    parameters: dict[str, Any] = {}

    def to_openai_schema(self) -> dict[str, Any]:
        """转为 OpenAI function calling 格式"""
        return {
            "type": "function",
            "function": {
                "name": self.name,
                "description": self.description,
                "parameters": {
                    "type": "object",
                    "properties": self.parameters,
                    "required": list(self.parameters.keys()),
                }
            }
        }

    @abstractmethod
    async def execute(self, **kwargs: str) -> ToolResult:
        """执行工具，子类实现"""
        ...

    def validate_params(self, **kwargs: str) -> str | None:
        """校验参数，返回错误信息或 None"""
        for key in self.parameters:
            if key not in kwargs:
                return f"缺少必要参数: {key}"
        return None
