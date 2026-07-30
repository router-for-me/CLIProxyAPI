# Ticket 1.4 — API routers (read-only endpoints)

**Phase**: 1 — Back end
**Blocks**: 1.5, 1.6
**Blocked by**: 1.2
**Files touched**:
- `tools/usage-dashboard/usage_dashboard/api/summary.py` (new)
- `tools/usage-dashboard/usage_dashboard/api/timeseries.py` (new)
- `tools/usage-dashboard/usage_dashboard/api/models.py` (new)
- `tools/usage-dashboard/usage_dashboard/api/accounts.py` (new)
- `tools/usage-dashboard/usage_dashboard/api/requests.py` (new)
- `tools/usage-dashboard/usage_dashboard/api/errors.py` (new)
- `tools/usage-dashboard/usage_dashboard/api/providers.py` (new)
- `tools/usage-dashboard/usage_dashboard/api/endpoints.py` (new)
- `tools/usage-dashboard/usage_dashboard/api/prices.py` (new)
- `tools/usage-dashboard/tests/test_api_readonly.py` (new)

**Files NOT touched**: `query.py`, `storage.py`

---

## 🎯 Goal

Every read-only JSON endpoint in the legacy server has a FastAPI equivalent
returning the **exact same response shape**. The route logic reuses the
existing `query.py` functions — no SQL is rewritten.

Endpoints in scope (all GET):

| Path | Query function |
|------|---------------|
| `/api/v1/summary` | `qy.query_summary` |
| `/api/v1/timeseries` | `qy.query_timeseries` |
| `/api/v1/models` | `qy.query_models` |
| `/api/v1/accounts` | `qy.query_accounts` |
| `/api/v1/requests` | `qy.query_requests` |
| `/api/v1/errors` | `qy.query_errors` |
| `/api/v1/providers` | `qy.query_providers` |
| `/api/v1/endpoints` | `qy.query_endpoints` |
| `/api/v1/prices` | `qy.query_prices` |
| `/api/v1/auth/check` | inline (`{auth_required, valid}`) |
| `/api/health` (legacy alias) | inline (`{ok, db_path}`) |
| `/api/summary` (legacy alias) | `qy.query_summary` (clamped limit) |
| `/api/requests` (legacy alias) | `qy.query_requests` (clamped limit) |

Mutations (PUT/DELETE) and `/api/v1/export` are out of scope — covered by
Tickets 1.5 and 1.6.

---

## 🔴 Mandatory TDD discipline

Red → Green → Refactor. The legacy `test_usage_dashboard.py` is the contract;
**do not modify it**. Each FastAPI router test asserts response equality
against the legacy response on a fixture DB.

---

## 🪜 Steps

### Step 1 — Red: parity tests against legacy

Create a fixture DB once and have both the legacy server and FastAPI serve
the same query against it. Use a shared conftest:

```python
# tests/conftest.py
import json
import os
import pytest
from fastapi.testclient import TestClient
from usage_dashboard import storage as st, collector as col, query as qy
from usage_dashboard.api import app


@pytest.fixture
def cfg_with_data(tmp_path):
    cfg = {"data_dir": str(tmp_path), "cliproxy_base_url": "http://fake",
           "management_key": "k", "default_limit": 100, "max_limit": 500}
    st.init_schema(cfg)
    events = [
        {"request_id": f"r{i}", "timestamp": f"2026-01-01T0{i}:00:00Z",
         "model": f"m{i%3}", "provider": "p", "endpoint": "e",
         "total_tokens": 100+i, "input_tokens": 60, "output_tokens": 40,
         "failed": 0, "latency_ms": 100} for i in range(10)
    ]
    col.insert_usage(cfg, events)
    return cfg


@pytest.fixture
def api_client(cfg_with_data):
    app.state.cfg = cfg_with_data
    with TestClient(app) as client:
        yield client
    app.state.cfg = None


@pytest.fixture
def legacy_response():
    """Helper: call legacy query function directly."""
    def _call(cfg, fn_name, qs):
        return getattr(qy, fn_name)(cfg, qs)
    return _call
```

```python
# tests/test_api_readonly.py
import pytest

ENDPOINTS = [
    ("/api/v1/summary",         "query_summary",    {"range": ["24h"]}),
    ("/api/v1/timeseries",      "query_timeseries", {"range": ["24h"], "group_by": ["model"]}),
    ("/api/v1/models",          "query_models",     {"range": ["24h"]}),
    ("/api/v1/accounts",        "query_accounts",   {"range": ["24h"]}),
    ("/api/v1/errors",          "query_errors",     {"range": ["24h"]}),
    ("/api/v1/providers",       "query_providers",  {"range": ["24h"]}),
    ("/api/v1/endpoints",       "query_endpoints",  {"range": ["24h"]}),
]


@pytest.mark.parametrize("path,fn,qs", ENDPOINTS)
def test_endpoint_matches_legacy(api_client, cfg_with_data, legacy_response, path, fn, qs):
    resp = api_client.get(path, params={k: v[0] for k, v in qs.items()})
    assert resp.status_code == 200
    fastapi_body = resp.json()
    legacy_body = legacy_response(cfg_with_data, fn, qs)
    assert fastapi_body == legacy_body, f"{path} mismatch"


def test_prices_endpoint(api_client):
    resp = api_client.get("/api/v1/prices")
    assert resp.status_code == 200
    assert "currency" in resp.json()


def test_requests_pagination(api_client):
    resp = api_client.get("/api/v1/requests", params={"range": "24h", "limit": 5})
    assert resp.status_code == 200
    body = resp.json()
    assert "requests" in body and "next_cursor" in body


def test_auth_check_no_token_required(api_client):
    app = api_client.app
    app.state.cfg["dashboard_token"] = ""
    resp = api_client.get("/api/v1/auth/check")
    assert resp.json() == {"auth_required": False, "valid": True}


def test_invalid_range_returns_400(api_client):
    resp = api_client.get("/api/v1/summary", params={"range": "bogus"})
    assert resp.status_code == 400
    assert "error" in resp.json()
```

