"""Calendar capability module for Shadow-Claw Personal Agent.

Provides LLM-callable tools for querying and creating calendar events.

Architecture
------------
- ``CalendarConnector`` (Protocol) — injectable interface; production uses
  GoogleCalendarConnector. Tests use ``StubCalendarConnector``.
- ``ClockFn`` — injectable ``() -> datetime`` for controllable time in tests.
- ``register_calendar_tools(connector, clock)`` — registers tools against
  ToolRegistry, closing over connector and clock. No global state owned here.
- Capability-level validation (end > start) lives here, not in the connector.
- Tools do NOT handle auth, persistence, or telemetry.

Registered tools
----------------
calendar_today         — agenda for today (uses injectable clock)
calendar_list_events   — events in a given date range
calendar_create_event  — create an event with start/end validation
calendar_find_free_time — find open slots on a given day
"""

from __future__ import annotations

import logging
from datetime import datetime
from typing import Callable, Protocol, runtime_checkable

from agent import ToolRegistry

LOGGER = logging.getLogger("shadow_claw_gateway.capabilities.calendar")

ClockFn = Callable[[], datetime]

_DATETIME_FORMATS = ("%Y-%m-%d %H:%M", "%Y-%m-%dT%H:%M", "%Y-%m-%d")


# ---------------------------------------------------------------------------
# Connector protocol
# ---------------------------------------------------------------------------


@runtime_checkable
class CalendarConnector(Protocol):
    async def list_events(self, start_date: str, end_date: str) -> list[dict]: ...
    async def create_event(
        self, title: str, start_time: str, end_time: str, description: str
    ) -> str: ...
    async def find_free_slots(self, date: str, duration_minutes: int) -> list[dict]: ...


# ---------------------------------------------------------------------------
# Stub connector
# ---------------------------------------------------------------------------


class StubCalendarConnector:
    """In-memory stub with injectable seed events; suitable for tests."""

    def __init__(self, events: list[dict] | None = None) -> None:
        if events is None:
            self._events: list[dict] = [
                {
                    "id": "evt_1",
                    "title": "Team standup",
                    "start": "2024-01-15 09:00",
                    "end": "2024-01-15 09:30",
                },
                {
                    "id": "evt_2",
                    "title": "Lunch with Sarah",
                    "start": "2024-01-15 12:00",
                    "end": "2024-01-15 13:00",
                },
            ]
        else:
            self._events = events
        self.created: list[dict] = []
        self._counter = 0

    async def list_events(self, start_date: str, end_date: str) -> list[dict]:
        # Stub returns all seeded events regardless of range
        return list(self._events)

    async def create_event(
        self, title: str, start_time: str, end_time: str, description: str = ""
    ) -> str:
        self._counter += 1
        evt_id = f"new_evt_{self._counter}"
        self.created.append(
            {
                "id": evt_id,
                "title": title,
                "start": start_time,
                "end": end_time,
                "description": description,
            }
        )
        return evt_id

    async def find_free_slots(self, date: str, duration_minutes: int) -> list[dict]:
        return [
            {"start": f"{date} 14:00", "end": f"{date} 15:00"},
            {"start": f"{date} 16:00", "end": f"{date} 17:00"},
        ]


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _parse_datetime(dt_str: str) -> datetime | None:
    for fmt in _DATETIME_FORMATS:
        try:
            return datetime.strptime(dt_str, fmt)
        except ValueError:
            continue
    return None


def _fmt_events(events: list[dict], header: str) -> str:
    if not events:
        return f"No events found ({header})."
    lines = [f"{header} ({len(events)} event(s)):"]
    for e in events:
        lines.append(
            f"  [{e.get('id', '?')}] {e.get('start', '?')} – {e.get('end', '?')}: {e.get('title', '(no title)')}"
        )
    return "\n".join(lines)


# ---------------------------------------------------------------------------
# Tool registration
# ---------------------------------------------------------------------------


