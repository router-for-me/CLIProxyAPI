# Issue Wave CPB-0036..0105 Lane 3 Report

## Scope
- Lane: 3
- Worktree: `cliproxyapi-plusplus`
- Target items: `CPB-0056` .. `CPB-0065`
- Date: 2026-02-22

## Per-Item Triage and Status

### CPB-0056 Kiro currently has no authentication available
- Status: `blocked`
- Triage: Requires end-to-end auth health contract and expected UX for no-auth conditions.
- Next action: Add acceptance test fixture for null-auth behavior and CLI diagnostics.

### CPB-0057 GitHub Copilot model-call failure flow
- Status: `blocked`
- Triage: Requires broader extraction of the thegent-managed flow and safe CLI command boundary.
- Next action: Add architecture decision + explicit flow tests before code changes.

### CPB-0058 Veo-style image generation + process-compose/HMR refresh
- Status: `blocked`
- Triage: No process-compose profile exists in this worktree; no deterministic HMR refresh harness.
- Next action: Add process-compose profile and scripted reload checks.

### CPB-0059 Token collisions with builderId / email/profile_arn empty
- Status: `blocked`
- Triage: Needs provider fixture and migration-safe token collision detection logic.
- Next action: Add token-store conflict fixture and collision-safe merge path.

### CPB-0060 Amazon Q `ValidationException` handling compatibility
- Status: `blocked`
- Triage: Upstream payload compatibility is not yet reproducible from local fixtures.
- Next action: Add failing regression payload + normalization lock.

### CPB-0061 Kiro config entry in UI guidance
- Status: `blocked`
- Triage: Requires coordination with product/UI/management-plane surfaces not owned by this codepath.
- Next action: Add documentation + UI contract with upstream team.

### CPB-0062 Cursor issue hardening
- Status: `blocked`
- Triage: Needs deterministic 4xx/stream error matrix before modifying translation logic.
- Next action: Add cursor regression fixture in executor integration tests.

### CPB-0063 Configurable HTTP timeout for extended thinking
- Status: `blocked`
- Triage: No configurable timeout knob is wired through the targeted request path.
- Next action: Add config schema + executor wiring + timeout override tests.

### CPB-0064 event stream fatal / stream protocol fragility
- Status: `blocked`
- Triage: Observed only via external stream sessions; no unit fixture today.
- Next action: Add stream stress/integrity fixture to capture protocol drop conditions.

### CPB-0065 failed to read config path is a directory
- Status: `partial`
- Result:
  - Existing config loader already returns empty config for optional mode and surfaces OS directory-read error for strict mode.
  - No local safe patch applied because behavior is acceptable for known startup flow, but error message surfacing still needs user-facing guidance in docs.
- Touched docs:
  - `docs/troubleshooting.md`
  - `docs/provider-operations.md`
- Next action: Add user-facing quick triage entry and end-to-end test that validates `--config` pointing at directory emits deterministic remediation text.

## Validation Commands

- `go test ./cmd/server ./pkg/llmproxy/executor ./pkg/llmproxy/runtime/executor ./pkg/llmproxy/logging ./pkg/llmproxy/translator/gemini/openai/chat-completions ./pkg/llmproxy/translator/codex/openai/chat-completions -count=1`
- Result: all targeted package tests passing.
