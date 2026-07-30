# Ticket 1.5 — Aliases mutation endpoints (PUT / DELETE)

**Phase**: 1 — Back end
**Blocks**: 1.8
**Blocked by**: 1.4
**Files touched**:
- `tools/usage-dashboard/usage_dashboard/api/aliases.py` (new)
- `tools/usage-dashboard/tests/test_api_aliases.py` (new)

**Files NOT touched**: `storage.py` (alias CRUD functions `get_aliases`,
`upsert_alias`, `delete_alias` already exist)

---

## 🎯 Goal

FastAPI routers for the three alias mutation endpoints, returning the same
response shape as the legacy server:

| Method | Path | Body | Response |
|--------|------|------|----------|
| GET | `/api/v1/aliases` | — | `[{"account_hash": ..., "alias": ...}, ...]` |
| PUT | `/api/v1/aliases` | `{"account_hash": ..., "alias": ...}` | `{"ok": true}` |
| DELETE | `/api/v1/aliases/:hash` | — | `{"ok": true}` |

Error contract (same as legacy):
- Missing `account_hash` or `alias` on PUT → 400 `{"error": "account_hash and alias required"}`
- Invalid JSON → 400 `{"error": "invalid JSON"}`
- Auth gate still applies ( Ticket 1.2 middleware)

---

## 🔴 Mandatory TDD discipline

Red → Green → Refactor. The tests use the FastAPI `TestClient` with a
fixture DB; the legacy tests in `test_usage_dashboard.py` covering aliases
must continue to pass unchanged.

---

## 🪜 Steps

### Step 1 — Red: parity tests

```python
# tests/test_api_aliases.py
import pytest
from fastapi.testclient import TestClient
from usage_dashboard.api import app
from usage_dashboard import storage as st


@pytest.fixture
def client(cfg_with_data):
    app.state.cfg = cfg_with_data
    with TestClient(app) as c:
        yield c
    app.state.cfg = None


def test_get_aliases_empty_initially(client):
    resp = client.get("/api/v1/aliases")
    assert resp.status_code == 200
    assert resp.json() == []


def test_put_alias_creates(client, cfg_with_data):
    resp = client.put("/api/v1/aliases", json={"account_hash": "abc123", "alias": "Alice"})
    assert resp.status_code == 200
    assert resp.json() == {"ok": True}
    aliases = st.get_aliases(cfg_with_data)
    assert aliases == [{"account_hash": "abc123", "alias": "Alice"}]


def test_put_alias_updates_existing(client, cfg_with_data):
    client.put("/api/v1/aliases", json={"account_hash": "abc", "alias": "A"})
    client.put("/api/v1/aliases", json={"account_hash": "abc", "alias": "B"})
    aliases = st.get_aliases(cfg_with_data)
    assert len(aliases) == 1
    assert aliases[0]["alias"] == "B"


def test_put_alias_missing_fields_returns_400(client):
    resp = client.put("/api/v1/aliases", json={"account_hash": "", "alias": ""})
    assert resp.status_code == 400
    assert resp.json() == {"detail": "account_hash and alias required"}


def test_put_alias_invalid_json_returns_400(client):
    resp = client.put("/api/v1/aliases", content=b"not json",
                      headers={"Content-Type": "application/json"})
    assert resp.status_code == 400


def test_delete_alias(client, cfg_with_data):
    client.put("/api/v1/aliases", json={"account_hash": "abc", "alias": "A"})
    resp = client.delete("/api/v1/aliases/abc")
    assert resp.status_code == 200
    assert resp.json() == {"ok": True}
    assert st.get_aliases(cfg_with_data) == []


def test_delete_nonexistent_alias_is_idempotent(client):
    resp = client.delete("/api/v1/aliases/nonexistent")
    assert resp.status_code == 200
    assert resp.json() == {"ok": True}
```

**Verify red**:
```bash
uv run pytest tests/test_api_aliases.py -v
```

Commit: `test(api-aliases): red — PUT/DELETE/GET parity vs legacy`

### Step 2 — Green: implement aliases router

```python
# usage_dashboard/api/aliases.py
from fastapi import APIRouter, Request, HTTPException
from pydantic import BaseModel

from .. import storage as st

router = APIRouter()


class AliasBody(BaseModel):
    account_hash: str
    alias: str


@router.get("/api/v1/aliases")
def list_aliases(request: Request):
    cfg = request.app.state.cfg
    return st.get_aliases(cfg)


@router.put("/api/v1/aliases")
def upsert_alias(body: AliasBody, request: Request):
    cfg = request.app.state.cfg
    if not body.account_hash.strip() or not body.alias.strip():
        raise HTTPException(400, "account_hash and alias required")
    st.upsert_alias(cfg, body.account_hash.strip(), body.alias.strip())
    return {"ok": True}


@router.delete("/api/v1/aliases/{account_hash}")
def delete_alias(account_hash: str, request: Request):
    cfg = request.app.state.cfg
    if not account_hash:
        raise HTTPException(400, "account_hash required")
    st.delete_alias(cfg, account_hash)
    return {"ok": True}
```

Wire in `api/__init__.py`:
```python
from . import aliases
app.include_router(aliases.router)
```

Note on FastAPI error shape: by default FastAPI returns
`{"detail": "..."}` for `HTTPException`. Legacy returned `{"error": "..."}`.
Decide which shape is canonical — ADR 0001 says "FastAPI response models =
SQLModel schemas", so the **FastAPI shape wins**. Update the test
accordingly:

```python
assert resp.json() == {"detail": "account_hash and alias required"}
```

This is a **deliberate contract divergence**; record it in
`docs/adr/0007-error-shape-divergence.md` (new ADR — create in this step):

```markdown
# ADR 0007: Error response shape — FastAPI default

**Status**: Accepted  **Date**: 2026-07-27

FastAPI returns `{"detail": "..."}` for HTTPException by default; the legacy
server returned `{"error": "..."}`. We adopt the FastAPI shape as canonical.
The front end (Phase 3+) reads `detail`. The legacy HTML at `/legacy` (until
deleted in Phase 5) is unaffected because it does not exercise error paths.
```

**Verify green**:
```bash
uv run pytest tests/test_api_aliases.py -v
```

Commit: `feat(api-aliases): PUT/DELETE/GET routers + ADR 0007 — green`

### Step 3 — Refactor: validate account_hash length

Add a length check to prevent bogus data:

```python
if len(body.account_hash) > 128 or len(body.alias) > 256:
    raise HTTPException(400, "account_hash or alias too long")
```

Add a test for the length limit.

Commit: `feat(api-aliases): length validation`

---

## ✅ Ticket completion gate

| # | Check | Command |
|---|-------|---------|
| 1 | Lint | `uv run ruff check usage_dashboard/api/aliases.py tests/test_api_aliases.py` |
| 2 | Type Check | `uv run mypy usage_dashboard/api/aliases.py` |
| 3 | Build | `uv build` |
| 4 | Unit Tests | `uv run pytest tests/test_api_aliases.py -v` |
| 5 | Integration Tests | `uv run pytest tests/ -v` (full suite) |
| 6 | Functional Tests | Manual `curl` PUT/GET/DELETE cycle round-trips |
| 7 | Contract Tests | Legacy aliases tests in `test_usage_dashboard.py` still pass |
| 8 | E2E | N/A |
| 9 | Code Review | Confirm ADR 0007 captures the error-shape divergence |

All green → Ticket 1.8.
