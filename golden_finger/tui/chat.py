"""聊天循环处理：LLM↔Tool 循环、思考动画、上下文压缩。"""

from __future__ import annotations

import asyncio
import contextlib
import json
import time
from typing import TYPE_CHECKING

from rich.text import Text as RichText
from textual.widgets import RichLog

from ..config import config
from ..llm import LLMClient
from ..tools.builtin import BUILTIN_TOOLS
from ..domain_execution import ToolExecutionGuard
from ..logging import log_system, log_step

from .constants import (
    MAX_CHAT_ROUNDS, REASONING_TRUNCATE_LEN, RESULT_PREVIEW_LEN,
    CHAT_MEMORY_TURN_LIMIT, CHAT_MEMORY_L2_THRESHOLD, CHAT_MEMORY_L3_THRESHOLD,
    CHAT_MEMORY_KEEP_RECENT, THINKING_TRACE_INTERVAL_SEC, THINKING_PHASES,
    UI_DEBOUNCE_SEC,
)

if TYPE_CHECKING:
    from .app import GoldenFingerApp


class DebouncedWriter:
    """防抖写入器：限制 RichLog 写入频率，避免快速刷屏导致卡顿。"""

    def __init__(self, log: RichLog) -> None:
        self._log = log
        self._last_write = 0.0

    async def write(self, *args, **kwargs) -> None:
        elapsed = time.monotonic() - self._last_write
        if elapsed < UI_DEBOUNCE_SEC:
            await asyncio.sleep(UI_DEBOUNCE_SEC - elapsed)
        self._log.write(*args, **kwargs)
        self._last_write = time.monotonic()


class ContextCompressor:
    """L1/L2/L3 三级上下文压缩，控制对话内存上限。"""

    def __init__(self, app: GoldenFingerApp) -> None:
        self._app = app

    @property
    def chat_memory(self) -> list[dict[str, str]]:
        return self._app.chat_memory

    @chat_memory.setter
    def chat_memory(self, value: list[dict[str, str]]) -> None:
        self._app.chat_memory = value

    @property
    def llm(self) -> LLMClient | None:
        return self._app.llm

    async def compress_if_needed(self, log: RichLog) -> None:
        if not self.chat_memory:
            return
        count = len(self.chat_memory)
        if count <= CHAT_MEMORY_L2_THRESHOLD:
            return
        if count <= CHAT_MEMORY_L3_THRESHOLD:
            await self._compress_level2(log)
            return
        await self._compress_level3(log)

    async def _compress_level2(self, log: RichLog) -> None:
        if self.llm is None or len(self.chat_memory) <= CHAT_MEMORY_KEEP_RECENT:
            return
        compress_part = self.chat_memory[:-CHAT_MEMORY_KEEP_RECENT]
        keep_part = self.chat_memory[-CHAT_MEMORY_KEEP_RECENT:]
        transcript = self._format_transcript(compress_part)
        if not transcript.strip():
            return
        prompt = (
            "你是“上下文压缩器”。请在不依赖外部上下文的前提下，"
            "压缩以下对话，重点提取：\n"
            "1) 用户目标与约束\n2) 关键决策与结论\n"
            "3) 工具调用过程与结果（成功/失败）\n4) 尚未完成事项\n"
            "输出要求：简洁中文，最多 10 条，每条不超过 50 字。"
        )
        try:
            resp = await self.llm.chat(
                messages=[
                    {"role": "system", "content": prompt},
                    {"role": "user", "content": transcript[:12000]},
                ],
                max_tokens=600,
            )
            summary = self.llm.extract_text(resp).strip()
            if not summary:
                return
            self.chat_memory = [
                {"role": "assistant", "content": f"【上下文压缩摘要-L2】\n{summary}"},
                *keep_part,
            ]
            log.write("[dim #6c7086]🧠 已执行二级上下文压缩（模型摘要）[/]")
            log_system("L2 上下文压缩完成", level="L2", kept_recent=CHAT_MEMORY_KEEP_RECENT)
        except Exception:
            pass

    async def _compress_level3(self, log: RichLog) -> None:
        if len(self.chat_memory) <= CHAT_MEMORY_KEEP_RECENT:
            return
        await self._compress_level2(log)
        anchor = None
        for msg in self.chat_memory:
            if "上下文压缩摘要" in msg.get("content", ""):
                anchor = msg
                break
        tail = self.chat_memory[-CHAT_MEMORY_KEEP_RECENT:]
        compact = {"role": "assistant", "content": "【上下文压缩摘要-L3】已执行工具式压缩：保留关键摘要与近期对话。"}
        self.chat_memory = [compact]
        if anchor is not None and anchor is not compact:
            self.chat_memory.append(anchor)
        self.chat_memory.extend(tail)
        log.write("[dim #6c7086]🧠 已执行三级上下文压缩（按需工具式压缩）[/]")
        log_system("L3 上下文压缩完成", level="L3")

    @staticmethod
    def _format_transcript(messages: list[dict[str, str]]) -> str:
        lines: list[str] = []
        for m in messages:
            role = m.get("role", "unknown")
            content = (m.get("content", "") or "").strip()
            if content:
                lines.append(f"[{role}] {content}")
        return "\n".join(lines)


