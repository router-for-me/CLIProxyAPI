# Issue Wave CPB-0591-0640 Lane 7 Report

## Scope
- Lane: lane-7
- Worktree: `/Users/kooshapari/temp-PRODVERCEL/485/kush/cliproxyapi-plusplus`
- Window: `CPB-0621` to `CPB-0625`

## Status Snapshot
- `implemented`: 0
- `planned`: 0
- `in_progress`: 5
- `blocked`: 0

## Per-Item Status

### CPB-0621 - Define non-subprocess integration path related to "[功能请求] 能不能给每个号单独配置代理？" (Go bindings surface + HTTP fallback contract + version negotiation).
- Status: `in_progress`
- Theme: `integration-api-bindings`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/690`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0621" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking` (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0622 - Harden "[Feature request] Add support for checking remaining Antigravity quota" with clearer validation, safer defaults, and defensive fallbacks.
- Status: `in_progress`
- Theme: `general-polish`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/687`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0622" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking` (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0623 - Operationalize "Feature Request: Priority-based Auth Selection for Specific Models" with observability, alerting thresholds, and runbook updates.
- Status: `in_progress`
- Theme: `provider-model-registry`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/685`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0623" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking` (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0624 - Convert "Update Gemini 3 model names: remove -preview suffix for gemini-3-pro and gemini-3-flash" into a provider-agnostic pattern and codify in shared translation utilities.
- Status: `in_progress`
- Theme: `provider-model-registry`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/683`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0624" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking` (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0625 - Add DX polish around "Frequent Tool-Call Failures with Gemini-2.5-pro in OpenAI-Compatible Mode" through improved command ergonomics and faster feedback loops.
- Status: `in_progress`
- Theme: `responses-and-chat-compat`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/682`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0625" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking` (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

## Evidence & Commands Run
- Pending command coverage for this planning-only wave.

## Next Actions
- Move item by item from `planned` to `implemented` only when code changes + regression evidence are available.
