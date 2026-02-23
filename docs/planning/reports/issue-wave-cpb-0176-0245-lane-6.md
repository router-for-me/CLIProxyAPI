# Issue Wave CPB-0176..0245 Lane 6 Report

## Scope

- Lane: lane-6
- Worktree: `/Users/kooshapari/temp-PRODVERCEL/485/kush/cliproxyapi-plusplus-wave-cpb4-6`
- Window: `CPB-0226` to `CPB-0235`

## Status Snapshot

- `planned`: 0
- `implemented`: 3
- `in_progress`: 7
- `blocked`: 0

## Per-Item Status

### CPB-0226 – Expand docs and examples for "Implement MCP Server for Memory Operations" with copy-paste quickstart and troubleshooting section.
- Status: `implemented`
- Theme: `thinking-and-reasoning`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1410`
- Rationale:
  - Added copy-paste MCP memory operations quickstart examples with `tools/list` and `tools/call` smoke tests.
  - Added a troubleshooting matrix row for memory-tool failures with concrete diagnosis/remediation flow.
- Implemented artifacts:
  - `docs/provider-quickstarts.md`
  - `docs/troubleshooting.md`
- Verification commands:
  - `rg -n "MCP Server \\(Memory Operations\\)|MCP memory tools fail" docs/provider-quickstarts.md docs/troubleshooting.md`

### CPB-0227 – Add QA scenarios for "■ stream disconnected before completion: stream closed before response.completed" including stream/non-stream parity and edge-case payloads.
- Status: `implemented`
- Theme: `responses-and-chat-compat`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1407`
- Rationale:
  - Added explicit stream/non-stream regression tests that reproduce upstream stream closure before `response.completed`.
  - Hardened `ExecuteStream` to fail loudly (408 statusErr) when the stream ends without completion event.
- Implemented artifacts:
  - `pkg/llmproxy/executor/codex_executor.go`
  - `pkg/llmproxy/executor/codex_executor_cpb0227_test.go`
- Verification commands:
  - `go test ./pkg/llmproxy/executor -run 'CPB0227|CPB0106' -count=1` (currently blocked by pre-existing compile error in `pkg/llmproxy/executor/claude_executor_test.go`)

### CPB-0228 – Port relevant thegent-managed flow implied by "Bug: /v1/responses returns 400 "Input must be a list" when input is string (regression 6.7.42, Droid auto-compress broken)" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Status: `implemented`
- Theme: `go-cli-extraction`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1403`
- Rationale:
  - Added regression coverage for `/v1/responses` string-input normalization to list form in Codex translation.
  - Added regression coverage for compaction fields (`previous_response_id`, `prompt_cache_key`, `safety_identifier`) when string input is used.
- Implemented artifacts:
  - `pkg/llmproxy/translator/codex/openai/responses/codex_openai-responses_request_test.go`
- Verification commands:
  - `go test ./pkg/llmproxy/translator/codex/openai/responses -run 'CPB0228|ConvertOpenAIResponsesRequestToCodex' -count=1`

### CPB-0229 – Ensure rollout safety for "Factory Droid CLI got 404" via feature flags, staged defaults, and migration notes.
- Status: `in_progress`
- Theme: `thinking-and-reasoning`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1401`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0229" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0230 – Define non-subprocess integration path related to "反代反重力的 claude 在 opencode 中使用出现 unexpected EOF 错误" (Go bindings surface + HTTP fallback contract + version negotiation).
- Status: `in_progress`
- Theme: `integration-api-bindings`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1400`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0230" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/...` (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0231 – Follow up on "Feature request: Cursor CLI support" by closing compatibility gaps and preventing regressions in adjacent providers.
- Status: `in_progress`
- Theme: `oauth-and-authentication`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1399`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0231" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0232 – Add process-compose/HMR refresh workflow tied to "bug: Invalid signature in thinking block (API 400) on follow-up requests" so local config and runtime can be reloaded deterministically.
- Status: `in_progress`
- Theme: `dev-runtime-refresh`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1398`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0232" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0233 – Operationalize "在 Visual Studio Code无法使用过工具" with observability, alerting thresholds, and runbook updates.
- Status: `in_progress`
- Theme: `error-handling-retries`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1405`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0233" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0234 – Convert "Vertex AI global 区域端点 URL 格式错误，导致无法访问 Gemini 3 Preview 模型" into a provider-agnostic pattern and codify in shared translation utilities.
- Status: `in_progress`
- Theme: `general-polish`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1395`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0234" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0235 – Add DX polish around "Session title generation fails for Claude models via Antigravity provider (OpenCode)" through improved command ergonomics and faster feedback loops.
- Status: `in_progress`
- Theme: `responses-and-chat-compat`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1394`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0235" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

## Evidence & Commands Run

- `rg -n "CPB-0176|CPB-0245" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`
- `go test ./pkg/llmproxy/executor -run 'CPB0227|CPB0106' -count=1` (fails due to pre-existing compile error in `pkg/llmproxy/executor/claude_executor_test.go:237`)
- `go test ./pkg/llmproxy/translator/codex/openai/responses -run 'CPB0228|ConvertOpenAIResponsesRequestToCodex' -count=1`
- `go test ./pkg/llmproxy/translator/openai/openai/responses -run 'ConvertOpenAIResponsesRequestToOpenAIChatCompletions' -count=1`
- `rg -n "MCP Server \\(Memory Operations\\)|MCP memory tools fail" docs/provider-quickstarts.md docs/troubleshooting.md`
- `rg -n "CPB0227|CPB0228" pkg/llmproxy/executor/codex_executor_cpb0227_test.go pkg/llmproxy/translator/codex/openai/responses/codex_openai-responses_request_test.go`

## Next Actions
- Unblock `go test ./pkg/llmproxy/executor` package compilation by fixing the unrelated `CloakConfig.CacheUserID` test fixture mismatch in `pkg/llmproxy/executor/claude_executor_test.go`.
- After executor package compile is green, rerun `go test ./pkg/llmproxy/executor -run 'CPB0227|CPB0106' -count=1` to capture a fully passing lane-6 evidence set.
