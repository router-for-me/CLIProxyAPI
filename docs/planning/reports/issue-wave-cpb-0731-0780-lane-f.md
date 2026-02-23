# Issue Wave CPB-0731-0780 Lane F Report

- Lane: `F (cliproxyapi-plusplus)`
- Window slice: `CPB-0771`..`CPB-0780`
- Scope: triage-only report (no code changes)

## Per-Item Triage

### CPB-0771
- Title focus: close compatibility gaps for Anthropic `anthropic-beta` header support with Claude thinking + tool use paths.
- Likely impacted paths:
  - `pkg/llmproxy/executor/claude_executor.go`
  - `pkg/llmproxy/runtime/executor/claude_executor.go`
  - `pkg/llmproxy/translator/codex/claude/codex_claude_request.go`
- Validation command: `rg -n "anthropic-beta|thinking|tool|input_schema|cache_control" pkg/llmproxy/executor/claude_executor.go pkg/llmproxy/runtime/executor/claude_executor.go pkg/llmproxy/translator/codex/claude/codex_claude_request.go`

### CPB-0772
- Title focus: harden Antigravity model handling in opencode CLI with clearer validation, safer defaults, and fallback behavior.
- Likely impacted paths:
  - `pkg/llmproxy/executor/antigravity_executor.go`
  - `pkg/llmproxy/runtime/executor/antigravity_executor.go`
  - `pkg/llmproxy/config/providers.json`
- Validation command: `go test ./pkg/llmproxy/executor -run 'TestAntigravity' -count=1`

### CPB-0773
- Title focus: operationalize native Gemini-format Antigravity gaps (model-list omissions + `gemini-3-pro-preview` web-search failures) with observability/runbook coverage.
- Likely impacted paths:
  - `pkg/llmproxy/registry/model_definitions.go`
  - `pkg/llmproxy/logging/request_logger.go`
  - `docs/provider-operations.md`
- Validation command: `rg -n "gemini-3-pro-preview|model list|web search|observability|runbook|Antigravity" pkg/llmproxy/registry/model_definitions.go pkg/llmproxy/logging/request_logger.go docs/provider-operations.md`

### CPB-0774
- Title focus: convert `checkSystemInstructions`/`cache_control` block-limit failures into a provider-agnostic shared pattern.
- Likely impacted paths:
  - `pkg/llmproxy/runtime/executor/claude_executor.go`
  - `pkg/llmproxy/executor/claude_executor.go`
  - `pkg/llmproxy/runtime/executor/caching_verify_test.go`
- Validation command: `rg -n "checkSystemInstructions|cache_control|maximum of 4 blocks|ensureCacheControl" pkg/llmproxy/runtime/executor/claude_executor.go pkg/llmproxy/executor/claude_executor.go pkg/llmproxy/runtime/executor/caching_verify_test.go`

### CPB-0775
- Title focus: improve DX and feedback loops for thinking-token constraints (`max_tokens` vs `thinking.budget_tokens`) across OpenAI/Gemini surfaces.
- Likely impacted paths:
  - `pkg/llmproxy/executor/thinking_providers.go`
  - `pkg/llmproxy/translator/openai/common/reasoning.go`
  - `docs/troubleshooting.md`
- Validation command: `rg -n "max_tokens|budget_tokens|thinking|reasoning" pkg/llmproxy/executor/thinking_providers.go pkg/llmproxy/translator/openai/common/reasoning.go docs/troubleshooting.md`

### CPB-0776
- Title focus: expand Anthropic OAuth breakage docs/quickstarts with actionable troubleshooting for post-commit regressions.
- Likely impacted paths:
  - `docs/provider-quickstarts.md`
  - `docs/troubleshooting.md`
  - `pkg/llmproxy/auth/claude/oauth_server.go`
- Validation command: `rg -n "Anthropic|Claude|OAuth|quickstart|troubleshoot|token" docs/provider-quickstarts.md docs/troubleshooting.md pkg/llmproxy/auth/claude/oauth_server.go`

### CPB-0777
- Title focus: add Droid-as-provider QA coverage for stream/non-stream parity and edge payload handling.
- Likely impacted paths:
  - `pkg/llmproxy/translator/openai/openai/chat-completions/openai_openai_request_test.go`
  - `pkg/llmproxy/translator/openai/openai/responses/openai_openai-responses_request_test.go`
  - `pkg/llmproxy/config/providers.json`
