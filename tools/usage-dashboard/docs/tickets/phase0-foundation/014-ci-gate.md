# Ticket 0.4 — CI workflow

**Phase**: 0 — Foundation
**Blocks**: —
**Blocked by**: 0.3
**Files touched** (new only):
- `.github/workflows/usage-dashboard-ci.yml`

**Files NOT touched**: anything outside `.github/workflows/`

---

## 🎯 Goal

A GitHub Actions workflow runs on every PR touching `tools/usage-dashboard/**`:
lint, type-check, build, unit tests for both Python and frontend.

This is the **enforcement gate** for all subsequent tickets — once it's green,
every later ticket must keep it green.

---

## 🔴 Mandatory TDD discipline

CI is tested by intentionally breaking and unbreaking a test in a throwaway
branch, observing the workflow run, then reverting. This is the Red → Green
cycle for CI.

---

## 🪜 Steps

### Step 1 — Red: throwaway breaking commit

On a scratch branch:
```bash
git checkout -b verify-ci-red
# Edit tests/test_packaging.py: change the assertion to require a bogus file
git commit -am "scratch: break packaging test to verify CI fails"
git push -u origin verify-ci-red
gh pr create --title "verify CI red" --body "throwaway"
```

**Verify red**: open the PR, confirm the workflow fails on the packaging
test. Do not merge.

Commit (on main after reverting): `revert: scratch break`

### Step 2 — Green: add workflow

```yaml
# .github/workflows/usage-dashboard-ci.yml
name: usage-dashboard CI

on:
  pull_request:
    paths:
      - "tools/usage-dashboard/**"
      - ".github/workflows/usage-dashboard-ci.yml"
  push:
    branches: [main]
    paths:
      - "tools/usage-dashboard/**"

defaults:
  run:
    working-directory: tools/usage-dashboard

jobs:
  python:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: astral-sh/setup-uv@v3
        with:
          version: "latest"
      - run: uv sync --extra dev
      - run: uv run ruff check .
      - run: uv run pytest tests/ -v
      - run: uv run pytest test_usage_dashboard.py -v

  frontend:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: pnpm/action-setup@v3
        with:
          version: 9
      - uses: actions/setup-node@v4
        with:
          node-version: 20
          cache: pnpm
          cache-dependency-path: tools/usage-dashboard/frontend/pnpm-lock.yaml
      - working-directory: tools/usage-dashboard/frontend
        run: pnpm install --frozen-lockfile
      - working-directory: tools/usage-dashboard/frontend
        run: pnpm lint
      - working-directory: tools/usage-dashboard/frontend
        run: pnpm typecheck
      - working-directory: tools/usage-dashboard/frontend
        run: pnpm test
      - working-directory: tools/usage-dashboard/frontend
        run: pnpm build
```

Open a real PR with this workflow. Confirm both jobs are green.

Commit: `ci(usage-dashboard): lint + typecheck + test + build`

### Step 3 — Refactor: paths filter, concurrency

Add concurrency to cancel superseded runs:
```yaml
concurrency:
  group: ${{ github.workflow }}-${{ github.ref }}
  cancel-in-progress: true
```

Commit: `ci(usage-dashboard): cancel superseded runs`

---

## ✅ Ticket completion gate

| # | Check | Command |
|---|-------|---------|
| 1 | Lint | Workflow runs `ruff check` and `pnpm lint` — both pass |
| 2 | Type Check | Workflow runs `pnpm typecheck` — passes |
| 3 | Build | Workflow runs `pnpm build` — passes |
| 4 | Unit Tests | Workflow runs both pytest suites + vitest — all pass |
| 5 | Integration Tests | N/A at CI layer |
| 6 | Functional Tests | Confirm the workflow triggers on PRs touching `tools/usage-dashboard/**` |
| 7 | Contract Tests | N/A |
| 8 | E2E | N/A |
| 9 | Code Review | Reviewer confirms the trigger paths and job matrix |

All green → Phase 0 complete. Move to Phase 1.
