# Issue Wave CPB-0781-0790 Lane D9 Report

- Lane: `D9`
- Scope: `CPB-0781` to `CPB-0790`
- Domain: `cliproxy`
- Status: in-progress (implementation + validation coverage)
- Completion time: 2026-02-23

## Completed Items

### CPB-0781
- Focus: FR: Add support for beta headers for Claude models.
- Code changes:
  - Added regression tests in `pkg/llmproxy/runtime/executor/codex_websockets_executor_headers_test.go` covering:
    - default `OpenAI-Beta` injection to `responses_websockets=2026-02-04` when missing,
    - preserving explicit websocket beta values,
    - replacing non-websocket beta values with required default,
    - Gin-context beta header handoff,
    - `Originator` behavior for auth-key vs API-key paths.
- Validation checks:
  - `go test ./pkg/llmproxy/runtime/executor -run "CodexWebsocketHeaders" -count=1`

### CPB-0782
- Focus: Create/refresh provider quickstart for Opus 4.5 support.
- Docs changes:
  - Added Opus 4.5 quickstart and streaming checks in `docs/provider-quickstarts.md`.

### CPB-0786
- Focus: Expand docs/examples for Nano Banana.
- Docs changes:
  - Added CPB-0786 Nano Banana probe section in `docs/provider-quickstarts.md`.
  - The section includes model-list and request probes with fallback guidance for alias visibility.

### CPB-0783
- Focus: Add deterministic recovery guidance for `gemini-3-pro-preview` tool-use failures.
- Code changes:
  - `cmd/cliproxyctl/main.go` now emits `tool_failure_remediation` in `dev --json` details.
  - Added `gemini3ProPreviewToolUsageRemediationHint` helper with a deterministic touch/down/up/model-check/canary sequence.
- Validation:
  - `go test ./cmd/cliproxyctl -run TestRunDevHintIncludesGeminiToolUsageRemediation`
- Docs changes:
  - Added the same deterministic recovery sequence to `docs/install.md` and `docs/troubleshooting.md`.

## Remaining in this window

### CPB-0784
- RooCode compatibility to shared provider-agnostic pattern.

### CPB-0785
- DX polish for `T.match` failures and command ergonomics.

### CPB-0787
- QA scenarios for stream/non-stream parity around channel switch / testing controls.

### CPB-0788
- Refactor around request concatenation issue complexity.

### CPB-0789
- Thinking rollout safety + stream contract hardening.

### CPB-0790
- Metadata/name standardization for `gemini-claude-sonnet-4-5` / cross-repo metadata.

## Read-Only Validation

- `rg -n "CPB-0781|CPB-0782|CPB-0783|CPB-0786" docs/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.md docs/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
- `go test ./pkg/llmproxy/runtime/executor -run "CodexWebsocketHeaders" -count=1`
- `rg -n "Opus 4.5|Nano Banana|CPB-0786" docs/provider-quickstarts.md`
