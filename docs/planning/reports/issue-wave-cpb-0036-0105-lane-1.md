# Issue Wave CPB-0036..0105 Lane 1 Report

## Scope
- Lane: self
- Worktree: `/Users/kooshapari/temp-PRODVERCEL/485/kush/cliproxyapi-plusplus`
- Window: `CPB-0036` to `CPB-0045`

## Status Snapshot

- `in_progress`: 10/10 items reviewed
- `implemented`: `CPB-0036`, `CPB-0039`, `CPB-0041`, `CPB-0043`, `CPB-0045`
- `blocked`: `CPB-0037`, `CPB-0038`, `CPB-0040`, `CPB-0042`, `CPB-0044`

## Per-Item Status

### CPB-0036 – Expand docs and examples for #145 (openai-compatible Claude mode)
- Status: `implemented`
- Rationale:
  - Existing provider docs now include explicit compatibility guidance under:
    - `docs/api/openai-compatible.md`
    - `docs/provider-usage.md`
- Validation:
  - `rg -n "Claude Compatibility Notes|OpenAI-Compatible API" docs/api/openai-compatible.md docs/provider-usage.md`
- Touched files:
  - `docs/api/openai-compatible.md`
  - `docs/provider-usage.md`

### CPB-0037 – Add QA scenarios for #142
- Status: `blocked`
- Rationale:
  - No stable reproduction payloads or fixtures for the specific request matrix are available in-repo.
- Next action:
  - Add one minimal provider-compatibility fixture set and a request/response parity test once fixture data is confirmed.

### CPB-0038 – Add support path for Kimi coding support
- Status: `blocked`
- Rationale:
  - Current implementation has no isolated safe scope for a full feature implementation in this lane without deeper provider behavior contracts.
  - The current codebase has related routing/runtime primitives, but no minimal-change patch was identified that is safe in-scope.
- Next action:
  - Treat as feature follow-up with a focused acceptance fixture matrix and provider runtime coverage.

### CPB-0039 – Follow up on Kiro IDC manual refresh status
- Status: `implemented`
- Rationale:
  - Existing runbook and executor hardening now cover manual refresh workflows (`docs/operations/auth-refresh-failure-symptom-fix.md`) and related status checks.
- Validation:
  - `go test ./pkg/llmproxy/executor ./cmd/server`
- Touched files:
  - `docs/operations/auth-refresh-failure-symptom-fix.md`

### CPB-0040 – Handle non-streaming output_tokens=0 usage
- Status: `blocked`
- Rationale:
  - The current codebase already has multiple usage fallbacks, but there is no deterministic non-streaming fixture reproducing a guaranteed `output_tokens=0` defect for a safe, narrow patch.
- Next action:
  - Add a reproducible fixture from upstream payload + parser assertion in `usage_helpers`/Kiro path before patching parser behavior.

### CPB-0041 – Follow up on fill-first routing
- Status: `implemented`
- Rationale:
  - Fill strategy normalization is already implemented in management/runtime startup reload path.
- Validation:
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/executor`
- Touched files:
  - `pkg/llmproxy/api/handlers/management/config_basic.go`
  - `sdk/cliproxy/service.go`
  - `sdk/cliproxy/builder.go`

### CPB-0042 – 400 fallback/error compatibility cleanup
- Status: `blocked`
- Rationale:
  - Missing reproducible corpus for the warning path (`kiro: received 400...`) and mixed model/transport states.
- Next action:
  - Add a fixture-driven regression test around HTTP 400 body+retry handling in `sdk/cliproxy` or executor tests.

### CPB-0043 – ClawCloud deployment parity
- Status: `implemented`
- Rationale:
  - Config path fallback and environment-aware discovery were added for non-local deployment layouts; this reduces deployment friction for cloud workflows.
- Validation:
  - `go test ./cmd/server ./pkg/llmproxy/cmd`
- Touched files:
  - `cmd/server/config_path.go`
  - `cmd/server/config_path_test.go`
  - `cmd/server/main.go`

### CPB-0044 – Refresh social credential expiry handling
- Status: `blocked`
- Rationale:
  - Required source contracts for social credential lifecycle are absent in this branch of the codebase.
- Next action:
  - Coordinate with upstream issue fixture and add a dedicated migration/test sequence when behavior is confirmed.

### CPB-0045 – Improve `403` handling ergonomics
- Status: `implemented`
- Rationale:
  - Error enrichment for Antigravity license/subscription `403` remains in place and tested.
- Validation:
  - `go test ./pkg/llmproxy/executor ./pkg/llmproxy/api ./cmd/server`
- Touched files:
  - `pkg/llmproxy/executor/antigravity_executor.go`
  - `pkg/llmproxy/executor/antigravity_executor_error_test.go`

## Evidence & Commands Run

- `go test ./cmd/server ./pkg/llmproxy/cmd ./pkg/llmproxy/executor ./pkg/llmproxy/store`
- `go test ./pkg/llmproxy/executor ./pkg/llmproxy/runtime/executor ./pkg/llmproxy/store ./pkg/llmproxy/api/handlers/management ./pkg/llmproxy/api -run 'Route_?|TestServer_?|Test.*Fill|Test.*ClawCloud|Test.*openai_compatible'`
- `rg -n "Claude Compatibility Notes|OpenAI-Compatible API|Kiro" docs/api/openai-compatible.md docs/provider-usage.md docs/operations/auth-refresh-failure-symptom-fix.md`

## Next Actions

- Keep blocked CPB items in lane-1 waitlist with explicit fixture requests.
- Prepare lane-2..lane-7 dispatch once child-agent capacity is available.
