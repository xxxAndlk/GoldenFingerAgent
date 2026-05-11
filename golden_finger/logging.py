"""金手指 Agent 系统 — 统一日志管理器

环形缓冲区 + 文件落盘 + SSE 订阅者推送。
四个日志类别: system / step / api_request / api_response
"""

import asyncio
import json
import logging
import threading
from collections import deque
from datetime import datetime, timezone
from logging.handlers import RotatingFileHandler
from pathlib import Path
from typing import Any

# ---- 日志条目 ----

_log_id_counter = 0
_log_id_lock = threading.Lock()


def _next_id() -> int:
    global _log_id_counter
    with _log_id_lock:
        _log_id_counter += 1
        return _log_id_counter


class LogEntry:
    """一条日志记录"""
    __slots__ = ("id", "timestamp", "category", "level", "message", "meta")

    def __init__(self, category: str, level: str, message: str, **meta):
        self.id = _next_id()
        self.timestamp = datetime.now(timezone.utc).isoformat()
        self.category = category
        self.level = level
        self.message = message
        self.meta = meta

    def to_dict(self) -> dict[str, Any]:
        return {
            "id": self.id,
            "timestamp": self.timestamp,
            "category": self.category,
            "level": self.level,
            "message": self.message,
            "meta": self.meta,
        }


# ---- LogManager 单例 ----

