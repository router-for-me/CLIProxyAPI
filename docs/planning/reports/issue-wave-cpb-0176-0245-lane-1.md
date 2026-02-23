# Issue Wave CPB-0176..0245 Lane 1 Report

## Scope

- Lane: lane-1
- Worktree: `/Users/kooshapari/temp-PRODVERCEL/485/kush/cliproxyapi-plusplus-wave-cpb4-1`
- Window: `CPB-0176` to `CPB-0185`

## Status Snapshot

- `planned`: 0
- `implemented`: 6
- `in_progress`: 4
- `blocked`: 0

## Per-Item Status

### CPB-0176 – Expand docs and examples for "After logging in with iFlowOAuth, most models cannot be used, only non-CLI models can be used." with copy-paste quickstart and troubleshooting section.
- Status: `implemented`
- Theme: `provider-model-registry`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1499`
- Rationale:
  - Added iFlow OAuth model-visibility quickstart guidance with explicit `/v1/models` checks.
  - Added troubleshooting and operator runbook paths for "OAuth success but only non-CLI subset available".
- Evidence:
  - `docs/provider-quickstarts.md`
  - `docs/troubleshooting.md`
  - `docs/provider-operations.md`
- Verification commands:
  - `rg -n "iFlow OAuth|non-CLI subset|\\^iflow/" docs/provider-quickstarts.md docs/troubleshooting.md docs/provider-operations.md`

### CPB-0177 – Add QA scenarios for "为什么我请求了很多次,但是使用统计里仍然显示使用为0呢?" including stream/non-stream parity and edge-case payloads.
- Status: `implemented`
- Theme: `websocket-and-streaming`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1497`
- Rationale:
  - Added stream/non-stream usage parsing tests for OpenAI chat and responses SSE payloads.
  - Added documentation parity probes for usage-zero symptom triage.
- Evidence:
  - `pkg/llmproxy/runtime/executor/usage_helpers_test.go`
  - `docs/provider-quickstarts.md`
  - `docs/troubleshooting.md`
  - `docs/provider-operations.md`
- Verification commands:
  - `go test ./pkg/llmproxy/runtime/executor -run 'ParseOpenAI(StreamUsageSSE|StreamUsageNoUsage|ResponsesStreamUsageSSE|ResponsesUsageTotalFallback)' -count=1`

### CPB-0178 – Refactor implementation behind "为什么配额管理里没有claude pro账号的额度?" to reduce complexity and isolate transformation boundaries.
- Status: `in_progress`
- Theme: `general-polish`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1496`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0178" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0179 – Ensure rollout safety for "最近几个版本，好像轮询失效了" via feature flags, staged defaults, and migration notes.
- Status: `in_progress`
- Theme: `websocket-and-streaming`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1495`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0179" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0180 – Standardize metadata and naming conventions touched by "iFlow error" across both repos.
- Status: `implemented`
- Theme: `error-handling-retries`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1494`
- Rationale:
  - Canonicalized iFlow metadata naming to `expires_at` in runtime refresh paths, SDK auth creation path, and management auth-file responses.
  - Updated iFlow refresh troubleshooting language to match canonical field name.
- Evidence:
  - `pkg/llmproxy/runtime/executor/iflow_executor.go`
  - `sdk/auth/iflow.go`
  - `pkg/llmproxy/api/handlers/management/auth_files.go`
  - `docs/operations/auth-refresh-failure-symptom-fix.md`
- Verification commands:
  - `rg -n "expires_at" pkg/llmproxy/runtime/executor/iflow_executor.go sdk/auth/iflow.go pkg/llmproxy/api/handlers/management/auth_files.go docs/operations/auth-refresh-failure-symptom-fix.md`

### CPB-0181 – Follow up on "Feature request [allow to configure RPM, TPM, RPD, TPD]" by closing compatibility gaps and preventing regressions in adjacent providers.
- Status: `in_progress`
- Theme: `provider-model-registry`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1493`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0181" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0182 – Harden "Antigravity using Ultra plan: Opus 4.6 gets 429 on CLIProxy but runs with Opencode-Auth" with clearer validation, safer defaults, and defensive fallbacks.
- Status: `in_progress`
- Theme: `thinking-and-reasoning`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1486`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0182" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0183 – Operationalize "gemini在cherry studio的openai接口无法控制思考长度" with observability, alerting thresholds, and runbook updates.
- Status: `implemented`
- Theme: `thinking-and-reasoning`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1484`
- Rationale:
  - Added troubleshooting matrix row for Gemini thinking-length control drift with deterministic checks.
  - Added operator runbook section including alert thresholds and mitigation runbook.
- Evidence:
  - `docs/troubleshooting.md`
  - `docs/provider-operations.md`
- Verification commands:
  - `rg -n "thinking-length control drift|processed thinking mode mismatch|thinking: original config from request|thinking: processed config to apply" docs/troubleshooting.md docs/provider-operations.md`

### CPB-0184 – Define non-subprocess integration path related to "codex5.3什么时候能获取到啊" (Go bindings surface + HTTP fallback contract + version negotiation).
- Status: `implemented`
- Theme: `integration-api-bindings`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1482`
- Rationale:
  - Extended SDK integration contract with codex5.3 capability negotiation guardrails.
  - Added operations + troubleshooting guidance for in-process-first integration and HTTP fallback checks.
- Evidence:
  - `docs/sdk-usage.md`
  - `docs/provider-operations.md`
  - `docs/troubleshooting.md`
- Verification commands:
  - `rg -n "codex 5.3|gpt-5.3-codex|non-subprocess|HTTP fallback" docs/sdk-usage.md docs/provider-operations.md docs/troubleshooting.md`

### CPB-0185 – Add DX polish around "Amp code doesn't route through CLIProxyAPI" through improved command ergonomics and faster feedback loops.
- Status: `implemented`
- Theme: `provider-model-registry`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1481`
- Rationale:
  - Added Amp-specific quickstart section with explicit proxy env, model canary, and routing sanity checks.
  - Added troubleshooting and runbook remediation for bypassed proxy traffic.
- Evidence:
  - `docs/provider-quickstarts.md`
  - `docs/troubleshooting.md`
  - `docs/provider-operations.md`
- Verification commands:
  - `rg -n "Amp|OPENAI_API_BASE|amp-route-check" docs/provider-quickstarts.md docs/troubleshooting.md docs/provider-operations.md`

## Evidence & Commands Run

- `rg -n "CPB-0176|CPB-0245" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`
- `go test ./pkg/llmproxy/runtime/executor -run 'ParseOpenAI(StreamUsageSSE|StreamUsageNoUsage|ResponsesStreamUsageSSE|ResponsesUsageTotalFallback)' -count=1`
- `rg -n "iFlow OAuth|usage parity|Amp Routing|codex 5.3" docs/provider-quickstarts.md docs/provider-operations.md docs/troubleshooting.md docs/sdk-usage.md`
- `go test ./pkg/llmproxy/runtime/executor -run 'IFlow|iflow' -count=1`
- `go test ./pkg/llmproxy/api/handlers/management -run 'IFlow|Auth' -count=1`

## Next Actions
- Continue CPB-0178..CPB-0183 with implementation changes in provider routing/metadata paths and update this lane report with per-item verification output.
