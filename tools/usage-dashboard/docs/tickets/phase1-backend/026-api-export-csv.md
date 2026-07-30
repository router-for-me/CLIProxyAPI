# Ticket 1.6 — CSV export endpoint

**Phase**: 1 — Back end
**Blocks**: 1.8
**Blocked by**: 1.4
**Files touched**:
- `tools/usage-dashboard/usage_dashboard/api/export.py` (new)
- `tools/usage-dashboard/tests/test_api_export.py` (new)

**Files NOT touched**: `query.py`, `pricing.py`

---

## 🎯 Goal

`GET /api/v1/export` streams a CSV identical to the legacy endpoint. It:

1. Calls `qy.query_requests(cfg, qs, no_limit=True)` to fetch all rows.
2. For each row, computes `estimated_cost` using `pricing.price_for`.
3. Returns CSV with headers `Content-Type: text/csv` and
   `Content-Disposition: attachment; filename="usage_export.csv"`.

The CSV column order must match the legacy endpoint exactly.

---

## 🔴 Mandatory TDD discipline

Red → Green → Refactor.

---

## 🪜 Steps

### Step 1 — Red: CSV parity test

```python
# tests/test_api_export.py
import csv
import io
import pytest
from fastapi.testclient import TestClient
from usage_dashboard.api import app


@pytest.fixture
def client(cfg_with_data):
    app.state.cfg = cfg_with_data
    with TestClient(app) as c:
        yield c
    app.state.cfg = None


def _parse_csv(text: str):
    reader = csv.DictReader(io.StringIO(text))
    return list(reader)


def test_export_returns_csv(client):
    resp = client.get("/api/v1/export", params={"range": "24h"})
    assert resp.status_code == 200
    assert resp.headers["content-type"].startswith("text/csv")
    assert "attachment" in resp.headers.get("content-disposition", "")
    rows = _parse_csv(resp.text)
    assert len(rows) > 0
    # Every row has the canonical columns
    for row in rows:
        assert "request_id" in row
        assert "model" in row
        assert "total_tokens" in row
        assert "estimated_cost" in row


def test_export_matches_legacy_query_shape(client, cfg_with_data, legacy_response):
    """First row of CSV matches legacy query_requests row (same column names)."""
    resp = client.get("/api/v1/export", params={"range": "24h"})
    csv_rows = _parse_csv(resp.text)
    legacy = legacy_response(cfg_with_data, "query_requests",
                             {"range": ["24h"], "limit": ["500"]})
    legacy_rows = legacy["requests"]
    assert len(csv_rows) == len(legacy_rows)
    # Spot-check first row
    for k in ("request_id", "model", "total_tokens"):
        assert csv_rows[0][k] == str(legacy_rows[0][k])


def test_export_handles_empty_range(client, cfg_with_data):
    """If no data in range, CSV has headers only."""
    resp = client.get("/api/v1/export", params={"range": "1h"})
    assert resp.status_code == 200
    rows = _parse_csv(resp.text)
    # May be empty list but headers present
    assert isinstance(rows, list)
```

**Verify red**:
```bash
uv run pytest tests/test_api_export.py -v
```

Commit: `test(api-export): red — CSV shape + parity`

### Step 2 — Green: implement export router

```python
# usage_dashboard/api/export.py
import csv
import io
import datetime as dt
from fastapi import APIRouter, Request, HTTPException
from fastapi.responses import StreamingResponse
from urllib.parse import parse_qs

from .. import query as qy
from .. import pricing as pr

router = APIRouter()


def _compute_estimated_cost(cfg, row):
    ts_str = row.get("timestamp", "")
    ts = 0
    if ts_str:
        try:
            ts = dt.datetime.fromisoformat(ts_str.replace("Z", "+00:00")).timestamp()
        except Exception:
            ts = 0
    pricing_cfg = pr.load_pricing(cfg)
    iv = pr.price_for(pricing_cfg, row.get("model", ""), ts)
    if not iv:
        return 0
    return (
        (row.get("input_tokens", 0) or 0) * float(iv.get("input_per_million", 0)) / 1_000_000
        + (row.get("output_tokens", 0) or 0) * float(iv.get("output_per_million", 0)) / 1_000_000
        + (row.get("cached_tokens", 0) or 0) * float(iv.get("cached_input_per_million", 0)) / 1_000_000
        + (row.get("reasoning_tokens", 0) or 0) * float(iv.get("reasoning_per_million", 0)) / 1_000_000
    )


CSV_COLUMNS = [
    "timestamp", "account_hash", "provider", "model", "endpoint",
    "request_id", "input_tokens", "output_tokens", "reasoning_tokens",
    "cached_tokens", "cache_read_tokens", "cache_creation_tokens",
    "total_tokens", "latency_ms", "ttft_ms", "failed", "fail_status",
    "estimated_cost",
]


@router.get("/api/v1/export")
def export(request: Request):
    cfg = request.app.state.cfg
    qs = parse_qs(request.url.query, keep_blank_values=True)
    try:
        data = qy.query_requests(cfg, qs, no_limit=True)
    except ValueError as exc:
        raise HTTPException(400, str(exc))
    rows = data.get("requests", [])
    for r in rows:
        r["estimated_cost"] = _compute_estimated_cost(cfg, r)

    buf = io.StringIO()
    writer = csv.DictWriter(buf, fieldnames=CSV_COLUMNS, extrasaction="ignore")
    writer.writeheader()
    for r in rows:
        writer.writerow(r)
    buf.seek(0)

    return StreamingResponse(
        iter([buf.getvalue()]),
        media_type="text/csv",
        headers={"Content-Disposition": "attachment; filename=usage_export.csv"},
    )
```

Wire in `api/__init__.py`:
```python
from . import export
app.include_router(export.router)
```

**Verify green**:
```bash
uv run pytest tests/test_api_export.py -v
```

Commit: `feat(api-export): CSV router — green`

### Step 3 — Refactor: streaming for large ranges

For ranges that produce > 10,000 rows, generate the CSV in chunks instead
of building the whole string in memory:

```python
async def _row_generator(cfg, rows):
    buf = io.StringIO()
    writer = csv.DictWriter(buf, fieldnames=CSV_COLUMNS, extrasaction="ignore")
    writer.writeheader()
    yield buf.getvalue()
    buf.seek(0); buf.truncate()
    for r in rows:
        writer.writerow(r)
        yield buf.getvalue()
        buf.seek(0); buf.truncate()


@router.get("/api/v1/export")
async def export_stream(request: Request):
    # ... same fetch logic ...
    return StreamingResponse(_row_generator(cfg, rows), media_type="text/csv", ...)
```

Add a test with a large fixture (> 1000 rows) to confirm streaming does not
OOM.

Commit: `refactor(api-export): streaming generator for large ranges`

---

## ✅ Ticket completion gate

| # | Check | Command |
|---|-------|---------|
| 1 | Lint | `uv run ruff check usage_dashboard/api/export.py tests/test_api_export.py` |
| 2 | Type Check | `uv run mypy usage_dashboard/api/export.py` |
| 3 | Build | `uv build` |
| 4 | Unit Tests | `uv run pytest tests/test_api_export.py -v` |
| 5 | Integration Tests | Export 100-row fixture, compare row-by-row with legacy |
| 6 | Functional Tests | `curl :8320/api/v1/export?range=24h -o out.csv` produces valid CSV |
| 7 | Contract Tests | CSV headers and first-row values match legacy query_requests |
| 8 | E2E | N/A |
| 9 | Code Review | Confirm CSV column order is documented and matches legacy |

All green → Ticket 1.8.
