"""Personal Agent orchestration helpers.

Request flow
------------

  user text
     │
     ▼
  route_user_intent()
     │
     ▼
  optional memory context
     │
     ▼
  existing agent/chat runtime

This module intentionally stays thin in the core milestone: it classifies the
request, enriches it with durable memory/knowledge context when available, and
returns structured metadata the gateway can log or display.
"""

from __future__ import annotations

from dataclasses import dataclass

import bot_state
from capability_router import CapabilityIntent, route_user_intent
from errors import MemoryBackendUnavailableError


@dataclass(frozen=True)
class PersonalAgentRequest:
    intent: CapabilityIntent
    memory_context: str


async def build_request_context(session_id: str, user_message: str) -> PersonalAgentRequest:
    """Build request context for a personal-agent turn."""
    intent = route_user_intent(user_message)
    cm = bot_state.conversation_manager
    if cm is None:
        raise MemoryBackendUnavailableError("ConversationManager is not initialized.")

    try:
        memory_context = await cm.build_memory_context(session_id, user_message)
    except Exception as exc:  # pragma: no cover - defensive edge for degraded memory runtime
        raise MemoryBackendUnavailableError(str(exc)) from exc

    return PersonalAgentRequest(intent=intent, memory_context=memory_context)
