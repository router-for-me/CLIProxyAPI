"""Persistent SQLite job store for Shadow-Claw tool executions."""

import asyncio
import math
import os
import sqlite3
import time
import uuid
from pathlib import Path

STATUS_PENDING = "pending"
STATUS_RUNNING = "running"
STATUS_COMPLETED = "completed"
STATUS_FAILED = "failed"
STATUS_LOST = "lost"
STATUS_EXPIRED = "expired"

VALID_STATUSES = frozenset(
    {STATUS_PENDING, STATUS_RUNNING, STATUS_COMPLETED, STATUS_FAILED, STATUS_LOST, STATUS_EXPIRED}
)

_CREATE_TABLE_SQL = """\
CREATE TABLE IF NOT EXISTS jobs (
    id TEXT PRIMARY KEY,
    tool_name TEXT NOT NULL,
    prompt TEXT,
    status TEXT NOT NULL DEFAULT 'pending',
    created_at REAL NOT NULL,
    updated_at REAL NOT NULL,
    completed_at REAL,
    result_summary TEXT,
    exit_code INTEGER,
    user_id INTEGER,
    chat_id INTEGER
)
"""

_CREATE_INDEX_SQL = """\
CREATE INDEX IF NOT EXISTS idx_jobs_status ON jobs (status);
"""

_INSERT_JOB_SQL = """\
INSERT INTO jobs (id, tool_name, prompt, status, created_at, updated_at, user_id, chat_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
"""

_UPDATE_STATUS_SQL = """\
UPDATE jobs SET status = ?, updated_at = ?, result_summary = ?, exit_code = ?,
    completed_at = CASE WHEN ? IN ('completed', 'failed') THEN ? ELSE completed_at END
WHERE id = ?
"""

_SELECT_JOB_SQL = "SELECT * FROM jobs WHERE id = ?"

_MARK_LOST_SQL = """\
UPDATE jobs SET status = 'lost', updated_at = ? WHERE status IN ('pending', 'running')
"""

_DELETE_EXPIRED_SQL = """\
DELETE FROM jobs WHERE created_at < ?
"""


def _row_to_dict(cursor: sqlite3.Cursor, row: sqlite3.Row) -> dict:
    columns = [desc[0] for desc in cursor.description]
    return dict(zip(columns, row))


