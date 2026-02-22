# Issue Wave CPB-0106..0175 Lane 7 Report

## Scope
- Lane: 7 (`cliproxyapi-plusplus-wave-cpb3-7`)
- Window: `CPB-0166..CPB-0175`
- Objective: triage all 10 items, implement safe quick wins, run focused validation, and document deferred/high-risk work.

## Per-Item Triage and Status

### CPB-0166 - Expand docs for 280KB body-limit + Opus 4.6 call failures
- Status: `DONE (safe docs quick win)`
- Quick wins shipped:
  - Added troubleshooting matrix entry for payload-size failures near `280KB` with immediate reproduction + remediation steps.
- Evidence:
  - `docs/troubleshooting.md`

### CPB-0167 - QA scenarios for `502 unknown provider for model gemini-claude-opus-4-6-thinking`
- Status: `PARTIAL (operator QA/runbook quick wins)`
- Quick wins shipped:
  - Added explicit troubleshooting row for `unknown provider` alias-mismatch symptom.
  - Added Antigravity alias continuity check in provider operations daily checks.
  - Added provider quickstart alias-bridge validation for `gemini-claude-opus-4-6-thinking`.
- Deferred:
  - No new e2e automation harness for stream/non-stream parity in this lane.
- Evidence:
  - `docs/troubleshooting.md`
  - `docs/provider-operations.md`
  - `docs/provider-quickstarts.md`

### CPB-0168 - Refactor Antigravity Opus 4.6 thinking transformation boundaries
- Status: `TRIAGED (deferred, high-risk refactor)`
- Assessment:
  - A safe implementation requires translator/refactor scope across request transformation layers and broader regression coverage.
- Lane action:
  - No high-risk translator refactor landed in this wave.

### CPB-0169 - Rollout safety for per-OAuth-account outbound proxy enforcement
- Status: `DONE (release-governance quick win)`
- Quick wins shipped:
  - Added explicit release checklist gate for per-OAuth-account behavior changes, strict/fail-closed defaults, and rollback planning.
- Evidence:
  - `docs/operations/release-governance.md`

### CPB-0170 - Quickstart refresh for Antigravity Opus integration bug
- Status: `DONE (provider quickstart quick win)`
- Quick wins shipped:
  - Added Antigravity section with alias-bridge config snippet and `/v1/models` sanity command for fast diagnosis.
- Evidence:
  - `docs/provider-quickstarts.md`

### CPB-0171 - Port quota-threshold account-switch flow into first-class CLI command(s)
- Status: `TRIAGED (deferred, command-surface expansion)`
- Assessment:
  - Shipping new CLI command(s) safely requires product/UX decisions and additional command integration tests outside lane-sized quick wins.
- Lane action:
  - Documented current operational mitigations in troubleshooting/runbook surfaces; no new CLI command added.

### CPB-0172 - Harden `iflow glm-4.7` `406` failures
- Status: `DONE (safe docs + runbook quick wins)`
- Quick wins shipped:
  - Added troubleshooting matrix entry for `iflow` `glm-4.7` `406` with checks and mitigation path.
  - Added provider quickstart validation command for `iflow/glm-4.7` and operator guidance.
  - Added operations runbook incident section for `406` reproduction + fallback routing.
- Evidence:
  - `docs/troubleshooting.md`
  - `docs/provider-quickstarts.md`
  - `docs/provider-operations.md`

### CPB-0173 - Operationalize `sdkaccess.RegisterProvider` vs sync/inline registration breakage
- Status: `TRIAGED (partial docs/runbook coverage, no invasive code change)`
- Assessment:
  - No direct `syncInlineAccessProvider` surface exists in this worktree branch; broad observability instrumentation would be cross-cutting.
- Lane action:
  - Added stronger provider/alias continuity checks and unknown-provider runbook entries to catch registry/config drift quickly.
- Evidence:
  - `docs/provider-operations.md`

### CPB-0174 - Process-compose/HMR refresh workflow for signed-model updates
- Status: `DONE (deterministic refresh-check docs quick win)`
- Quick wins shipped:
  - Extended install workflow with deterministic post-edit refresh verification via `/v1/models`.
- Evidence:
  - `docs/install.md`

### CPB-0175 - DX polish for `Qwen Free allocated quota exceeded`
- Status: `DONE (safe docs + defensive keyword hardening)`
- Quick wins shipped:
  - Added troubleshooting and provider-operations guidance for `Qwen Free allocated quota exceeded` incidents.
  - Hardened suspension keyword detection to include `allocated quota exceeded` / `quota exhausted` patterns.
  - Added test coverage for new suspension phrase variants.
- Evidence:
  - `docs/troubleshooting.md`
  - `docs/provider-operations.md`
  - `pkg/llmproxy/auth/kiro/rate_limiter.go`
  - `pkg/llmproxy/auth/kiro/rate_limiter_test.go`

## Focused Test Evidence
- `go test ./pkg/llmproxy/auth/kiro`
  - `ok   github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/auth/kiro`

## Changed Files (Lane 7)
- `pkg/llmproxy/auth/kiro/rate_limiter.go`
- `pkg/llmproxy/auth/kiro/rate_limiter_test.go`
- `docs/troubleshooting.md`
- `docs/provider-quickstarts.md`
- `docs/provider-operations.md`
- `docs/operations/release-governance.md`
- `docs/install.md`
- `docs/planning/reports/issue-wave-cpb-0106-0175-lane-7.md`

## Summary
- Triaged all 10 scoped items.
- Landed low-risk, high-signal quick wins in docs/runbooks plus one focused defensive code/test hardening.
- Deferred high-risk command/translator refactors (`CPB-0168`, `CPB-0171`, deeper `CPB-0173`) with explicit rationale.
