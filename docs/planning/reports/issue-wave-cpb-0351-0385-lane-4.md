# Issue Wave CPB-0351..CPB-0385 Lane 4 Report

## Scope

- Lane: lane-4
- Worktree: `/Users/kooshapari/temp-PRODVERCEL/485/kush/cliproxyapi-plusplus-workstream-cpb8-4`
- Window: `CPB-0366` to `CPB-0370`

## Status Snapshot

- `implemented`: 2
- `planned`: 0
- `in_progress`: 3
- `blocked`: 0

## Per-Item Status

### CPB-0366 – Expand docs and examples for "ℹ ⚠️ Response stopped due to malformed function call. 在 Gemini CLI 中 频繁出现这个提示，对话中断" with copy-paste quickstart and troubleshooting section.
- Status: `in_progress`
- Theme: `websocket-and-streaming`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1100`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0366" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0367 – Add QA scenarios for "【功能请求】添加禁用项目按键（或优先级逻辑）" including stream/non-stream parity and edge-case payloads.
- Status: `implemented`
- Theme: `general-polish`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1098`
- Rationale:
  - Added explicit stream/non-stream parity and edge-case QA scenarios for disabled-project controls in provider quickstarts.
  - Included copy-paste curl payloads and log inspection guidance tied to `project_control.disable_button`.
- Proposed verification commands:
  - `rg -n "Disabled project button QA scenarios \\(CPB-0367\\)" docs/provider-quickstarts.md`
  - `rg -n "CPB-0367" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`
- Next action: none for this item.

### CPB-0368 – Define non-subprocess integration path related to "有支持豆包的反代吗" (Go bindings surface + HTTP fallback contract + version negotiation).
- Status: `in_progress`
- Theme: `integration-api-bindings`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1097`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0368" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0369 – Ensure rollout safety for "Wrong workspace selected for OpenAI accounts" via feature flags, staged defaults, and migration notes.
- Status: `implemented`
- Theme: `provider-model-registry`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1095`
- Rationale:
  - Added release-governance checklist item for workspace-selection mismatch with explicit runbook linkage.
  - Captured rollout guardrail requiring `/v1/models` workspace inventory validation before release lock.
- Proposed verification commands:
  - `rg -n "Workspace selection and OpenAI accounts \\(CPB-0369\\)" docs/operations/release-governance.md`
  - `rg -n "CPB-0369" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`
- Next action: none for this item.

### CPB-0370 – Standardize metadata and naming conventions touched by "Anthropic web_search fails in v6.7.x - invalid tool name web_search_20250305" across both repos.
- Status: `in_progress`
- Theme: `responses-and-chat-compat`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1094`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0370" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

## Evidence & Commands Run

- `rg -n 'CPB-0366|CPB-0370' docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
- `rg -n "Disabled project button QA scenarios \\(CPB-0367\\)" docs/provider-quickstarts.md`
- `rg -n "Workspace selection and OpenAI accounts \\(CPB-0369\\)" docs/operations/release-governance.md`


## Next Actions
- Continue in-progress items (`CPB-0366`, `CPB-0368`, `CPB-0370`) in next tranche.
