"""Inbox capability module for Shadow-Claw Personal Agent.

Provides LLM-callable tools for reading, searching, and replying to email/messages.

Architecture
------------
- ``InboxConnector`` (Protocol) — injectable interface; production uses GmailConnector
  (not in this file). Tests use ``StubInboxConnector``.
- ``register_inbox_tools(connector)`` — registers three agent tools against
  ToolRegistry, closing over the connector. No global state owned here;
  call this once per bot lifetime.
- Tools do NOT handle auth, rate-limiting, or telemetry — those are gateway concerns.

Registered tools
----------------
inbox_list_unread  — list N most recent unread threads
inbox_search       — keyword / sender search
inbox_reply        — send a reply to a thread
"""

from __future__ import annotations

import logging
from typing import Protocol, runtime_checkable

from agent import ToolRegistry

LOGGER = logging.getLogger("shadow_claw_gateway.capabilities.inbox")


# ---------------------------------------------------------------------------
# Connector protocol — thin interface the capability depends on
# ---------------------------------------------------------------------------


@runtime_checkable
class InboxConnector(Protocol):
    async def list_unread(self, max_results: int = 10) -> list[dict]: ...
    async def search(self, query: str, max_results: int = 10) -> list[dict]: ...
    async def reply(self, thread_id: str, body: str) -> str: ...


# ---------------------------------------------------------------------------
# Stub connector for offline / test use
# ---------------------------------------------------------------------------


class StubInboxConnector:
    """In-memory stub; suitable for tests and demo mode."""

    def __init__(self, messages: list[dict] | None = None) -> None:
        # Each message: {thread_id, from, subject, snippet, unread}
        if messages is None:
            self._messages: list[dict] = [
                {
                    "thread_id": "t1",
                    "from": "alice@example.com",
                    "subject": "Project update",
                    "snippet": "Hey, the Q1 report is ready.",
                    "unread": True,
                },
                {
                    "thread_id": "t2",
                    "from": "bob@example.com",
                    "subject": "Meeting tomorrow",
                    "snippet": "Can we move the standup to 10am?",
                    "unread": True,
                },
            ]
        else:
            self._messages = messages
        self.sent: list[dict] = []

    async def list_unread(self, max_results: int = 10) -> list[dict]:
        return [m for m in self._messages if m.get("unread")][:max_results]

    async def search(self, query: str, max_results: int = 10) -> list[dict]:
        q = query.lower()
        return [
            m for m in self._messages
            if q in m.get("subject", "").lower()
            or q in m.get("from", "").lower()
            or q in m.get("snippet", "").lower()
        ][:max_results]

    async def reply(self, thread_id: str, body: str) -> str:
        self.sent.append({"thread_id": thread_id, "body": body})
        return f"Reply sent to thread {thread_id}."


# ---------------------------------------------------------------------------
# Tool registration
# ---------------------------------------------------------------------------


def _fmt_messages(messages: list[dict]) -> str:
    if not messages:
        return "No messages found."
    lines = []
    for m in messages:
        unread_mark = "●" if m.get("unread") else "○"
        lines.append(
            f"[{unread_mark}] {m['thread_id']} | From: {m['from']} | {m['subject']}\n"
            f"    {m.get('snippet', '')}"
        )
    return "\n".join(lines)


def register_inbox_tools(connector: InboxConnector) -> None:
    """Register inbox tools against ToolRegistry, closing over *connector*.

    Call once at startup. Calling again re-registers (overwrites) the tools
    with the new connector — useful in tests.
    """

    @ToolRegistry.register(
        "inbox_list_unread",
        "List the most recent unread messages/emails in the inbox.",
        {
            "type": "object",
            "properties": {
                "max_results": {
                    "type": "integer",
                    "description": "Maximum number of messages to return (default 10, max 50).",
                },
            },
            "required": [],
        },
    )
    async def inbox_list_unread(max_results: int = 10) -> str:
        max_results = min(max(1, max_results), 50)
        LOGGER.debug("inbox_list_unread max_results=%d", max_results)
        try:
            messages = await connector.list_unread(max_results=max_results)
        except Exception as exc:
            LOGGER.warning("inbox_list_unread connector error: %s", exc)
            return f"Failed to fetch inbox: {exc}"
        return _fmt_messages(messages)

    @ToolRegistry.register(
        "inbox_search",
        "Search the inbox by keyword, sender address, or subject line.",
        {
            "type": "object",
            "properties": {
                "query": {
                    "type": "string",
                    "description": "Search query (keyword, sender, or subject fragment).",
                },
                "max_results": {
                    "type": "integer",
                    "description": "Maximum number of results (default 10, max 50).",
                },
            },
            "required": ["query"],
        },
    )
    async def inbox_search(query: str, max_results: int = 10) -> str:
        if not query or not query.strip():
            return "Error: query must not be empty."
        max_results = min(max(1, max_results), 50)
        LOGGER.debug("inbox_search query=%r max_results=%d", query, max_results)
        try:
            messages = await connector.search(query.strip(), max_results=max_results)
        except Exception as exc:
            LOGGER.warning("inbox_search connector error: %s", exc)
            return f"Search failed: {exc}"
        return _fmt_messages(messages)

    @ToolRegistry.register(
        "inbox_reply",
        "Send a reply to a specific email thread.",
        {
            "type": "object",
            "properties": {
                "thread_id": {
                    "type": "string",
                    "description": "The thread ID to reply to (from inbox_list_unread or inbox_search results).",
                },
                "body": {
                    "type": "string",
                    "description": "The body text of the reply.",
                },
            },
            "required": ["thread_id", "body"],
        },
    )
    async def inbox_reply(thread_id: str, body: str) -> str:
        if not thread_id or not thread_id.strip():
            return "Error: thread_id must not be empty."
        if not body or not body.strip():
            return "Error: reply body must not be empty."
        LOGGER.debug("inbox_reply thread_id=%r", thread_id)
        try:
            return await connector.reply(thread_id.strip(), body.strip())
        except Exception as exc:
            LOGGER.warning("inbox_reply connector error: %s", exc)
            return f"Failed to send reply: {exc}"
