# Ticket 1.2 — FastAPI app skeleton with lifespan

**Phase**: 1 — Back end
**Blocks**: 1.3, 1.4, 1.7, 1.8
**Blocked by**: 1.1
**Files touched**:
- `tools/usage-dashboard/usage_dashboard/api/__init__.py` (new)
- `tools/usage-dashboard/usage_dashboard/api/health.py` (new)
- `tools/usage-dashboard/tests/test_api_health.py` (new)

**Files NOT touched**: legacy `server.py`, `collector.py`

---

## 🎯 Goal

A FastAPI app exists with a `lifespan` context manager (startup/shutdown
hooks) and a single health endpoint. The lifespan starts and stops a
collector task stub (real collector lands in Ticket 1.3). The legacy
`server.py` keeps running unchanged; the FastAPI app is importable but
not yet wired into CLI.

This ticket is deliberately minimal: it only proves the FastAPI skeleton
boots. Adding routes happens in 1.4–1.6.

---

## 🔴 Mandatory TDD discipline

Red → Green → Refactor. Use FastAPI's `TestClient` (built on httpx) for
HTTP tests.

---

## 🪜 Steps

### Step 1 — Red: health endpoint test

```python
# tests/test_api_health.py
from fastapi.testclient import TestClient

from usage_dashboard.api import app, collector_state


def test_health_returns_ok():
    with TestClient(app) as client:
        resp = client.get("/api/v1/health")
        assert resp.status_code == 200
        body = resp.json()
        assert "last_poll_at" in body
        assert "last_poll_ok" in body


def test_lifespan_starts_and_stops_collector_stub():
    """When the app starts, a collector task is registered as running;
    when it shuts down, the task is cancelled."""
    with TestClient(app) as client:
        assert collector_state["task"] is not None
        assert not collector_state["task"].done()
    assert collector_state["task"].done() or collector_state["task"].cancelled()
```

**Verify red**:
```bash
uv add fastapi uvicorn
uv run pytest tests/test_api_health.py -v
```
Fails: `usage_dashboard.api` does not exist.

Commit: `test(api): red — health + lifespan collector stub`

### Step 2 — Green: FastAPI app

```python
# usage_dashboard/api/__init__.py
"""FastAPI app for the usage dashboard.

This module is the single source of truth for the app instance and its
lifespan. Routers are registered here; route handlers live in per-resource
modules (added in subsequent tickets).
"""
from contextlib import asynccontextmanager
import asyncio
import logging

from fastapi import FastAPI

log = logging.getLogger(__name__)

# Observability handle for tests; the real collector task is created in
# Ticket 1.3 and stored here.
collector_state: dict = {"task": None}


async def _collector_stub():
    """Placeholder collector loop. Replaced in Ticket 1.3."""
    while True:
        await asyncio.sleep(1)


@asynccontextmanager
async def lifespan(app: FastAPI):
    log.info("usage-dashboard FastAPI starting")
    task = asyncio.create_task(_collector_stub(), name="collector-stub")
    collector_state["task"] = task
    try:
        yield
    finally:
        task.cancel()
        try:
            await task
        except asyncio.CancelledError:
            pass
        collector_state["task"] = None
        log.info("usage-dashboard FastAPI stopped")


app = FastAPI(
    title="CLIProxyAPI Usage Dashboard",
    version="0.1.0",
    lifespan=lifespan,
)
```

```python
# usage_dashboard/api/health.py
from fastapi import APIRouter

router = APIRouter()


@router.get("/api/v1/health")
def health():
    return {"last_poll_at": None, "last_poll_ok": True}
```

Wire the router in `api/__init__.py`:
```python
from . import health
app.include_router(health.router)
```

**Verify green**:
```bash
uv run pytest tests/test_api_health.py -v
```

Commit: `feat(api): FastAPI skeleton with lifespan + health — green`

### Step 3 — Refactor: auth gate middleware

Add an HTTP middleware that mirrors the legacy auth gate: requests to
non-public paths require loopback OR a valid `X-Dashboard-Token`:

```python
# Append to api/__init__.py
from fastapi import Request
from fastapi.responses import JSONResponse

_PUBLIC_PATHS = {"/", "/legacy", "/index.html", "/login",
                 "/api/v1/auth/check", "/static/chart.js"}


def _is_loopback(host: str) -> bool:
    return host in ("127.0.0.1", "::1", "localhost")


@app.middleware("http")
async def auth_gate(request: Request, call_next):
    cfg = request.app.state.cfg  # populated in Ticket 1.8 CLI cutover
    if cfg is None:  # test mode — allow
        return await call_next(request)
    path = request.url.path
    if path in _PUBLIC_PATHS:
        return await call_next(request)
    host = cfg.get("dashboard_host", "127.0.0.1")
    if not _is_loopback(host) and not cfg.get("dashboard_token"):
        return JSONResponse({"error": "non-loopback bind requires dashboard_token"}, 503)
    token = cfg.get("dashboard_token") or ""
    if token and request.headers.get("X-Dashboard-Token", "").strip() != token:
        return JSONResponse({"error": "unauthorized"}, 401)
    return await call_next(request)


@app.on_event("startup")
async def _attach_state():
    # In production this is set by the CLI before uvicorn.run().
    # In tests it stays None, which the middleware interprets as "allow".
    if not hasattr(app.state, "cfg"):
        app.state.cfg = None
```

Add a test for the auth gate in `tests/test_api_health.py`:

```python
def test_auth_gate_blocks_non_loopback_without_token():
    app.state.cfg = {"dashboard_host": "0.0.0.0", "dashboard_token": ""}
    try:
        with TestClient(app) as client:
            resp = client.get("/api/v1/health")
            assert resp.status_code == 503
    finally:
        app.state.cfg = None
```

**Verify refactor**:
```bash
uv run pytest tests/test_api_health.py -v
```

Commit: `feat(api): auth gate middleware (mirrors legacy _gate)`

---

## ✅ Ticket completion gate

| # | Check | Command |
|---|-------|---------|
| 1 | Lint | `uv run ruff check usage_dashboard/api tests/test_api_health.py` |
| 2 | Type Check | `uv run mypy usage_dashboard/api` |
| 3 | Build | `uv build` |
| 4 | Unit Tests | `uv run pytest tests/test_api_health.py -v` |
| 5 | Integration Tests | `uv run pytest tests/ -v` |
| 6 | Functional Tests | `uv run uvicorn usage_dashboard.api:app --port 8321` then `curl :8321/api/v1/health` returns 200 |
| 7 | Contract Tests | `/api/v1/health` shape matches legacy: `{last_poll_at, last_poll_ok}` keys |
| 8 | E2E | N/A |
| 9 | Code Review | Confirm `server.py` is untouched; lifespan correctly cancels task |

All green → Tickets 1.3, 1.4, 1.7 can start (in parallel where independent).
