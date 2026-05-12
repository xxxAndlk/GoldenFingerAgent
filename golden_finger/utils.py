"""金手指 Agent 系统 — 通用工具函数"""

import json
import re
from typing import Any


def parse_json(text: str, default: Any = None) -> dict[str, Any]:
    """从 LLM 回复文本中提取 JSON 对象。

    支持纯 JSON 和 ```json ... ``` 代码块两种格式。
    """
    try:
        return json.loads(text)
    except json.JSONDecodeError:
        pass
    match = re.search(r'```(?:json)?\s*(.*?)\s*```', text, re.DOTALL)
    if match:
        try:
            return json.loads(match.group(1))
        except json.JSONDecodeError:
            pass
    if default is not None:
        return default
    return {}
