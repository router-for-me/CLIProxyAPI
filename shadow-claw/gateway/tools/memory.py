"""Memory tools: store, recall, and list persistent memories.

Inspired by topoteretes/cognee — gives the LLM the ability to remember
facts across conversations.
"""

import json

from agent import tool


def _get_manager():
    """Lazy import to avoid circular dependency at registration time."""
    import bot_state
    return bot_state.conversation_manager


@tool(
    "memory_store",
    "Store a fact, note, or piece of information for later recall. "
    "Use this when the user asks you to remember something.",
    {
        "type": "object",
        "properties": {
            "key": {
                "type": "string",
                "description": "Short identifier for this memory (e.g., 'favorite_color', 'home_address')",
            },
            "content": {
                "type": "string",
                "description": "The content to remember",
            },
            "tags": {
                "type": "array",
                "items": {"type": "string"},
                "description": "Optional tags for categorization",
            },
        },
        "required": ["key", "content"],
    },
)
async def memory_store(key: str, content: str, tags: list[str] | None = None) -> str:
    mgr = _get_manager()
    if mgr is None:
        return "Memory system is not available."
    return await mgr.store_memory(key, content, tags)


@tool(
    "memory_recall",
    "Search past conversations and stored memories for relevant information. "
    "Use this when the user asks about something you might have stored previously.",
    {
        "type": "object",
        "properties": {
            "query": {
                "type": "string",
                "description": "Search query to find relevant memories",
            },
        },
        "required": ["query"],
    },
)
async def memory_recall(query: str) -> str:
    mgr = _get_manager()
    if mgr is None:
        return "Memory system is not available."
    results = await mgr.recall(query, limit=5)
    if not results:
        return "No matching memories found."
    lines = []
    for r in results:
        tags_str = f" [tags: {r['tags']}]" if r.get("tags") else ""
        lines.append(f"- {r['key']}: {r['content']}{tags_str}")
    return "\n".join(lines)


@tool(
    "memory_list",
    "List all stored memories, optionally filtered by tag.",
    {
        "type": "object",
        "properties": {
            "tag": {
                "type": "string",
                "description": "Optional tag to filter memories",
            },
        },
        "required": [],
    },
)
async def memory_list(tag: str | None = None) -> str:
    mgr = _get_manager()
    if mgr is None:
        return "Memory system is not available."
    results = await mgr.list_memories(tag=tag, limit=20)
    if not results:
        return "No memories stored yet."
    lines = [f"Stored memories ({len(results)}):"]
    for r in results:
        lines.append(f"- {r['key']}: {r['content']}")
    return "\n".join(lines)
