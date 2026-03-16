"""SQLite-backed conversation history and memory storage for Shadow-Claw agent.

Provides ConversationManager for:
- Storing and retrieving conversation history per session
- Storing and searching user-defined memories (key/value + tags)
- Full-text search via SQLite FTS5
"""

import asyncio
import functools
import json
import logging
import sqlite3
import time
from pathlib import Path

LOGGER = logging.getLogger("shadow_claw_gateway.memory")

DEFAULT_DB_PATH = str(Path(__file__).parent / "data" / "conversations.db")

_SCHEMA_SQL = """
CREATE TABLE IF NOT EXISTS conversations (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id  TEXT    NOT NULL,
    role        TEXT    NOT NULL,
    content     TEXT    NOT NULL,
    timestamp   REAL    NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_conv_session ON conversations (session_id);
CREATE INDEX IF NOT EXISTS idx_conv_ts      ON conversations (timestamp);

CREATE TABLE IF NOT EXISTS memories (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    key           TEXT    NOT NULL UNIQUE,
    content       TEXT    NOT NULL,
    tags          TEXT,
    created_at    REAL    NOT NULL,
    last_accessed REAL    NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_mem_key ON memories (key);
"""

_FTS_SQL = """
CREATE VIRTUAL TABLE IF NOT EXISTS memories_fts USING fts5(
    key, content, tags,
    content=memories,
    content_rowid=id
);

CREATE TRIGGER IF NOT EXISTS memories_ai AFTER INSERT ON memories BEGIN
    INSERT INTO memories_fts(rowid, key, content, tags)
    VALUES (new.id, new.key, new.content, new.tags);
END;

CREATE TRIGGER IF NOT EXISTS memories_ad AFTER DELETE ON memories BEGIN
    INSERT INTO memories_fts(memories_fts, rowid, key, content, tags)
    VALUES ('delete', old.id, old.key, old.content, old.tags);
END;

CREATE TRIGGER IF NOT EXISTS memories_au AFTER UPDATE ON memories BEGIN
    INSERT INTO memories_fts(memories_fts, rowid, key, content, tags)
    VALUES ('delete', old.id, old.key, old.content, old.tags);
    INSERT INTO memories_fts(rowid, key, content, tags)
    VALUES (new.id, new.key, new.content, new.tags);
END;
"""


