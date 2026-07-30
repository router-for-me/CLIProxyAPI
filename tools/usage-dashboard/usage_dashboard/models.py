"""SQLModel table classes matching the v4 SQLite schema.

Declarative only — schema creation and migration stay in storage.py
(hand-written, ADR 0002). The schema-consistency test in
tests/test_models_schema.py guards drift.
"""


from sqlmodel import Field, SQLModel


class UsageEvent(SQLModel, table=True):
    __tablename__ = "usage_events"

    id: int | None = Field(default=None, primary_key=True)
    event_key: str = Field(unique=True, index=True)
    timestamp: str
    ts_epoch: float = Field(index=True)
    utc_date: str = Field(index=True)
    utc_hour: str = Field(index=True)
    request_id: str | None = None
    account_hash: str | None = Field(default=None, index=True)
    provider: str | None = None
    model: str | None = Field(default=None, index=True)
    alias: str | None = None
    endpoint: str | None = None
    auth_type: str | None = None
    executor_type: str | None = None
    service_tier: str | None = None
    reasoning_effort: str | None = None
    failed: int = 0
    fail_status: int | None = 0
    latency_ms: int | None = 0
    ttft_ms: int | None = 0
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
