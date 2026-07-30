# tools/usage-dashboard/Makefile
PLAYWRIGHT ?= pnpm exec playwright

.PHONY: help dev dev-backend dev-frontend test lint build-frontend api-types clean release

help:
	@echo "Targets:"
	@echo "  dev               Start backend + Vite dev server in parallel"
	@echo "  test              Run pytest + vitest"
	@echo "  lint              Run ruff + eslint"
	@echo "  build-frontend    pnpm build -> frontend/dist/"
	@echo "  e2e               Run Playwright E2E tests (starts backend + Vite)"
	@echo "  api-types         Regenerate TS types from FastAPI OpenAPI (Phase 2)"
	@echo "  clean             Remove build artifacts"

PYTHON ?= uv run python
PNPM ?= pnpm


e2e:
	cd frontend && $(PLAYWRIGHT) test --config=../e2e/playwright.config.ts
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
release: build-frontend
	git add -f frontend/dist
	@echo "staged frontend/dist for release commit"
api-types:
	bash frontend/scripts/regen-types.sh


clean:
	rm -rf frontend/dist .mypy_cache .pytest_cache .ruff_cache
	find . -type d -name __pycache__ -exec rm -rf {} +