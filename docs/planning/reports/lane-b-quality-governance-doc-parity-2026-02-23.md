# Lane B Report: Quality/Governance + Docs-Code Parity (2026-02-23)

## Scope
Owner lane: CLIPROXYAPI-PLUSPLUS lane B in this worktree.

## Task Completion (10/10)
1. Baseline quality commands run and failures collected.
2. Resolved deterministic quality failures in Go/docs surfaces.
3. Added stream/non-stream token usage parity test coverage.
4. Reconciled docs status drift for issue #258 in fragmented validation report.
5. Added automated regression guard and wired it into Taskfile.
6. Improved provider operations runbook with concrete verifiable parity commands.
7. Updated report text contains no stale pending markers.
8. Re-ran verification commands and captured pass/fail.
9. Listed unresolved blocked items needing larger refactor.
10. Produced lane report with changed files and command evidence.

## Baseline and Immediate Failures
- `task quality:quick` (initial baseline): progressed through fmt/lint/tests; later reruns exposed downstream provider-smoke script failure (see unresolved blockers).
- `go vet ./...`: pass.
- Selected tests baseline: `go test ./pkg/llmproxy/runtime/executor ...` pass for targeted slices.

Deterministic failures captured during this lane:
- `go test ./pkg/llmproxy/runtime/executor -run 'TestParseOpenAIStreamUsageResponsesParity' -count=1`
  - Fail before fix: `input tokens = 0, want 11`.
- `./.github/scripts/check-open-items-fragmented-parity.sh`
  - Fail before doc reconciliation: `missing implemented status for #258`.

## Fixes Applied
- Stream usage parser parity fix:
  - `pkg/llmproxy/runtime/executor/usage_helpers.go`
  - `parseOpenAIStreamUsage` now supports both `prompt/completion_tokens` and `input/output_tokens`, including cached/reasoning fallback fields.
- New parity/token tests:
  - `pkg/llmproxy/runtime/executor/usage_helpers_test.go`
  - `pkg/llmproxy/runtime/executor/codex_token_count_test.go`
- Docs drift reconciliation for #258:
  - `docs/reports/fragemented/OPEN_ITEMS_VALIDATION_2026-02-22.md`
  - `docs/reports/fragemented/merged.md`
- Automated drift guard:
  - `.github/scripts/check-open-items-fragmented-parity.sh`
  - Task wiring in `Taskfile.yml` via `quality:docs-open-items-parity` and inclusion in `quality:release-lint`.
- Runbook update with concrete commands:
  - `docs/provider-operations.md` section `Stream/Non-Stream Usage Parity Check`.

## Verification Rerun (Post-Fix)
Pass:
- `go test ./pkg/llmproxy/runtime/executor -run 'TestParseOpenAIStreamUsageResponsesParity|TestCountCodexInputTokens_FunctionCall(OutputObjectIncluded|ArgumentsObjectIncluded)' -count=1`
- `go test ./pkg/llmproxy/runtime/executor -run 'TestParseOpenAI(StreamUsageResponsesParity|UsageResponses)|TestNormalizeCodexToolSchemas|TestCountCodexInputTokens_FunctionCall(OutputObjectIncluded|ArgumentsObjectIncluded)' -count=1`
- `go vet ./...`
- `./.github/scripts/check-open-items-fragmented-parity.sh`
- `task quality:release-lint`

Fail (known non-lane blocker):
- `QUALITY_PACKAGES='./pkg/llmproxy/runtime/executor' task quality:quick:check`
  - No longer fails in `test:provider-smoke-matrix:test` after script fix.
  - Current failure is shared-env lint contention in `lint:changed`: `parallel golangci-lint is running`.

## Unresolved Blocked Items (Need Larger Refactor/Separate Lane)
1. `task quality:quick:check` is intermittently blocked in shared environment by concurrent lint runs: `parallel golangci-lint is running`.

## Changed Files
- `pkg/llmproxy/runtime/executor/usage_helpers.go`
- `pkg/llmproxy/runtime/executor/usage_helpers_test.go`
- `pkg/llmproxy/runtime/executor/codex_token_count_test.go`
- `.github/scripts/check-open-items-fragmented-parity.sh`
- `Taskfile.yml`
- `docs/reports/fragemented/OPEN_ITEMS_VALIDATION_2026-02-22.md`
- `docs/reports/fragemented/merged.md`
- `docs/provider-operations.md`
- `scripts/provider-smoke-matrix-test.sh`
- `docs/planning/reports/issue-wave-gh-next32-lane-5.md`
- `docs/planning/reports/lane-b-quality-governance-doc-parity-2026-02-23.md`

## C2 Follow-up Verification (2026-02-23)
- `bash scripts/provider-smoke-matrix-test.sh`: pass (includes new case `create_fake_curl works with required args only`).
- `QUALITY_PACKAGES='./pkg/llmproxy/runtime/executor' task quality:quick:check`: fail due to `parallel golangci-lint is running` in `lint:changed`.
- `./.github/scripts/check-open-items-fragmented-parity.sh`: pass.
