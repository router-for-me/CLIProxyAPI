# Issue Wave CPB-0106..0175 Lane 1 Report



## Scope

- Lane: lane-1
- Worktree: `/Users/kooshapari/temp-PRODVERCEL/485/kush/cliproxyapi-plusplus-wave-cpb3-1`
- Window: `CPB-0106` to `CPB-0115`

## Status Snapshot

- `in_progress`: 10/10 items reviewed
- `planned`: 10
- `implemented`: 0
- `blocked`: 0

## Per-Item Status

### CPB-0106 – Expand docs and examples for "[BUG] claude code 接入 cliproxyapi 使用时，模型的输出没有呈现流式，而是一下子蹦出来回答结果" with copy-paste quickstart and troubleshooting section.
- Status: `planned`
- Theme: `responses-and-chat-compat`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1620`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires targeted fixture capture and acceptance-path parity tests before safe implementation.
- Proposed verification commands:
  - `rg -n "CPB-0106" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/... ./cmd/... ./sdk/...` (after implementation or fixture updates).
- Next action: create minimal reproducible payload/regression case and implement in the assigned `cpb3-1` worktree.

### CPB-0107 – Add QA scenarios for "[Feature Request] Session-Aware Hybrid Routing Strategy" including stream/non-stream parity and edge-case payloads.
- Status: `planned`
- Theme: `oauth-and-authentication`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1617`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires targeted fixture capture and acceptance-path parity tests before safe implementation.
- Proposed verification commands:
  - `rg -n "CPB-0107" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/... ./cmd/... ./sdk/...` (after implementation or fixture updates).
- Next action: create minimal reproducible payload/regression case and implement in the assigned `cpb3-1` worktree.

### CPB-0108 – Refactor implementation behind "Any Plans to support Jetbrains IDE?" to reduce complexity and isolate transformation boundaries.
- Status: `planned`
- Theme: `responses-and-chat-compat`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1615`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires targeted fixture capture and acceptance-path parity tests before safe implementation.
- Proposed verification commands:
  - `rg -n "CPB-0108" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/... ./cmd/... ./sdk/...` (after implementation or fixture updates).
- Next action: create minimal reproducible payload/regression case and implement in the assigned `cpb3-1` worktree.

### CPB-0109 – Ensure rollout safety for "[bug] codex oauth登录流程失败" via feature flags, staged defaults, and migration notes.
- Status: `planned`
- Theme: `provider-model-registry`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1612`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires targeted fixture capture and acceptance-path parity tests before safe implementation.
- Proposed verification commands:
  - `rg -n "CPB-0109" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/... ./cmd/... ./sdk/...` (after implementation or fixture updates).
- Next action: create minimal reproducible payload/regression case and implement in the assigned `cpb3-1` worktree.

### CPB-0110 – Standardize metadata and naming conventions touched by "qwen auth 里获取到了 qwen3.5，但是 ai 客户端获取不到这个模型" across both repos.
- Status: `planned`
- Theme: `oauth-and-authentication`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1611`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires targeted fixture capture and acceptance-path parity tests before safe implementation.
- Proposed verification commands:
  - `rg -n "CPB-0110" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/... ./cmd/... ./sdk/...` (after implementation or fixture updates).
- Next action: create minimal reproducible payload/regression case and implement in the assigned `cpb3-1` worktree.

### CPB-0111 – Follow up on "fix: handle response.function_call_arguments.done in codex→claude streaming translator" by closing compatibility gaps and preventing regressions in adjacent providers.
- Status: `planned`
- Theme: `responses-and-chat-compat`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1609`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires targeted fixture capture and acceptance-path parity tests before safe implementation.
- Proposed verification commands:
  - `rg -n "CPB-0111" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/... ./cmd/... ./sdk/...` (after implementation or fixture updates).
- Next action: create minimal reproducible payload/regression case and implement in the assigned `cpb3-1` worktree.

### CPB-0112 – Harden "不能正确统计minimax-m2.5/kimi-k2.5的Token" with clearer validation, safer defaults, and defensive fallbacks.
- Status: `planned`
- Theme: `thinking-and-reasoning`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1607`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires targeted fixture capture and acceptance-path parity tests before safe implementation.
- Proposed verification commands:
  - `rg -n "CPB-0112" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/... ./cmd/... ./sdk/...` (after implementation or fixture updates).
- Next action: create minimal reproducible payload/regression case and implement in the assigned `cpb3-1` worktree.

### CPB-0113 – Operationalize "速速支持qwen code的qwen3.5" with observability, alerting thresholds, and runbook updates.
- Status: `planned`
- Theme: `general-polish`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1603`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires targeted fixture capture and acceptance-path parity tests before safe implementation.
- Proposed verification commands:
  - `rg -n "CPB-0113" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/... ./cmd/... ./sdk/...` (after implementation or fixture updates).
- Next action: create minimal reproducible payload/regression case and implement in the assigned `cpb3-1` worktree.

### CPB-0114 – Port relevant thegent-managed flow implied by "[Feature Request] Antigravity channel should support routing claude-haiku-4-5-20251001 model (used by Claude Code pre-flight checks)" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Status: `planned`
- Theme: `go-cli-extraction`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1596`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires targeted fixture capture and acceptance-path parity tests before safe implementation.
- Proposed verification commands:
  - `rg -n "CPB-0114" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/... ./cmd/... ./sdk/...` (after implementation or fixture updates).
- Next action: create minimal reproducible payload/regression case and implement in the assigned `cpb3-1` worktree.

### CPB-0115 – Define non-subprocess integration path related to "希望为提供商添加请求优先级功能，最好是以模型为基础来进行请求" (Go bindings surface + HTTP fallback contract + version negotiation).
- Status: `planned`
- Theme: `integration-api-bindings`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1594`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires targeted fixture capture and acceptance-path parity tests before safe implementation.
- Proposed verification commands:
  - `rg -n "CPB-0115" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/... ./cmd/... ./sdk/...` (after implementation or fixture updates).
- Next action: create minimal reproducible payload/regression case and implement in the assigned `cpb3-1` worktree.

## Evidence & Commands Run

- `rg -n "CPB-0106|CPB-0175" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`
- No repository code changes were performed in this lane pass; reports only.

## Next Actions

- Move item by item from `planned` to `implemented` only when fixture + tests + code/docs change are committed.
