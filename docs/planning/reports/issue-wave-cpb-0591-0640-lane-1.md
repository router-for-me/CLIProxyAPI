# Issue Wave CPB-0591-0640 Lane 1 Report

## Scope
- Lane: lane-1
- Worktree: `/Users/kooshapari/temp-PRODVERCEL/485/kush/cliproxyapi-plusplus`
- Window: `CPB-0591` to `CPB-0595`

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

### CPB-0591 - Follow up on "Feature Request: Complete OpenAI Tool Calling Format Support for Claude Models (Cursor MCP Compatibility)" by closing compatibility gaps and preventing regressions in adjacent providers.
- Status: `in_progress`
- Theme: `responses-and-chat-compat`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/735`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0591" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking` (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0592 - Harden "Bug: /v1/responses endpoint does not correctly convert message format for Anthropic API" with clearer validation, safer defaults, and defensive fallbacks.
<<<<<<< HEAD
- Status: `in_progress`
=======
- Status: `implemented`
>>>>>>> archive/pr-234-head-20260223
- Theme: `responses-and-chat-compat`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/736`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
<<<<<<< HEAD
- Proposed verification commands:
  - `rg -n "CPB-0592" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking` (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0593 - Operationalize "请问有计划支持显示目前剩余额度吗" with observability, alerting thresholds, and runbook updates.
- Status: `in_progress`
=======
- Verified:
  - Commit: `aa1e2e2b`
  - Test: `go test ./pkg/llmproxy/translator/claude/openai/responses -run TestConvertOpenAIResponsesRequestToClaude`

### CPB-0593 - Operationalize "请问有计划支持显示目前剩余额度吗" with observability, alerting thresholds, and runbook updates.
- Status: `implemented`
>>>>>>> archive/pr-234-head-20260223
- Theme: `general-polish`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/734`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
<<<<<<< HEAD
- Proposed verification commands:
  - `rg -n "CPB-0593" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking` (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.
=======
- Verification:
  - `git diff --name-only HEAD~1 docs/api/management.md docs/provider-operations.md docs/troubleshooting.md`
  - `docs/api/management.md` includes the `GET /v0/management/kiro-quota` API and examples.
  - Manual review of management API usage and runbook examples in:
    - `docs/api/management.md`
    - `docs/provider-operations.md`
    - `docs/troubleshooting.md`
>>>>>>> archive/pr-234-head-20260223

### CPB-0594 - Convert "reasoning_content is null for extended thinking models (thinking goes to content instead)" into a provider-agnostic pattern and codify in shared translation utilities.
- Status: `in_progress`
- Theme: `thinking-and-reasoning`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/732`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0594" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking` (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0595 - Create/refresh provider quickstart derived from "Use actual Anthropic token counts instead of estimation for reasoning_tokens" including setup, auth, model select, and sanity-check commands.
- Status: `in_progress`
- Theme: `docs-quickstarts`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/731`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0595" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking` (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

## Evidence & Commands Run
- Pending command coverage for this planning-only wave.

## Next Actions
- Move item by item from `planned` to `implemented` only when code changes + regression evidence are available.
