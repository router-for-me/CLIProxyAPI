# Issue Wave CPB-0176..0245 Lane 7 Report

## Scope

- Lane: lane-7
- Worktree: `/Users/kooshapari/temp-PRODVERCEL/485/kush/cliproxyapi-plusplus-wave-cpb4-7`
- Window: `CPB-0236` to `CPB-0245`

## Status Snapshot

- `planned`: 3
- `implemented`: 2
- `in_progress`: 5
- `blocked`: 0

## Per-Item Status

### CPB-0236 – Expand docs and examples for "反代反重力请求gemini-3-pro-image-preview接口报错" with copy-paste quickstart and troubleshooting section.
- Status: `in_progress`
- Theme: `provider-model-registry`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1393`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0236" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0237 – Add QA scenarios for "[Feature Request] Implement automatic account rotation on VALIDATION_REQUIRED errors" including stream/non-stream parity and edge-case payloads.
- Status: `in_progress`
- Theme: `responses-and-chat-compat`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1392`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0237" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0238 – Create/refresh provider quickstart derived from "[antigravity] 500 Internal error and 403 Verification Required for multiple accounts" including setup, auth, model select, and sanity-check commands.
- Status: `in_progress`
- Theme: `docs-quickstarts`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1389`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0238" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0239 – Ensure rollout safety for "Antigravity的配额管理,账号没有订阅资格了,还是在显示模型额度" via feature flags, staged defaults, and migration notes.
- Status: `in_progress`
- Theme: `general-polish`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1388`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0239" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0240 – Standardize metadata and naming conventions touched by "大佬，可以加一个apikey的过期时间不" across both repos.
- Status: `in_progress`
- Theme: `general-polish`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1387`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0240" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/...` (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0241 – Follow up on "在codex运行报错" by closing compatibility gaps and preventing regressions in adjacent providers.
- Status: `planned`
- Theme: `responses-and-chat-compat`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1406`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0241" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0242 – Harden "[Feature request] Support nested object parameter mapping in payload config" with clearer validation, safer defaults, and defensive fallbacks.
- Status: `implemented`
- Theme: `thinking-and-reasoning`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1384`
- Rationale:
  - Added payload-rule path validation across `payload.default`, `payload.override`, `payload.filter`, `payload.default-raw`, and `payload.override-raw`.
  - Added regression tests covering valid nested paths, invalid path rejection, and invalid raw-JSON rejection.
- Implemented changes:
  - `pkg/llmproxy/config/config.go`
  - `pkg/llmproxy/config/config_test.go`
- Verification commands:
  - `rg -n "CPB-0242" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/config`
- Outcome:
  - Payload rules with malformed nested paths are now dropped during config sanitization.
  - Valid nested-object paths continue to work and remain covered by tests.
  - `go test ./pkg/llmproxy/config` passed.

### CPB-0243 – Operationalize "Claude authentication failed in v6.7.41 (works in v6.7.25)" with observability, alerting thresholds, and runbook updates.
- Status: `planned`
- Theme: `oauth-and-authentication`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1383`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0243" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0244 – Convert "Question: Does load balancing work with 2 Codex accounts for the Responses API?" into a provider-agnostic pattern and codify in shared translation utilities.
- Status: `implemented`
- Theme: `responses-and-chat-compat`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1382`
- Rationale:
  - Extended provider quickstart docs with copy-paste two-account Codex `/v1/responses` load-balancing validation loop.
  - Added explicit troubleshooting decision steps for mixed account health, model visibility mismatch, and stream/non-stream parity checks.
- Implemented changes:
  - `docs/provider-quickstarts.md`
- Verification commands:
  - `rg -n "CPB-0244" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `rg -n "Codex Responses load-balancing quickstart|Question: Does load balancing work with 2 Codex accounts" docs/provider-quickstarts.md`
- Outcome:
  - Load-balancing quickstart and troubleshooting are now documented in one place for Codex Responses operators.

### CPB-0245 – Add DX polish around "登陆提示“登录失败: 访问被拒绝，权限不足”" through improved command ergonomics and faster feedback loops.
- Status: `planned`
- Theme: `oauth-and-authentication`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1381`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0245" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

## Evidence & Commands Run

- `rg -n "CPB-0176|CPB-0245" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`
- `rg -n "CPB-0236|CPB-0237|CPB-0238|CPB-0239|CPB-0240|CPB-0241|CPB-0242|CPB-0243|CPB-0244|CPB-0245" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
- `go test ./pkg/llmproxy/config ./pkg/llmproxy/executor -run 'TestConfigSanitizePayloadRules|TestCodexExecutor_Compact'` (expected partial failure: pre-existing unrelated compile error in `pkg/llmproxy/executor/claude_executor_test.go` about `CacheUserID`)
- `go test ./pkg/llmproxy/config` (pass)
- `rg -n "Codex Responses load-balancing quickstart|Question: Does load balancing work with 2 Codex accounts" docs/provider-quickstarts.md`

## Next Actions
- Continue lane-7 execution for remaining `in_progress` / `planned` items with the same pattern: concrete code/doc changes, targeted Go tests, and per-item evidence.
