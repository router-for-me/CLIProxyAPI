# Issue Wave CPB-0246..0280 Lane 3 Report

## Scope

- Lane: lane-3
- Worktree: `/Users/kooshapari/temp-PRODVERCEL/485/kush/cliproxyapi-plusplus`
- Window: `CPB-0256` to `CPB-0265`

## Status Snapshot

- `implemented`: 2
- `planned`: 0
- `in_progress`: 8
- `blocked`: 0

## Per-Item Status

### CPB-0256 – Expand docs and examples for "“Error 404: Requested entity was not found" for gemini 3 by gemini-cli" with copy-paste quickstart and troubleshooting section.
- Status: `implemented`
- Theme: `responses-and-chat-compat`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1325`
- Delivered:
  - Added copy-paste Gemini CLI 404 quickstart (`docs/provider-quickstarts.md`) with model exposure checks and non-stream -> stream parity validation sequence.
  - Added troubleshooting matrix row for Gemini CLI/Gemini 3 `404 Requested entity was not found` with immediate check/remediation guidance (`docs/troubleshooting.md`).
- Verification commands:
  - `rg -n "Gemini CLI 404 quickstart|Requested entity was not found" docs/provider-quickstarts.md docs/troubleshooting.md`

### CPB-0257 – Add QA scenarios for "nvidia openai接口连接失败" including stream/non-stream parity and edge-case payloads.
- Status: `implemented`
- Theme: `websocket-and-streaming`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1324`
- Delivered:
  - Added NVIDIA OpenAI-compatible QA scenarios with stream/non-stream parity and edge-case payload checks (`docs/provider-quickstarts.md`).
  - Hardened OpenAI-compatible executor non-stream path to explicitly set `Accept: application/json` and force `stream=false` request payload (`pkg/llmproxy/runtime/executor/openai_compat_executor.go`).
  - Added regression tests for non-stream and stream request shaping parity (`pkg/llmproxy/runtime/executor/openai_compat_executor_compact_test.go`).
- Verification commands:
  - `go test ./pkg/llmproxy/runtime/executor -run 'TestOpenAICompatExecutorExecute_NonStreamForcesJSONAcceptAndStreamFalse|TestOpenAICompatExecutorExecuteStream_SetsSSEAcceptAndStreamTrue|TestOpenAICompatExecutorCompactPassthrough' -count=1`

### CPB-0258 – Refactor implementation behind "Feature Request: Add generateImages endpoint support for Gemini API" to reduce complexity and isolate transformation boundaries.
- Status: `in_progress`
- Theme: `thinking-and-reasoning`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1322`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0258" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0259 – Ensure rollout safety for "iFlow Error: LLM returned 200 OK but response body was empty (possible rate limit)" via feature flags, staged defaults, and migration notes.
- Status: `in_progress`
- Theme: `oauth-and-authentication`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1321`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0259" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0260 – Standardize metadata and naming conventions touched by "feat: add code_execution and url_context tool passthrough for Gemini" across both repos.
- Status: `in_progress`
- Theme: `thinking-and-reasoning`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1318`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0260" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

## Evidence & Commands Run

- `rg -n 'CPB-0256|CPB-0265' docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`
- `go test ./pkg/llmproxy/runtime/executor -run 'TestOpenAICompatExecutorExecute_NonStreamForcesJSONAcceptAndStreamFalse|TestOpenAICompatExecutorExecuteStream_SetsSSEAcceptAndStreamTrue|TestOpenAICompatExecutorCompactPassthrough' -count=1`

## Next Actions
- Continue `CPB-0258..CPB-0265` with reproducible fixtures first, then implementation in small validated batches.
