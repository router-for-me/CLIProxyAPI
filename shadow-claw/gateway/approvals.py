"""Durable approval lifecycle backed by JobStore.

State flow
----------

  create(tool, payload)
         │
         ▼
   pending approval
         │
     ┌───┴────┐
     ▼        ▼
  approve   reject/expire
     │
     ▼
 approved (terminal for approval flow; execution continues elsewhere)
"""

from __future__ import annotations

import json
import time
from dataclasses import dataclass

from errors import ApprovalExpiredError, ApprovalNotFoundError, AlreadyResolvedError
from jobstore import STATUS_COMPLETED, STATUS_EXPIRED, STATUS_FAILED, STATUS_PENDING, JobStore

_APPROVAL_TOOL_NAME = "approval_request"


@dataclass
class ApprovalRequest:
    id: str
    tool_name: str
    payload: dict
    status: str
    created_at: float | None = None
    updated_at: float | None = None


class ApprovalStore:
    """Thin approval facade over the canonical JobStore backend."""

    def __init__(self, job_store: JobStore, ttl_seconds: int = 3600) -> None:
        self._job_store = job_store
        self._ttl_seconds = ttl_seconds

    async def create(self, tool_name: str, payload: dict, user_id: int | None = None, chat_id: int | None = None) -> ApprovalRequest:
        approval_id = await self._job_store.create_job(
            _APPROVAL_TOOL_NAME,
            prompt=json.dumps({"tool_name": tool_name, "payload": payload}, ensure_ascii=False, sort_keys=True),
            user_id=user_id,
            chat_id=chat_id,
        )
        row = await self._job_store.get_job(approval_id)
        return ApprovalRequest(
            id=approval_id,
            tool_name=tool_name,
            payload=payload,
            status=row["status"],
            created_at=row.get("created_at"),
            updated_at=row.get("updated_at"),
        )

    async def get(self, approval_id: str) -> ApprovalRequest:
        row = await self._job_store.get_job(approval_id)
        if row is None or row.get("tool_name") != _APPROVAL_TOOL_NAME:
            raise ApprovalNotFoundError(f"Approval '{approval_id}' not found.")
        prompt = row.get("prompt") or "{}"
        parsed = json.loads(prompt)
        return ApprovalRequest(
            id=row["id"],
            tool_name=parsed.get("tool_name", "unknown"),
            payload=parsed.get("payload", {}),
            status=row["status"],
            created_at=row.get("created_at"),
            updated_at=row.get("updated_at"),
        )

    async def approve(self, approval_id: str) -> ApprovalRequest:
        approval = await self.get(approval_id)
        self._ensure_open(approval)
        await self._job_store.update_status(approval_id, STATUS_COMPLETED, result_summary="approved")
        return await self.get(approval_id)

    async def reject(self, approval_id: str) -> ApprovalRequest:
        approval = await self.get(approval_id)
        self._ensure_open(approval)
        await self._job_store.update_status(approval_id, STATUS_FAILED, result_summary="rejected")
        return await self.get(approval_id)

    async def expire_if_needed(self, approval_id: str) -> ApprovalRequest:
        approval = await self.get(approval_id)
        if approval.status != STATUS_PENDING:
            return approval
        created_at = approval.created_at or time.time()
        if (time.time() - created_at) > self._ttl_seconds:
            await self._job_store.update_status(approval_id, STATUS_EXPIRED, result_summary="expired")
            return await self.get(approval_id)
        return approval

    def _ensure_open(self, approval: ApprovalRequest) -> None:
        if approval.status == STATUS_EXPIRED:
            raise ApprovalExpiredError(f"Approval '{approval.id}' expired.")
        if approval.status != STATUS_PENDING:
            raise AlreadyResolvedError(f"Approval '{approval.id}' already resolved as {approval.status}.")
