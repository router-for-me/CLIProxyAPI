# Ticket 1.3 — Async collector (httpx + asyncio.Task)

**Phase**: 1 — Back end
**Blocks**: 1.8
**Blocked by**: 1.1, 1.2
**Files touched**:
- `tools/usage-dashboard/usage_dashboard/collector_async.py` (new — keep `collector.py` untouched until 1.9)
- `tools/usage-dashboard/tests/test_collector_async.py` (new)

**Files NOT touched**: legacy `collector.py`

---

## 🎯 Goal

A new `collector_async.py` module implements the poll loop using
`httpx.AsyncClient` and async SQLModel sessions. It exposes:

- `async def collect_once(cfg) -> CollectorHealth`
- `async def collect_forever(cfg, stop_event=None)`
- `class AsyncCollectorLock` (fcntl-based, same lockfile as legacy)
- `class CollectorHealth` dataclass with the same fields as legacy
  `CollectorState`: `last_poll_at, last_poll_ok, last_poll_error,
  inserted, duplicates, errors, dropped`

Behavior must be byte-identical to the legacy collector for the same input:
- HTTP auth: `Authorization: Bearer <management_key>`
- Batch URL: `GET {base_url}/v0/management/usage-queue?count={n}`
- Per-item isolation: one malformed record does not abort the batch
- Idempotency via `event_key` UNIQUE constraint
- `collector_state` persisted after every poll

The legacy `collector.py` stays the source of truth for `normalize_record`,
`_event_key`, `_account_hash`, `_safe_int` — `collector_async.py` **imports
those functions**, it does not duplicate them. This avoids behavior drift.

---

## 🔴 Mandatory TDD discipline

Red → Green → Refactor. Each test in `test_collector_async.py` is the
async equivalent of the corresponding test in `test_usage_dashboard.py`.
Do not delete or modify the legacy tests — they are the contract.

---

## 🪜 Steps

### Step 1 — Red: HTTP fetch + normalize + insert

```python
# tests/test_collector_async.py
import asyncio
import pytest
import httpx
from usage_dashboard import collector_async as ca
from usage_dashboard import collector as legacy
from usage_dashboard import storage as st


@pytest.mark.asyncio
async def test_fetch_usage_batch_returns_records(tmp_path, monkeypatch):
    cfg = {"cliproxy_base_url": "http://fake", "management_key": "k",
           "data_dir": str(tmp_path), "batch_size": 5}
    st.init_schema(cfg)

    async def fake_get(self, url, headers):
        return httpx.Response(200, json={"items": [{"request_id": "r1"}, {"request_id": "r2"}]})

    monkeypatch.setattr(httpx.AsyncClient, "get", fake_get)
    items = await ca.fetch_usage_batch(cfg, 5)
    assert len(items) == 2


@pytest.mark.asyncio
async def test_insert_usage_isolates_malformed(tmp_path):
    cfg = {"data_dir": str(tmp_path)}
    st.init_schema(cfg)
    good = {"request_id": "r1", "timestamp": "2026-01-01T00:00:00Z",
            "model": "m", "total_tokens": 100}
    bad = {"request_id": None, "timestamp": "not-a-date"}  # triggers ValueError
    inserted, duplicates, errors = await ca.insert_usage(cfg, [good, bad])
    assert inserted == 1
    assert errors == 1
    assert duplicates == 0


@pytest.mark.asyncio
async def test_collect_once_persists_state(tmp_path, monkeypatch):
    cfg = {"cliproxy_base_url": "http://fake", "management_key": "k",
           "data_dir": str(tmp_path), "batch_size": 5,
           "poll_interval_seconds": 1}
    st.init_schema(cfg)

    call_count = {"n": 0}
    async def fake_get(self, url, headers):
        call_count["n"] += 1
        if call_count["n"] == 1:
            return httpx.Response(200, json={"items": [{"request_id": "r1",
                "timestamp": "2026-01-01T00:00:00Z", "model": "m", "total_tokens": 100}]})
        return httpx.Response(200, json={"items": []})

    monkeypatch.setattr(httpx.AsyncClient, "get", fake_get)
    health = await ca.collect_once(cfg)
    assert health.inserted == 1
    assert health.last_poll_ok is True
    state = st.load_state(cfg)
    assert state["last_poll_ok"] == "true"
```

**Verify red**:
```bash
uv add httpx aiosqlite pytest-asyncio
uv run pytest tests/test_collector_async.py -v
```

Commit: `test(collector-async): red — fetch + insert + state`

### Step 2 — Green: implement collector_async.py

