"""金手指 Agent 系统 — SQLite 存储

管理宿主画像、执行日志、Skill 元数据。
"""

import sqlite3
from datetime import datetime
from pathlib import Path
from typing import Any

from ..config import config
from ..models import HostProfile, ExecutionReport, ExecutionSummary, SkillManifest


class SQLiteStore:
    """SQLite 数据库管理"""

    def __init__(self, db_path: Path | None = None):
        self.db_path = db_path or (config.data_dir / "golden_finger.db")
        self._conn: sqlite3.Connection | None = None

    @property
    def conn(self) -> sqlite3.Connection:
        if self._conn is None:
            self._conn = sqlite3.connect(str(self.db_path))
            self._conn.row_factory = sqlite3.Row
            self._conn.execute("PRAGMA journal_mode=WAL")
            self._init_tables()
        return self._conn

    def _init_tables(self):
        self.conn.executescript("""
            CREATE TABLE IF NOT EXISTS host_profile (
                host_id TEXT PRIMARY KEY,
                data TEXT NOT NULL,
                updated_at TEXT NOT NULL
            );

            CREATE TABLE IF NOT EXISTS execution_logs (
                execution_id TEXT PRIMARY KEY,
                plan_id TEXT NOT NULL,
                original_query TEXT NOT NULL,
                report_json TEXT NOT NULL,
                created_at TEXT NOT NULL
            );

            CREATE TABLE IF NOT EXISTS execution_summaries (
                summary_id TEXT PRIMARY KEY,
                execution_id TEXT NOT NULL,
                original_query TEXT NOT NULL,
                summary_json TEXT NOT NULL,
                created_at TEXT NOT NULL
            );

            CREATE TABLE IF NOT EXISTS skill_meta (
                skill_name TEXT PRIMARY KEY,
                manifest_json TEXT NOT NULL,
                updated_at TEXT NOT NULL
            );

            CREATE TABLE IF NOT EXISTS event_log (
                id INTEGER PRIMARY KEY AUTOINCREMENT,
                timestamp TEXT NOT NULL,
                domain TEXT NOT NULL,
                event_type TEXT NOT NULL,
                detail TEXT NOT NULL
            );
        """)
        self.conn.commit()

    # ---- 宿主画像 ----

    def save_host_profile(self, profile: HostProfile):
        profile.updated_at = datetime.now().isoformat()
        self.conn.execute(
            "INSERT OR REPLACE INTO host_profile (host_id, data, updated_at) VALUES (?, ?, ?)",
            (profile.host_id, profile.model_dump_json(), profile.updated_at)
        )
        self.conn.commit()

    def load_host_profile(self) -> HostProfile | None:
        row = self.conn.execute(
            "SELECT data FROM host_profile ORDER BY updated_at DESC LIMIT 1"
        ).fetchone()
        if row:
            return HostProfile.model_validate_json(row["data"])
        return None

    # ---- 执行日志 ----

    def save_execution_report(self, report: ExecutionReport):
        self.conn.execute(
            "INSERT OR REPLACE INTO execution_logs (execution_id, plan_id, original_query, report_json, created_at) VALUES (?, ?, ?, ?, ?)",
            (report.execution_id, report.plan_id, "", report.model_dump_json(), __import__('datetime').datetime.now().isoformat())  # noqa
        )
        self.conn.commit()

    def get_recent_executions(self, limit: int = 20) -> list[dict[str, Any]]:
        rows = self.conn.execute(
            "SELECT execution_id, plan_id, created_at, report_json FROM execution_logs ORDER BY created_at DESC LIMIT ?",
            (limit,)
        ).fetchall()
        return [dict(r) for r in rows]

    # ---- 执行摘要 ----

    def save_execution_summary(self, summary: ExecutionSummary):
        self.conn.execute(
            "INSERT OR REPLACE INTO execution_summaries (summary_id, execution_id, original_query, summary_json, created_at) VALUES (?, ?, ?, ?, ?)",
            (summary.summary_id, summary.execution_id, summary.original_query, summary.model_dump_json(), summary.created_at)
        )
        self.conn.commit()

    # ---- Skill 元数据 ----

    def save_skill_meta(self, manifest: SkillManifest):
        self.conn.execute(
            "INSERT OR REPLACE INTO skill_meta (skill_name, manifest_json, updated_at) VALUES (?, ?, ?)",
            (manifest.name, manifest.model_dump_json(), __import__('datetime').datetime.now().isoformat())  # noqa
        )
        self.conn.commit()

    def load_all_skill_meta(self) -> list[SkillManifest]:
        rows = self.conn.execute("SELECT manifest_json FROM skill_meta").fetchall()
        return [SkillManifest.model_validate_json(r["manifest_json"]) for r in rows]

    # ---- 事件日志 ----

    def log_event(self, domain: str, event_type: str, detail: str = ""):
        self.conn.execute(
            "INSERT INTO event_log (timestamp, domain, event_type, detail) VALUES (?, ?, ?, ?)",
            (__import__('datetime').datetime.now().isoformat(), domain, event_type, detail)  # noqa
        )
        self.conn.commit()

    def close(self):
        if self._conn:
            self._conn.close()
            self._conn = None
