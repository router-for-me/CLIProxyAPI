"""SQLModel table classes matching the v4 SQLite schema.

Declarative only — schema creation and migration stay in storage.py
(hand-written, ADR 0002). The schema-consistency test in
tests/test_models_schema.py guards drift.
"""

from typing import Optional

from sqlmodel import SQLModel, Field


class UsageEvent(SQLModel, table=True):
    __tablename__ = "usage_events"

    id: Optional[int] = Field(default=None, primary_key=True)
    event_key: str = Field(unique=True, index=True)
    timestamp: str
    ts_epoch: float = Field(index=True)
    utc_date: str = Field(index=True)
    utc_hour: str = Field(index=True)
    request_id: Optional[str] = None
    account_hash: Optional[str] = Field(default=None, index=True)
    provider: Optional[str] = None
    model: Optional[str] = Field(default=None, index=True)
    alias: Optional[str] = None
    endpoint: Optional[str] = None
    auth_type: Optional[str] = None
    executor_type: Optional[str] = None
    service_tier: Optional[str] = None
    reasoning_effort: Optional[str] = None
    failed: int = 0
    fail_status: Optional[int] = 0
    latency_ms: Optional[int] = 0
    ttft_ms: Optional[int] = 0
    input_tokens: int = 0
    output_tokens: int = 0
    reasoning_tokens: int = 0
    cached_tokens: int = 0
    cache_read_tokens: int = 0
    cache_creation_tokens: int = 0
    total_tokens: int = 0


class KeyAlias(SQLModel, table=True):
    __tablename__ = "key_aliases"

    account_hash: str = Field(primary_key=True)
    alias: str


class CollectorStateRow(SQLModel, table=True):
    __tablename__ = "collector_state"

    key: str = Field(primary_key=True)
    value: str