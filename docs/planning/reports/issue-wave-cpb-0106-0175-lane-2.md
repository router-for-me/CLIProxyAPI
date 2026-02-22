# Issue Wave CPB-0106..0175 Lane 2 Report



## Scope

- Lane: lane-2
- Worktree: `/Users/kooshapari/temp-PRODVERCEL/485/kush/cliproxyapi-plusplus-wave-cpb3-2`
- Window: `CPB-0116` to `CPB-0125`

## Status Snapshot

- `in_progress`: 10/10 items reviewed
- `planned`: 10
- `implemented`: 0
- `blocked`: 0

## Per-Item Status

### CPB-0116 – Add process-compose/HMR refresh workflow tied to "gpt-5.3-codex-spark error" so local config and runtime can be reloaded deterministically.
- Status: `planned`
- Theme: `dev-runtime-refresh`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1593`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires targeted fixture capture and acceptance-path parity tests before safe implementation.
- Proposed verification commands:
  - `rg -n "CPB-0116" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/... ./cmd/... ./sdk/...` (after implementation or fixture updates).
- Next action: create minimal reproducible payload/regression case and implement in the assigned `cpb3-2` worktree.

### CPB-0117 – Add QA scenarios for "[Bug] Claude Code 2.1.37 random cch in x-anthropic-billing-header causes severe prompt-cache miss on third-party upstreams" including stream/non-stream parity and edge-case payloads.
- Status: `planned`
- Theme: `thinking-and-reasoning`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1592`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires targeted fixture capture and acceptance-path parity tests before safe implementation.
- Proposed verification commands:
  - `rg -n "CPB-0117" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/... ./cmd/... ./sdk/...` (after implementation or fixture updates).
- Next action: create minimal reproducible payload/regression case and implement in the assigned `cpb3-2` worktree.

### CPB-0118 – Refactor implementation behind "()强制思考会在2m左右时返回500错误" to reduce complexity and isolate transformation boundaries.
- Status: `planned`
- Theme: `responses-and-chat-compat`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1591`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires targeted fixture capture and acceptance-path parity tests before safe implementation.
- Proposed verification commands:
  - `rg -n "CPB-0118" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/... ./cmd/... ./sdk/...` (after implementation or fixture updates).
- Next action: create minimal reproducible payload/regression case and implement in the assigned `cpb3-2` worktree.

### CPB-0119 – Create/refresh provider quickstart derived from "配额管理可以刷出额度，但是调用的时候提示额度不足" including setup, auth, model select, and sanity-check commands.
- Status: `planned`
- Theme: `docs-quickstarts`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1590`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires targeted fixture capture and acceptance-path parity tests before safe implementation.
- Proposed verification commands:
  - `rg -n "CPB-0119" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/... ./cmd/... ./sdk/...` (after implementation or fixture updates).
- Next action: create minimal reproducible payload/regression case and implement in the assigned `cpb3-2` worktree.

### CPB-0120 – Standardize metadata and naming conventions touched by "每次更新或者重启 使用统计数据都会清空" across both repos.
- Status: `planned`
- Theme: `general-polish`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1589`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires targeted fixture capture and acceptance-path parity tests before safe implementation.
- Proposed verification commands:
  - `rg -n "CPB-0120" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/... ./cmd/... ./sdk/...` (after implementation or fixture updates).
- Next action: create minimal reproducible payload/regression case and implement in the assigned `cpb3-2` worktree.

### CPB-0121 – Follow up on "iflow GLM 5 时不时会返回 406" by closing compatibility gaps and preventing regressions in adjacent providers.
- Status: `planned`
- Theme: `thinking-and-reasoning`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1588`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires targeted fixture capture and acceptance-path parity tests before safe implementation.
- Proposed verification commands:
  - `rg -n "CPB-0121" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/... ./cmd/... ./sdk/...` (after implementation or fixture updates).
- Next action: create minimal reproducible payload/regression case and implement in the assigned `cpb3-2` worktree.

### CPB-0122 – Harden "封号了，pro号没了，又找了个免费认证bot分享出来" with clearer validation, safer defaults, and defensive fallbacks.
- Status: `planned`
- Theme: `general-polish`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1587`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires targeted fixture capture and acceptance-path parity tests before safe implementation.
- Proposed verification commands:
  - `rg -n "CPB-0122" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/... ./cmd/... ./sdk/...` (after implementation or fixture updates).
- Next action: create minimal reproducible payload/regression case and implement in the assigned `cpb3-2` worktree.

### CPB-0123 – Operationalize "gemini-cli 不能自定请求头吗？" with observability, alerting thresholds, and runbook updates.
- Status: `planned`
- Theme: `cli-ux-dx`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1586`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires targeted fixture capture and acceptance-path parity tests before safe implementation.
- Proposed verification commands:
  - `rg -n "CPB-0123" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/... ./cmd/... ./sdk/...` (after implementation or fixture updates).
- Next action: create minimal reproducible payload/regression case and implement in the assigned `cpb3-2` worktree.

### CPB-0124 – Convert "bug: Invalid thinking block signature when switching from Gemini CLI to Claude OAuth mid-conversation" into a provider-agnostic pattern and codify in shared translation utilities.
- Status: `planned`
- Theme: `thinking-and-reasoning`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1584`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires targeted fixture capture and acceptance-path parity tests before safe implementation.
- Proposed verification commands:
  - `rg -n "CPB-0124" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/... ./cmd/... ./sdk/...` (after implementation or fixture updates).
- Next action: create minimal reproducible payload/regression case and implement in the assigned `cpb3-2` worktree.

### CPB-0125 – Add DX polish around "I saved 10M tokens (89%) on my Claude Code sessions with a CLI proxy" through improved command ergonomics and faster feedback loops.
- Status: `planned`
- Theme: `thinking-and-reasoning`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1583`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires targeted fixture capture and acceptance-path parity tests before safe implementation.
- Proposed verification commands:
  - `rg -n "CPB-0125" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/... ./cmd/... ./sdk/...` (after implementation or fixture updates).
- Next action: create minimal reproducible payload/regression case and implement in the assigned `cpb3-2` worktree.

## Evidence & Commands Run

- `rg -n "CPB-0106|CPB-0175" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`
- No repository code changes were performed in this lane pass; reports only.

## Next Actions

- Move item by item from `planned` to `implemented` only when fixture + tests + code/docs change are committed.
