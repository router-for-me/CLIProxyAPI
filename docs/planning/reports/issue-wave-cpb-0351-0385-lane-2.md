# Issue Wave CPB-0351..CPB-0385 Lane 2 Report

## Scope

- Lane: lane-2
- Worktree: `/Users/kooshapari/temp-PRODVERCEL/485/kush/cliproxyapi-plusplus-workstream-cpb8-2`
- Window: `CPB-0356` to `CPB-0360`

## Status Snapshot

- `implemented`: 0
- `planned`: 0
- `in_progress`: 5
- `blocked`: 0

## Per-Item Status

### CPB-0356 – Expand docs and examples for "kiro使用orchestrator 模式调用的时候会报错400" with copy-paste quickstart and troubleshooting section.
- Status: `in_progress`
- Theme: `thinking-and-reasoning`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1120`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0356" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0357 – Create/refresh provider quickstart derived from "Error code: 400 - {'detail': 'Unsupported parameter: user'}" including setup, auth, model select, and sanity-check commands.
- Status: `in_progress`
- Theme: `docs-quickstarts`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1119`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0357" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0358 – Refactor implementation behind "添加智谱OpenAI兼容提供商获取模型和测试会失败" to reduce complexity and isolate transformation boundaries.
- Status: `in_progress`
- Theme: `websocket-and-streaming`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1118`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0358" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0359 – Ensure rollout safety for "gemini-3-pro-high (Antigravity): malformed_function_call error with tools" via feature flags, staged defaults, and migration notes.
- Status: `in_progress`
- Theme: `responses-and-chat-compat`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1113`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0359" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0360 – Standardize metadata and naming conventions touched by "该凭证暂无可用模型，这是被封号了的意思吗" across both repos.
- Status: `in_progress`
- Theme: `general-polish`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1111`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0360" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

## Evidence & Commands Run

- `rg -n 'CPB-0356|CPB-0360' docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
- No repository code changes were performed in this lane in this pass; planning only.


## Next Actions
- Move item by item from `planned` to `implemented` only when regression tests and code updates are committed.