class LogManager:
    """统一日志管理器"""

    MAX_BUFFER = 5000
    MAX_FILE_BYTES = 10 * 1024 * 1024  # 10MB
    BACKUP_COUNT = 5

    def __init__(self):
        self._buffer: deque[LogEntry] = deque(maxlen=self.MAX_BUFFER)
        self._lock = threading.Lock()
        self._subscribers: list[asyncio.Queue[LogEntry]] = []
        self._subscribers_lock = threading.Lock()
        self._file_handler: RotatingFileHandler | None = None
        self._setup_done = False

    def setup(self, logs_dir: Path | None = None, log_level: str = "INFO"):
        """初始化日志系统（文件 handler + Python logging 集成）"""
        if self._setup_done:
            return
        self._setup_done = True

        if logs_dir is None:
            from .config import config
            logs_dir = Path(config.logs_dir)
            log_level = config.log_level

        logs_dir = Path(logs_dir)
        logs_dir.mkdir(parents=True, exist_ok=True)

        # 文件 handler
        self._file_handler = RotatingFileHandler(
            logs_dir / "golden_finger.log",
            maxBytes=self.MAX_FILE_BYTES,
            backupCount=self.BACKUP_COUNT,
            encoding="utf-8",
        )
        self._file_handler.setFormatter(
            logging.Formatter(
                "%(asctime)s | %(levelname)-7s | %(message)s",
                datefmt="%Y-%m-%d %H:%M:%S",
            )
        )

        # 配置 golden_finger logger（仅设置级别，handler 由 _store_and_notify 直接管理）
        logger = logging.getLogger("golden_finger")
        logger.setLevel(getattr(logging, log_level.upper(), logging.INFO))
        logger.propagate = True

        self._emit_system("INFO", "日志系统初始化完成",
                          logs_dir=str(logs_dir), buffer_size=self.MAX_BUFFER)

    def _emit_system(self, level: str, message: str, **meta):
        """内部发射 system 日志（也写到 Python logger）"""
        # Python logging
        py_logger = logging.getLogger("golden_finger")
        log_method = getattr(py_logger, level.lower(), py_logger.info)
        log_method(message)

        # LogEntry
        entry = LogEntry("system", level, message, **meta)
        self._store_and_notify(entry)

    # ---- 公开 API ----

    def log_system(self, level: str, message: str, **meta):
        entry = LogEntry("system", level, message, **meta)
        self._store_and_notify(entry)
        py_logger = logging.getLogger("golden_finger")
        getattr(py_logger, level.lower(), py_logger.info)(message)

    def log_step(self, level: str, message: str, **meta):
        entry = LogEntry("step", level, message, **meta)
        self._store_and_notify(entry)
        logging.getLogger("golden_finger").info(f"[STEP] {message}")

    def log_api_request(self, level: str, message: str, **meta):
        entry = LogEntry("api_request", level, message, **meta)
        self._store_and_notify(entry)
        logging.getLogger("golden_finger").info(f"[API_REQ] {message}")

    def log_api_response(self, level: str, message: str, **meta):
        entry = LogEntry("api_response", level, message, **meta)
        self._store_and_notify(entry)
        logging.getLogger("golden_finger").info(f"[API_RES] {message}")

    def _store_and_notify(self, entry: LogEntry):
        """存入缓冲区并通知所有 SSE 订阅者"""
        with self._lock:
            self._buffer.append(entry)

        # 写入文件
        if self._file_handler:
            record = logging.LogRecord(
                name="golden_finger",
                level=getattr(logging, entry.level.upper(), logging.INFO),
                pathname="",
                lineno=0,
                msg=f"[{entry.category.upper()}] {entry.message} | {json.dumps(entry.meta, ensure_ascii=False)}",
                args=(),
                exc_info=None,
            )
            self._file_handler.emit(record)

        # 通知 SSE 订阅者
        with self._subscribers_lock:
            dead: list[int] = []
            for i, q in enumerate(self._subscribers):
                try:
                    q.put_nowait(entry)
                except asyncio.QueueFull:
                    dead.append(i)
            for i in reversed(dead):
                self._subscribers.pop(i)

    # ---- 查询 API ----

    def query(self, categories: list[str] | None = None,
              level: str | None = None,
              q: str | None = None,
              limit: int = 200,
              offset: int = 0) -> tuple[list[dict[str, Any]], int]:
        """查询日志，返回 (entries, total)"""
        with self._lock:
            entries = list(self._buffer)

        # 过滤
        if categories:
            entries = [e for e in entries if e.category in categories]
        if level:
            level_upper = level.upper()
            entries = [e for e in entries if e.level.upper() == level_upper]
        if q:
            q_lower = q.lower()
            entries = [e for e in entries if q_lower in e.message.lower()
                       or q_lower in json.dumps(e.meta, ensure_ascii=False).lower()]

        total = len(entries)
        page = entries[offset:offset + limit]
        return [e.to_dict() for e in page], total

    def stats(self) -> dict[str, Any]:
        """返回日志统计信息"""
        with self._lock:
            entries = list(self._buffer)

        category_counts: dict[str, int] = {}
        level_counts: dict[str, int] = {}
        api_success = api_total = 0
        total_tokens = 0
        total_llm_duration = 0
        llm_call_count = 0

        for e in entries:
            category_counts[e.category] = category_counts.get(e.category, 0) + 1
            level_counts[e.level] = level_counts.get(e.level, 0) + 1

            if e.category == "api_response":
                api_total += 1
                if e.meta.get("status_code") == 200:
                    api_success += 1
                tokens = (e.meta.get("input_tokens", 0) + e.meta.get("output_tokens", 0))
                total_tokens += tokens
                duration = e.meta.get("duration_ms", 0)
                if duration:
                    total_llm_duration += duration
                    llm_call_count += 1

        return {
            "total_entries": len(entries),
            "by_category": category_counts,
            "by_level": level_counts,
            "api_success_rate": round(api_success / api_total * 100, 1) if api_total > 0 else 100.0,
            "total_tokens": total_tokens,
            "llm_call_count": llm_call_count,
            "avg_llm_duration_ms": round(total_llm_duration / llm_call_count) if llm_call_count > 0 else 0,
        }

    # ---- SSE 订阅者管理 ----

    def subscribe(self) -> asyncio.Queue[LogEntry]:
        """注册一个 SSE 订阅者，返回其消息队列"""
        q: asyncio.Queue[LogEntry] = asyncio.Queue(maxsize=256)
        with self._subscribers_lock:
            self._subscribers.append(q)
        return q

    def unsubscribe(self, q: asyncio.Queue[LogEntry]):
        """移除 SSE 订阅者"""
        with self._subscribers_lock:
            try:
                self._subscribers.remove(q)
            except ValueError:
                pass

    def shutdown(self):
        """关闭日志系统"""
        if self._file_handler:
            self._file_handler.close()
            self._file_handler = None
        with self._subscribers_lock:
            self._subscribers.clear()
        self._setup_done = False


# ---- 全局实例 ----

log_manager = LogManager()


# ---- 模块级便捷函数 ----

def log_system(message: str, level: str = "INFO", **meta):
    log_manager.log_system(level, message, **meta)


def log_step(message: str, level: str = "INFO", **meta):
    log_manager.log_step(level, message, **meta)


def log_api_request(message: str, level: str = "INFO", **meta):
    log_manager.log_api_request(level, message, **meta)


def log_api_response(message: str, level: str = "INFO", **meta):
    log_manager.log_api_response(level, message, **meta)
