"""User-visible status summaries for Personal Agent workflows.

The goal is simple and explicit status output, not a complex UI layer.
"""

from __future__ import annotations

from jobstore import format_jobs_list


def format_workflow_status(jobs: list[dict]) -> str:
    """Return a Telegram-friendly workflow status summary."""
    return format_jobs_list(jobs)
