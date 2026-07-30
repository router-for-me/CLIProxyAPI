# Ticket 5.4 — Cutover: delete legacy HTML, `/legacy` route, dashboard.html

**Phase**: 5 — Polish
**Blocks**: 5.5
**Blocked by**: 5.1, 5.2, 5.3
**Files removed**:
- `tools/usage-dashboard/usage_dashboard/dashboard.html`
- `tools/usage-dashboard/usage_dashboard/api/legacy_html.py`
- `tools/usage-dashboard/tests/test_api_legacy_html.py`

**Files touched**:
- `tools/usage-dashboard/usage_dashboard/api/__init__.py` (remove legacy router, remove `/legacy` from public paths)
- `tools/usage-dashboard/usage_dashboard/collector.py` (legacy sync collector — kept for normalize_record helpers but `collect_forever`, `CollectorLock` can be removed if unused; check first)

---

## 🎯 Goal

After this ticket, the React app at `/` is the only UI. Legacy `dashboard.html`
is gone. The `/legacy` route is gone. The sync `collector.py` is reduced to
just the helper functions that `collector_async.py` still imports
(`normalize_record`, `_event_key`, `_account_hash`, `_safe_int`,
`parse_rfc3339`, `_redact_error`).

This is the **cutover commit** for the entire rewrite.

---

## 🔴 Mandatory TDD discipline

Delete → run full suite → confirm green. If anything breaks, restore and
fix before re-deleting.

---

## 🪜 Steps

### Step 1 — Red: prove nothing references legacy_html

```bash
cd /mnt/disk8t/code/CLIProxyAPI/tools/usage-dashboard
grep -rn "from .legacy_html\|from usage_dashboard.api.legacy_html\|/legacy" \
     --include="*.py" --include="*.ts" --include="*.tsx" .
```

Expected: zero matches. If any match, fix first.

Commit (if cleanup needed): `refactor: remove last references to /legacy`

### Step 2 — Green: delete

```bash
git rm usage_dashboard/dashboard.html
git rm usage_dashboard/api/legacy_html.py
git rm tests/test_api_legacy_html.py
```

Update `api/__init__.py`:
- Remove `from . import legacy_html` and `app.include_router(legacy_html.router)`.
- Remove `/legacy` and `/legacy/usage` from `_PUBLIC_PATHS`.

Check `collector.py` usage:
```bash
grep -rn "from .collector import\|from usage_dashboard.collector import\|import collector" \
     --include="*.py" .
```

Identify which functions are still used by `collector_async.py` and tests.
Trim `collector.py` to only those functions. Specifically delete
`collect_once`, `collect_forever`, `CollectorLock`, `COLLECTOR_STATE`,
`snapshot` from the sync module — the async equivalents live in
`collector_async.py`. Keep `normalize_record`, `_event_key`, `_account_hash`,
`_safe_int`, `parse_rfc3339`, `_redact_error`, `_iso`.

If `test_usage_dashboard.py` tests the deleted sync functions, **delete those
specific tests** — they test code that no longer exists. Keep the tests for
`normalize_record`, `parse_rfc3339`, etc.

**Verify green**:
```bash
uv run pytest -v
make e2e
```

Commit: `refactor(cutover): delete legacy dashboard.html + /legacy route (Phase 5)`

### Step 3 — Refactor: rename collector_async → collector

Now that the sync collector is gone, the async one is the only collector.
Rename for clarity:

```bash
git mv usage_dashboard/collector_async.py usage_dashboard/collector.py
# Update imports everywhere (grep + sed or LSP rename)
grep -rl "collector_async" --include="*.py" . | xargs sed -i 's/collector_async/collector/g'
```

Tests:
```bash
uv run pytest -v
```

If the LSP is available, use `lsp rename` instead of sed to rename
`collector_async` → `collector` and `AsyncCollectorLock` → `CollectorLock`.

Commit: `refactor(collector): rename collector_async → collector`

---

## ✅ Ticket completion gate

| # | Check | Command |
|---|-------|---------|
| 1 | Lint | `uv run ruff check .` + `pnpm lint` |
| 2 | Type Check | `uv run mypy usage_dashboard` + `pnpm typecheck` |
| 3 | Build | `uv build` + `pnpm build` |
| 4 | Unit Tests | `uv run pytest tests/ -v` |
| 5 | Integration Tests | `uv run pytest test_usage_dashboard.py -v` (remaining tests, sync collector helpers) |
| 6 | Functional Tests | `python usage_dashboard.py run`; `/` serves React; `/legacy` returns 404 |
| 7 | Contract Tests | All API endpoints unchanged; `/api/v1/*` shapes unchanged |
| 8 | E2E | `make e2e` green — both overview and usage specs |
| 9 | Code Review | No dangling imports; `dashboard.html` is gone; sync collector stripped to helpers |

All green → Ticket 5.5.
