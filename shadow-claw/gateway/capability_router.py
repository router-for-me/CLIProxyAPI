"""Capability router for Personal Agent Mode.

Maps user intent and tool names into high-level product capabilities.
"""

from __future__ import annotations

from dataclasses import dataclass


@dataclass(frozen=True)
class CapabilityIntent:
    name: str
    route_name: str
    rationale: str


_KEYWORD_MAP: tuple[tuple[str, CapabilityIntent], ...] = (
    ("email", CapabilityIntent("inbox", "inbox", "Matched inbox keyword")),
    ("inbox", CapabilityIntent("inbox", "inbox", "Matched inbox keyword")),
    ("mail", CapabilityIntent("inbox", "inbox", "Matched inbox keyword")),
    ("calendar", CapabilityIntent("calendar", "calendar", "Matched calendar keyword")),
    ("agenda", CapabilityIntent("calendar", "calendar", "Matched calendar keyword")),
    ("meeting", CapabilityIntent("calendar", "calendar", "Matched calendar keyword")),
    ("remember", CapabilityIntent("memory", "memory", "Matched memory keyword")),
    ("memory", CapabilityIntent("memory", "memory", "Matched memory keyword")),
    ("know", CapabilityIntent("memory", "memory", "Matched memory keyword")),
)


def route_user_intent(text: str) -> CapabilityIntent:
    lowered = (text or "").lower()
    for keyword, intent in _KEYWORD_MAP:
        if keyword in lowered:
            return intent
    return CapabilityIntent("general", "general", "Default personal-agent route")
