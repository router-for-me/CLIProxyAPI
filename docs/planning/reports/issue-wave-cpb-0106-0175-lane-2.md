# Issue Wave CPB-0106..0175 Lane 2 Report

## Scope
- Lane: 2
- Worktree: `cliproxyapi-plusplus-wave-cpb3-2`
- Target items: `CPB-0116` .. `CPB-0125`
- Date: 2026-02-22

## Per-Item Triage and Status

### CPB-0116 - process-compose/HMR refresh workflow for `gpt-5.3-codex-spark` reload determinism
- Status: `triaged-existing`
- Triage:
  - Existing local refresh workflow and watcher-based reload path already documented (`docs/install.md`, `examples/process-compose.dev.yaml`).
  - Existing operational spark mismatch runbook already present (`docs/provider-operations.md`).
- Lane action:
  - No code mutation required in this lane for safe quick win.

### CPB-0117 - QA scenarios for random `x-anthropic-billing-header` cache misses
- Status: `implemented`
- Result:
  - Added explicit non-stream/stream parity validation commands and rollback threshold guidance in operations runbook.
- Touched files:
  - `docs/provider-operations.md`

### CPB-0118 - Refactor forced-thinking 500 path around ~2m runtime
- Status: `blocked`
- Triage:
  - No deterministic failing fixture in-repo tied to this exact regression path.
  - Safe refactor without reproducer risks behavior regressions across translator/executor boundaries.
- Next action:
  - Add replay fixture + benchmark guardrails (p50/p95) before structural refactor.

### CPB-0119 - Provider quickstart for quota-visible but request-insufficient path
- Status: `implemented`
- Result:
  - Added iFlow quota/entitlement quickstart section with setup, model inventory, non-stream parity check, stream parity check, and triage guidance.
- Touched files:
  - `docs/provider-quickstarts.md`

### CPB-0120 - Standardize metadata and naming conventions across repos
- Status: `blocked`
- Triage:
  - Item explicitly spans both repos; this lane is scoped to a single worktree.
  - No safe unilateral rename/migration in this repo alone.
- Next action:
  - Coordinate cross-repo migration note/changelog with compatibility contract.

### CPB-0121 - Follow-up for intermittent iFlow GLM-5 `406`
- Status: `implemented`
- Result:
  - Extended iFlow reasoning-preservation model detection to include `glm-5`.
  - Normalized model IDs by stripping optional provider prefixes (e.g. `iflow/glm-5`) before compatibility checks.
  - Added targeted regression tests for both `glm-5` and prefixed `iflow/glm-5` cases.
- Touched files:
  - `pkg/llmproxy/runtime/executor/iflow_executor.go`
  - `pkg/llmproxy/runtime/executor/iflow_executor_test.go`

### CPB-0122 - Harden free-auth-bot sharing scenario with safer defaults
- Status: `blocked`
- Triage:
  - Source issue implies external account-sharing/abuse workflows; no safe local patch contract in this repo.
  - No deterministic fixture covering intended validation behavior change.
- Next action:
  - Define explicit policy-compatible validation contract and add fixtures first.

### CPB-0123 - Operationalize Gemini CLI custom headers with observability/alerts/runbook
- Status: `implemented`
- Result:
  - Added operations guardrail section with validation, thresholded alerts, and rollback guidance for custom-header rollouts.
- Touched files:
  - `docs/provider-operations.md`

### CPB-0124 - Provider-agnostic pattern for invalid thinking signature across provider switch
- Status: `blocked`
- Triage:
  - Existing translator code already uses shared skip-signature sentinel patterns across Gemini/Claude paths.
  - No new failing fixture specific to "Gemini CLI -> Claude OAuth mid-conversation" to justify safe behavior mutation.
- Next action:
  - Add cross-provider conversation-switch fixture first, then generalize only if gap is reproduced.

### CPB-0125 - DX polish for token-savings CLI proxy ergonomics
- Status: `blocked`
- Triage:
  - No explicit command/UX contract in-repo for the requested ergonomic changes.
  - Safe changes require product-surface decision (flags/output modes/feedback timing) not encoded in current tests.
- Next action:
  - Define CLI UX acceptance matrix, then implement with command-level tests.

## Validation Commands

- Focused package tests (touched code):
  - `go test ./pkg/llmproxy/runtime/executor -run 'TestPreserveReasoningContentInMessages|TestIFlowExecutorParseSuffix|TestApplyClaudeHeaders_AnthropicUsesXAPIKeyAndDefaults|TestApplyClaudeHeaders_NonAnthropicUsesBearer' -count=1`
  - Result: passing.

- Triage evidence commands used:
  - `rg -n "CPB-0116|...|CPB-0125" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.md`
  - `sed -n '1040,1188p' docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.md`
  - `rg -n "gpt-5.3-codex-spark|process-compose|x-anthropic-billing-header|iflow|GLM|thinking signature" pkg cmd docs test`

## Change Summary

- Implemented safe quick wins for:
  - `CPB-0117` (runbook QA parity + rollback guidance)
  - `CPB-0119` (provider quickstart refresh for quota/entitlement mismatch)
  - `CPB-0121` (iFlow GLM-5 compatibility + regression tests)
  - `CPB-0123` (Gemini custom-header operational guardrails)
- Deferred high-risk or cross-repo items with explicit blockers:
  - `CPB-0118`, `CPB-0120`, `CPB-0122`, `CPB-0124`, `CPB-0125`
- Triaged as already covered by existing lane-repo artifacts:
  - `CPB-0116`
