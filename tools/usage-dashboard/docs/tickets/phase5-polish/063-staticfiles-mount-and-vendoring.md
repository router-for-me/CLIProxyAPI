# Ticket 5.3 — FastAPI StaticFiles mount + vendored dist

**Phase**: 5 — Polish
**Blocks**: 5.4
**Blocked by**: Phase 4 complete
**Files touched**:
- `tools/usage-dashboard/usage_dashboard/api/__init__.py` (add StaticFiles mount)
- `tools/usage-dashboard/.gitignore` (force-add `frontend/dist/`)
- `tools/usage-dashboard/frontend/dist/**` (committed on release — Phase 5 is the first release)
- `tools/usage-dashboard/Makefile` (`make release` target)
- `tools/usage-dashboard/tests/test_static_mount.py` (new)

---

## 🎯 Goal

FastAPI serves the built React app at `/` via
`app.mount("/", StaticFiles(directory=frontend_dist, html=True))`. Hashed
asset filenames get immutable cache headers; `index.html` gets
`no-cache`.

`frontend/dist/` is committed to Git at release time, so a fresh
`git clone && uv sync && python3 usage_dashboard.py run` works with no
Node installed.

The `/legacy` route continues to serve the old `dashboard.html` until
Ticket 5.4 deletes it.

---

## 🔴 Mandatory TDD discipline

Red → Green → Refactor.

---

## 🪜 Steps

### Step 1 — Red: static mount test

```python
# tests/test_static_mount.py
from pathlib import Path
import subprocess
import sys

import pytest
from fastapi.testclient import TestClient


def test_root_serves_index_html():
    """When frontend/dist exists, GET / returns the built index.html."""
    from usage_dashboard.api import app
    dist = Path(__file__).resolve().parents[1] / "frontend" / "dist"
    if not dist.exists():
        pytest.skip("frontend/dist not built yet")
    with TestClient(app) as client:
        resp = client.get("/")
        assert resp.status_code == 200
        assert "text/html" in resp.headers["content-type"]
        assert "<div id=\"root\">" in resp.text or "root" in resp.text


def test_hashed_assets_have_immutable_cache_header():
    from usage_dashboard.api import app
    dist = Path(__file__).resolve().parents[1] / "frontend" / "dist" / "assets"
    if not dist.exists():
        pytest.skip("no built assets")
    asset = next(dist.glob("*.js"))
    with TestClient(app) as client:
        resp = client.get(f"/assets/{asset.name}")
        assert resp.status_code == 200
        assert "max-age=31536000" in resp.headers.get("cache-control", "")
        assert "immutable" in resp.headers.get("cache-control", "")


def test_index_html_is_no_cache():
    from usage_dashboard.api import app
    dist = Path(__file__).resolve().parents[1] / "frontend" / "dist"
    if not dist.exists():
        pytest.skip("frontend/dist not built yet")
    with TestClient(app) as client:
        resp = client.get("/")
        cc = resp.headers.get("cache-control", "")
        assert "no-cache" in cc or "max-age=0" in cc
```

Commit: `test(static-mount): red — root serves dist + cache headers`

### Step 2 — Green: add StaticFiles mount

```python
# Append to usage_dashboard/api/__init__.py
import os
from fastapi.staticfiles import StaticFiles

_HERE = os.path.dirname(os.path.abspath(__file__))
_FRONTEND_DIST = os.path.normpath(os.path.join(_HERE, "..", "..", "frontend", "dist"))


class ImmutableCacheStaticFiles(StaticFiles):
    async def get_response(self, path, scope):
        resp = await super().get_response(path, scope)
        if path != "" and path != "index.html":
            resp.headers["Cache-Control"] = "public, max-age=31536000, immutable"
        else:
            resp.headers["Cache-Control"] = "no-cache"
        return resp


if os.path.isdir(_FRONTEND_DIST):
    app.mount("/", ImmutableCacheStaticFiles(directory=_FRONTEND_DIST, html=True),
              name="frontend")
    # Note: this is mounted LAST so /api/* and /legacy routes win.
else:
    log.warning("frontend/dist not built; serving API only. Run `pnpm build`.")
```

Important: the mount is at the end of `api/__init__.py`, after all routers
are registered. FastAPI evaluates routes in registration order, so `/api/...`
and `/legacy` match before the catch-all `/`.

Build and commit:
```bash
cd tools/usage-dashboard
make build-frontend
# Update .gitignore: keep frontend/dist ignored for dev, but force-add it
# on release.
git add -f frontend/dist
git commit -m "release(frontend): vendored dist for zero-Node deployment"
```

Add `make release` target:
```makefile
release: build-frontend
	git add -f frontend/dist
	@echo "staged frontend/dist for release commit"
```

**Verify green**:
```bash
uv run pytest tests/test_static_mount.py -v
uv run python usage_dashboard.py serve &
curl http://127.0.0.1:8320/  # → React index.html
kill %1
```

Commit: `feat(static-mount): serve React dist with cache headers — green`

### Step 3 — Refactor: graceful 404 for SPA routes

When a browser deep-links to `/usage`, the StaticFiles handler returns 404
because there is no such file. Fix by adding a catch-all that returns
`index.html` for unknown non-API routes:

```python
# In api/__init__.py, before the StaticFiles mount:
from fastapi.responses import FileResponse

@app.get("/{full_path:path}", include_in_schema=False)
async def spa_fallback(full_path: str):
    if full_path.startswith(("api/", "static/", "legacy")):
        raise HTTPException(404)
    index = os.path.join(_FRONTEND_DIST, "index.html")
    if os.path.isfile(index):
        return FileResponse(index, media_type="text/html")
    raise HTTPException(404)
```

Test deep-linking:
```python
def test_usage_deep_link_serves_index():
    from usage_dashboard.api import app
    dist = Path(__file__).resolve().parents[1] / "frontend" / "dist"
    if not dist.exists():
        pytest.skip()
    with TestClient(app) as client:
        resp = client.get("/usage")
        assert resp.status_code == 200
        assert "text/html" in resp.headers["content-type"]
```

Commit: `feat(static-mount): SPA fallback for deep links`

---

## ✅ Ticket completion gate

| # | Check | Command |
|---|-------|---------|
| 1 | Lint | `uv run ruff check usage_dashboard/api` |
| 2 | Type Check | `uv run mypy usage_dashboard/api` |
| 3 | Build | `make build-frontend` (dist committed) |
| 4 | Unit Tests | `uv run pytest tests/test_static_mount.py -v` |
| 5 | Integration Tests | `python usage_dashboard.py serve`; browser opens `/` → React app; `/usage` deep link works |
| 6 | Functional Tests | Without Node installed, `git clone && uv sync && python3 usage_dashboard.py run` works |
| 7 | Contract Tests | `/api/v1/*` still returns JSON; `/` returns HTML; no route collision |
| 8 | E2E | Manual: open `/` and `/usage` directly (deep-link), confirm React Router picks up |
| 9 | Code Review | `frontend/dist/` is committed at release only; dev workflow still uses Vite |

All green → Ticket 5.4.
