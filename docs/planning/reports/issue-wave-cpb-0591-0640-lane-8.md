# Issue Wave CPB-0591-0640 Lane 8 Report

## Scope
- Lane: lane-8
- Worktree: `/Users/kooshapari/temp-PRODVERCEL/485/kush/cliproxyapi-plusplus`
- Window: `CPB-0626` to `CPB-0630`

## Status Snapshot
- `implemented`: 0
- `planned`: 0
- `in_progress`: 5
- `blocked`: 0

## Per-Item Status

### CPB-0626 - Expand docs and examples for "Feature: Persist stats to disk (Docker-friendly) instead of in-memory only" with copy-paste quickstart and troubleshooting section.
- Status: `in_progress`
- Theme: `install-and-ops`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/681`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0626" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking` (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0627 - Port relevant thegent-managed flow implied by "Support developer role" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Status: `in_progress`
- Theme: `go-cli-extraction`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/680`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0627" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking` (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0628 - Refactor implementation behind "[Bug] Token counting endpoint /v1/messages/count_tokens significantly undercounts tokens" to reduce complexity and isolate transformation boundaries.
- Status: `in_progress`
- Theme: `thinking-and-reasoning`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/679`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0628" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking` (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0629 - Create/refresh provider quickstart derived from "[Feature] Automatic Censoring Logs" including setup, auth, model select, and sanity-check commands.
- Status: `in_progress`
- Theme: `docs-quickstarts`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/678`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0629" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking` (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0630 - Standardize metadata and naming conventions touched by "Translator: remove Copilot mention in OpenAI->Claude stream comment" across both repos.
- Status: `in_progress`
- Theme: `responses-and-chat-compat`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/677`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0630" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking` (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

## Evidence & Commands Run
- Pending command coverage for this planning-only wave.

## Next Actions
- Move item by item from `planned` to `implemented` only when code changes + regression evidence are available.
