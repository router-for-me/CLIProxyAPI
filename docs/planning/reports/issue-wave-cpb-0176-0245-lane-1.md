# Issue Wave CPB-0176..0245 Lane 1 Report

## Scope

- Lane: lane-1
- Worktree: `/Users/kooshapari/temp-PRODVERCEL/485/kush/cliproxyapi-plusplus-wave-cpb4-1`
- Window: `CPB-0176` to `CPB-0185`

## Status Snapshot

- `planned`: 10
- `implemented`: 0
- `in_progress`: 0
- `blocked`: 0

## Per-Item Status

### CPB-0176 – Expand docs and examples for "After logging in with iFlowOAuth, most models cannot be used, only non-CLI models can be used." with copy-paste quickstart and troubleshooting section.
- Status: `planned`
- Theme: `provider-model-registry`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1499`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0176" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0177 – Add QA scenarios for "为什么我请求了很多次,但是使用统计里仍然显示使用为0呢?" including stream/non-stream parity and edge-case payloads.
- Status: `planned`
- Theme: `websocket-and-streaming`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1497`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0177" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0178 – Refactor implementation behind "为什么配额管理里没有claude pro账号的额度?" to reduce complexity and isolate transformation boundaries.
- Status: `planned`
- Theme: `general-polish`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1496`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0178" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0179 – Ensure rollout safety for "最近几个版本，好像轮询失效了" via feature flags, staged defaults, and migration notes.
- Status: `planned`
- Theme: `websocket-and-streaming`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1495`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0179" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0180 – Standardize metadata and naming conventions touched by "iFlow error" across both repos.
- Status: `planned`
- Theme: `error-handling-retries`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1494`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0180" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/...` (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0181 – Follow up on "Feature request [allow to configure RPM, TPM, RPD, TPD]" by closing compatibility gaps and preventing regressions in adjacent providers.
- Status: `planned`
- Theme: `provider-model-registry`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1493`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0181" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0182 – Harden "Antigravity using Ultra plan: Opus 4.6 gets 429 on CLIProxy but runs with Opencode-Auth" with clearer validation, safer defaults, and defensive fallbacks.
- Status: `planned`
- Theme: `thinking-and-reasoning`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1486`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0182" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0183 – Operationalize "gemini在cherry studio的openai接口无法控制思考长度" with observability, alerting thresholds, and runbook updates.
- Status: `planned`
- Theme: `thinking-and-reasoning`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1484`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0183" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0184 – Define non-subprocess integration path related to "codex5.3什么时候能获取到啊" (Go bindings surface + HTTP fallback contract + version negotiation).
- Status: `planned`
- Theme: `integration-api-bindings`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1482`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0184" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0185 – Add DX polish around "Amp code doesn't route through CLIProxyAPI" through improved command ergonomics and faster feedback loops.
- Status: `planned`
- Theme: `provider-model-registry`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1481`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0185" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

## Evidence & Commands Run

- `rg -n "CPB-0176|CPB-0245" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`
- No repository code changes were performed in this lane in this pass; planning only.

## Next Actions
- Move item by item from `planned` to `implemented` only when regression tests and code updates are committed.