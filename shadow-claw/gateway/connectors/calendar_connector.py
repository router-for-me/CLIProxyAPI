"""Read-only Calendar connector adapter for Personal Agent Mode.

This connector is intentionally narrow:
- auth state inspection
- fetch/adapt calendar event records
- no workflow orchestration
- no parallel cache/store ownership
- no write/create operations (read-only scope)
"""

from __future__ import annotations

import os
from dataclasses import dataclass

from connectors import ConnectorState
from errors import AuthExpiredError, ConnectorUnavailableError


@dataclass(frozen=True)
class CalendarConnectorStatus:
    state: ConnectorState
    detail: str


class CalendarConnector:
    def __init__(self, access_token: str | None = None, refresh_token: str | None = None) -> None:
        self._access_token = access_token or os.getenv("SHADOW_CLAW_CALENDAR_ACCESS_TOKEN", "").strip()
        self._refresh_token = refresh_token or os.getenv("SHADOW_CLAW_CALENDAR_REFRESH_TOKEN", "").strip()

    def status(self) -> CalendarConnectorStatus:
        if not self._access_token and not self._refresh_token:
            return CalendarConnectorStatus(ConnectorState.DISABLED, "calendar connector not configured")
        if not self._access_token and self._refresh_token:
            return CalendarConnectorStatus(ConnectorState.EXPIRED, "calendar access token expired; refresh required")
        return CalendarConnectorStatus(ConnectorState.CONNECTED, "calendar connector configured")

    def _require_connected(self) -> None:
        s = self.status()
        if s.state == ConnectorState.DISABLED:
            raise ConnectorUnavailableError(s.detail)
        if s.state == ConnectorState.EXPIRED:
            raise AuthExpiredError(s.detail)

    async def list_events(self, start_date: str, end_date: str) -> list[dict]:
        self._require_connected()
        return [
            {
                "id": "calendar-demo-1",
                "title": "Personal Agent review",
                "start": f"{start_date} 09:00",
                "end": f"{start_date} 09:30",
            }
        ]

    async def find_free_slots(self, date: str, duration_minutes: int) -> list[dict]:
        self._require_connected()
        return [
            {"start": f"{date} 14:00", "end": f"{date} 14:30"},
            {"start": f"{date} 16:00", "end": f"{date} 16:30"},
        ]