class ConversationManager:
    """Thread-safe conversation and memory manager backed by SQLite."""

    def __init__(self, db_path: str | None = None, history_limit: int = 10):
        self._db_path = db_path or DEFAULT_DB_PATH
        self._history_limit = history_limit
        self._fts_available = False

        db_dir = Path(self._db_path).parent
        db_dir.mkdir(parents=True, exist_ok=True)

        self._conn = sqlite3.connect(self._db_path, check_same_thread=False)
        self._conn.execute("PRAGMA journal_mode=WAL")
        self._conn.executescript(_SCHEMA_SQL)
        self._conn.commit()

        # FTS5 may not be compiled into all SQLite builds
        try:
            self._conn.executescript(_FTS_SQL)
            self._conn.commit()
            self._fts_available = True
        except sqlite3.OperationalError:
            LOGGER.warning("FTS5 not available; memory search will use LIKE fallback")

    # ------------------------------------------------------------------
    # Conversation history
    # ------------------------------------------------------------------

    def store_message_sync(self, session_id: str, role: str, content: str) -> None:
        now = time.time()
        self._conn.execute(
            "INSERT INTO conversations (session_id, role, content, timestamp) VALUES (?, ?, ?, ?)",
            (session_id, role, content, now),
        )
        self._conn.commit()

    def get_history_sync(self, session_id: str, limit: int | None = None) -> list[dict]:
        n = limit or self._history_limit
        rows = self._conn.execute(
            "SELECT role, content FROM conversations "
            "WHERE session_id = ? ORDER BY timestamp DESC LIMIT ?",
            (session_id, n),
        ).fetchall()
        # Return in chronological order
        return [{"role": r[0], "content": r[1]} for r in reversed(rows)]

    # ------------------------------------------------------------------
    # Memory store/recall
    # ------------------------------------------------------------------

    def store_memory_sync(self, key: str, content: str, tags: list[str] | None = None, source: str | None = None) -> str:
        now = time.time()
        all_tags = list(tags or [])
        if source:
            all_tags.append(f"source:{source}")
        tags_str = ",".join(all_tags)
        try:
            self._conn.execute(
                "INSERT INTO memories (key, content, tags, created_at, last_accessed) "
                "VALUES (?, ?, ?, ?, ?)",
                (key, content, tags_str, now, now),
            )
        except sqlite3.IntegrityError:
            # Key exists — update
            self._conn.execute(
                "UPDATE memories SET content = ?, tags = ?, last_accessed = ? WHERE key = ?",
                (content, tags_str, now, key),
            )
        self._conn.commit()
        return f"Stored memory '{key}'"

    def get_memory_sync(self, key: str) -> dict | None:
        """Exact-key retrieval; returns None when key is absent."""
        row = self._conn.execute(
            "SELECT key, content, tags FROM memories WHERE key = ?", (key,)
        ).fetchone()
        if row is None:
            return None
        self._update_access_times([row[0]])
        return {"key": row[0], "content": row[1], "tags": row[2]}

    def list_by_source_sync(self, source: str, limit: int = 20) -> list[dict]:
        """List memories whose tags include 'source:<source>'."""
        tag_frag = f"%source:{source}%"
        rows = self._conn.execute(
            "SELECT key, content, tags FROM memories "
            "WHERE tags LIKE ? ORDER BY last_accessed DESC LIMIT ?",
            (tag_frag, limit),
        ).fetchall()
        return [{"key": r[0], "content": r[1], "tags": r[2]} for r in rows]

    def delete_memory_sync(self, key: str) -> bool:
        """Delete a memory by key. Returns True when a row was deleted."""
        cursor = self._conn.execute("DELETE FROM memories WHERE key = ?", (key,))
        self._conn.commit()
        return cursor.rowcount > 0

    def recall_sync(self, query: str, limit: int = 3, source: str | None = None) -> list[dict]:
        """Search memories by query using FTS5 or LIKE fallback.

        ``source`` optionally scopes the search to memories tagged
        ``source:<source>`` (e.g. ``source="gmail"``).
        """
        src_pattern = f"%source:{source}%" if source else None
        if self._fts_available:
            try:
                sql = (
                    "SELECT m.key, m.content, m.tags FROM memories m "
                    "JOIN memories_fts f ON m.id = f.rowid "
                    "WHERE memories_fts MATCH ?"
                )
                params: list = [query]
                if src_pattern:
                    sql += " AND m.tags LIKE ?"
                    params.append(src_pattern)
                sql += " ORDER BY rank LIMIT ?"
                params.append(limit)
                rows = self._conn.execute(sql, params).fetchall()
                self._update_access_times([r[0] for r in rows])
                return [{"key": r[0], "content": r[1], "tags": r[2]} for r in rows]
            except sqlite3.OperationalError:
                pass  # Fall through to LIKE

        # LIKE fallback
        pattern = f"%{query}%"
        where = ["(content LIKE ? OR key LIKE ? OR tags LIKE ?)"]
        params = [pattern, pattern, pattern]
        if src_pattern:
            where.append("tags LIKE ?")
            params.append(src_pattern)
        params.append(limit)
        sql = (
            "SELECT key, content, tags FROM memories WHERE "
            + " AND ".join(where)
            + " ORDER BY last_accessed DESC LIMIT ?"
        )
        rows = self._conn.execute(sql, params).fetchall()
        self._update_access_times([r[0] for r in rows])
        return [{"key": r[0], "content": r[1], "tags": r[2]} for r in rows]

    def list_memories_sync(self, tag: str | None = None, limit: int = 10) -> list[dict]:
        if tag:
            rows = self._conn.execute(
                "SELECT key, content, tags FROM memories WHERE tags LIKE ? "
                "ORDER BY last_accessed DESC LIMIT ?",
                (f"%{tag}%", limit),
            ).fetchall()
        else:
            rows = self._conn.execute(
                "SELECT key, content, tags FROM memories "
                "ORDER BY last_accessed DESC LIMIT ?",
                (limit,),
            ).fetchall()
        return [{"key": r[0], "content": r[1], "tags": r[2]} for r in rows]

    def _update_access_times(self, keys: list[str]) -> None:
        if not keys:
            return
        now = time.time()
        for key in keys:
            self._conn.execute(
                "UPDATE memories SET last_accessed = ? WHERE key = ?", (now, key)
            )
        self._conn.commit()

    # ------------------------------------------------------------------
    # Context builder — used by agent loop
    # ------------------------------------------------------------------

    def build_memory_context_sync(self, session_id: str, user_message: str) -> str:
        """Build a memory context string to inject as a system message.

        Combines recent conversation history + relevant memories.
        """
        memories = self.recall_sync(user_message, limit=3)
        if not memories:
            return ""

        parts = ["Relevant memories:"]
        for mem in memories:
            parts.append(f"- [{mem['key']}] {mem['content']}")
        return "\n".join(parts)

    # ------------------------------------------------------------------
    # Async wrappers
    # ------------------------------------------------------------------

    async def _run(self, fn, *args):
        loop = asyncio.get_running_loop()
        return await loop.run_in_executor(None, fn, *args)

    async def store_message(self, session_id: str, role: str, content: str) -> None:
        await self._run(self.store_message_sync, session_id, role, content)

    async def get_history(self, session_id: str, limit: int | None = None) -> list[dict]:
        return await self._run(self.get_history_sync, session_id, limit)

    async def store_memory(self, key: str, content: str, tags: list[str] | None = None, source: str | None = None) -> str:
        return await self._run(self.store_memory_sync, key, content, tags, source)

    async def recall(self, query: str, limit: int = 3, source: str | None = None) -> list[dict]:
        return await self._run(self.recall_sync, query, limit, source)

    async def list_memories(self, tag: str | None = None, limit: int = 10) -> list[dict]:
        return await self._run(self.list_memories_sync, tag, limit)

    async def get_memory(self, key: str) -> dict | None:
        return await self._run(self.get_memory_sync, key)

    async def list_by_source(self, source: str, limit: int = 20) -> list[dict]:
        return await self._run(self.list_by_source_sync, source, limit)

    async def delete_memory(self, key: str) -> bool:
        return await self._run(self.delete_memory_sync, key)

    async def build_memory_context(self, session_id: str, user_message: str) -> str:
        return await self._run(self.build_memory_context_sync, session_id, user_message)

    def close(self) -> None:
        try:
            self._conn.close()
        except Exception:
            pass
