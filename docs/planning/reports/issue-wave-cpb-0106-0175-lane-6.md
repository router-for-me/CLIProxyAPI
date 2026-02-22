# Issue Wave CPB-0106..0175 Lane 6 Report



## Scope

- Lane: lane-6
- Worktree: `/Users/kooshapari/temp-PRODVERCEL/485/kush/cliproxyapi-plusplus-wave-cpb3-6`
- Window: `CPB-0156` to `CPB-0165`

## Status Snapshot

- `in_progress`: 10/10 items reviewed
- `planned`: 10
- `implemented`: 0
- `blocked`: 0

## Per-Item Status

### CPB-0156 – Expand docs and examples for "Invalid JSON payload received: Unknown name \"deprecated\"" with copy-paste quickstart and troubleshooting section.
- Status: `planned`
- Theme: `responses-and-chat-compat`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1531`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires targeted fixture capture and acceptance-path parity tests before safe implementation.
- Proposed verification commands:
  - `rg -n "CPB-0156" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/... ./cmd/... ./sdk/...` (after implementation or fixture updates).
- Next action: create minimal reproducible payload/regression case and implement in the assigned `cpb3-6` worktree.

### CPB-0157 – Add QA scenarios for "bug: proxy_ prefix applied to tool_choice.name but not tools[].name causes 400 errors on OAuth requests" including stream/non-stream parity and edge-case payloads.
- Status: `planned`
- Theme: `thinking-and-reasoning`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1530`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires targeted fixture capture and acceptance-path parity tests before safe implementation.
- Proposed verification commands:
  - `rg -n "CPB-0157" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/... ./cmd/... ./sdk/...` (after implementation or fixture updates).
- Next action: create minimal reproducible payload/regression case and implement in the assigned `cpb3-6` worktree.

### CPB-0158 – Refactor implementation behind "请求为Windows添加启动自动更新命令" to reduce complexity and isolate transformation boundaries.
- Status: `planned`
- Theme: `general-polish`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1528`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires targeted fixture capture and acceptance-path parity tests before safe implementation.
- Proposed verification commands:
  - `rg -n "CPB-0158" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/... ./cmd/... ./sdk/...` (after implementation or fixture updates).
- Next action: create minimal reproducible payload/regression case and implement in the assigned `cpb3-6` worktree.

### CPB-0159 – Ensure rollout safety for "反重力逻辑加载失效" via feature flags, staged defaults, and migration notes.
- Status: `planned`
- Theme: `websocket-and-streaming`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1526`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires targeted fixture capture and acceptance-path parity tests before safe implementation.
- Proposed verification commands:
  - `rg -n "CPB-0159" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/... ./cmd/... ./sdk/...` (after implementation or fixture updates).
- Next action: create minimal reproducible payload/regression case and implement in the assigned `cpb3-6` worktree.

### CPB-0160 – Standardize metadata and naming conventions touched by "support openai image generations api(/v1/images/generations)" across both repos.
- Status: `planned`
- Theme: `general-polish`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1525`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires targeted fixture capture and acceptance-path parity tests before safe implementation.
- Proposed verification commands:
  - `rg -n "CPB-0160" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/... ./cmd/... ./sdk/...` (after implementation or fixture updates).
- Next action: create minimal reproducible payload/regression case and implement in the assigned `cpb3-6` worktree.

### CPB-0161 – Define non-subprocess integration path related to "The account has available credit, but a 503 or 429 error is occurring." (Go bindings surface + HTTP fallback contract + version negotiation).
- Status: `planned`
- Theme: `integration-api-bindings`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1521`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires targeted fixture capture and acceptance-path parity tests before safe implementation.
- Proposed verification commands:
  - `rg -n "CPB-0161" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/... ./cmd/... ./sdk/...` (after implementation or fixture updates).
- Next action: create minimal reproducible payload/regression case and implement in the assigned `cpb3-6` worktree.

### CPB-0162 – Harden "openclaw调用CPA 中的codex5.2 报错。" with clearer validation, safer defaults, and defensive fallbacks.
- Status: `planned`
- Theme: `thinking-and-reasoning`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1517`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires targeted fixture capture and acceptance-path parity tests before safe implementation.
- Proposed verification commands:
  - `rg -n "CPB-0162" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/... ./cmd/... ./sdk/...` (after implementation or fixture updates).
- Next action: create minimal reproducible payload/regression case and implement in the assigned `cpb3-6` worktree.

### CPB-0163 – Operationalize "opus4.6都支持1m的上下文了，请求体什么时候从280K调整下，现在也太小了，动不动就报错" with observability, alerting thresholds, and runbook updates.
- Status: `planned`
- Theme: `general-polish`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1515`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires targeted fixture capture and acceptance-path parity tests before safe implementation.
- Proposed verification commands:
  - `rg -n "CPB-0163" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/... ./cmd/... ./sdk/...` (after implementation or fixture updates).
- Next action: create minimal reproducible payload/regression case and implement in the assigned `cpb3-6` worktree.

### CPB-0164 – Convert "Token refresh logic fails with generic 500 error ("server busy") from iflow provider" into a provider-agnostic pattern and codify in shared translation utilities.
- Status: `planned`
- Theme: `thinking-and-reasoning`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1514`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires targeted fixture capture and acceptance-path parity tests before safe implementation.
- Proposed verification commands:
  - `rg -n "CPB-0164" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/... ./cmd/... ./sdk/...` (after implementation or fixture updates).
- Next action: create minimal reproducible payload/regression case and implement in the assigned `cpb3-6` worktree.

### CPB-0165 – Add DX polish around "bug: Nullable type arrays in tool schemas cause 400 error on Antigravity/Droid Factory" through improved command ergonomics and faster feedback loops.
- Status: `planned`
- Theme: `responses-and-chat-compat`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1513`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires targeted fixture capture and acceptance-path parity tests before safe implementation.
- Proposed verification commands:
  - `rg -n "CPB-0165" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/... ./cmd/... ./sdk/...` (after implementation or fixture updates).
- Next action: create minimal reproducible payload/regression case and implement in the assigned `cpb3-6` worktree.

## Evidence & Commands Run

- `rg -n "CPB-0106|CPB-0175" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`
- No repository code changes were performed in this lane pass; reports only.

## Next Actions

- Move item by item from `planned` to `implemented` only when fixture + tests + code/docs change are committed.
