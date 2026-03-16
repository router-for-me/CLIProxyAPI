"""Connector adapters for Personal Agent Mode."""

from enum import Enum


class ConnectorState(str, Enum):
    CONNECTED = "connected"
    DEGRADED = "degraded"
    EXPIRED = "expired"
    DISABLED = "disabled"


__all__ = ["ConnectorState"]
