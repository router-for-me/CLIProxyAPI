"""Cognee-backed KnowledgeVault adapter.

Drop-in replacement for KnowledgeVault that uses cognee for semantic
search and knowledge graph relationships instead of SQLite FTS5.
Falls back to the original KnowledgeVault when cognee is unavailable.
"""

from __future__ import annotations

import asyncio
import functools
import logging
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from knowledge_vault import KnowledgeItem

LOGGER = logging.getLogger("shadow_claw_gateway.adapters.cognee")

_cognee_available = False
try:
    import cognee
    _cognee_available = True
except ImportError:
    LOGGER.info("cognee not installed — using SQLite fallback")


class CogneeKnowledgeVault:
    """KnowledgeVault-compatible adapter backed by cognee.

    Implements the same public interface as KnowledgeVault so it can be
    swapped in without changing callers.
    """

    def __init__(self) -> None:
        if not _cognee_available:
            raise ImportError("cognee is not installed. Run: pip install cognee")
        self._initialized = False

    async def _ensure_init(self) -> None:
        if self._initialized:
            return
        try:
            await cognee.prune.prune_system(metadata=True)
            self._initialized = True
            LOGGER.info("cognee adapter initialized")
        except Exception as e:
            LOGGER.warning("cognee init failed, will retry: %s", e)

    # ------------------------------------------------------------------
    # Write path
    # ------------------------------------------------------------------

    def ingest_sync(self, item: "KnowledgeItem") -> str:
        """Upsert a knowledge snapshot via cognee."""
        loop = asyncio.new_event_loop()
        try:
            return loop.run_until_complete(self.ingest(item))
        finally:
            loop.close()

    async def ingest(self, item: "KnowledgeItem") -> str:
        await self._ensure_init()
        # cognee.add accepts text content with metadata
        content = f"[{item.ref}] ({item.source}/{item.kind}) {item.summary}"
        try:
            await cognee.add(content, dataset_name=item.source)
            await cognee.cognify()
            LOGGER.debug("cognee.ingest ref=%s source=%s", item.ref, item.source)
            return f"Stored knowledge '{item.ref}'"
        except Exception as e:
            LOGGER.error("cognee ingest failed for %s: %s", item.ref, e)
            return f"Failed to store '{item.ref}': {e}"

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
        loop = asyncio.new_event_loop()
        try:
            return loop.run_until_complete(self.lookup(query, source, kind, limit))
        finally:
            loop.close()

    async def lookup(
        self,
        query: str,
        source: str | None = None,
        kind: str | None = None,
        limit: int = 5,
    ) -> list[dict]:
        await self._ensure_init()
        try:
            from cognee.api.v1.search import SearchType
            results = await cognee.search(SearchType.INSIGHTS, query_text=query)
        except Exception as e:
            LOGGER.error("cognee search failed: %s", e)
            return []

        if not results:
            return []

        # Normalize cognee results to match KnowledgeVault format
        items = []
        for r in results[:limit * 2]:
            text = str(r) if not isinstance(r, dict) else r.get("text", str(r))
            item = {"key": "", "content": text, "tags": ""}
            # Try to extract ref from content pattern [ref]
            if text.startswith("[") and "]" in text:
                ref_end = text.index("]")
                item["key"] = text[1:ref_end]
                item["content"] = text[ref_end + 1:].strip()
            items.append(item)

        # Filter by source/kind if requested
        if source:
            items = [i for i in items if source in i.get("content", "")]
        if kind:
            items = [i for i in items if kind in i.get("tags", "") or kind in i.get("content", "")]

        return items[:limit]

    def get_by_ref_sync(self, ref: str) -> dict | None:
        results = self.lookup_sync(ref, limit=1)
        for r in results:
            if r.get("key") == ref:
                return r
        return None

    async def get_by_ref(self, ref: str) -> dict | None:
        results = await self.lookup(ref, limit=1)
        for r in results:
            if r.get("key") == ref:
                return r
        return None

    def snapshot_context_sync(self, topic: str, limit: int = 5) -> str:
        results = self.lookup_sync(topic, limit=limit)
        if not results:
            return ""
        lines = ["Relevant knowledge:"]
        for r in results:
            key = r.get("key", "?")
            content = r.get("content", "")
            lines.append(f"- [{key}] {content}")
        return "\n".join(lines)

    async def snapshot_context(self, topic: str, limit: int = 5) -> str:
        fn = functools.partial(self.snapshot_context_sync, topic, limit)
        loop = asyncio.get_running_loop()
        return await loop.run_in_executor(None, fn)

    def list_by_source_sync(self, source: str, limit: int = 20) -> list[dict]:
        return self.lookup_sync(f"source:{source}", limit=limit)

    async def list_by_source(self, source: str, limit: int = 20) -> list[dict]:
        return await self.lookup(f"source:{source}", limit=limit)

    def delete_sync(self, ref: str) -> bool:
        LOGGER.warning("cognee adapter does not support delete yet: %s", ref)
        return False

    async def delete(self, ref: str) -> bool:
        return self.delete_sync(ref)


def is_cognee_available() -> bool:
    """Check if cognee is installed and importable."""
    return _cognee_available
