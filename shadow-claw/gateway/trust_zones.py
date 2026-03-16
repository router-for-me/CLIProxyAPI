"""Trust-zone policies for Personal Agent capabilities.

Observe / Reason / Act is the core safety split for Shadow-Claw's future
personal-agent workflows.
"""

from __future__ import annotations

from enum import Enum


class TrustZone(str, Enum):
    OBSERVE = "observe"
    REASON = "reason"
    ACT = "act"


_TOOL_ZONES: dict[str, TrustZone] = {
    # Read-oriented / knowledge tools
    "memory_store": TrustZone.OBSERVE,
    "memory_recall": TrustZone.OBSERVE,
    "memory_list": TrustZone.OBSERVE,
    "knowledge_ingest": TrustZone.OBSERVE,
    "knowledge_lookup": TrustZone.OBSERVE,
    "knowledge_context": TrustZone.OBSERVE,
    "inbox_list_unread": TrustZone.OBSERVE,
    "inbox_search": TrustZone.OBSERVE,
    "calendar_today": TrustZone.OBSERVE,
    "calendar_list_events": TrustZone.OBSERVE,
    "calendar_find_free_time": TrustZone.OBSERVE,
    "browse_url": TrustZone.OBSERVE,
    "browse_search": TrustZone.OBSERVE,
    "research_topic": TrustZone.REASON,
    "plan_task": TrustZone.REASON,
    "plan_execute": TrustZone.REASON,
    # Side-effecting actions
    "inbox_reply": TrustZone.ACT,
    "calendar_create_event": TrustZone.ACT,
    "desktop_command": TrustZone.ACT,
    "payment_send": TrustZone.ACT,
}


_APPROVAL_REQUIRED: set[str] = {
    "inbox_reply",
    "calendar_create_event",
    "desktop_command",
    "payment_send",
}


def zone_for_tool(tool_name: str) -> TrustZone:
    """Return the trust zone for a given tool name."""
    return _TOOL_ZONES.get(tool_name, TrustZone.REASON)


def should_require_approval(tool_name: str) -> bool:
    """Whether invoking *tool_name* should require explicit user approval."""
    return tool_name in _APPROVAL_REQUIRED
