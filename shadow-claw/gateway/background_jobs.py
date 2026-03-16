"""Execution mode classification and background job dispatch.

Execution matrix
----------------

                    OBSERVE zone    REASON zone    ACT zone
                    -----------     -----------    --------
sync-safe tools     SYNC            SYNC           SYNC
approval-required   —               —              APPROVAL_REQUIRED
background tools    —               BACKGROUND     BACKGROUND

Rules applied in order:
  1. ACT tools in _APPROVAL_REQUIRED → APPROVAL_REQUIRED
  2. Tools in _BACKGROUND_TOOLS → BACKGROUND
  3. Everything else → SYNC
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import Enum
from typing import Awaitable, Callable

from trust_zones import should_require_approval

# Tools whose wall-clock execution time is long enough to warrant a job record.
_BACKGROUND_TOOLS: frozenset[str] = frozenset(
    {
        "research_topic",      # depth=deep drives autoresearch subprocess
        "plan_execute",        # may iterate through multiple agent tool turns
        "check_pje",           # Selenium PJe scraper (30-60s per query)
        "research_company",    # gpt-researcher deep company research
        "transcribe_audio",    # Whisper transcription (can take minutes)
        "osint_username",      # maigret scan (60-120s)
        "osint_domain",        # theHarvester scan (30-60s)
    }
)


class ExecutionMode(str, Enum):
    SYNC = "sync"
    BACKGROUND = "background"
    APPROVAL_REQUIRED = "approval_required"


def classify_execution_mode(tool_name: str) -> ExecutionMode:
    """Return the execution mode for a given tool name."""
    if should_require_approval(tool_name):
        return ExecutionMode.APPROVAL_REQUIRED
    if tool_name in _BACKGROUND_TOOLS:
        return ExecutionMode.BACKGROUND
    return ExecutionMode.SYNC


@dataclass(frozen=True)
class DispatchResult:
    mode: ExecutionMode
    job_id: str | None
    result: str | None


async def dispatch(
    tool_name: str,
    invoke: Callable[[], Awaitable[str]],
    *,
    job_store=None,
    approval_store=None,
    payload: dict | None = None,
    user_id: int | None = None,
    chat_id: int | None = None,
) -> DispatchResult:
    """Dispatch a tool invocation according to its execution mode.

    Parameters
    ----------
    tool_name:
        Canonical name of the tool being invoked.
    invoke:
        Zero-argument async callable that executes the tool and returns its
        string output. Only called for SYNC and BACKGROUND modes.
    job_store:
        Optional ``JobStore`` instance. Required for BACKGROUND tracking;
        falls back to SYNC when absent.
    approval_store:
        Optional ``ApprovalStore`` instance. Required for APPROVAL_REQUIRED;
        falls back to SYNC when absent.
    payload:
        Structured tool arguments, stored in the approval request for later
        inspection or replay.
    user_id / chat_id:
        Telegram identifiers forwarded to job/approval records for attribution.

    Returns
    -------
    DispatchResult
        ``mode`` reflects the actual execution path taken (may differ from the
        classified mode when required infrastructure is absent).
        ``job_id`` is set for BACKGROUND and APPROVAL_REQUIRED modes.
        ``result`` is set when the tool ran synchronously to completion.
    """
    mode = classify_execution_mode(tool_name)

    if mode == ExecutionMode.APPROVAL_REQUIRED:
        if approval_store is None:
            # No approval infrastructure configured — run inline and log the gap.
            result = await invoke()
            return DispatchResult(mode=ExecutionMode.SYNC, job_id=None, result=result)
        approval = await approval_store.create(
            tool_name=tool_name,
            payload=payload or {},
            user_id=user_id,
            chat_id=chat_id,
        )
        return DispatchResult(mode=mode, job_id=approval.id, result=None)

    if mode == ExecutionMode.BACKGROUND:
        if job_store is None:
            # No job store — run inline.
            result = await invoke()
            return DispatchResult(mode=ExecutionMode.SYNC, job_id=None, result=result)
        job_id = await job_store.create_job(tool_name, user_id=user_id, chat_id=chat_id)
        await job_store.update_status(job_id, "running")
        try:
            result = await invoke()
            summary = (result or "")[:200] or None
            await job_store.update_status(job_id, "completed", result_summary=summary)
            return DispatchResult(mode=mode, job_id=job_id, result=result)
        except Exception as exc:
            await job_store.update_status(job_id, "failed", result_summary=str(exc)[:200])
            raise

    # SYNC — run inline and return immediately.
    result = await invoke()
    return DispatchResult(mode=ExecutionMode.SYNC, job_id=None, result=result)
