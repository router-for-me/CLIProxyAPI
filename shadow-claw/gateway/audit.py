"""Persistent audit logging with search and export for Shadow-Claw gateway."""

import asyncio
import functools
import json
import logging
import sqlite3
import time
from collections import Counter
from pathlib import Path

LOGGER = logging.getLogger("shadow_claw_gateway.audit")

DEFAULT_DB_PATH = str(Path(__file__).parent / "data" / "audit.db")

_SCHEMA_SQL = """
CREATE TABLE IF NOT EXISTS audit_events (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp       REAL    NOT NULL,
    event_type      TEXT    NOT NULL,
    user_id         INTEGER,
    chat_id         INTEGER,
    tool_name       TEXT,
    model           TEXT,
    route           TEXT,
    duration_ms     INTEGER,
    success         INTEGER,
    error_text      TEXT,
    details         TEXT
);

CREATE INDEX IF NOT EXISTS idx_audit_timestamp  ON audit_events (timestamp);
CREATE INDEX IF NOT EXISTS idx_audit_event_type ON audit_events (event_type);
CREATE INDEX IF NOT EXISTS idx_audit_user_id    ON audit_events (user_id);
"""

# Maps log_event field names to audit schema columns.
_FIELD_MAP = {
    "telegram_user_id": "user_id",
    "chat_id": "chat_id",
    "tool_name": "tool_name",
    "model": "model",
    "route": "route",
    "duration_ms": "duration_ms",
    "error": "error_text",
}

# Schema columns that accept direct field mapping.
_SCHEMA_COLUMNS = frozenset(
    {
        "user_id",
        "chat_id",
        "tool_name",
        "model",
        "route",
        "duration_ms",
        "success",
        "error_text",
    }
)

# Fields that should not be stored in the catch-all details blob.
_SUPPRESS_FROM_DETAILS = frozenset(
    {
        "event",
        "telegram_user_id",
        "chat_id",
        "tool_name",
        "model",
        "route",
        "duration_ms",
        "error",
        "error_text",
        "ok",
        "success",
        "user_id",
    }
)


def _map_fields(event_type: str, fields: dict) -> dict:
    """Translate log_event keyword args into audit schema column values."""
    row = {}

    for src_key, value in fields.items():
        dest_key = _FIELD_MAP.get(src_key, src_key)
        if dest_key in _SCHEMA_COLUMNS:
            row[dest_key] = value

    # Derive success from the "ok" field when present.
    if "success" not in row and "ok" in fields:
        row["success"] = 1 if fields["ok"] else 0

    # Infer success from event_type suffix when not explicitly provided.
    if "success" not in row:
        if event_type.endswith(".success") or event_type.endswith(".completed"):
            row.setdefault("success", 1)
        elif event_type.endswith(".error") or event_type.endswith(".timeout"):
            row.setdefault("success", 0)
        elif event_type.endswith(".denied") or event_type.endswith(".disabled"):
            row.setdefault("success", 0)

    # Pack remaining fields into details JSON.
    extras = {
        k: v
        for k, v in fields.items()
        if k not in _SUPPRESS_FROM_DETAILS and v is not None
    }
    if extras:
        try:
            row["details"] = json.dumps(extras, ensure_ascii=False, default=str)
        except (TypeError, ValueError):
            row["details"] = str(extras)

    return row