class ChatHandler:
    """管理 LLM↔Tool 多轮对话循环、思考动画、上下文压缩。"""

    def __init__(self, app: GoldenFingerApp) -> None:
        self._app = app
        self._compressor = ContextCompressor(app)

    @property
    def llm(self) -> LLMClient | None:
        return self._app.llm

    async def run(self, query: str, log: RichLog, writer: DebouncedWriter) -> None:
        assert self.llm is not None
        await self._compressor.compress_if_needed(log)

        system_prompt = (
            "你是金手指(GoldenFinger)，一个运行在终端TUI中的AI编程助手。"
            "使用中文回复。可以调用工具：读文件、写文件、执行命令、搜索网页。"
            "代码用 ``` 语法高亮。回复简洁，重点突出。"
            f"{self._app.cli_context_prompt}"
        )
        messages: list[dict[str, object]] = [
            {"role": "system", "content": system_prompt},
        ]
        messages.extend(self._app.chat_memory)
        messages.append({"role": "user", "content": query})

        tool_schemas = [t.to_openai_schema() for t in BUILTIN_TOOLS.values()]
        total_input = 0
        total_output = 0
        final_text = ""

        for round_num in range(MAX_CHAT_ROUNDS):
            if round_num > 0:
                await writer.write(f"[dim #585b70]── 第{round_num + 1}轮 ──[/]")

            label = "◆ 金手指" if round_num == 0 else "  ◇"
            await writer.write(f"[bold #94e2d5]{label}[/]  ")

            self._app._update_status_text("› 思考中...")
            await asyncio.sleep(0)

            t_start = time.time()
            thinking_task = asyncio.create_task(
                self._emit_thinking_trace(log, writer, t_start)
            )
            try:
                resp = await self.llm.chat(
                    messages=messages,
                    tools=tool_schemas,
                    max_tokens=4096,
                )
            except Exception as e:
                thinking_task.cancel()
                with contextlib.suppress(asyncio.CancelledError):
                    await thinking_task
                await writer.write(f"[#f38ba8]✗ LLM 调用失败: {e}[/]")
                self._app._update_status_text("✗ 调用失败")
                log_system(f"LLM 调用失败 (chat loop): {e}", level="ERROR")
                return
            else:
                thinking_task.cancel()
                with contextlib.suppress(asyncio.CancelledError):
                    await thinking_task

            elapsed = time.time() - t_start
            reasoning = self.llm.extract_reasoning(resp)
            if reasoning:
                if len(reasoning) > REASONING_TRUNCATE_LEN:
                    await writer.write(
                        f"[dim #6c7086]  › {reasoning[:REASONING_TRUNCATE_LEN]}[/]"
                    )
                    await writer.write(
                        f"[dim #45475a]     ... (推理共 {len(reasoning)} 字符，已截断)[/]"
                    )
                else:
                    await writer.write(f"[dim #6c7086]  › {reasoning}[/]")

            text = self.llm.extract_text(resp)
            tool_calls = self.llm.extract_tool_calls(resp)
            usage = self.llm.extract_usage(resp)
            total_input += usage.get("input", 0)
            total_output += usage.get("output", 0)

            if text:
                await writer.write(RichText(text))
                final_text = text

            if not tool_calls and not text:
                await writer.write("[dim #6c7086](空响应)[/]")

            if not tool_calls:
                await writer.write(
                    f"[dim #585b70]  ⏱ {elapsed:.1f}s"
                    f"  ↑{usage.get('input', 0)} ↓{usage.get('output', 0)} tok[/]\n"
                )
                break

            # 工具调用
            assistant_msg: dict[str, object] = {
                "role": "assistant",
                "content": text or None,
            }
            if reasoning:
                assistant_msg["reasoning_content"] = reasoning
            if tool_calls:
                assistant_msg["tool_calls"] = tool_calls
            messages.append(assistant_msg)

            for tc in tool_calls:
                func = tc.get("function", {})
                tool_name = str(func.get("name", ""))
                params_str = func.get("arguments", "{}")
                params = {}
                try:
                    params = json.loads(params_str)
                except json.JSONDecodeError:
                    pass

                preview = ", ".join(
                    f"{k}={str(v)[:40]!r}" for k, v in params.items()
                )
                await writer.write(f"[#f9e2af]  ⚙ {tool_name}({preview})[/]")
                self._app._update_status_text(f"⚙ 执行 {tool_name}...")
                await asyncio.sleep(0)

                t_tool = time.time()
                log_step(f"工具调用开始: {tool_name}", domain="execution", tool_name=tool_name)
                tool_log = await ToolExecutionGuard.execute_tool(tool_name, params)
                tool_elapsed = time.time() - t_tool

                if tool_log.success and tool_log.result is not None:
                    rp = str(tool_log.result)[:RESULT_PREVIEW_LEN].replace("\n", " ")
                    await writer.write(
                        f"[#a6e3a1]    ✓ {tool_elapsed:.1f}s  |  {rp}[/]"
                    )
                elif tool_log.success:
                    await writer.write(f"[#a6e3a1]    ✓ {tool_elapsed:.1f}s[/]")
                else:
                    await writer.write(
                        f"[#f38ba8]    ✗ {tool_elapsed:.1f}s  |  {tool_log.error}[/]"
                    )

                result_content = (
                    json.dumps(tool_log.result, ensure_ascii=False)
                    if tool_log.success and tool_log.result
                    else (str(tool_log.error) or "(无输出)")
                )
                messages.append({
                    "role": "tool",
                    "tool_call_id": str(tc.get("id", "tool")),
                    "content": result_content,
                })

        if total_input > 0:
            await writer.write(
                f"[dim #585b70]── 总计 输入：{total_input}"
                f" 输出：{total_output} tokens ──[/]"
            )
        self._remember_turn(query, final_text)
        self._app._update_status_text("✦ 就绪")
        await writer.write("")

    def _remember_turn(self, user_query: str, assistant_text: str) -> None:
        if user_query.strip():
            self._app.chat_memory.append({"role": "user", "content": user_query.strip()})
        if assistant_text.strip():
            self._app.chat_memory.append({"role": "assistant", "content": assistant_text.strip()})
        max_messages = CHAT_MEMORY_TURN_LIMIT * 2
        if len(self._app.chat_memory) > max_messages:
            self._app.chat_memory = self._app.chat_memory[-max_messages:]

    @staticmethod
    async def _emit_thinking_trace(log: RichLog, writer: DebouncedWriter, started_at: float) -> None:
        await writer.write("[dim #6c7086]  › 思考开始：正在解析你的问题...[/]")
        index = 0
        try:
            while True:
                await asyncio.sleep(THINKING_TRACE_INTERVAL_SEC)
                elapsed = time.time() - started_at
                dots = "." * ((index % 3) + 1)
                if index < len(THINKING_PHASES):
                    phase = THINKING_PHASES[index]
                    await writer.write(
                        f"[dim #6c7086]  › 思考中{dots} {phase}（{elapsed:.1f}s）[/]"
                    )
                elif index % 5 == 0:
                    await writer.write(
                        f"[dim #6c7086]  › 思考中{dots} 深度推理（{elapsed:.1f}s）[/]"
                    )
                index += 1
        except asyncio.CancelledError:
            elapsed = time.time() - started_at
            await writer.write(
                f"[dim #585b70]  › 思考结束，开始输出结果（{elapsed:.1f}s）[/]"
            )
