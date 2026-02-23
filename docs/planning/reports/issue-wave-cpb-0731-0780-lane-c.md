# Issue Wave CPB-0731-0780 Lane C Report

- Lane: `C (cliproxyapi-plusplus)`
- Window slice: `CPB-0747`..`CPB-0754`
- Scope: triage-only report (no code changes)

## Per-Item Triage

### CPB-0747
- Title focus: Add QA scenarios for Zeabur-deploy ask, especially stream/non-stream parity and edge payloads.
- Likely impacted paths:
  - `pkg/llmproxy/translator/openai/openai/responses/openai_openai-responses_request_test.go`
  - `pkg/llmproxy/translator/openai/openai/chat-completions/openai_openai_request_test.go`
  - `docs/provider-quickstarts.md`
- Validation command: `rg -n "stream|non-stream|edge-case|Zeabur|部署" pkg/llmproxy/translator/openai/openai/responses/openai_openai-responses_request_test.go pkg/llmproxy/translator/openai/openai/chat-completions/openai_openai_request_test.go docs/provider-quickstarts.md`

### CPB-0748
- Title focus: Refresh Gemini quickstart around non-standard OpenAI fields parser failures.
- Likely impacted paths:
  - `docs/provider-quickstarts.md`
  - `docs/troubleshooting.md`
  - `pkg/llmproxy/util/gemini_schema.go`
- Validation command: `rg -n "Gemini|non-standard|OpenAI fields|parser" docs/provider-quickstarts.md docs/troubleshooting.md pkg/llmproxy/util/gemini_schema.go`

### CPB-0749
- Title focus: Rollout safety for HTTP proxy token-unobtainable flow after Google auth success.
- Likely impacted paths:
  - `pkg/llmproxy/util/proxy.go`
  - `pkg/llmproxy/executor/oauth_upstream.go`
  - `pkg/llmproxy/api/handlers/management/oauth_callback.go`
- Validation command: `go test ./pkg/llmproxy/executor -run TestOAuthUpstream -count=1`

### CPB-0750
- Title focus: Standardize metadata/naming around Antigravity auth failures.
- Likely impacted paths:
  - `pkg/llmproxy/executor/antigravity_executor.go`
  - `pkg/llmproxy/config/oauth_model_alias_migration.go`
  - `docs/provider-catalog.md`
- Validation command: `rg -n "antigravity|oauth_model_alias|alias" pkg/llmproxy/executor/antigravity_executor.go pkg/llmproxy/config/oauth_model_alias_migration.go docs/provider-catalog.md`

### CPB-0751
- Title focus: Gemini 3 Pro preview compatibility follow-up with adjacent-provider regression guardrails.
- Likely impacted paths:
  - `pkg/llmproxy/executor/gemini_executor.go`
  - `pkg/llmproxy/executor/gemini_cli_executor.go`
  - `pkg/llmproxy/executor/gemini_cli_executor_model_test.go`
- Validation command: `go test ./pkg/llmproxy/executor -run TestGeminiCLIExecutor -count=1`

### CPB-0752
- Title focus: Harden Windows Hyper-V reserved-port behavior with safer defaults and fallback handling.
- Likely impacted paths:
  - `pkg/llmproxy/cmd/run.go`
  - `pkg/llmproxy/config/config.go`
  - `docs/troubleshooting.md`
- Validation command: `rg -n "port|listen|bind|addr" pkg/llmproxy/cmd/run.go pkg/llmproxy/config/config.go docs/troubleshooting.md`

### CPB-0753
- Title focus: Operationalize Gemini image-generation support with observability thresholds and runbook updates.
- Likely impacted paths:
  - `pkg/llmproxy/util/image.go`
  - `pkg/llmproxy/logging/request_logger.go`
  - `docs/provider-operations.md`
- Validation command: `rg -n "image|gemini-3-pro-image-preview|observability|threshold|runbook" pkg/llmproxy/util/image.go pkg/llmproxy/logging/request_logger.go docs/provider-operations.md`

### CPB-0754
- Title focus: Deterministic process-compose/HMR refresh workflow for Gemini native file-upload support.
- Likely impacted paths:
  - `examples/process-compose.dev.yaml`
  - `pkg/llmproxy/watcher/config_reload.go`
  - `docs/sdk-watcher.md`
- Validation command: `go test ./pkg/llmproxy/watcher -run TestWatcher -count=1`

## Validation Block
`rg -n "CPB-0747|CPB-0748|CPB-0749|CPB-0750|CPB-0751|CPB-0752|CPB-0753|CPB-0754" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.md`
`rg -n "stream|non-stream|edge-case|Zeabur|部署" pkg/llmproxy/translator/openai/openai/responses/openai_openai-responses_request_test.go pkg/llmproxy/translator/openai/openai/chat-completions/openai_openai_request_test.go docs/provider-quickstarts.md`
`rg -n "Gemini|non-standard|OpenAI fields|parser" docs/provider-quickstarts.md docs/troubleshooting.md pkg/llmproxy/util/gemini_schema.go`
`go test ./pkg/llmproxy/executor -run TestOAuthUpstream -count=1`
`rg -n "antigravity|oauth_model_alias|alias" pkg/llmproxy/executor/antigravity_executor.go pkg/llmproxy/config/oauth_model_alias_migration.go docs/provider-catalog.md`
`go test ./pkg/llmproxy/executor -run TestGeminiCLIExecutor -count=1`
`rg -n "port|listen|bind|addr" pkg/llmproxy/cmd/run.go pkg/llmproxy/config/config.go docs/troubleshooting.md`
`rg -n "image|gemini-3-pro-image-preview|observability|threshold|runbook" pkg/llmproxy/util/image.go pkg/llmproxy/logging/request_logger.go docs/provider-operations.md`
`go test ./pkg/llmproxy/watcher -run TestWatcher -count=1`
