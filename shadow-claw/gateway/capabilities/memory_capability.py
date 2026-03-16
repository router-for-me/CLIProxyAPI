"""Memory capability for Shadow-Claw Personal Agent.

Orchestrates both the raw memory store (user-defined facts) and the
KnowledgeVault (connector-derived personal context snapshots).

Architecture
------------
This module owns no storage or auth.  It wires together::

  Agent (tools / loop)
       │
       ├─► remember / recall / list   ─► ConversationManager  (user facts)
       │
       ├─► knowledge_ingest            ─► KnowledgeVault ─► ConversationManager
       ├─► knowledge_lookup
       └─► knowledge_context           (merged LLM-ready context string)

``register_memory_tools(vault, manager)`` follows the same injection pattern
as ``inbox_capability`` and ``calendar_capability``: call it once at startup
with live dependencies; unit tests inject stubs.

The ``MemoryCapability`` class provides a programmatic API usable by other
capability modules that need to cross-reference knowledge during their own
tool implementations.
"""

from __future__ import annotations

import logging
from typing import TYPE_CHECKING

from agent import ToolRegistry

if TYPE_CHECKING:
    from memory_store import ConversationManager
    from knowledge_vault import KnowledgeVault, KnowledgeItem

LOGGER = logging.getLogger("shadow_claw_gateway.capabilities.memory")


# ---------------------------------------------------------------------------
# Programmatic API — used by other capabilities
# ---------------------------------------------------------------------------


class MemoryCapability:
    """High-level memory + knowledge orchestrator.

    Parameters
    ----------
    manager:
        A live ``ConversationManager`` for raw user-fact storage.
    vault:
        A live ``KnowledgeVault`` for connector-derived knowledge.
    """

    def __init__(self, manager: "ConversationManager", vault: "KnowledgeVault") -> None:
        self._manager = manager
        self._vault = vault

    # ------------------------------------------------------------------
    # Raw memory (user-defined facts)
    # ------------------------------------------------------------------

    async def remember(self, key: str, content: str, tags: list[str] | None = None) -> str:
        return await self._manager.store_memory(key, content, tags)

    async def recall(self, query: str, limit: int = 5) -> list[dict]:
        return await self._manager.recall(query, limit=limit)

    # ------------------------------------------------------------------
    # Knowledge vault (connector-derived snapshots)
    # ------------------------------------------------------------------

    async def ingest_knowledge(
        self,
        ref: str,
        source: str,
        kind: str,
        summary: str,
        tags: list[str] | None = None,
    ) -> str:
        from knowledge_vault import KnowledgeItem

        item = KnowledgeItem(
            ref=ref,
            source=source,
            kind=kind,
            summary=summary,
            tags=list(tags or []),
        )
        return await self._vault.ingest(item)

    async def lookup_knowledge(
        self,
        query: str,
        source: str | None = None,
        kind: str | None = None,
        limit: int = 5,
    ) -> list[dict]:
        return await self._vault.lookup(query, source=source, kind=kind, limit=limit)

    # ------------------------------------------------------------------
    # Context builder — merged view for the agent loop
    # ------------------------------------------------------------------

    async def build_context(
        self,
        session_id: str,
        user_message: str,
        memory_limit: int = 3,
        knowledge_limit: int = 3,
    ) -> str:
        """Build a merged context string to inject as a system message.

        Combines relevant user memories and connector-derived knowledge
        snippets.  Returns an empty string when nothing is relevant.
        """
        raw_mems = await self._manager.recall(user_message, limit=memory_limit)
        knowledge = await self._vault.lookup(user_message, limit=knowledge_limit)

        parts: list[str] = []
        if raw_mems:
            parts.append("Relevant memories:")
            for m in raw_mems:
                parts.append(f"- [{m['key']}] {m['content']}")
        if knowledge:
            parts.append("Relevant knowledge:")
            for k in knowledge:
                parts.append(f"- [{k['key']}] {k['content']}")
        return "\n".join(parts)

    @classmethod
    def from_bot_state(cls) -> "MemoryCapability":
        """Construct from the live ``bot_state`` subsystems (production path)."""
        import bot_state
        from knowledge_vault import KnowledgeVault

        manager = bot_state.conversation_manager
        if manager is None:
            raise RuntimeError("ConversationManager not yet initialised in bot_state")
        vault = KnowledgeVault(manager)
        return cls(manager, vault)


