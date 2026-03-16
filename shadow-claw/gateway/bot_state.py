"""Shared mutable state for the Shadow-Claw gateway.

Holds late-initialized subsystems and the structured logger.
Other modules import this instead of reaching into bot.py globals,
which prevents circular imports.
"""

import json
import logging

LOGGER = logging.getLogger("shadow_claw_gateway")

# Late-initialized subsystems — set in bot._init_subsystems()
config = None
job_store = None
audit_log = None
rate_limiter = None
conversation_manager = None
approval_store = None
connector_registry = {}
personal_agent_enabled = False


def log_event(event: str, **fields) -> None:
    """Structured event logger. Replaced at runtime by metrics/audit wrappers."""
    payload = {"event": event}
    for key, value in fields.items():
        if value is not None:
            payload[key] = value
    LOGGER.info(json.dumps(payload, ensure_ascii=False, sort_keys=True))
