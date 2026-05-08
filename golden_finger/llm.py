"""金手指 Agent 系统 — LLM 抽象层

支持 OpenAI 和 Anthropic，统一接口。
"""

import asyncio
import json
from typing import Any, AsyncGenerator

import httpx

from .config import config


class LLMError(Exception):
    """LLM 调用错误"""
    pass


class LLMClient:
    """统一的 LLM 客户端"""

    def __init__(self):
        self._client: httpx.AsyncClient | None = None

    async def _get_client(self) -> httpx.AsyncClient:
        if self._client is None:
            self._client = httpx.AsyncClient(timeout=config.llm_timeout_sec)
        return self._client

    async def close(self):
        if self._client:
            await self._client.aclose()
            self._client = None

    # ---- 公开接口 ----

    async def chat(
        self,
        messages: list[dict[str, Any]],
        tools: list[dict[str, Any]] | None = None,
        tool_choice: str = "auto",
        model: str | None = None,
        max_tokens: int = 4096,
    ) -> dict[str, Any]:
        """发送对话请求，返回完整响应"""
        if config.llm_provider == "openai":
            return await self._chat_openai(messages, tools, tool_choice, model, max_tokens)
        elif config.llm_provider == "anthropic":
            return await self._chat_anthropic(messages, tools, tool_choice, model, max_tokens)
        else:
            raise LLMError(f"不支持的 LLM 提供商: {config.llm_provider}")

    async def chat_stream(
        self,
        messages: list[dict[str, Any]],
        tools: list[dict[str, Any]] | None = None,
        model: str | None = None,
        max_tokens: int = 4096,
    ) -> AsyncGenerator[str, None]:
        """流式对话，逐个产出文本片段"""
        if config.llm_provider == "openai":
            async for chunk in self._chat_stream_openai(messages, tools, model, max_tokens):
                yield chunk
        elif config.llm_provider == "anthropic":
            async for chunk in self._chat_stream_anthropic(messages, tools, model, max_tokens):
                yield chunk
        else:
            raise LLMError(f"不支持的 LLM 提供商: {config.llm_provider}")

    # ---- OpenAI 适配 ----

    async def _chat_openai(
        self,
        messages: list[dict[str, Any]],
        tools: list[dict[str, Any]] | None,
        tool_choice: str,
        model: str | None,
        max_tokens: int,
    ) -> dict[str, Any]:
        client = await self._get_client()
        url = f"{config.openai_base_url}/chat/completions"
        headers = {
            "Authorization": f"Bearer {config.openai_api_key}",
            "Content-Type": "application/json",
        }
        body: dict[str, Any] = {
            "model": model or config.openai_model,
            "messages": messages,
            "max_tokens": max_tokens,
        }
        if tools:
            body["tools"] = tools
            body["tool_choice"] = tool_choice

        for attempt in range(config.llm_max_retries + 1):
            try:
                resp = await client.post(url, headers=headers, json=body)
                if resp.status_code == 200:
                    return resp.json()
                else:
                    detail = resp.text[:500]
                    if attempt < config.llm_max_retries:
                        await asyncio.sleep(2 ** attempt)
                        continue
                    raise LLMError(f"OpenAI API {resp.status_code}: {detail}")
            except httpx.TimeoutException:
                if attempt < config.llm_max_retries:
                    await asyncio.sleep(2 ** attempt)
                    continue
                raise LLMError("OpenAI API 请求超时")
        raise LLMError("OpenAI API 不可达")

    async def _chat_stream_openai(
        self,
        messages: list[dict[str, Any]],
        tools: list[dict[str, Any]] | None,
        model: str | None,
        max_tokens: int,
    ) -> AsyncGenerator[str, None]:
        client = await self._get_client()
        url = f"{config.openai_base_url}/chat/completions"
        headers = {
            "Authorization": f"Bearer {config.openai_api_key}",
            "Content-Type": "application/json",
        }
        body: dict[str, Any] = {
            "model": model or config.openai_model,
            "messages": messages,
            "max_tokens": max_tokens,
            "stream": True,
        }
        if tools:
            body["tools"] = tools

        async with client.stream("POST", url, headers=headers, json=body) as resp:
            if resp.status_code != 200:
                detail = await resp.aread()
                raise LLMError(f"OpenAI API {resp.status_code}: {detail[:500]}")
            async for line in resp.aiter_lines():
                if line.startswith("data: "):
                    data = line[6:]
                    if data == "[DONE]":
                        break
                    try:
                        chunk = json.loads(data)
                        delta = chunk["choices"][0].get("delta", {})
                        if "content" in delta and delta["content"]:
                            yield delta["content"]
                    except (json.JSONDecodeError, KeyError, IndexError):
                        continue

    # ---- Anthropic 适配 ----

    def _convert_tools_for_anthropic(self, tools: list[dict[str, Any]]) -> list[dict[str, Any]]:
        """将 OpenAI 格式的 tools 转为 Anthropic 格式"""
        converted: list[dict[str, Any]] = []
        for t in tools:
            if t.get("type") == "function":
                func = t["function"]
                converted.append({
                    "name": func["name"],
                    "description": func.get("description", ""),
                    "input_schema": {
                        "type": "object",
                        "properties": func.get("parameters", {}).get("properties", {}),
                        "required": func.get("parameters", {}).get("required", []),
                    }
                })
        return converted

    async def _chat_anthropic(
        self,
        messages: list[dict[str, Any]],
        tools: list[dict[str, Any]] | None,
        tool_choice: str,
        model: str | None,
        max_tokens: int,
    ) -> dict[str, Any]:
        client = await self._get_client()
        url = "https://api.anthropic.com/v1/messages"
        headers = {
            "x-api-key": config.anthropic_api_key,
            "anthropic-version": "2023-06-01",
            "Content-Type": "application/json",
        }

        # 提取 system 消息
        system_msg = ""
        anthropic_msgs: list[dict[str, Any]] = []
        for m in messages:
            if m["role"] == "system":
                system_msg += m["content"] + "\n"
            else:
                anthropic_msgs.append({"role": m["role"], "content": m["content"]})

        body: dict[str, Any] = {
            "model": model or config.anthropic_model,
            "messages": anthropic_msgs,
            "max_tokens": max_tokens,
        }
        if system_msg.strip():
            body["system"] = system_msg.strip()
        if tools:
            body["tools"] = self._convert_tools_for_anthropic(tools)

        for attempt in range(config.llm_max_retries + 1):
            try:
                resp = await client.post(url, headers=headers, json=body)
                if resp.status_code == 200:
                    return self._normalize_anthropic_response(resp.json())
                else:
                    detail = resp.text[:500]
                    if attempt < config.llm_max_retries:
                        await asyncio.sleep(2 ** attempt)
                        continue
                    raise LLMError(f"Anthropic API {resp.status_code}: {detail}")
            except httpx.TimeoutException:
                if attempt < config.llm_max_retries:
                    await asyncio.sleep(2 ** attempt)
                    continue
                raise LLMError("Anthropic API 请求超时")
        raise LLMError("Anthropic API 不可达")

    async def _chat_stream_anthropic(
        self,
        messages: list[dict[str, Any]],
        tools: list[dict[str, Any]] | None,
        model: str | None,
        max_tokens: int,
    ) -> AsyncGenerator[str, None]:
        client = await self._get_client()
        url = "https://api.anthropic.com/v1/messages"
        headers = {
            "x-api-key": config.anthropic_api_key,
            "anthropic-version": "2023-06-01",
            "Content-Type": "application/json",
        }

        system_msg = ""
        anthropic_msgs: list[dict[str, Any]] = []
        for m in messages:
            if m["role"] == "system":
                system_msg += m["content"] + "\n"
            else:
                anthropic_msgs.append({"role": m["role"], "content": m["content"]})

        body: dict[str, Any] = {
            "model": model or config.anthropic_model,
            "messages": anthropic_msgs,
            "max_tokens": max_tokens,
            "stream": True,
        }
        if system_msg.strip():
            body["system"] = system_msg.strip()
        if tools:
            body["tools"] = self._convert_tools_for_anthropic(tools)

        async with client.stream("POST", url, headers=headers, json=body) as resp:
            if resp.status_code != 200:
                detail = await resp.aread()
                raise LLMError(f"Anthropic API {resp.status_code}: {detail[:500]}")
            async for line in resp.aiter_lines():
                if line.startswith("data: "):
                    data = line[6:]
                    try:
                        event = json.loads(data)
                        if event.get("type") == "content_block_delta":
                            delta = event.get("delta", {})
                            if "text" in delta:
                                yield delta["text"]
                    except json.JSONDecodeError:
                        continue

    def _normalize_anthropic_response(self, raw: dict[str, Any]) -> dict[str, Any]:
        """将 Anthropic 响应转为 OpenAI 格式风格，方便统一处理"""
        content_blocks = raw.get("content", [])
        text_parts: list[str] = []
        tool_calls: list[dict[str, Any]] = []
        for block in content_blocks:
            if block["type"] == "text":
                text_parts.append(block["text"])
            elif block["type"] == "tool_use":
                tool_calls.append({
                    "id": block.get("id", ""),
                    "type": "function",
                    "function": {
                        "name": block.get("name", ""),
                        "arguments": json.dumps(block.get("input", {}))
                    }
                })

        return {
            "choices": [{
                "message": {
                    "role": "assistant",
                    "content": "\n".join(text_parts) or None,
                    "tool_calls": tool_calls if tool_calls else None
                }
            }],
            "usage": {
                "input_tokens": raw.get("usage", {}).get("input_tokens", 0),
                "output_tokens": raw.get("usage", {}).get("output_tokens", 0),
            }
        }

    # ---- 便捷方法 ----

    @staticmethod
    def extract_text(response: dict[str, Any]) -> str:
        """从响应中提取纯文本"""
        try:
            msg = response["choices"][0]["message"]
            return msg.get("content") or ""
        except (KeyError, IndexError):
            return ""

    @staticmethod
    def extract_reasoning(response: dict[str, Any]) -> str:
        """从响应中提取推理/思考内容 (DeepSeek reasoning_content)"""
        try:
            msg = response["choices"][0]["message"]
            return msg.get("reasoning_content") or ""
        except (KeyError, IndexError):
            return ""

    @staticmethod
    def extract_usage(response: dict[str, Any]) -> dict[str, int]:
        """从响应中提取 token 用量"""
        try:
            usage = response.get("usage", {})
            return {
                "input": usage.get("prompt_tokens", usage.get("input_tokens", 0)),
                "output": usage.get("completion_tokens", usage.get("output_tokens", 0)),
                "total": usage.get("total_tokens", 0),
            }
        except (KeyError, AttributeError):
            return {"input": 0, "output": 0, "total": 0}

    @staticmethod
    def extract_tool_calls(response: dict[str, Any]) -> list[dict[str, Any]]:
        """从响应中提取工具调用"""
        try:
            msg = response["choices"][0]["message"]
            return msg.get("tool_calls") or []
        except (KeyError, IndexError):
            return []