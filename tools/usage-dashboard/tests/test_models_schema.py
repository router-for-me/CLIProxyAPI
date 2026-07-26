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