# Issue Wave CPB-0781-0830 Lane E Report

- Lane: `E (cliproxyapi-plusplus)`
- Window: `CPB-0813` to `CPB-0820`
- Scope: triage-only report (no code edits)

## Items

### CPB-0813
- Title focus: Operationalize "Account banned after using CLI Proxy API on VPS" with observability, alerting thresholds, and runbook updates.
- Likely impacted paths:
  - `docs/operations`
  - `docs/troubleshooting.md`
  - `pkg/llmproxy/api/handlers/management`
- Validation command: `rg -n "CPB-0813|CPB-0813" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.md docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`

### CPB-0814
- Title focus: Convert "Bug: config.example.yaml has incorrect auth-dir default, causes auth files to be saved in wrong location" into a provider-agnostic pattern and codify in shared translation utilities.
- Likely impacted paths:
  - `pkg/llmproxy/translator`
  - `pkg/llmproxy/executor`
  - `pkg/llmproxy/runtime/executor`
- Validation command: `rg -n "CPB-0814|CPB-0814" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.md docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`

### CPB-0815
- Title focus: Add DX polish around "Security: Auth directory created with overly permissive 0o755 instead of 0o700" through improved command ergonomics and faster feedback loops.
- Likely impacted paths:
  - `pkg/llmproxy/translator`
  - `pkg/llmproxy/executor`
  - `docs/troubleshooting.md`
- Validation command: `rg -n "CPB-0815|CPB-0815" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.md docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`

### CPB-0816
- Title focus: Create/refresh provider quickstart derived from "Gemini CLI Oauth with Claude Code" including setup, auth, model select, and sanity-check commands.
- Likely impacted paths:
  - `docs/provider-quickstarts.md`
  - `docs/troubleshooting.md`
  - `docs/planning/README.md`
- Validation command: `rg -n "CPB-0816|CPB-0816" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.md docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`

### CPB-0817
- Title focus: Port relevant thegent-managed flow implied by "Gemini cli使用不了" into first-class cliproxy Go CLI command(s) with interactive setup support.
- Likely impacted paths:
  - `cmd`
  - `sdk/cliproxy`
  - `pkg/llmproxy/api/handlers/management`
- Validation command: `rg -n "CPB-0817|CPB-0817" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.md docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`

### CPB-0818
- Title focus: Refactor implementation behind "麻烦大佬能不能更进模型id，比如gpt已经更新了小版本5.1了" to reduce complexity and isolate transformation boundaries.
- Likely impacted paths:
  - `pkg/llmproxy/translator`
  - `pkg/llmproxy/executor`
  - `pkg/llmproxy/runtime/executor`
- Validation command: `rg -n "CPB-0818|CPB-0818" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.md docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`

### CPB-0819
- Title focus: Ensure rollout safety for "Factory Droid: /compress (session compact) fails on Gemini 2.5 via CLIProxyAPI" via feature flags, staged defaults, and migration notes.
- Likely impacted paths:
  - `docs/operations/release-governance.md`
  - `docs/troubleshooting.md`
  - `pkg/llmproxy/config`
- Validation command: `rg -n "CPB-0819|CPB-0819" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.md docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`

### CPB-0820
- Title focus: Standardize metadata and naming conventions touched by "Feat Request: Support gpt-5-pro" across both repos.
- Likely impacted paths:
  - `pkg/llmproxy/registry/model_registry.go`
  - `docs/operations/release-governance.md`
  - `docs/provider-quickstarts.md`
- Validation command: `rg -n "CPB-0820|CPB-0820" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.md docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`

## Verification

- `rg -n "CPB-0813|CPB-0820" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.md`
- `rg -n "quickstart|troubleshooting|stream|tool|reasoning|provider" docs/provider-quickstarts.md docs/troubleshooting.md`
- `go test ./pkg/llmproxy/translator/... -run "TestConvert|TestTranslate" -count=1`