class AuditLog:
    """Thread-safe SQLite audit log with async helpers."""

    def __init__(self, db_path: str | None = None):
        self._db_path = db_path or DEFAULT_DB_PATH
        db_dir = Path(self._db_path).parent
        db_dir.mkdir(parents=True, exist_ok=True)

        self._conn = sqlite3.connect(self._db_path, check_same_thread=False)
        self._conn.execute("PRAGMA journal_mode=WAL")
        self._conn.executescript(_SCHEMA_SQL)
        self._conn.commit()

    # ------------------------------------------------------------------
    # Internal helpers
    # ------------------------------------------------------------------

    def _insert(self, event_type: str, mapped: dict) -> int:
        cols = ["timestamp", "event_type"]
        vals = [time.time(), event_type]

        for col in (
            "user_id",
            "chat_id",
            "tool_name",
            "model",
            "route",
            "duration_ms",
            "success",
            "error_text",
            "details",
        ):
            if col in mapped:
                cols.append(col)
                vals.append(mapped[col])

        placeholders = ", ".join("?" for _ in cols)
        col_names = ", ".join(cols)
        sql = f"INSERT INTO audit_events ({col_names}) VALUES ({placeholders})"
        cursor = self._conn.execute(sql, vals)
        self._conn.commit()
        return cursor.lastrowid

    def _query(
        self,
        event_type: str | None = None,
        user_id: int | None = None,
        tool_name: str | None = None,
        since_ts: float | None = None,
        limit: int = 50,
    ) -> list[dict]:
        clauses = []
        params = []
        if event_type is not None:
            clauses.append("event_type = ?")
            params.append(event_type)
        if user_id is not None:
            clauses.append("user_id = ?")
            params.append(user_id)
        if tool_name is not None:
            clauses.append("tool_name = ?")
            params.append(tool_name)
        if since_ts is not None:
            clauses.append("timestamp >= ?")
            params.append(since_ts)

        where = (" WHERE " + " AND ".join(clauses)) if clauses else ""
        sql = (
            f"SELECT id, timestamp, event_type, user_id, chat_id, tool_name, "
            f"model, route, duration_ms, success, error_text, details "
            f"FROM audit_events{where} ORDER BY timestamp DESC LIMIT ?"
        )
        params.append(limit)

        cursor = self._conn.execute(sql, params)
        columns = [desc[0] for desc in cursor.description]
        rows = []
        for row in cursor.fetchall():
            entry = dict(zip(columns, row))
            if entry.get("details"):
                try:
                    entry["details"] = json.loads(entry["details"])
                except (json.JSONDecodeError, TypeError):
                    pass
            rows.append(entry)
        return rows

    def _build_summary(self, hours: int) -> str:
        since_ts = time.time() - hours * 3600

        cursor = self._conn.execute(
            "SELECT event_type, model, duration_ms, success "
            "FROM audit_events WHERE timestamp >= ?",
            (since_ts,),
        )
        rows = cursor.fetchall()

        chat_total = 0
        chat_ok = 0
        chat_fail = 0
        tool_total = 0
        tool_ok = 0
        tool_fail = 0
        auth_denials = 0
        model_counter: Counter = Counter()
        latencies: list[int] = []

        for event_type, model, duration_ms, success in rows:
            if event_type.startswith("chat.request."):
                if event_type in ("chat.request.success", "chat.request.error"):
                    chat_total += 1
                    if success == 1:
                        chat_ok += 1
                    else:
                        chat_fail += 1
                    if model:
                        model_counter[model] += 1
                    if duration_ms is not None:
                        latencies.append(duration_ms)
            elif event_type.startswith("tool.job."):
                if event_type == "tool.job.completed":
                    tool_total += 1
                    if success == 1:
                        tool_ok += 1
                    else:
                        tool_fail += 1
                    if duration_ms is not None:
                        latencies.append(duration_ms)
            elif event_type == "gateway.auth.denied":
                auth_denials += 1

        top_model = model_counter.most_common(1)
        top_model_str = (
            f"{top_model[0][0]} ({top_model[0][1]} requests)"
            if top_model
            else "n/a"
        )
        avg_latency = (
            f"{int(sum(latencies) / len(latencies))}ms" if latencies else "n/a"
        )

        label = f"last {hours}h" if hours != 1 else "last 1h"
        return (
            f"Activity Summary ({label}):\n"
            f"- {chat_total} chat requests ({chat_ok} ok, {chat_fail} failed)\n"
            f"- {tool_total} tool executions ({tool_ok} ok, {tool_fail} failed)\n"
            f"- {auth_denials} auth denials\n"
            f"- Top model: {top_model_str}\n"
            f"- Avg latency: {avg_latency}"
        )

    def _cleanup(self, older_than_days: int) -> int:
        cutoff = time.time() - older_than_days * 86400
        cursor = self._conn.execute(
            "DELETE FROM audit_events WHERE timestamp < ?", (cutoff,)
        )
        self._conn.commit()
        return cursor.rowcount

    # ------------------------------------------------------------------
    # Public synchronous API
    # ------------------------------------------------------------------

    def record_sync(self, event_type: str, **fields) -> int:
        mapped = _map_fields(event_type, fields)
        return self._insert(event_type, mapped)

    def search_sync(
        self,
        event_type: str | None = None,
        user_id: int | None = None,
        tool_name: str | None = None,
        since_hours: float | None = None,
        limit: int = 50,
    ) -> list[dict]:
        since_ts = (time.time() - since_hours * 3600) if since_hours else None
        return self._query(
            event_type=event_type,
            user_id=user_id,
            tool_name=tool_name,
            since_ts=since_ts,
            limit=limit,
        )

    def summary_sync(self, hours: int = 1) -> str:
        return self._build_summary(hours)

    def export_json_sync(
        self,
        event_type: str | None = None,
        since_hours: float = 24,
        limit: int = 1000,
    ) -> str:
        since_ts = time.time() - since_hours * 3600
        rows = self._query(
            event_type=event_type, since_ts=since_ts, limit=limit
        )
        return json.dumps(rows, ensure_ascii=False, default=str)

    def cleanup_sync(self, older_than_days: int = 30) -> int:
        return self._cleanup(older_than_days)

    # ------------------------------------------------------------------
    # Async wrappers (run_in_executor)
    # ------------------------------------------------------------------

    async def record(self, event_type: str, **fields) -> int:
        loop = asyncio.get_running_loop()
        return await loop.run_in_executor(
            None, functools.partial(self.record_sync, event_type, **fields)
        )

    async def search(
        self,
        event_type: str | None = None,
        user_id: int | None = None,
        tool_name: str | None = None,
        since_hours: float | None = None,
        limit: int = 50,
    ) -> list[dict]:
        loop = asyncio.get_running_loop()
        return await loop.run_in_executor(
            None,
            functools.partial(
                self.search_sync,
                event_type=event_type,
                user_id=user_id,
                tool_name=tool_name,
                since_hours=since_hours,
                limit=limit,
            ),
        )

    async def summary(self, hours: int = 1) -> str:
        loop = asyncio.get_running_loop()
        return await loop.run_in_executor(None, self.summary_sync, hours)

    async def export_json(
        self,
        event_type: str | None = None,
        since_hours: float = 24,
        limit: int = 1000,
    ) -> str:
        loop = asyncio.get_running_loop()
        return await loop.run_in_executor(
            None,
            functools.partial(
                self.export_json_sync,
                event_type=event_type,
                since_hours=since_hours,
                limit=limit,
            ),
        )

    async def cleanup(self, older_than_days: int = 30) -> int:
        loop = asyncio.get_running_loop()
        return await loop.run_in_executor(
            None, self.cleanup_sync, older_than_days
        )

    def close(self) -> None:
        try:
            self._conn.close()
        except Exception:
            pass


def install_audit(original_log_event, audit_log: AuditLog):
    """Return a wrapped log_event that also records to the audit database.

    The wrapper calls the original log_event first (preserving its behaviour),
    then fires a non-blocking audit record.  Any error during audit recording
    is logged but never raised.
    """

    @functools.wraps(original_log_event)
    def wrapped(event: str, **fields) -> None:
        # Always call the original logger first.
        original_log_event(event, **fields)

        # Record to audit DB (non-blocking best-effort).
        try:
            loop = asyncio.get_running_loop()
            loop.run_in_executor(None, functools.partial(audit_log.record_sync, event, **fields))
        except RuntimeError:
            # No running loop (sync context or early startup) — record inline.
            try:
                audit_log.record_sync(event, **fields)
            except Exception:
                LOGGER.debug("audit record failed for %s", event, exc_info=True)
        except Exception:
            LOGGER.debug("audit record failed for %s", event, exc_info=True)

    return wrapped
