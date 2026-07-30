# Ticket 0.3 — Makefile with parallel dev orchestration

**Phase**: 0 — Foundation
**Blocks**: 0.4
**Blocked by**: 0.1, 0.2
**Files touched** (new only):
- `tools/usage-dashboard/Makefile`

**Files NOT touched**: everything else

---

## 🎯 Goal

A single `make dev` starts the legacy back end (`python3 usage_dashboard.py serve`)
and the Vite dev server (`pnpm dev`) in parallel, killing both on Ctrl-C.
Other targets: `make test`, `make lint`, `make build-frontend`,
`make api-types` (placeholder — Phase 2 fills it).

---

## 🔴 Mandatory TDD discipline

The Makefile itself is testable via a smoke test. Red → Green → Refactor.

---

## 🪜 Steps

### Step 1 — Red: Makefile targets test

```python
# tests/test_makefile.py
import subprocess
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]


def test_makefile_exists():
    assert (ROOT / "tools/usage-dashboard/Makefile").is_file()


def test_make_help_lists_targets():
    """`make help` must list the canonical targets."""
    result = subprocess.run(
        ["make", "help"],
        cwd=str(ROOT / "tools/usage-dashboard"),
        capture_output=True,
        text=True,
    )
    assert result.returncode == 0
    for target in ("dev", "test", "lint", "build-frontend", "api-types"):
        assert target in result.stdout
```

**Verify red**: `make help` fails (no Makefile).

Commit: `test(makefile): red — canonical targets`

### Step 2 — Green: write Makefile

```makefile
# tools/usage-dashboard/Makefile
.PHONY: help dev dev-backend dev-frontend test lint build-frontend api-types clean

help:
	@echo "Targets:"
	@echo "  dev               Start backend + Vite dev server in parallel"
	@echo "  test              Run pytest + vitest"
	@echo "  lint              Run ruff + eslint"
	@echo "  build-frontend    pnpm build → frontend/dist/"
	@echo "  api-types         Regenerate TS types from FastAPI OpenAPI (Phase 2)"
	@echo "  clean             Remove build artifacts"

PYTHON ?= uv run python
PNPM ?= pnpm

dev:
	@trap 'kill 0' INT; \
	$(MAKE) dev-backend & \
	$(MAKE) dev-frontend & \
	wait

dev-backend:
	$(PYTHON) usage_dashboard.py serve

dev-frontend:
	cd frontend && $(PNPM) dev

test:
	$(PYTHON) -m pytest
	cd frontend && $(PNPM) test

lint:
	$(PYTHON) -m ruff check .
	cd frontend && $(PNPM) lint

build-frontend:
	cd frontend && $(PNPM) build

api-types:
	@echo "Phase 2 — not yet implemented"

clean:
	rm -rf frontend/dist .mypy_cache .pytest_cache .ruff_cache
	find . -type d -name __pycache__ -exec rm -rf {} +
```

**Verify green**:
```bash
make help
uv run pytest tests/test_makefile.py -v
```

Commit: `feat(makefile): parallel dev orchestration — green`

### Step 3 — Refactor: validate `make dev` manually

- Run `make dev` in a terminal.
- Open `http://localhost:5173` — placeholder shows.
- Open `http://localhost:8320/api/v1/health` — legacy back end responds.
- Ctrl-C kills both processes.

Commit: `chore(makefile): verified parallel dev works`

---

## ✅ Ticket completion gate

| # | Check | Command |
|---|-------|---------|
| 1 | Lint | `uv run ruff check .` (skip frontend lint — Ticket 0.2 covers it) |
| 2 | Type Check | N/A (no new code) |
| 3 | Build | `make build-frontend` succeeds |
| 4 | Unit Tests | `uv run pytest tests/test_makefile.py -v` |
| 5 | Integration Tests | `make dev` starts both processes; manual kill via Ctrl-C works |
| 6 | Functional Tests | Both ports respond as expected |
| 7 | Contract Tests | N/A |
| 8 | E2E | N/A |
| 9 | Code Review | Diff is a single new Makefile |

All green → move to Ticket 0.4.
