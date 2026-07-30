# Ticket 5.5 — Update README and deployment docs

**Phase**: 5 — Polish
**Blocks**: —
**Blocked by**: 5.4
**Files touched**:
- `tools/usage-dashboard/README.md`
- `tools/usage-dashboard/docs/deployment.md`
- `tools/usage-dashboard/docs/cleanroom-design.md` (mark as fully historical; the rewrite supersedes it)
- Any systemd unit / docker-compose snippet referenced

---

## 🎯 Goal

Documentation reflects the new architecture. An operator reading the README
cold knows:

- What dependencies to install (`uv sync`).
- How to start (`python3 usage_dashboard.py run`).
- How to develop (`make dev` for backend + frontend).
- What the new file layout is.
- Where the ADRs live.

---

## 🔴 Mandatory TDD discipline

Docs are validated by a "fresh clone" smoke test: clone the repo on a
clean machine, follow the README step by step, and confirm the dashboard
boots.

---

## 🪜 Steps

### Step 1 — Red: fresh-clone smoke test

On a clean machine or in a Docker container:
```bash
git clone <repo> && cd CLIProxyAPI/tools/usage-dashboard
# Follow README from scratch
```

Record every step that fails or is unclear.

Commit: `docs(readme): record fresh-clone gaps`

### Step 2 — Green: rewrite README

Update the README to cover:

1. **Quick start** (operator): `uv sync && python3 usage_dashboard.py run`.
2. **Development**: `make dev`, port numbers, where the React app lives.
3. **Architecture** summary: link to `docs/adr/` and `docs/rewrite-plan.md`.
4. **Configuration**: keep the existing table; note any new keys (none expected).
5. **Layout**: update the directory tree.

Update `docs/deployment.md`:
- systemd unit: command becomes `uv run python /opt/usage-dashboard/usage_dashboard.py run`.
- docker-compose: base image stays `python:3.12-slim`; add a build stage that
  runs `pnpm build` if you want to rebuild on deploy, otherwise use the
  committed `frontend/dist/`.

Update `docs/cleanroom-design.md` header: mark as **fully superseded** by
the rewrite; keep for historical context.

**Verify green**:
```bash
# Re-run the fresh-clone smoke test on the updated docs
```

Commit: `docs: rewrite README + deployment for FastAPI + React`

### Step 3 — Refactor: changelog

Add a `CHANGELOG.md` entry:
```markdown
# CHANGELOG

## [Unreleased] — FastAPI + React rewrite

### Breaking changes
- Python runtime now requires `uv sync` to install dependencies
  (fastapi, uvicorn, sqlmodel, httpx, pydantic).
- Legacy `BaseHTTPRequestHandler` server removed; back end is FastAPI.
- Legacy single-file `dashboard.html` removed; UI is a React SPA.
- Error response shape changed from `{"error": "..."}` to `{"detail": "..."}`.

### Added
- OpenAPI at `/docs` and `/openapi.json`.
- Auto-generated TypeScript types from OpenAPI.
- TanStack Query + Zustand state management.
- Playwright E2E suite.

### Removed
- `usage_dashboard/server.py`
- `usage_dashboard/dashboard.html`
- `/legacy` route
```

Commit: `docs(changelog): FastAPI + React rewrite entry`

---

## ✅ Ticket completion gate (also the **project** completion gate)

| # | Check | Command |
|---|-------|---------|
| 1 | Lint | `uv run ruff check .` + `pnpm lint` |
| 2 | Type Check | `uv run mypy usage_dashboard` + `pnpm typecheck` |
| 3 | Build | `uv build` + `pnpm build` |
| 4 | Unit Tests | `uv run pytest -v` (Python) + `pnpm test` (frontend) |
| 5 | Integration Tests | Full pytest + vitest suites green |
| 6 | Functional Tests | Fresh-clone smoke test on a clean machine |
| 7 | Contract Tests | All API endpoint shapes match `app.openapi()` |
| 8 | E2E | `make e2e` green — overview + usage specs pass |
| 9 | Code Review | Final review against ADRs; confirm every ADR is reflected in code |

All green → **Phase 5 complete. Rewrite complete.** 🎯