```python
# usage_dashboard/collector_async.py
"""Async collector: httpx + asyncio.Task. Imports normalization helpers
from collector.py to avoid drift."""
import asyncio
import contextlib
import fcntl
import logging
import os
from dataclasses import dataclass, asdict
from datetime import datetime, timezone
from typing import Optional

import httpx
from sqlmodel import Session, create_engine, select

from . import collector as legacy
from . import config as cfg_mod
from . import storage as st
from .models import UsageEvent, CollectorStateRow

log = logging.getLogger(__name__)
_utc = timezone.utc


@dataclass
class CollectorHealth:
    last_poll_at: Optional[str] = None
    last_poll_ok: bool = True
    last_poll_error: Optional[str] = None
    inserted: int = 0
    duplicates: int = 0
    errors: int = 0
    dropped: int = 0

    def to_state_dict(self) -> dict:
        d = asdict(self)
        d["last_poll_at"] = d["last_poll_at"] or ""
        d["last_poll_ok"] = "true" if d["last_poll_ok"] else "false"
        d["last_poll_error"] = d["last_poll_error"] or ""
        return {k: str(v) for k, v in d.items()}


async def fetch_usage_batch(cfg, count):
    url = f"{cfg['cliproxy_base_url']}/v0/management/usage-queue"
    headers = {"Authorization": f"Bearer {cfg['management_key']}"}
    async with httpx.AsyncClient(timeout=10) as client:
        resp = await client.get(url, headers=headers, params={"count": count})
        resp.raise_for_status()
        data = resp.json() or {}
    items = data.get("items") or []
    return [i for i in items if i]


async def insert_usage(cfg, items):
    inserted = duplicates = errors = 0
    engine = create_engine(f"sqlite:///{cfg_mod.db_path_for(cfg)}")
    with Session(engine) as session:
        for payload in items:
            try:
                normalized = legacy.normalize_record(payload)
                event = UsageEvent(**normalized)
                session.add(event)
                session.commit()
                inserted += 1
            except Exception as exc:
                session.rollback()
                msg = str(exc).lower()
                if "unique" in msg:
                    duplicates += 1
                else:
                    errors += 1
                    log.warning("skipping malformed record: %s", exc)
    engine.dispose()
    return inserted, duplicates, errors


async def collect_once(cfg) -> CollectorHealth:
    health = CollectorHealth()
    try:
        items = await fetch_usage_batch(cfg, int(cfg.get("batch_size") or 100))
        inserted, duplicates, errors = await insert_usage(cfg, items)
        health.inserted = inserted
        health.duplicates = duplicates
        health.errors = errors
        health.last_poll_ok = errors == 0
        health.last_poll_at = datetime.now(_utc).isoformat()
    except Exception as exc:
        health.last_poll_ok = False
        health.last_poll_error = legacy._redact_error(str(exc))
        log.error("collector poll failed: %s", exc)
        raise
    finally:
        st.save_state(cfg, health.to_state_dict())
    return health


async def collect_forever(cfg, stop_event=None):
    interval = max(1.0, float(cfg.get("poll_interval_seconds") or 2))
    while True:
        if stop_event and stop_event.is_set():
            return
        try:
            await collect_once(cfg)
        except Exception:
            pass  # already persisted in finally
        if stop_event:
            try:
                await asyncio.wait_for(stop_event.wait(), timeout=interval)
            except asyncio.TimeoutError:
                pass
        else:
            await asyncio.sleep(interval)


class AsyncCollectorLock:
    """fcntl-based exclusive lock — same lockfile as legacy CollectorLock."""
    def __init__(self, cfg):
        self.path = os.path.join(cfg_mod.data_dir_for(cfg), "collector.lock")

    @contextlib.asynccontextmanager
    async def __aenter__(self):
        cfg_mod.ensure_dirs({"data_dir": os.path.dirname(self.path)})
        self.fd = open(self.path, "w")
        try:
            fcntl.flock(self.fd.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            self.fd.close()
            raise RuntimeError("another collector already holds the lock")
        yield self

    async def __aexit__(self, exc_type, exc, tb):
        try:
            fcntl.flock(self.fd.fileno(), fcntl.LOCK_UN)
        finally:
            self.fd.close()
```

**Verify green**:
```bash
uv run pytest tests/test_collector_async.py -v
```

Commit: `feat(collector-async): httpx + asyncio.Task + fcntl lock — green`

### Step 3 — Refactor: behavior parity test vs legacy

```python
# tests/test_collector_async.py (append)
@pytest.mark.asyncio
async def test_parity_with_legacy_insert(tmp_path):
    """Same input → same rows in DB, byte-for-byte."""
    cfg = {"data_dir": str(tmp_path)}
    st.init_schema(cfg)
    payload = {"request_id": "r1", "timestamp": "2026-01-01T00:00:00Z",
               "model": "m", "total_tokens": 100, "input_tokens": 60,
               "output_tokens": 40}
    legacy.insert_usage(cfg, [payload])               # legacy sync
    await ca.insert_usage(cfg, [payload])             # async, should be dedup
    with st.db_connect(cfg) as conn:
        n = conn.execute("SELECT COUNT(*) FROM usage_events").fetchone()[0]
    assert n == 1  # dedup via event_key UNIQUE
```

**Verify refactor**:
```bash
uv run pytest tests/test_collector_async.py -v
uv run pytest test_usage_dashboard.py -v   # legacy still green
```

Commit: `test(collector-async): parity with legacy insert`

---

## ✅ Ticket completion gate

| # | Check | Command |
|---|-------|---------|
| 1 | Lint | `uv run ruff check usage_dashboard/collector_async.py tests/test_collector_async.py` |
| 2 | Type Check | `uv run mypy usage_dashboard/collector_async.py` |
| 3 | Build | `uv build` |
| 4 | Unit Tests | `uv run pytest tests/test_collector_async.py -v` |
| 5 | Integration Tests | Run legacy collector + async collector against the same fixture queue; assert identical DB state |
| 6 | Functional Tests | `collect_once` against a fake httpx server returns correct `(inserted, duplicates, errors)` |
| 7 | Contract Tests | Parity test: same payload to legacy `insert_usage` and async `insert_usage` produces identical rows |
| 8 | E2E | N/A |
| 9 | Code Review | Confirm `normalize_record`, `_event_key`, `_account_hash` are imported, not duplicated |

All green → Ticket 1.8.
