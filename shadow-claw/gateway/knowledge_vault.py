"""Knowledge Vault — normalized snapshot layer for connector-derived personal context.

Every item flowing in from a connector (Gmail, Calendar, Contacts…) or from a
research tool is normalised into a ``KnowledgeItem`` and stored via
``ConversationManager``.  The vault provides:

* **Snapshot semantics** — each (source, entity_id) pair has exactly one live
  snapshot; calling ``ingest`` again upserts silently.
* **Reference semantics** — items are addressable by their canonical ``ref``
  (``"<source>:<entity_id>"``), e.g. ``"gmail:msg_abc123"``.
* **Source-aware recall** — search can be scoped to a single source or entity kind.

Data-flow
---------

  Connectors / sources
         │
         ▼
   ingest(KnowledgeItem)
         │
         │  key     = item.ref         ("gmail:msg_abc123")
         │  content = item.summary
         │  tags    = [item.kind, "source:<source>", ...item.tags]
         │  source  = item.source       (written as tag by ConversationManager)
         ▼
  ConversationManager.store_memory_sync()
         │
         ▼
    SQLite memories table
         │
   ┌─────┴──────────────────────────────────┐
   ▼                                        ▼
lookup(query, source?, kind?)         get_by_ref(ref)
   │                                        │
   ▼                                        ▼
[{key, content, tags}, ...]         {key, content, tags} | None
"""

from __future__ import annotations

import asyncio
import functools
import logging
import time
from dataclasses import dataclass, field
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from memory_store import ConversationManager

LOGGER = logging.getLogger("shadow_claw_gateway.knowledge_vault")


# ---------------------------------------------------------------------------
# Data model
# ---------------------------------------------------------------------------


@dataclass
class KnowledgeItem:
    """A single normalised snapshot of a connector-derived entity.

    Parameters
    ----------
    ref:
        Canonical reference in ``"<source>:<entity_id>"`` format.
        Examples: ``"gmail:msg_abc123"``, ``"calendar:evt_001"``.
    source:
        Data origin label, e.g. ``"gmail"``, ``"calendar"``, ``"research"``.
    kind:
        Entity type, e.g. ``"email"``, ``"event"``, ``"contact"``, ``"fact"``.
    summary:
        Human-readable description stored as the memory content.
    tags:
        Extra caller-supplied tags for retrieval.
    snapshot_at:
        Unix timestamp; defaults to ``time.time()`` at creation.
    """

    ref: str
    source: str
    kind: str
    summary: str
    tags: list[str] = field(default_factory=list)
    snapshot_at: float = field(default_factory=time.time)


# ---------------------------------------------------------------------------
# KnowledgeVault
# ---------------------------------------------------------------------------


class KnowledgeVault:
    """Snapshot store backed by :class:`~memory_store.ConversationManager`.

    Dependencies are injected so the vault is independently testable.
    """

    def __init__(self, manager: "ConversationManager") -> None:
        self._manager = manager

    # ------------------------------------------------------------------
    # Write path
    # ------------------------------------------------------------------

    def ingest_sync(self, item: KnowledgeItem) -> str:
        """Upsert a knowledge snapshot.

        The ``kind`` tag is stored first so ``lookup_sync`` can filter by it
        using a simple positional parse without field-name overhead.
        """
        tags = [item.kind] + item.tags
        LOGGER.debug("knowledge_vault.ingest ref=%s source=%s kind=%s", item.ref, item.source, item.kind)
        return self._manager.store_memory_sync(
            item.ref, item.summary, tags, source=item.source
        )

    # ------------------------------------------------------------------
    # Read path
    # ------------------------------------------------------------------

    def lookup_sync(
        self,
        query: str,
        source: str | None = None,
        kind: str | None = None,
        limit: int = 5,
    ) -> list[dict]:
        """Search knowledge by semantic query, optionally scoped by source/kind.

        Returns raw memory dicts with keys ``key`` (= ref), ``content``
        (= summary), ``tags`` (raw comma-separated string).
        """
        # Over-fetch so kind filtering can trim without a second query
        fetch_n = limit * 2 if kind else limit
        results = self._manager.recall_sync(query, limit=fetch_n, source=source)
        if kind:
            results = [
                r for r in results
                if kind in r.get("tags", "").split(",")
            ]
        return results[:limit]

    def get_by_ref_sync(self, ref: str) -> dict | None:
        """Retrieve the latest snapshot for a canonical ref, or None."""
        return self._manager.get_memory_sync(ref)

    def snapshot_context_sync(self, topic: str, limit: int = 5) -> str:
        """Build an LLM-ready context string for ``topic``.

        Returns an empty string when no relevant items exist.
        """
        results = self.lookup_sync(topic, limit=limit)
        if not results:
            return ""
        lines = ["Relevant knowledge:"]
        for r in results:
            lines.append(f"- [{r['key']}] {r['content']}")
        return "\n".join(lines)

    def list_by_source_sync(self, source: str, limit: int = 20) -> list[dict]:
        """List all snapshots for a given source label."""
        return self._manager.list_by_source_sync(source, limit=limit)

    def delete_sync(self, ref: str) -> bool:
        """Remove a snapshot by ref. Returns True when a row was deleted."""
        return self._manager.delete_memory_sync(ref)

    # ------------------------------------------------------------------
    # Async wrappers
    # ------------------------------------------------------------------

    async def ingest(self, item: KnowledgeItem) -> str:
        loop = asyncio.get_running_loop()
        return await loop.run_in_executor(None, self.ingest_sync, item)

    async def lookup(
        self,
        query: str,
        source: str | None = None,
        kind: str | None = None,
        limit: int = 5,
    ) -> list[dict]:
        fn = functools.partial(self.lookup_sync, query, source, kind, limit)
        loop = asyncio.get_running_loop()
        return await loop.run_in_executor(None, fn)

    async def get_by_ref(self, ref: str) -> dict | None:
        loop = asyncio.get_running_loop()
        return await loop.run_in_executor(None, self.get_by_ref_sync, ref)

    async def snapshot_context(self, topic: str, limit: int = 5) -> str:
        fn = functools.partial(self.snapshot_context_sync, topic, limit)
        loop = asyncio.get_running_loop()
        return await loop.run_in_executor(None, fn)

    async def list_by_source(self, source: str, limit: int = 20) -> list[dict]:
        fn = functools.partial(self.list_by_source_sync, source, limit)
        loop = asyncio.get_running_loop()
        return await loop.run_in_executor(None, fn)

    async def delete(self, ref: str) -> bool:
        loop = asyncio.get_running_loop()
        return await loop.run_in_executor(None, self.delete_sync, ref)