# ---------------------------------------------------------------------------
# ToolRegistry registration
# ---------------------------------------------------------------------------


def register_memory_tools(
    vault: "KnowledgeVault",
    manager: "ConversationManager",
) -> None:
    """Register knowledge-vault agent tools into ``ToolRegistry``.

    Call once at startup after the vault and manager are ready.
    The registered tools complement (not replace) the raw ``memory_store``,
    ``memory_recall``, and ``memory_list`` tools in ``tools/memory.py``.

    Registered tools
    ----------------
    knowledge_ingest   — store/update a connector-derived knowledge snapshot
    knowledge_lookup   — search knowledge, optionally scoped by source or kind
    knowledge_context  — build an LLM-ready context string for a topic
    """
    cap = MemoryCapability(manager, vault)

    @ToolRegistry.register(
        "knowledge_ingest",
        "Store or update a normalized snapshot of connector-derived information "
        "(e.g. an email summary, a calendar event, a contact fact). "
        "Use this when a connector produces structured personal context that "
        "the agent should be able to recall later.",
        {
            "type": "object",
            "properties": {
                "ref": {
                    "type": "string",
                    "description": "Canonical reference in '<source>:<entity_id>' format, "
                    "e.g. 'gmail:msg_abc123'.",
                },
                "source": {
                    "type": "string",
                    "description": "Data origin label, e.g. 'gmail', 'calendar', 'research'.",
                },
                "kind": {
                    "type": "string",
                    "description": "Entity type, e.g. 'email', 'event', 'contact', 'fact'.",
                },
                "summary": {
                    "type": "string",
                    "description": "Human-readable summary to store.",
                },
                "tags": {
                    "type": "array",
                    "items": {"type": "string"},
                    "description": "Optional extra tags for retrieval.",
                },
            },
            "required": ["ref", "source", "kind", "summary"],
        },
    )
    async def knowledge_ingest(
        ref: str,
        source: str,
        kind: str,
        summary: str,
        tags: list[str] | None = None,
    ) -> str:
        return await cap.ingest_knowledge(ref, source, kind, summary, tags)

    @ToolRegistry.register(
        "knowledge_lookup",
        "Search stored knowledge snapshots. Optionally narrow by source "
        "(e.g. 'gmail') or kind (e.g. 'email'). Returns matching summaries.",
        {
            "type": "object",
            "properties": {
                "query": {
                    "type": "string",
                    "description": "Search query.",
                },
                "source": {
                    "type": "string",
                    "description": "Limit results to this data origin (optional).",
                },
                "kind": {
                    "type": "string",
                    "description": "Limit results to this entity type (optional).",
                },
                "limit": {
                    "type": "integer",
                    "description": "Maximum number of results (default 5).",
                },
            },
            "required": ["query"],
        },
    )
    async def knowledge_lookup(
        query: str,
        source: str | None = None,
        kind: str | None = None,
        limit: int = 5,
    ) -> str:
        results = await cap.lookup_knowledge(query, source=source, kind=kind, limit=limit)
        if not results:
            return "No matching knowledge found."
        lines = []
        for r in results:
            tags_str = f" [{r['tags']}]" if r.get("tags") else ""
            lines.append(f"- {r['key']}: {r['content']}{tags_str}")
        return "\n".join(lines)

    @ToolRegistry.register(
        "knowledge_context",
        "Build a concise, LLM-ready context summary for a topic, drawing from "
        "both user memories and connector-derived knowledge snapshots.",
        {
            "type": "object",
            "properties": {
                "topic": {
                    "type": "string",
                    "description": "Topic or question to build context for.",
                },
            },
            "required": ["topic"],
        },
    )
    async def knowledge_context(topic: str) -> str:
        return await vault.snapshot_context(topic)
