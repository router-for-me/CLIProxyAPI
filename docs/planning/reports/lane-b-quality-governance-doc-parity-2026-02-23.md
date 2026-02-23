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

## C4 Parity Guard Hardening Addendum (2026-02-23)

### Scope
- Harden `.github/scripts/check-open-items-fragmented-parity.sh` to avoid `#258` status false positives/negatives.
- Add fixture-based regression coverage for parity behavior.
- Re-verify parity and quick quality-governance path signals.

### Changes Applied
- `.github/scripts/check-open-items-fragmented-parity.sh`
  - Added `REPORT_PATH`/`ISSUE_ID` overrides for deterministic testability.
  - Replaced broad keyword matching with boundary-safe extraction of the exact `Issue #258` block.
  - Enforced explicit status mapping from `- Status:` or `- #status:` lines.
  - Added canonical status mapping:
    - Pass tokens: `implemented|done|fixed|resolved|complete|completed`
    - Fail tokens: `partial|partially|blocked|pending|todo|not implemented`
- Added regression suite:
  - `.github/scripts/tests/check-open-items-fragmented-parity-test.sh`
  - `.github/scripts/tests/fixtures/open-items-parity/pass-status-implemented.md`
  - `.github/scripts/tests/fixtures/open-items-parity/pass-hash-status-done.md`
  - `.github/scripts/tests/fixtures/open-items-parity/fail-status-partial.md`
  - `.github/scripts/tests/fixtures/open-items-parity/fail-missing-status.md`

### Verified Command Outcomes
- `./.github/scripts/check-open-items-fragmented-parity.sh`
  - Result: pass
  - Expected success signal: `[OK] fragmented open-items report parity checks passed`
- `./.github/scripts/tests/check-open-items-fragmented-parity-test.sh`
  - Result: pass
  - Expected success signals:
    - `[OK] passes_with_status_implemented`
    - `[OK] passes_with_hash_status_done`
    - `[OK] fails_with_partial_status`
    - `[OK] fails_without_status_mapping`
- `task test:provider-smoke-matrix:test`
  - Result: pass
  - Expected terminal signal: `[OK] provider-smoke-matrix script test suite passed`
- `QUALITY_PACKAGES='./pkg/llmproxy/runtime/executor' task quality:quick:check`
  - Result: blocked in shared environment (non-code blocker)
  - Observed error signal: `Error: parallel golangci-lint is running`

### Concise Runbook Note (Exact Expectations)
- Parity guard command:
  - `./.github/scripts/check-open-items-fragmented-parity.sh`
  - Expect `[OK] fragmented open-items report parity checks passed` on success.
  - Expect `[FAIL] ... status ...` when `Issue #258` status mapping is missing, partial, blocked, pending, or otherwise non-implemented.
- Regression command:
  - `./.github/scripts/tests/check-open-items-fragmented-parity-test.sh`
  - Expect four `[OK]` case lines and zero `[FAIL]` lines.