class JobStore:
    """Thread-safe SQLite job store. All public methods are async-compatible."""

    def __init__(self, db_path: str | None = None):
        if db_path is None:
            db_path = os.path.join(
                os.path.dirname(os.path.abspath(__file__)), "data", "jobs.db"
            )
        self._db_path = db_path
        self._conn: sqlite3.Connection | None = None
        self._ensure_directory()
        self._init_db()

    def _ensure_directory(self) -> None:
        directory = os.path.dirname(self._db_path)
        if directory:
            Path(directory).mkdir(parents=True, exist_ok=True)

    def _init_db(self) -> None:
        self._conn = sqlite3.connect(self._db_path, check_same_thread=False)
        self._conn.row_factory = _row_to_dict
        self._conn.execute("PRAGMA journal_mode=WAL")
        self._conn.execute(_CREATE_TABLE_SQL)
        self._conn.execute(_CREATE_INDEX_SQL)
        self._conn.commit()

    def _get_conn(self) -> sqlite3.Connection:
        if self._conn is None:
            raise RuntimeError("JobStore is closed")
        return self._conn

    async def _run(self, fn, *args):
        loop = asyncio.get_running_loop()
        return await loop.run_in_executor(None, fn, *args)

    # -- synchronous primitives (run inside executor) --

    def _create_job_sync(
        self, tool_name: str, prompt: str | None, user_id: int | None, chat_id: int | None
    ) -> str:
        job_id = uuid.uuid4().hex
        now = time.time()
        conn = self._get_conn()
        conn.execute(
            _INSERT_JOB_SQL,
            (job_id, tool_name, prompt, STATUS_PENDING, now, now, user_id, chat_id),
        )
        conn.commit()
        return job_id

    def _update_status_sync(
        self,
        job_id: str,
        status: str,
        result_summary: str | None = None,
        exit_code: int | None = None,
    ) -> None:
        if status not in VALID_STATUSES:
            raise ValueError(f"Invalid status: {status!r}")
        now = time.time()
        conn = self._get_conn()
        conn.execute(
            _UPDATE_STATUS_SQL,
            (status, now, result_summary, exit_code, status, now, job_id),
        )
        conn.commit()

    def _get_job_sync(self, job_id: str) -> dict | None:
        conn = self._get_conn()
        cursor = conn.execute(_SELECT_JOB_SQL, (job_id,))
        return cursor.fetchone()

    def _list_recent_sync(
        self, limit: int = 10, tool_name: str | None = None, status: str | None = None
    ) -> list[dict]:
        clauses: list[str] = []
        params: list = []
        if tool_name is not None:
            clauses.append("tool_name = ?")
            params.append(tool_name)
        if status is not None:
            if status not in VALID_STATUSES:
                raise ValueError(f"Invalid status filter: {status!r}")
            clauses.append("status = ?")
            params.append(status)
        where = f" WHERE {' AND '.join(clauses)}" if clauses else ""
        sql = f"SELECT * FROM jobs{where} ORDER BY created_at DESC LIMIT ?"
        params.append(max(1, limit))
        conn = self._get_conn()
        return conn.execute(sql, params).fetchall()

    def _cleanup_expired_sync(self, ttl_hours: float = 24) -> int:
        cutoff = time.time() - (ttl_hours * 3600)
        conn = self._get_conn()
        cursor = conn.execute(_DELETE_EXPIRED_SQL, (cutoff,))
        conn.commit()
        return cursor.rowcount

    def _mark_lost_on_restart_sync(self) -> int:
        now = time.time()
        conn = self._get_conn()
        cursor = conn.execute(_MARK_LOST_SQL, (now,))
        conn.commit()
        return cursor.rowcount

    # -- async public API --

    async def create_job(
        self,
        tool_name: str,
        prompt: str | None = None,
        user_id: int | None = None,
        chat_id: int | None = None,
    ) -> str:
        return await self._run(self._create_job_sync, tool_name, prompt, user_id, chat_id)

    async def update_status(
        self,
        job_id: str,
        status: str,
        result_summary: str | None = None,
        exit_code: int | None = None,
    ) -> None:
        await self._run(self._update_status_sync, job_id, status, result_summary, exit_code)

    async def get_job(self, job_id: str) -> dict | None:
        return await self._run(self._get_job_sync, job_id)

    async def list_recent(
        self, limit: int = 10, tool_name: str | None = None, status: str | None = None
    ) -> list[dict]:
        return await self._run(self._list_recent_sync, limit, tool_name, status)

    async def cleanup_expired(self, ttl_hours: float = 24) -> int:
        return await self._run(self._cleanup_expired_sync, ttl_hours)

    async def mark_lost_on_restart(self) -> int:
        return await self._run(self._mark_lost_on_restart_sync)

    def close(self) -> None:
        if self._conn is not None:
            self._conn.close()
            self._conn = None


def _relative_time(timestamp: float) -> str:
    """Return a human-readable relative time string like '2m ago' or '1h ago'."""
    delta = time.time() - timestamp
    if delta < 0:
        return "just now"
    if delta < 60:
        return f"{int(delta)}s ago"
    if delta < 3600:
        return f"{int(delta // 60)}m ago"
    if delta < 86400:
        return f"{int(delta // 3600)}h ago"
    days = int(delta // 86400)
    return f"{days}d ago"


def _truncate_prompt(prompt: str | None, max_len: int = 40) -> str:
    if not prompt:
        return ""
    cleaned = prompt.replace("\n", " ").strip()
    if len(cleaned) <= max_len:
        return cleaned
    return cleaned[: max_len - 3] + "..."


def format_jobs_list(jobs: list[dict]) -> str:
    """Return a human-readable Telegram-friendly summary of jobs."""
    if not jobs:
        return "No jobs found."

    count = len(jobs)
    lines = [f"Recent Jobs (last {count}):"]
    for job in jobs:
        status = job.get("status", "unknown")
        tool = job.get("tool_name", "?")
        created = job.get("created_at", 0)
        prompt = _truncate_prompt(job.get("prompt"))
        age = _relative_time(created)
        prompt_part = f' - "{prompt}"' if prompt else ""
        lines.append(f"[{status}] {tool} - {age}{prompt_part}")

    return "\n".join(lines)
