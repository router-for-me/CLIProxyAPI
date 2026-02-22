# Issue Wave CPB-0421..CPB-0455 Lane 3 Report

## Scope
- Lane: lane-3
- Worktree: `/Users/kooshapari/temp-PRODVERCEL/485/kush/cliproxyapi-plusplus-workstream-cpb10-3`
- Window: `CPB-0431` to `CPB-0435`

## Status Snapshot

- `implemented`: 0
- `planned`: 0
- `in_progress`: 5
- `blocked`: 0

## Per-Item Status

### CPB-0431 – Follow up on "Management Usage report resets at restart" by closing compatibility gaps and preventing regressions in adjacent providers.
- Status: `in_progress`
- Theme: `provider-model-registry`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1013`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0431" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0432 – Harden "使用gemini-3-pro-image-preview 模型，生成不了图片" with clearer validation, safer defaults, and defensive fallbacks.
- Status: `in_progress`
- Theme: `websocket-and-streaming`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1012`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0432" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0433 – Operationalize "「建议」希望能添加一个手动控制某 oauth 认证是否参与反代的功能" with observability, alerting thresholds, and runbook updates.
- Status: `in_progress`
- Theme: `oauth-and-authentication`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1010`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0433" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0434 – Convert "[Bug] Missing mandatory tool_use.id in request payload causing failure on subsequent tool calls" into a provider-agnostic pattern and codify in shared translation utilities.
- Status: `in_progress`
- Theme: `thinking-and-reasoning`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1009`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0434" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0435 – Add process-compose/HMR refresh workflow tied to "添加openai v1 chat接口，使用responses调用，出现截断，最后几个字不显示" so local config and runtime can be reloaded deterministically.
- Status: `in_progress`
- Theme: `dev-runtime-refresh`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1008`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0435" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

## Evidence & Commands Run

- `rg -n 'CPB-0431|CPB-0435' docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
- No repository code changes were performed in this lane in this pass; planning only.

## Next Actions
- Move item by item from `planned` to `implemented` only when regression tests and code updates are committed.
