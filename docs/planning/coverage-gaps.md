# Coverage Gaps Report

Date: 2026-02-22

## Current Snapshot

- Scope assessed:
  - `pkg/llmproxy/api`, `pkg/llmproxy/translator`, `sdk/api/handlers`
  - selected quality commands in `Taskfile.yml`
- Baseline commands executed:
  - `go test ./pkg/llmproxy/api -run 'TestServer_|TestResponsesWebSocketHandler_.*'`
  - `go test ./pkg/llmproxy/api -run 'TestServer_ControlPlane_MessageLifecycle|TestServer_ControlPlane_UnsupportedCapability|TestServer_RoutesNamespaceIsolation|TestServer_ResponsesRouteSupportsHttpAndWebsocketShapes|TestServer_StartupSmokeEndpoints'`
  - `QUALITY_PACKAGES='./pkg/llmproxy/api ./sdk/api/handlers/openai' task quality:quick`
- `task quality:fmt:check`
- `task lint:changed` (environment reports golangci-lint Go 1.25 binary mismatch with Go 1.26 target)
- `go test ./pkg/llmproxy/api -run 'TestServer_'`
- `go test ./sdk/api/handlers -run 'TestRequestExecutionMetadata'`
- `/.github/scripts/check-distributed-critical-paths.sh`
- `QUALITY_PACKAGES='./pkg/llmproxy/api ./sdk/api/handlers/openai' task quality:quick:check`
- `task quality:quick:all` now runs with a configurable sibling target (`QUALITY_PARENT_TASK`) and defaults to quick checks in tandem by default; follow-up remains for missing sibling lint binaries.

## Gap Matrix

- Unit:
  - Coverage improved for API route lifecycle and websocket idempotency.
  - Added startup smoke assertions for `/v1/models` and `/v1/metrics/providers`, plus repeated `setupRoutes` route-count stability checks.
  - Added `requestExecutionMetadata` regression tests (idempotency key propagation + session/auth metadata).
  - Added control-plane shell endpoint coverage for `/message`, `/messages`, `/status`, `/events` in `pkg/llmproxy/api/server_test.go`.
  - Added command-label translation tests for `/message` aliases (`ask`, `exec`, `max`, `continue`, `resume`).
  - Added `/message` idempotency replay test that asserts duplicate key reuse and no duplicate in-memory message append.
  - Added idempotency negative test for different `Idempotency-Key` values and in-flight message-copy isolation for `/messages`.
  - Added task-level quality gates (`quality:ci`, `lint:changed` with PR ranges, `test:smoke`) and workflow/required-check wiring for CI pre-merge gates.
  - Added `quality:release-lint` and required-check `quality-staged-check` in CI; added docs/code snippet parse coverage for release lint.
  - Added thinking validation coverage for level rebound and budget boundary clamping in `pkg/llmproxy/thinking/validate_test.go`:
    - unsupported/rebound level handling and deterministic clamping to supported levels,
    - min/max/zero/negative budget normalization for non-strict suffix-paths,
    - explicit strict out-of-range rejection (`ErrBudgetOutOfRange`) when same-provider budget requests are too high.
    - auto-mode behavior for dynamic-capable vs non-dynamic models (`ModeAuto` midpoint fallback and preservation paths).
  - Remaining: complete route-namespace matrix for command-label translation across orchestrator-facing surfaces beyond `/message`, and status/event replay windows.
- Integration:
  - Remaining: end-to-end provider cheapest-path smoke for live process orchestration against every provider auth mode. Unit-level smoke now covers:
    - `/v1/models` namespace behavior for OpenAI-compatible and `claude-cli` User-Agent paths.
    - `/v1/metrics/providers` response shape and metric-field assertions with seeded usage data.
    - control-plane lifecycle endpoints with idempotency replay windows.
  - Remaining: live provider smoke and control-plane session continuity across process restarts.
- E2E:
  - Remaining: end-to-end harness for `/agent/*` parity and full resume/continuation semantics.
  - Remaining: live-process orchestration for `/v1/models`, `/v1/metrics/providers`, and `/v1/responses` websocket fallback.
  - Added first smoke-level unit checks for `/message` lifecycle and `/v1` models/metrics namespace dispatch.
- Chaos:
  - Remaining: websocket drop/reconnect and upstream timeout injection suite.
- Perf:
  - Remaining: concurrent fanout/p99/p95 measurement for `/v1/responses` stream fanout.
- Security:
  - Remaining: token leak and origin-header downgrade guard assertions.
- Docs:
- Remaining: close loop on `docs/planning/README` command matrix references in onboarding guides and add explicit evidence links for the cheapest-provider matrix tasks.

## Close-out Owner

- Owner placeholder: `cliproxy` sprint lead
- Required before lane closure: each unchecked item in this file must have evidence in `docs/planning/agents.md`.
