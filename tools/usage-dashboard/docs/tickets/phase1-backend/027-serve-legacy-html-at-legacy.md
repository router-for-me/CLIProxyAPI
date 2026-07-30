# Ticket 1.7 — Serve legacy dashboard at `/legacy`

**Phase**: 1 — Back end
**Blocks**: 1.8
**Blocked by**: 1.2
**Files touched**:
- `tools/usage-dashboard/usage_dashboard/api/legacy_html.py` (new)
- `tools/usage-dashboard/tests/test_api_legacy_html.py` (new)

**Files NOT touched**: `dashboard.html` (will be deleted only in Ticket 1.9)

---

## 🎯 Goal

FastAPI serves the legacy `dashboard.html` at `/legacy` and `/legacy/usage`
so the old UI remains usable during the React migration. After Phase 5
cutover, `/legacy` is removed and `/` serves the React app.

This ticket is the **bridge** that lets us delete `server.py` in Ticket 1.9
without losing the old UI for visual comparison.

---

## 🔴 Mandatory TDD discipline

Red → Green → Refactor.

---

## 🪜 Steps

### Step 1 — Red: legacy HTML test

```python
# tests/test_api_legacy_html.py
import os
from fastapi.testclient import TestClient
from usage_dashboard.api import app


def test_legacy_root_renders_html():
    with TestClient(app) as client:
        resp = client.get("/legacy")
        assert resp.status_code == 200
        assert "text/html" in resp.headers["content-type"]
        assert "<title>" in resp.text
        assert "CLIProxyAPI" in resp.text


def test_legacy_usage_renders_html():
    with TestClient(app) as client:
        resp = client.get("/legacy/usage")
        assert resp.status_code == 200
        assert "text/html" in resp.headers["content-type"]


def test_legacy_html_is_the_existing_dashboard_file():
    """Byte-equal to the existing usage_dashboard/dashboard.html."""
    here = os.path.dirname(os.path.abspath(__file__))
    expected = open(
        os.path.join(here, "..", "usage_dashboard", "dashboard.html"),
        encoding="utf-8",
    ).read()
    with TestClient(app) as client:
        resp = client.get("/legacy")
        assert resp.text == expected
```

**Verify red**:
```bash
uv run pytest tests/test_api_legacy_html.py -v
```

Commit: `test(api-legacy): red — /legacy serves dashboard.html`

### Step 2 — Green: static HTML response

```python
# usage_dashboard/api/legacy_html.py
import os
from fastapi import APIRouter
from fastapi.responses import HTMLResponse

router = APIRouter()

_HERE = os.path.dirname(os.path.abspath(__file__))
_DASHBOARD_HTML_PATH = os.path.join(_HERE, "..", "dashboard.html")


def _load_dashboard_html() -> str:
    with open(_DASHBOARD_HTML_PATH, encoding="utf-8") as f:
        return f.read()


DASHBOARD_HTML = _load_dashboard_html()


@router.get("/legacy", response_class=HTMLResponse)
def legacy_root():
    return DASHBOARD_HTML


@router.get("/legacy/usage", response_class=HTMLResponse)
def legacy_usage():
    # The legacy dashboard.html handles both routes client-side;
    # we serve the same HTML at both paths.
    return DASHBOARD_HTML
```

**Important**: The legacy HTML makes API calls to `/api/v1/...` paths. The
FastAPI routers in Tickets 1.4–1.6 serve those paths, so the legacy UI
will work transparently against FastAPI.

Wire in `api/__init__.py`:
```python
from . import legacy_html
app.include_router(legacy_html.router)
```

Also add `/legacy` and `/legacy/usage` to `_PUBLIC_PATHS` in the auth
middleware (Ticket 1.2).

**Verify green**:
```bash
uv run pytest tests/test_api_legacy_html.py -v
uv run pytest tests/ -v
```

Commit: `feat(api-legacy): serve dashboard.html at /legacy — green`

### Step 3 — Refactor: cache HTML in memory

The HTML is loaded once at import time (above). Confirm no per-request
file read happens by adding a test that mocks `open()` and asserts it is
not called during a request:

```python
def test_legacy_html_is_cached_not_read_per_request(monkeypatch):
    """After import, no file read should happen per request."""
    from usage_dashboard.api import legacy_html
    monkeypatch.setattr("builtins.open", lambda *a, **k: (_ for _ in ()).throw(AssertionError("per-request file read")))
    with TestClient(app) as client:
        resp = client.get("/legacy")
        assert resp.status_code == 200
```

Commit: `test(api-legacy): confirm HTML is cached at import time`

---

## ✅ Ticket completion gate

| # | Check | Command |
|---|-------|---------|
| 1 | Lint | `uv run ruff check usage_dashboard/api/legacy_html.py` |
| 2 | Type Check | `uv run mypy usage_dashboard/api/legacy_html.py` |
| 3 | Build | `uv build` |
| 4 | Unit Tests | `uv run pytest tests/test_api_legacy_html.py -v` |
| 5 | Integration Tests | Start uvicorn, open `http://localhost:8321/legacy` in a browser, confirm the old dashboard renders and API calls succeed |
| 6 | Functional Tests | `curl :8321/legacy` returns the full HTML |
| 7 | Contract Tests | Response body byte-equals `usage_dashboard/dashboard.html` |
| 8 | E2E | Manual: legacy dashboard at `/legacy` shows live data |
| 9 | Code Review | Confirm `/legacy` added to public paths (no auth required) |

All green → Ticket 1.8.
