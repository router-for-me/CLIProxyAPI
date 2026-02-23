# Issue Wave CPB-0316..CPB-0350 Lane 2 Report

## Scope

- Lane: lane-2
- Worktree: `/Users/kooshapari/temp-PRODVERCEL/485/kush/cliproxyapi-plusplus-wave-cpb7-2`
- Window: `CPB-0321` to `CPB-0325`

## Status Snapshot

<<<<<<< HEAD
- `implemented`: 0
- `planned`: 0
- `in_progress`: 5
=======
- `implemented`: 5
- `planned`: 0
- `in_progress`: 0
>>>>>>> archive/pr-234-head-20260223
- `blocked`: 0

## Per-Item Status

### CPB-0321 – Follow up on "🚨🔥 CRITICAL BUG REPORT: Invalid Function Declaration Schema in API Request 🔥🚨" by closing compatibility gaps and preventing regressions in adjacent providers.
<<<<<<< HEAD
- Status: `in_progress`
- Theme: `provider-model-registry`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1189`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0321" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0322 – Define non-subprocess integration path related to "认证失败: Failed to exchange token" (Go bindings surface + HTTP fallback contract + version negotiation).
- Status: `in_progress`
- Theme: `integration-api-bindings`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1186`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0322" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0323 – Create/refresh provider quickstart derived from "Model combo support" including setup, auth, model select, and sanity-check commands.
- Status: `in_progress`
- Theme: `docs-quickstarts`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1184`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0323" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0324 – Convert "使用 Antigravity OAuth 使用openai格式调用opencode问题" into a provider-agnostic pattern and codify in shared translation utilities.
- Status: `in_progress`
- Theme: `oauth-and-authentication`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1173`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0324" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.

### CPB-0325 – Add DX polish around "今天中午开始一直429" through improved command ergonomics and faster feedback loops.
- Status: `in_progress`
- Theme: `error-handling-retries`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1172`
- Rationale:
  - Item remains `proposed` in the 1000-item execution board.
  - Requires implementation-ready acceptance criteria and target-path verification before execution.
- Proposed verification commands:
  - `rg -n "CPB-0325" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `go test ./pkg/llmproxy/api ./pkg/llmproxy/thinking`  (if implementation touches those surfaces)
- Next action: add reproducible payload/regression case, then implement in assigned workstream.
=======
- Status: `implemented`
- Theme: `provider-model-registry`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1189`
- Rationale:
  - Hardened Antigravity schema cleaning by removing invalid style-only tool declaration properties rejected by upstream validators.
  - Added regression test to verify invalid properties are stripped without breaking valid tool schema fields.
- Proposed verification commands:
  - `GOCACHE=$PWD/.cache/go-build go test ./pkg/llmproxy/util -run 'TestCleanJSONSchemaForAntigravity_RemovesInvalidToolProperties' -count=1`
  - `rg -n "CPB-0321" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`
- Next action: none for this item.

### CPB-0322 – Define non-subprocess integration path related to "认证失败: Failed to exchange token" (Go bindings surface + HTTP fallback contract + version negotiation).
- Status: `implemented`
- Theme: `integration-api-bindings`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1186`
- Rationale:
  - Added seam-based Gemini auth client factory for non-subprocess SDK login path so exchange-failure scenarios are testable without live OAuth calls.
  - Added regression coverage for exchange failure propagation and project ID passthrough in `GeminiAuthenticator.Login`.
- Proposed verification commands:
  - `GOCACHE=$PWD/.cache/go-build go test ./sdk/auth -run 'TestGeminiAuthenticatorLogin_' -count=1`
  - `rg -n "CPB-0322" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`
- Next action: none for this item.

### CPB-0323 – Create/refresh provider quickstart derived from "Model combo support" including setup, auth, model select, and sanity-check commands.
- Status: `implemented`
- Theme: `docs-quickstarts`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1184`
- Rationale:
  - Added `Model Combo Support (Alias Routing Quickstart)` section to provider quickstarts with concrete config and end-to-end curl verification.
  - Included setup, model selection, and deterministic sanity checks for mapped-source → target-model routing.
- Proposed verification commands:
  - `rg -n "Model Combo Support|model-mappings|force-model-mappings" docs/provider-quickstarts.md`
  - `rg -n "CPB-0323" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`
- Next action: none for this item.

### CPB-0324 – Convert "使用 Antigravity OAuth 使用openai格式调用opencode问题" into a provider-agnostic pattern and codify in shared translation utilities.
- Status: `implemented`
- Theme: `oauth-and-authentication`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1173`
- Rationale:
  - Unified OpenAI-to-Antigravity request conversion through shared OpenAI→Gemini→Antigravity pipeline.
  - Preserved Antigravity-specific wrapping while reducing divergence from Gemini compatibility paths.
- Proposed verification commands:
  - `GOCACHE=$PWD/.cache/go-build go test ./pkg/llmproxy/translator/antigravity/openai/chat-completions -count=1`
  - `rg -n "CPB-0324" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`
- Next action: none for this item.

### CPB-0325 – Add DX polish around "今天中午开始一直429" through improved command ergonomics and faster feedback loops.
- Status: `implemented`
- Theme: `error-handling-retries`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1172`
- Rationale:
  - Added `Retry-After` propagation from executor errors to API responses when passthrough headers are unavailable.
  - Added precedence guard so upstream passthrough `Retry-After` headers remain authoritative.
- Proposed verification commands:
  - `GOCACHE=$PWD/.cache/go-build go test ./sdk/api/handlers -run 'TestWriteErrorResponse_(RetryAfterFromError|AddonRetryAfterTakesPrecedence|AddonHeaders)' -count=1`
  - `rg -n "CPB-0325" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`
- Next action: none for this item.
>>>>>>> archive/pr-234-head-20260223

## Evidence & Commands Run

- `rg -n 'CPB-0321|CPB-0325' docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
<<<<<<< HEAD
- No repository code changes were performed in this lane in this pass; planning only.


## Next Actions
- Move item by item from `planned` to `implemented` only when regression tests and code updates are committed.
=======
- `GOCACHE=$PWD/.cache/go-build go test ./pkg/llmproxy/util -run 'TestCleanJSONSchemaForAntigravity_RemovesInvalidToolProperties' -count=1`
- `GOCACHE=$PWD/.cache/go-build go test ./pkg/llmproxy/translator/antigravity/openai/chat-completions -count=1`
- `GOCACHE=$PWD/.cache/go-build go test ./sdk/api/handlers -run 'TestWriteErrorResponse_(RetryAfterFromError|AddonRetryAfterTakesPrecedence|AddonHeaders)' -count=1`
- `GOCACHE=$PWD/.cache/go-build go test ./sdk/auth -run 'TestGeminiAuthenticatorLogin_' -count=1`
- `rg -n "Model Combo Support|model-mappings|force-model-mappings" docs/provider-quickstarts.md`


## Next Actions
- Lane complete for `CPB-0321`..`CPB-0325`.
>>>>>>> archive/pr-234-head-20260223
