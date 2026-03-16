"""Named error taxonomy for Shadow-Claw Personal Agent Mode.

Capability modules and connectors should raise specific exceptions instead of
anonymous RuntimeError strings where practical. Gateway orchestration can then
map these into visible degraded states and structured telemetry.
"""

from __future__ import annotations


class ShadowClawError(Exception):
    """Base class for Personal Agent runtime errors."""


class ValidationError(ShadowClawError):
    """User or connector input failed validation."""


class ConnectorUnavailableError(ShadowClawError):
    """Connector is disabled, unreachable, or otherwise unavailable."""


class AuthExpiredError(ShadowClawError):
    """Connector credentials expired and require relinking or refresh."""


class RateLimitError(ShadowClawError):
    """Upstream provider rate limited the request."""


class MemoryBackendUnavailableError(ShadowClawError):
    """Memory backend is temporarily unavailable or degraded."""


class ApprovalError(ShadowClawError):
    """Base class for approval-lifecycle errors."""


class ApprovalNotFoundError(ApprovalError):
    """Approval request ID does not exist."""


class ApprovalExpiredError(ApprovalError):
    """Approval request exists but has expired."""


class AlreadyResolvedError(ApprovalError):
    """Approval request was already approved or rejected."""


class PermissionDeniedError(ShadowClawError):
    """Action requires approval or is forbidden by policy."""


class BrowserTimeoutError(ShadowClawError):
    """Browser task exceeded time budget."""


class ElementNotFoundError(ShadowClawError):
    """Expected page or UI element could not be found."""


class QueueUnavailableError(ShadowClawError):
    """Background job runtime is unavailable."""


class PartialExecutionError(ShadowClawError):
    """Workflow completed partially and can potentially be resumed."""


class SchemaValidationError(ShadowClawError):
    """Structured model or connector payload failed schema validation."""
