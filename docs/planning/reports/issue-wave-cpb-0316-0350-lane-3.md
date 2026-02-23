# Issue Wave CPB-0316..CPB-0350 Lane 3 Report

## Scope

- Lane: lane-3
- Worktree: `/Users/kooshapari/temp-PRODVERCEL/485/kush/cliproxyapi-plusplus-wave-cpb7-3`
- Window: `CPB-0326` to `CPB-0330`

## Status Snapshot

<<<<<<< HEAD
- `implemented`: 0
- `planned`: 0
- `in_progress`: 5
=======
- `implemented`: 2
- `planned`: 0
- `in_progress`: 3
>>>>>>> archive/pr-234-head-20260223
- `blocked`: 0

## Per-Item Status

### CPB-0326 – Expand docs and examples for "gemini api 使用openai 兼容的url 使用时 tool_call 有问题" with copy-paste quickstart and troubleshooting section.
<<<<<<< HEAD
- Status: `in_progress`
- Theme: `thinking-and-reasoning`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1168`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0326" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.
=======
- Status: `implemented`
- Theme: `thinking-and-reasoning`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1168`
- Rationale:
  - Ensured Gemini→OpenAI non-stream conversion emits `tool_calls[].index` for every tool call entry.
  - Added regression coverage for multi-tool-call index ordering in OpenAI-compatible output.
- Proposed verification commands:
  - `GOCACHE=$PWD/.cache/go-build go test ./pkg/llmproxy/translator/gemini/openai/chat-completions -count=1`
  - `rg -n "CPB-0326" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`
- Next action: none for this item.
>>>>>>> archive/pr-234-head-20260223

### CPB-0327 – Add QA scenarios for "linux一键安装的如何更新" including stream/non-stream parity and edge-case payloads.
- Status: `in_progress`
- Theme: `install-and-ops`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1167`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0327" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0328 – Refactor implementation behind "新增微软copilot GPT5.2codex模型" to reduce complexity and isolate transformation boundaries.
- Status: `in_progress`
- Theme: `general-polish`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1166`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0328" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0329 – Ensure rollout safety for "Tool Calling Not Working in Cursor When Using Claude via CLIPROXYAPI + Antigravity Proxy" via feature flags, staged defaults, and migration notes.
- Status: `in_progress`
- Theme: `responses-and-chat-compat`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1165`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0329" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0330 – Standardize metadata and naming conventions touched by "[Improvement] Allow multiple model mappings to have the same Alias" across both repos.
<<<<<<< HEAD
- Status: `in_progress`
- Theme: `provider-model-registry`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1163`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0330" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.
=======
- Status: `implemented`
- Theme: `provider-model-registry`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1163`
- Rationale:
  - Existing `OAuthModelAlias` sanitizer already allows multiple aliases for one upstream model.
  - Added `CHANGELOG.md` note and preserved compatibility behavior via existing migration/sanitization tests.
- Verification commands:
  - `rg -n "CPB-0330" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/config -run OAuthModelAlias -count=1`
- Next action: proceed with remaining lane items in order.
>>>>>>> archive/pr-234-head-20260223

## Evidence & Commands Run

- `rg -n 'CPB-0326|CPB-0330' docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
<<<<<<< HEAD
- No repository code changes were performed in this lane in this pass; planning only.


## Next Actions
- Move item by item from `planned` to `implemented` only when regression tests and code updates are committed.
=======
- `go test ./pkg/llmproxy/config -run OAuthModelAlias -count=1`
- `GOCACHE=$PWD/.cache/go-build go test ./pkg/llmproxy/translator/gemini/openai/chat-completions -count=1`
- `CHANGELOG.md` updated for CPB-0330 compatibility note.


## Next Actions
- Continue in-progress items (`CPB-0327`..`CPB-0329`) in next tranche.
>>>>>>> archive/pr-234-head-20260223
