# Issue Wave CPB-0781-0830 Lane D Report

- Lane: `D (cliproxyapi-plusplus)`
- Window: `CPB-0805` to `CPB-0812`
- Scope: triage-only report (no code edits)

## Items

### CPB-0805
- Title focus: Define non-subprocess integration path related to "Feature: Add Image Support for Gemini 3" (Go bindings surface + HTTP fallback contract + version negotiation).
- Likely impacted paths:
  - `cmd`
  - `sdk/cliproxy`
  - `pkg/llmproxy/api/handlers/management`
- Validation command: `rg -n "CPB-0805|CPB-0805" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.md docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`

### CPB-0806
- Title focus: Expand docs and examples for "Bug: Gemini 3 Thinking Budget requires normalization in CLI Translator" with copy-paste quickstart and troubleshooting section.
- Likely impacted paths:
  - `docs/provider-quickstarts.md`
  - `docs/troubleshooting.md`
  - `docs/planning/README.md`
- Validation command: `rg -n "CPB-0806|CPB-0806" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.md docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`

### CPB-0807
- Title focus: Add QA scenarios for "Feature Request: Support for Gemini 3 Pro Preview" including stream/non-stream parity and edge-case payloads.
- Likely impacted paths:
  - `pkg/llmproxy/translator/gemini/openai/chat-completions`
  - `pkg/llmproxy/translator/antigravity/openai/responses`
  - `pkg/llmproxy/executor`
- Validation command: `rg -n "CPB-0807|CPB-0807" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.md docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`

### CPB-0808
- Title focus: Refactor implementation behind "[Suggestion] Improve Prompt Caching - Don't do round-robin for all every request" to reduce complexity and isolate transformation boundaries.
- Likely impacted paths:
  - `pkg/llmproxy/translator`
  - `pkg/llmproxy/executor`
  - `pkg/llmproxy/runtime/executor`
- Validation command: `rg -n "CPB-0808|CPB-0808" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.md docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`

### CPB-0809
- Title focus: Ensure rollout safety for "Feature Request: Support Google Antigravity provider" via feature flags, staged defaults, and migration notes.
- Likely impacted paths:
  - `docs/operations/release-governance.md`
  - `docs/troubleshooting.md`
  - `pkg/llmproxy/config`
- Validation command: `rg -n "CPB-0809|CPB-0809" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.md docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`

### CPB-0810
- Title focus: Standardize metadata and naming conventions touched by "Add copilot cli proxy" across both repos.
- Likely impacted paths:
  - `pkg/llmproxy/registry/model_registry.go`
  - `docs/operations/release-governance.md`
  - `docs/provider-quickstarts.md`
- Validation command: `rg -n "CPB-0810|CPB-0810" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.md docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`

### CPB-0811
- Title focus: Follow up on "`gemini-3-pro-preview` is missing" by closing compatibility gaps and preventing regressions in adjacent providers.
- Likely impacted paths:
  - `pkg/llmproxy/translator`
  - `pkg/llmproxy/executor`
  - `docs/troubleshooting.md`
- Validation command: `rg -n "CPB-0811|CPB-0811" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.md docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`

### CPB-0812
- Title focus: Add process-compose/HMR refresh workflow tied to "Adjust gemini-3-pro-preview`s doc" so local config and runtime can be reloaded deterministically.
- Likely impacted paths:
  - `examples/process-compose.yaml`
  - `docker-compose.yml`
  - `docs/getting-started.md`
- Validation command: `rg -n "CPB-0812|CPB-0812" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.md docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`

## Verification

- `rg -n "CPB-0805|CPB-0812" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.md`
- `rg -n "quickstart|troubleshooting|stream|tool|reasoning|provider" docs/provider-quickstarts.md docs/troubleshooting.md`
- `go test ./pkg/llmproxy/translator/... -run "TestConvert|TestTranslate" -count=1`
