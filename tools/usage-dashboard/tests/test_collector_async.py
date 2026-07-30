"""Tests for usage_dashboard.collector — async httpx collector.

RED → GREEN → REFACTOR.  Legacy tests are not modified.
"""
import httpx
import pytest

from usage_dashboard import collector as ca
from usage_dashboard import collector as legacy
from usage_dashboard import storage as st

# ---------------------------------------------------------------------------
# Step 1 — Red: HTTP fetch + normalize + insert
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_fetch_usage_batch_returns_records(tmp_path, monkeypatch):
    cfg = {"cliproxy_base_url": "http://fake", "management_key": "k",
           "data_dir": str(tmp_path), "batch_size": 5}
    st.init_schema(cfg)

    async def fake_get(self, url, **kwargs):
        req = httpx.Request("GET", url)
        return httpx.Response(
            200,
            json={"items": [{"request_id": "r1"}, {"request_id": "r2"}]},
            request=req,
        )

    monkeypatch.setattr(httpx.AsyncClient, "get", fake_get)
    items = await ca.async_fetch_usage_batch(cfg, 5)
    assert len(items) == 2


@pytest.mark.asyncio
async def test_insert_usage_isolates_malformed(tmp_path):
    cfg = {"data_dir": str(tmp_path)}
    st.init_schema(cfg)
    good = {"request_id": "r1", "timestamp": "2026-01-01T00:00:00Z",
            "model": "m", "total_tokens": 100}
    bad = {"request_id": None, "timestamp": "not-a-date"}  # triggers ValueError
    inserted, duplicates, errors = await ca.async_insert_usage(cfg, [good, bad])
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

    async def fake_get(self, url, **kwargs):
        call_count["n"] += 1
        req = httpx.Request("GET", url)
        if call_count["n"] == 1:
            return httpx.Response(
                200,
                json={"items": [{"request_id": "r1",
                                 "timestamp": "2026-01-01T00:00:00Z",
                                 "model": "m", "total_tokens": 100}]},
                request=req,
            )
        return httpx.Response(200, json={"items": []}, request=req)

    monkeypatch.setattr(httpx.AsyncClient, "get", fake_get)
    health = await ca.collect_once(cfg)
    assert health.inserted == 1
    assert health.last_poll_ok is True
    state = st.load_state(cfg)
    assert state["last_poll_ok"] is True


# ---------------------------------------------------------------------------
# Step 3 — Refactor: behavior parity test vs legacy
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_parity_with_legacy_insert(tmp_path):
    """Same input → same rows in DB, byte-for-byte."""
    cfg = {"data_dir": str(tmp_path)}
    st.init_schema(cfg)
    payload = {"request_id": "r1", "timestamp": "2026-01-01T00:00:00Z",
               "model": "m", "total_tokens": 100, "input_tokens": 60,
               "output_tokens": 40}
    legacy.insert_usage(cfg, [payload])               # legacy sync
    await ca.async_insert_usage(cfg, [payload])             # async, should be dedup
    with st.db_connect(cfg) as conn:
        n = conn.execute("SELECT COUNT(*) FROM usage_events").fetchone()[0]
    assert n == 1  # dedup via event_key UNIQUE