- Validation command: `rg -n "Droid|droid|stream|non-stream|edge|provider" pkg/llmproxy/translator/openai/openai/chat-completions/openai_openai_request_test.go pkg/llmproxy/translator/openai/openai/responses/openai_openai-responses_request_test.go pkg/llmproxy/config/providers.json`

### CPB-0778
- Title focus: refactor JSON schema / structured output internals to isolate transformation boundaries and reduce coupling.
- Likely impacted paths:
  - `pkg/llmproxy/translator/kiro/openai/kiro_openai_request.go`
  - `pkg/llmproxy/runtime/executor/codex_executor_schema_test.go`
  - `pkg/llmproxy/executor/token_helpers.go`
- Validation command: `go test ./pkg/llmproxy/runtime/executor -run 'Schema|Structured|ResponseFormat' -count=1`

### CPB-0779
- Title focus: port relevant thegent-managed flow for thinking parity into first-class `cliproxy` Go CLI commands with interactive setup.
- Likely impacted paths:
  - `cmd/cliproxyctl/main.go`
  - `cmd/cliproxyctl/main_test.go`
  - `pkg/llmproxy/cmd/thegent_login.go`
- Validation command: `go test ./cmd/cliproxyctl -run 'Test.*(login|provider|doctor|models)' -count=1`

### CPB-0780
- Title focus: standardize metadata/naming for Docker-based Gemini login flows across config, registry, and install docs.
- Likely impacted paths:
  - `docs/install.md`
  - `pkg/llmproxy/config/oauth_model_alias_migration.go`
  - `pkg/llmproxy/registry/model_registry.go`
- Validation command: `rg -n "docker|Gemini|gemini|login|oauth|alias|metadata" docs/install.md pkg/llmproxy/config/oauth_model_alias_migration.go pkg/llmproxy/registry/model_registry.go`

## Validation Block
`rg -n "CPB-0771|CPB-0772|CPB-0773|CPB-0774|CPB-0775|CPB-0776|CPB-0777|CPB-0778|CPB-0779|CPB-0780" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.md`
`rg -n "anthropic-beta|thinking|tool|input_schema|cache_control" pkg/llmproxy/executor/claude_executor.go pkg/llmproxy/runtime/executor/claude_executor.go pkg/llmproxy/translator/codex/claude/codex_claude_request.go`
`go test ./pkg/llmproxy/executor -run 'TestAntigravity' -count=1`
`rg -n "gemini-3-pro-preview|model list|web search|observability|runbook|Antigravity" pkg/llmproxy/registry/model_definitions.go pkg/llmproxy/logging/request_logger.go docs/provider-operations.md`
`rg -n "checkSystemInstructions|cache_control|maximum of 4 blocks|ensureCacheControl" pkg/llmproxy/runtime/executor/claude_executor.go pkg/llmproxy/executor/claude_executor.go pkg/llmproxy/runtime/executor/caching_verify_test.go`
`rg -n "max_tokens|budget_tokens|thinking|reasoning" pkg/llmproxy/executor/thinking_providers.go pkg/llmproxy/translator/openai/common/reasoning.go docs/troubleshooting.md`
`rg -n "Anthropic|Claude|OAuth|quickstart|troubleshoot|token" docs/provider-quickstarts.md docs/troubleshooting.md pkg/llmproxy/auth/claude/oauth_server.go`
`rg -n "Droid|droid|stream|non-stream|edge|provider" pkg/llmproxy/translator/openai/openai/chat-completions/openai_openai_request_test.go pkg/llmproxy/translator/openai/openai/responses/openai_openai-responses_request_test.go pkg/llmproxy/config/providers.json`
`go test ./pkg/llmproxy/runtime/executor -run 'Schema|Structured|ResponseFormat' -count=1`
`go test ./cmd/cliproxyctl -run 'Test.*(login|provider|doctor|models)' -count=1`
`rg -n "docker|Gemini|gemini|login|oauth|alias|metadata" docs/install.md pkg/llmproxy/config/oauth_model_alias_migration.go pkg/llmproxy/registry/model_registry.go`
