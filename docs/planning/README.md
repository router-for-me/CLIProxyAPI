# Planning Quality Lifecycle

## Quality Command Matrix

- `task quality:fmt` — Format all Go sources in repo.
- `task quality:fmt:check` — Validate formatting without mutation.
- `task quality:ci` — Pre-merge quality gate (non-mutating; fmt check + vet + optional staticcheck + diff/staged lint).
- `task quality:fmt-staged` — Format and lint staged files only.
- `task quality:fmt-staged:check` — Check formatting and lint staged/diff files (PR-safe, non-mutating).
- `task quality:quick` — Fast loop (`QUALITY_PACKAGES` scoped optional), readonly.
- `task quality:quick:check` — Fast non-mutating quality loop (`quality:fmt:check` + `lint:changed` + targeted tests).
- `task quality:quick:all` — Run `quality:quick` and equivalent sibling project quality checks via `quality:parent-sibling`.
- `task lint` — Run `golangci-lint` across all packages.
- `task lint:changed` — Run `golangci-lint` on changed/staged Go files.
- `task test:smoke` — Startup and control-plane smoke test subset in CI.
- `task quality:vet` — Run `go vet ./...`.
- `task quality:staticcheck` — Optional staticcheck run (`ENABLE_STATICCHECK=1`).
- `task quality:release-lint` — Validate release-facing config examples and docs snippets.
- `task test:unit` / `task test:integration` — Tag-filtered package tests.
- `task test:baseline` — Run `go test` with JSON and plain-text baseline output (`target/test-baseline.json` and `target/test-baseline.txt`).
- `task test` — Full test suite.
- `task verify:all` — Unified local audit entrypoint (`fmt:check`, `test:smoke`, `lint:changed`, `release-lint`, `vet`, `staticcheck`, `test`).
- `task hooks:install` — Install local pre-commit checks.

## Recommended local sequence

1. `task quality:fmt:check`
2. `task quality:quick`
3. `task lint:changed`
4. `task quality:vet` (or `task quality:staticcheck` when needed)
5. `task test` (or `task test:unit`)
6. `task test:smoke`
7. `task verify:all` before PR handoff.

## CI alignment notes

- `preflight` is shared by all test/quality tasks and fails fast on missing `go`, `task`, or `git`.
- `preflight` also validates `task -l`, and if a `Makefile` exists validates `make -n` for build-task sanity.
- `task` now includes `cache:unlock` in test gates to avoid stale lock contention.
- CI baseline artifacts are now emitted as both JSON and text for auditability.

## Active task waves

- CPB-0106..0175 documented and tracked in `docs/planning/reports/issue-wave-cpb-0106-0175-*`.
- CPB-0176..0245 planning wave now initialized with all 70 CPB items distributed across 7 lanes:
  - `docs/planning/issue-wave-cpb-0176-0245-2026-02-22.md`
  - `docs/planning/reports/issue-wave-cpb-0176-0245-lane-1.md` through `lane-7.md`
