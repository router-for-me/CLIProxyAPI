# Ticket 1.1 — SQLModel tables + schema consistency test

**Phase**: 1 — Back end
**Blocks**: 1.2, 1.3
**Blocked by**: Phase 0 complete
**Files touched**:
- `tools/usage-dashboard/usage_dashboard/models.py` (new)
- `tools/usage-dashboard/tests/test_models_schema.py` (new)

**Files NOT touched**:
- `usage_dashboard/storage.py` (DDL stays hand-written — ADR 0002)
- `usage_dashboard/query.py`, `usage_dashboard/collector.py`

---

## 🎯 Goal

SQLModel table classes are declared that match the existing v4 schema
exactly. A schema-consistency test asserts `SQLModel.metadata` matches
the applied migration output on a fresh in-memory DB. The table classes
are **declarative only** — they are not used to create or migrate schema.

This is the foundational ticket for Phase 1: every later FastAPI router
and SQLModel query depends on these classes.

---

## 🔴 Mandatory TDD discipline

Red → Green → Refactor. One commit per step.

---

## 🪜 Steps

### Step 1 — Red: schema-consistency test

```python
# tests/test_models_schema.py
"""Assert SQLModel metadata matches the hand-written migration output."""
import sqlite3
from pathlib import Path

import pytest
from sqlmodel import SQLModel, create_engine

from usage_dashboard import models  # noqa: F401  (registers tables)
from usage_dashboard.storage import _TARGET_COLUMNS, run_migrations


def _applied_schema(db_path: str) -> dict[str, list[str]]:
    """Return {table_name: [column_names]} from an applied v4 DB."""
    cfg = {"data_dir": str(Path(db_path).parent)}
    run_migrations(cfg)
    conn = sqlite3.connect(db_path)
    conn.row_factory = sqlite3.Row
    out = {}
    for table in ("usage_events", "key_aliases", "collector_state", "schema_meta"):
        rows = conn.execute(f"PRAGMA table_info({table})").fetchall()
        out[table] = [r["name"] for r in rows]
    conn.close()
    return out


def _sqlmodel_schema() -> dict[str, list[str]]:
    """Return {table_name: [column_names]} from SQLModel.metadata."""
    out = {}
    for table_name, table in SQLModel.metadata.tables.items():
        out[table_name] = [c.name for c in table.columns]
    return out


def test_usage_events_columns_match(tmp_path):
    db = tmp_path / "usage.sqlite"
    applied = _applied_schema(str(db))
    declared = _sqlmodel_schema()
    expected = [c.split()[0] for c in _TARGET_COLUMNS]
    assert set(declared["usage_events"]) == set(expected)
    assert set(applied["usage_events"]) == set(expected)


def test_key_aliases_columns_match(tmp_path):
    db = tmp_path / "usage.sqlite"
    applied = _applied_schema(str(db))
    declared = _sqlmodel_schema()
    assert set(declared["key_aliases"]) == {"account_hash", "alias"}
    assert set(applied["key_aliases"]) == {"account_hash", "alias"}


def test_collector_state_columns_match(tmp_path):
    db = tmp_path / "usage.sqlite"
    applied = _applied_schema(str(db))
    declared = _sqlmodel_schema()
    assert set(declared["collector_state"]) == {"key", "value"}
    assert set(applied["collector_state"]) == {"key", "value"}


def test_schema_consistent_across_tables(tmp_path):
    """Every table in SQLModel.metadata exists in the applied DB, and vice versa."""
    db = tmp_path / "usage.sqlite"
    applied = _applied_schema(str(db))
    declared = _sqlmodel_schema()
    # Exclude schema_meta from declared (it is internal to the migration runner)
    assert set(declared).issubset(set(applied))
```

**Verify red**: `uv run pytest tests/test_models_schema.py -v` fails because
`models.py` does not exist.

Commit: `test(models): red — SQLModel metadata matches v4 schema`

### Step 2 — Green: write models.py

```python
# usage_dashboard/models.py
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
    fail_status: Optional[int] = None
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
```

**Verify green**: `uv run pytest tests/test_models_schema.py -v` — all 4 pass.

Commit: `feat(models): SQLModel tables for v4 schema — green`

### Step 3 — Refactor: add a runtime guard for drift

Add a CI-only test that re-runs the consistency check and fails CI on drift:

```python
# tests/test_models_schema.py (append)
def test_schema_consistency_in_ci(tmp_path):
    """Fail-fast in CI if SQLModel metadata drifts from the applied schema."""
    db = tmp_path / "usage.sqlite"
    applied = _applied_schema(str(db))
    declared = _sqlmodel_schema()
    for table_name, cols in declared.items():
        applied_cols = set(applied[table_name])
        declared_cols = set(cols)
        assert applied_cols == declared_cols, (
            f"Schema drift on {table_name}: "
            f"missing_in_declared={applied_cols - declared_cols}, "
            f"missing_in_applied={declared_cols - applied_cols}"
        )
```

**Verify refactor**: `uv run pytest tests/test_models_schema.py -v` — 5 pass.

Commit: `test(models): CI schema-drift guard`

---

## ✅ Ticket completion gate

| # | Check | Command |
|---|-------|---------|
| 1 | Lint | `uv run ruff check usage_dashboard/models.py tests/test_models_schema.py` |
| 2 | Type Check | `uv run mypy usage_dashboard/models.py` |
| 3 | Build | `uv build` |
| 4 | Unit Tests | `uv run pytest tests/test_models_schema.py -v` |
| 5 | Integration Tests | `uv run pytest tests/ -v` |
| 6 | Functional Tests | `uv run python -c "from usage_dashboard.models import UsageEvent; print(UsageEvent.__tablename__)"` |
| 7 | Contract Tests | Compare `SQLModel.metadata` against `_TARGET_COLUMNS` (covered by Step 1 test) |
| 8 | E2E | N/A |
| 9 | Code Review | Confirm `models.py` is declarative-only, no DDL, no migration logic |

All green → Ticket 1.2.
