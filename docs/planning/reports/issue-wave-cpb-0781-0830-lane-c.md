# Issue Wave CPB-0781-0830 Lane C Report

- Lane: `C (cliproxyapi-plusplus)`
- Window: `CPB-0797` to `CPB-0804`
- Scope: triage-only report (no code edits)

## Per-Item Triage

### CPB-0797
- Title focus: Add QA scenarios for "token无计数" including stream/non-stream parity and edge-case payloads.
- Likely impacted paths:
  - `pkg/llmproxy/translator/gemini/openai/chat-completions`
  - `pkg/llmproxy/translator/antigravity/openai/responses`
  - `pkg/llmproxy/executor`
- Validation command: `rg -n "CPB-0797" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.md docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`

### CPB-0798
- Title focus: Port relevant thegent-managed flow implied by "cursor with antigravity" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Likely impacted paths:
  - `cmd`
  - `sdk/cliproxy`
  - `pkg/llmproxy/api/handlers/management`
- Validation command: `rg -n "CPB-0798" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.md docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`

### CPB-0799
- Title focus: Create/refresh provider quickstart derived from "认证未走代理" including setup, auth, model select, and sanity-check commands.
- Likely impacted paths:
  - `docs/provider-quickstarts.md`
  - `docs/troubleshooting.md`
  - `docs/planning/README.md`
- Validation command: `rg -n "CPB-0799" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.md docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`

### CPB-0800
- Title focus: Standardize metadata and naming conventions touched by "[Feature Request] Add --manual-callback mode for headless/remote OAuth (especially for users behind proxy / Clash TUN in China)" across both repos.
- Likely impacted paths:
  - `pkg/llmproxy/registry/model_registry.go`
  - `docs/operations/release-governance.md`
  - `docs/provider-quickstarts.md`
- Validation command: `rg -n "CPB-0800" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.md docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`

### CPB-0801
- Title focus: Follow up on "Regression: gemini-3-pro-preview unusable due to removal of 429 retry logic in d50b0f7" by closing compatibility gaps and preventing regressions in adjacent providers.
- Likely impacted paths:
  - `pkg/llmproxy/translator`
  - `pkg/llmproxy/executor`
  - `docs/troubleshooting.md`
- Validation command: `rg -n "CPB-0801" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.md docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`

### CPB-0802
- Title focus: Harden "Gemini 3 Pro no response in Roo Code with AI Studio setup" with clearer validation, safer defaults, and defensive fallbacks.
- Likely impacted paths:
  - `pkg/llmproxy/translator`
  - `pkg/llmproxy/executor`
  - `pkg/llmproxy/runtime/executor`
- Validation command: `rg -n "CPB-0802" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.md docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`

### CPB-0803
- Title focus: Operationalize "CLIProxyAPI error in huggingface" with observability, alerting thresholds, and runbook updates.
- Likely impacted paths:
  - `docs/operations`
  - `docs/troubleshooting.md`
  - `pkg/llmproxy/api/handlers/management`
- Validation command: `rg -n "CPB-0803" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.md docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`

### CPB-0804
- Title focus: Convert "Post "https://chatgpt.com/backend-api/codex/responses": Not Found" into a provider-agnostic pattern and codify in shared translation utilities.
- Likely impacted paths:
  - `pkg/llmproxy/translator`
  - `pkg/llmproxy/executor`
  - `pkg/llmproxy/runtime/executor`
- Validation command: `rg -n "CPB-0804" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.md docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`

## Verification

- `rg -n "CPB-0797|CPB-0804" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.md`
- `rg -n "quickstart|troubleshooting|stream|tool|reasoning|provider" docs/provider-quickstarts.md docs/troubleshooting.md`
- `go test ./pkg/llmproxy/translator/... -run "TestConvert|TestTranslate" -count=1`