**Verify red**:
```bash
uv run pytest tests/test_api_readonly.py -v
```

Commit: `test(api): red — read-only endpoint parity vs legacy`

### Step 2 — Green: implement routers

Each router follows the same pattern — call the existing `query.py`
function with FastAPI's parsed query params, wrapped in a
`HTTPException(400)` on `ValueError`:

```python
# usage_dashboard/api/summary.py
from fastapi import APIRouter, Request, HTTPException
from urllib.parse import parse_qs

from .. import query as qy

router = APIRouter()


@router.get("/api/v1/summary")
def summary(request: Request):
    cfg = request.app.state.cfg
    qs = parse_qs(request.url.query, keep_blank_values=True)
    try:
        return qy.query_summary(cfg, qs)
    except ValueError as exc:
        msg = str(exc)
        if any(kw in msg.lower() for kw in ("pricing", "interval", "negative", "rate")):
            raise HTTPException(500, "pricing configuration error")
        raise HTTPException(400, msg)
```

Create analogous routers for each endpoint. Wire all in `api/__init__.py`:

```python
from . import (summary, timeseries, models, accounts, requests, errors,
               providers, endpoints, prices)
app.include_router(summary.router)
app.include_router(timeseries.router)
# ... etc.
```

Special: `/api/v1/auth/check` and `/api/health` go in `health.py`:

```python
@router.get("/api/v1/auth/check")
def auth_check(request: Request):
    cfg = request.app.state.cfg or {}
    required = bool(cfg.get("dashboard_token"))
    valid = (not required) or _is_authorized(request, cfg)
    return {"auth_required": required, "valid": valid}


@router.get("/api/health")
def legacy_health(request: Request):
    cfg = request.app.state.cfg or {}
    from .. import config as cfg_mod
    return {"ok": True, "db_path": cfg_mod.db_path_for(cfg)}
```

Legacy aliases `/api/summary` and `/api/requests` clamp `limit` to
`max_limit(cfg, requested)`:

```python
@router.get("/api/requests")
def legacy_requests(request: Request):
    cfg = request.app.state.cfg
    qs = parse_qs(request.url.query, keep_blank_values=True)
    legacy_qs = {"limit": [str(qy.max_limit(cfg, qy._first(qs, "limit")))]}
    legacy_qs.update({k: v for k, v in qs.items() if k in ("range", "from", "to", "model")})
    return qy.query_requests(cfg, legacy_qs)
```

**Verify green**:
```bash
uv run pytest tests/test_api_readonly.py -v
uv run pytest test_usage_dashboard.py -v   # legacy suite untouched, still green
```

Commit: `feat(api): read-only routers — green (parity vs legacy)`

### Step 3 — Refactor: shared error handler

Extract the `ValueError → HTTPException` mapping into a decorator so every
router is identical except for the query function call:

```python
# usage_dashboard/api/_errors.py
import functools
from fastapi import HTTPException


def map_query_errors(fn):
    @functools.wraps(fn)
    def wrapper(*args, **kwargs):
        try:
            return fn(*args, **kwargs)
        except ValueError as exc:
            msg = str(exc)
            if any(kw in msg.lower() for kw in ("pricing", "interval", "negative", "rate")):
                raise HTTPException(500, "pricing configuration error")
            raise HTTPException(400, msg)
    return wrapper
```

Apply to every router. Verify all tests still pass.

Commit: `refactor(api): shared error decorator`

---

## ✅ Ticket completion gate

| # | Check | Command |
|---|-------|---------|
| 1 | Lint | `uv run ruff check usage_dashboard/api tests/test_api_readonly.py` |
| 2 | Type Check | `uv run mypy usage_dashboard/api` |
| 3 | Build | `uv build` |
| 4 | Unit Tests | `uv run pytest tests/test_api_readonly.py -v` (parametrized parity tests) |
| 5 | Integration Tests | `uv run pytest tests/ -v` |
| 6 | Functional Tests | Start `uvicorn usage_dashboard.api:app`, `curl` each endpoint, all 200 |
| 7 | Contract Tests | Each endpoint's JSON body equals legacy `query.py` output (Step 1 tests) |
| 8 | E2E | N/A (no UI yet) |
| 9 | Code Review | Confirm `query.py` is unchanged; routers are thin pass-throughs |

All green → Tickets 1.5 and 1.6.
