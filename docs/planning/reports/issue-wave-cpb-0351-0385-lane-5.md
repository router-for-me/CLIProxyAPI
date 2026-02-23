# Issue Wave CPB-0351..CPB-0385 Lane 5 Report

## Scope

- Lane: lane-5
- Worktree: `/Users/kooshapari/temp-PRODVERCEL/485/kush/cliproxyapi-plusplus-workstream-cpb8-5`
- Window: `CPB-0371` to `CPB-0375`

## Status Snapshot

- `implemented`: 3
- `planned`: 0
- `in_progress`: 2
- `blocked`: 0

## Per-Item Status

### CPB-0371 – Follow up on "Antigravity 生图无法指定分辨率" by closing compatibility gaps and preventing regressions in adjacent providers.
- Status: `in_progress`
- Theme: `thinking-and-reasoning`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1093`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0371" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0372 – Harden "文件写方式在docker下容易出现Inode变更问题" with clearer validation, safer defaults, and defensive fallbacks.
- Status: `in_progress`
- Theme: `oauth-and-authentication`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1092`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0372" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0373 – Operationalize "命令行中返回结果一切正常，但是在cherry studio中找不到模型" with observability, alerting thresholds, and runbook updates.
- Status: `implemented`
- Theme: `websocket-and-streaming`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1090`
- Rationale:
  - Added troubleshooting guidance for Cherry Studio model-visibility mismatch with explicit workspace filter checks.
  - Included deterministic remediation steps aligned with `/v1/models` inventory and workspace alias exposure.
- Proposed verification commands:
  - `rg -n "Cherry Studio can't find the model even though CLI runs succeed" docs/troubleshooting.md`
  - `rg -n "CPB-0373" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`
- Next action: none for this item.

### CPB-0374 – Create/refresh provider quickstart derived from "[Feedback #1044] 尝试通过 Payload 设置 Gemini 3 宽高比失败 (Google API 400 Error)" including setup, auth, model select, and sanity-check commands.
- Status: `implemented`
- Theme: `docs-quickstarts`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1089`
- Rationale:
  - Added dedicated Gemini 3 aspect-ratio quickstart with concrete `imageConfig` payload and failure diagnosis.
  - Included copy-paste check flow for `INVALID_IMAGE_CONFIG` and ratio/dimension consistency guidance.
- Proposed verification commands:
  - `rg -n "Gemini 3 Aspect Ratio Quickstart \\(CPB-0374\\)" docs/provider-quickstarts.md`
  - `rg -n "CPB-0374" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`
- Next action: none for this item.

### CPB-0375 – Add DX polish around "反重力2API opus模型 Error searching files" through improved command ergonomics and faster feedback loops.
- Status: `implemented`
- Theme: `websocket-and-streaming`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1086`
- Rationale:
  - Added troubleshooting entry with reproducible checks for `Error searching files` and translator/tool schema mismatch analysis.
  - Captured operator-focused remediation steps for search tool alias/schema registration before retry.
- Proposed verification commands:
  - `rg -n "Antigravity 2 API Opus model returns Error searching files" docs/troubleshooting.md`
  - `rg -n "CPB-0375" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`
- Next action: none for this item.

## Evidence & Commands Run

- `rg -n 'CPB-0371|CPB-0375' docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
- `rg -n "Cherry Studio can't find the model even though CLI runs succeed|Antigravity 2 API Opus model returns Error searching files" docs/troubleshooting.md`
- `rg -n "Gemini 3 Aspect Ratio Quickstart \\(CPB-0374\\)" docs/provider-quickstarts.md`


## Next Actions
- Continue in-progress items (`CPB-0371`, `CPB-0372`) in next tranche.
