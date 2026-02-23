# Issue Wave CPB-0176..0245 Lane 3 Report

## Scope

- Lane: lane-3
- Worktree: `/Users/kooshapari/temp-PRODVERCEL/485/kush/cliproxyapi-plusplus-wave-cpb4-3`
- Window: `CPB-0196` to `CPB-0205`

## Status Snapshot

- `planned`: 0
- `implemented`: 2
- `in_progress`: 8
- `blocked`: 0

## Per-Item Status

### CPB-0196 – Expand docs and examples for "为啥openai的端点可以添加多个密钥，但是a社的端点不能添加" with copy-paste quickstart and troubleshooting section.
- Status: `in_progress`
- Theme: `general-polish`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1457`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0196" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0197 – Add QA scenarios for "轮询会无差别轮询即便某个账号在很久前已经空配额" including stream/non-stream parity and edge-case payloads.
- Status: `in_progress`
- Theme: `websocket-and-streaming`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1456`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0197" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0198 – Refactor implementation behind "When I don’t add the authentication file, opening Claude Code keeps throwing a 500 error, instead of directly using the AI provider I’ve configured." to reduce complexity and isolate transformation boundaries.
- Status: `in_progress`
- Theme: `provider-model-registry`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1455`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0198" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0199 – Ensure rollout safety for "6.7.53版本反重力无法看到opus-4.6模型" via feature flags, staged defaults, and migration notes.
- Status: `in_progress`
- Theme: `oauth-and-authentication`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1453`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0199" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0200 – Standardize metadata and naming conventions touched by "Codex OAuth failed" across both repos.
- Status: `in_progress`
- Theme: `oauth-and-authentication`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1451`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0200" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/...` (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0201 – Follow up on "Google asking to Verify account" by closing compatibility gaps and preventing regressions in adjacent providers.
- Status: `in_progress`
- Theme: `responses-and-chat-compat`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1447`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0201" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0202 – Harden "API Error" with clearer validation, safer defaults, and defensive fallbacks.
- Status: `implemented`
- Theme: `responses-and-chat-compat`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1445`
- Rationale:
  - Hardened error envelope validation so arbitrary JSON error payloads without top-level `error` are normalized into OpenAI-compatible error format.
  - Added regression tests to lock expected behavior for passthrough envelope JSON vs non-envelope JSON wrapping.
- Verification commands:
  - `go test ./sdk/api/handlers -run 'TestBuildErrorResponseBody|TestWriteErrorResponse' -count=1`
- Evidence:
  - `sdk/api/handlers/handlers.go`
  - `sdk/api/handlers/handlers_build_error_response_test.go`

### CPB-0203 – Add process-compose/HMR refresh workflow tied to "Unable to use GPT 5.3 codex (model_not_found)" so local config and runtime can be reloaded deterministically.
- Status: `in_progress`
- Theme: `dev-runtime-refresh`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1443`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0203" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0204 – Create/refresh provider quickstart derived from "gpt-5.3-codex 请求400 显示不存在该模型" including setup, auth, model select, and sanity-check commands.
- Status: `in_progress`
- Theme: `docs-quickstarts`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1442`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0204" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0205 – Add DX polish around "The requested model 'gpt-5.3-codex' does not exist." through improved command ergonomics and faster feedback loops.
- Status: `implemented`
- Theme: `responses-and-chat-compat`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1441`
- Rationale:
  - Improved `404 model_not_found` error messaging to append a deterministic discovery hint (`GET /v1/models`) when upstream/translated message indicates unknown model.
  - Added regression coverage for `gpt-5.3-codex does not exist` path to ensure hint remains present.
- Verification commands:
  - `go test ./sdk/api/handlers -run 'TestBuildErrorResponseBody|TestWriteErrorResponse' -count=1`
  - `go test ./sdk/api/handlers/openai -run 'TestHandleErrorAsOpenAIError' -count=1`
- Evidence:
  - `sdk/api/handlers/handlers.go`
  - `sdk/api/handlers/handlers_build_error_response_test.go`

## Evidence & Commands Run

- `rg -n "CPB-0176|CPB-0245" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`
- `gofmt -w sdk/api/handlers/handlers.go sdk/api/handlers/handlers_build_error_response_test.go`
- `go test ./sdk/api/handlers -run 'TestBuildErrorResponseBody|TestWriteErrorResponse' -count=1`
  - Result: `ok  	github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers	1.651s`
- `go test ./sdk/api/handlers/openai -run 'TestHandleErrorAsOpenAIError' -count=1`
  - Result: `ok  	github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers/openai	1.559s [no tests to run]`

## Next Actions
- Continue CPB-0196/0197/0198/0199/0200/0201/0203/0204 with issue-grounded repro cases and targeted package tests per item.
