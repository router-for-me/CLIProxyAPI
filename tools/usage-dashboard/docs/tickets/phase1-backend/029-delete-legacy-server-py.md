# Ticket 1.9 — Delete legacy `server.py`

**Phase**: 1 — Back end
**Blocks**: Phase 2
**Blocked by**: 1.8
**Files removed**:
- `tools/usage-dashboard/usage_dashboard/server.py`

**Files touched**:
- `tools/usage-dashboard/usage_dashboard/__init__.py` (remove `server` import if any)
- Any remaining references to `server.py` (grep and clean)

---

## 🎯 Goal

`server.py` is gone. The FastAPI app in `usage_dashboard/api/` is the only
HTTP server. All tests pass.

This is the **cutover commit** for Phase 1. After this, the back-end
architecture matches ADR 0001.

---

## 🔴 Mandatory TDD discipline

This ticket is mostly deletion. The TDD discipline here is **delete → run
full suite → confirm green**. If anything breaks, restore and fix before
re-deleting.

---

## 🪜 Steps

### Step 1 — Red: prove nothing references server.py

```bash
cd /mnt/disk8t/code/CLIProxyAPI/tools/usage-dashboard
grep -rn "from . import server\|from usage_dashboard.server\|from .server\|import server" \
     --include="*.py" .
```

Expected: zero matches. If any match, those are the red tests — fix them
first by removing the import and replacing with `api`-based equivalents.

Commit (if cleanup needed): `refactor: remove last references to legacy server.py`

### Step 2 — Green: delete server.py

```bash
git rm usage_dashboard/server.py
uv run pytest tests/ -v
uv run pytest test_usage_dashboard.py -v
```

If all green:
Commit: `refactor(server): delete legacy BaseHTTPRequestHandler (Phase 1 cutover)`

### Step 3 — Refactor: clean up dashboard.html location

`dashboard.html` is still loaded by `api/legacy_html.py` from its current
path (`usage_dashboard/dashboard.html`). Leave it there — Phase 5 will
delete it together with the `/legacy` route.

Verify the path is correct:
```bash
uv run pytest tests/test_api_legacy_html.py -v
```

Commit (if path adjusted): `chore: confirm dashboard.html path for /legacy route`

---

## ✅ Ticket completion gate

| # | Check | Command |
|---|-------|---------|
| 1 | Lint | `uv run ruff check usage_dashboard/` |
| 2 | Type Check | `uv run mypy usage_dashboard/` |
| 3 | Build | `uv build` |
| 4 | Unit Tests | `uv run pytest tests/ -v` |
| 5 | Integration Tests | `uv run pytest test_usage_dashboard.py -v` — legacy suite still green |
| 6 | Functional Tests | `python usage_dashboard.py run`; `/legacy` and all `/api/v1/*` endpoints respond |
| 7 | Contract Tests | Every legacy endpoint shape unchanged — covered by `tests/test_api_*.py` parity tests |
| 8 | E2E | Manual: open `/legacy` in browser, full dashboard works against live DB |
| 9 | Code Review | Confirm `server.py` is gone; no dangling imports; Phase 1 acceptance criteria met |

All green → **Phase 1 complete**. Move to Phase 2.