def register_calendar_tools(
    connector: CalendarConnector,
    clock: ClockFn | None = None,
) -> None:
    """Register calendar tools against ToolRegistry, closing over *connector*.

    Parameters
    ----------
    connector:
        The calendar backend. Any object satisfying ``CalendarConnector``.
    clock:
        Injectable callable returning current ``datetime``. Defaults to
        ``datetime.now``. Override in tests to pin the current time.
    """
    _clock = clock or datetime.now

    @ToolRegistry.register(
        "calendar_today",
        "Get the agenda for today — list of events scheduled for the current day.",
        {
            "type": "object",
            "properties": {},
            "required": [],
        },
    )
    async def calendar_today() -> str:
        today = _clock().strftime("%Y-%m-%d")
        LOGGER.debug("calendar_today today=%s", today)
        try:
            events = await connector.list_events(today, today)
        except Exception as exc:
            LOGGER.warning("calendar_today connector error: %s", exc)
            return f"Failed to fetch today's agenda: {exc}"
        return _fmt_events(events, f"Today's agenda ({today})")

    @ToolRegistry.register(
        "calendar_list_events",
        "List calendar events within a date range. Provide start_date and optionally end_date.",
        {
            "type": "object",
            "properties": {
                "start_date": {
                    "type": "string",
                    "description": "Start date in YYYY-MM-DD format.",
                },
                "end_date": {
                    "type": "string",
                    "description": "End date in YYYY-MM-DD format (defaults to start_date for single day).",
                },
            },
            "required": ["start_date"],
        },
    )
    async def calendar_list_events(start_date: str, end_date: str | None = None) -> str:
        end = end_date or start_date
        if not start_date.strip():
            return "Error: start_date must not be empty."
        LOGGER.debug("calendar_list_events %s -> %s", start_date, end)
        try:
            events = await connector.list_events(start_date.strip(), end.strip())
        except Exception as exc:
            LOGGER.warning("calendar_list_events connector error: %s", exc)
            return f"Failed to fetch events: {exc}"
        return _fmt_events(events, f"{start_date} to {end}")

    @ToolRegistry.register(
        "calendar_create_event",
        "Create a new calendar event. Validates that end_time is strictly after start_time.",
        {
            "type": "object",
            "properties": {
                "title": {"type": "string", "description": "Event title."},
                "start_time": {
                    "type": "string",
                    "description": "Start time in YYYY-MM-DD HH:MM format.",
                },
                "end_time": {
                    "type": "string",
                    "description": "End time in YYYY-MM-DD HH:MM format.",
                },
                "description": {
                    "type": "string",
                    "description": "Optional event description.",
                },
            },
            "required": ["title", "start_time", "end_time"],
        },
    )
    async def calendar_create_event(
        title: str,
        start_time: str,
        end_time: str,
        description: str = "",
    ) -> str:
        if not title or not title.strip():
            return "Error: event title must not be empty."
        start_dt = _parse_datetime(start_time)
        if start_dt is None:
            return (
                f"Invalid start_time '{start_time}'. Use YYYY-MM-DD HH:MM format."
            )
        end_dt = _parse_datetime(end_time)
        if end_dt is None:
            return (
                f"Invalid end_time '{end_time}'. Use YYYY-MM-DD HH:MM format."
            )
        if end_dt <= start_dt:
            return (
                f"end_time must be after start_time "
                f"(got start={start_time}, end={end_time})."
            )
        LOGGER.debug("calendar_create_event title=%r %s -> %s", title, start_time, end_time)
        try:
            evt_id = await connector.create_event(
                title.strip(), start_time.strip(), end_time.strip(), description.strip()
            )
        except Exception as exc:
            LOGGER.warning("calendar_create_event connector error: %s", exc)
            return f"Failed to create event: {exc}"
        return (
            f"Event created: '{title}' from {start_time} to {end_time} (ID: {evt_id})."
        )

    @ToolRegistry.register(
        "calendar_find_free_time",
        "Find available free time slots on a given day for a meeting of given duration.",
        {
            "type": "object",
            "properties": {
                "date": {
                    "type": "string",
                    "description": "Date to check in YYYY-MM-DD format.",
                },
                "duration_minutes": {
                    "type": "integer",
                    "description": "Required slot duration in minutes (e.g. 30, 60).",
                },
            },
            "required": ["date", "duration_minutes"],
        },
    )
    async def calendar_find_free_time(date: str, duration_minutes: int) -> str:
        if not date or not date.strip():
            return "Error: date must not be empty."
        if duration_minutes <= 0:
            return "Error: duration_minutes must be a positive integer."
        LOGGER.debug(
            "calendar_find_free_time date=%s duration=%d", date, duration_minutes
        )
        try:
            slots = await connector.find_free_slots(date.strip(), duration_minutes)
        except Exception as exc:
            LOGGER.warning("calendar_find_free_time connector error: %s", exc)
            return f"Failed to query free time: {exc}"
        if not slots:
            return f"No free {duration_minutes}-minute slots found on {date}."
        lines = [f"Free {duration_minutes}-minute slots on {date}:"]
        for slot in slots:
            lines.append(f"  {slot['start']} – {slot['end']}")
        return "\n".join(lines)
