"""Read-only Gmail connector adapter for Personal Agent Mode.

This connector is intentionally narrow:
- auth state inspection
- fetch/adapt inbox records
- no workflow orchestration
- no parallel cache/store ownership
- no write/send operations (read-only scope)
"""

from __future__ import annotations

import os
from dataclasses import dataclass

from connectors import ConnectorState
from errors import AuthExpiredError, ConnectorUnavailableError


@dataclass(frozen=True)
class GmailConnectorStatus:
    state: ConnectorState
    detail: str


class GmailConnector:
    def __init__(self, access_token: str | None = None, refresh_token: str | None = None) -> None:
        self._access_token = access_token or os.getenv("SHADOW_CLAW_GMAIL_ACCESS_TOKEN", "").strip()
        self._refresh_token = refresh_token or os.getenv("SHADOW_CLAW_GMAIL_REFRESH_TOKEN", "").strip()

    def status(self) -> GmailConnectorStatus:
        if not self._access_token and not self._refresh_token:
            return GmailConnectorStatus(ConnectorState.DISABLED, "gmail connector not configured")
        if not self._access_token and self._refresh_token:
            return GmailConnectorStatus(ConnectorState.EXPIRED, "gmail access token expired; refresh required")
        return GmailConnectorStatus(ConnectorState.CONNECTED, "gmail connector configured")

    def _require_connected(self) -> None:
        s = self.status()
        if s.state == ConnectorState.DISABLED:
            raise ConnectorUnavailableError(s.detail)
        if s.state == ConnectorState.EXPIRED:
            raise AuthExpiredError(s.detail)

    async def list_unread(self, max_results: int = 10) -> list[dict]:
        self._require_connected()
        rows = [
            {
                "thread_id": "gmail-demo-1",
                "from": "demo@gmail.com",
                "subject": "Welcome to Personal Agent Mode",
                "snippet": "This is a placeholder Gmail connector response.",
                "unread": True,
            }
        ]
        return rows[:max_results]

    async def search(self, query: str, max_results: int = 10) -> list[dict]:
        self._require_connected()
        messages = await self.list_unread(max_results=max_results)
        q = query.lower()
        return [
            m for m in messages
            if q in m["subject"].lower() or q in m["snippet"].lower() or q in m["from"].lower()
        ]
