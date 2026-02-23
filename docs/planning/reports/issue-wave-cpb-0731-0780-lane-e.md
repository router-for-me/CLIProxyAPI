# Issue Wave CPB-0731-0780 Lane E Report

## Scope
- Lane: `E`
- Worktree: `/Users/kooshapari/temp-PRODVERCEL/485/kush/cliproxyapi-plusplus`
- Window handled in this report: `CPB-0763..CPB-0770`
- Constraint followed: report-only triage, no code edits.

## Per-Item Triage

### CPB-0763
- Title focus: Codex reasoning-token omissions need observability thresholds and runbook coverage.
- Likely impacted paths:
  - `pkg/llmproxy/translator/codex/openai/chat-completions/codex_openai_response.go`
  - `pkg/llmproxy/translator/codex/gemini/codex_gemini_response.go`
  - `docs/troubleshooting.md`
- Concrete validation command: `rg -n "reasoning|token|usage" pkg/llmproxy/translator/codex docs/troubleshooting.md`

### CPB-0764
- Title focus: Normalize XHigh reasoning-effort handling into shared provider-agnostic translation behavior.
- Likely impacted paths:
  - `pkg/llmproxy/translator/codex/openai/chat-completions/codex_openai_request.go`
  - `pkg/llmproxy/translator/codex/gemini/codex_gemini_request.go`
  - `pkg/llmproxy/translator/translator/translator.go`
- Concrete validation command: `go test ./pkg/llmproxy/translator/codex/... -run 'Reasoning|Effort|XHigh' -count=1`

### CPB-0765
- Title focus: Refresh Gemini reasoning-effort quickstart with setup/auth/model/sanity-check flow.
- Likely impacted paths:
  - `docs/provider-quickstarts.md`
  - `docs/troubleshooting.md`
  - `cmd/server/main.go`
- Concrete validation command: `rg -n "Gemini|reasoning|effort|quickstart" docs/provider-quickstarts.md docs/troubleshooting.md cmd/server/main.go`

### CPB-0766
- Title focus: Document and troubleshoot iflow token refresh failures (missing access token response).
- Likely impacted paths:
  - `pkg/llmproxy/auth/iflow/iflow_auth.go`
  - `pkg/llmproxy/auth/iflow/iflow_token.go`
  - `docs/troubleshooting.md`
- Concrete validation command: `go test ./pkg/llmproxy/auth/iflow -run 'Token|Refresh|Access' -count=1`

### CPB-0767
- Title focus: Add QA coverage for Antigravity/Claude `tools.0.custom.input_schema` required-field failures.
- Likely impacted paths:
  - `pkg/llmproxy/auth/antigravity/auth.go`
  - `pkg/llmproxy/translator/codex/claude/codex_claude_request.go`
  - `pkg/llmproxy/translator/codex/claude/codex_claude_request_test.go`
- Concrete validation command: `go test ./pkg/llmproxy/translator/codex/claude -run 'tool|schema|input_schema' -count=1`

### CPB-0768
- Title focus: Refactor Amazon Q support to isolate transformation boundaries and reduce coupling.
- Likely impacted paths:
  - `pkg/llmproxy/auth/qwen/qwen_auth.go`
  - `pkg/llmproxy/translator/openai/openai/chat-completions/openai_openai_request.go`
  - `pkg/llmproxy/config/providers.json`
- Concrete validation command: `rg -n "amazonq|qwen|transform|translator" pkg/llmproxy/auth pkg/llmproxy/translator pkg/llmproxy/config/providers.json`

### CPB-0769
- Title focus: Roll out tier-based provider prioritization with safe flags and migration notes.
- Likely impacted paths:
  - `pkg/llmproxy/config/config.go`
  - `pkg/llmproxy/config/provider_registry_generated.go`
  - `docs/install.md`
- Concrete validation command: `go test ./pkg/llmproxy/config -run 'Provider|Tier|Priority|Migration' -count=1`

### CPB-0770
- Title focus: Standardize Gemini 3 Pro + Codex CLI naming/metadata conventions across surfaces.
- Likely impacted paths:
  - `pkg/llmproxy/registry/model_definitions.go`
  - `pkg/llmproxy/registry/model_registry.go`
  - `pkg/llmproxy/config/oauth_model_alias_migration.go`
- Concrete validation command: `go test ./pkg/llmproxy/registry -run 'Gemini|Codex|Metadata|Alias' -count=1`

## Validation (Read-Only Commands)
`rg -n "CPB-0763|CPB-0764|CPB-0765|CPB-0766|CPB-0767|CPB-0768|CPB-0769|CPB-0770" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.md`
`rg -n "reasoning|effort|token|input_schema|provider prioritization|Gemini 3 Pro" docs/provider-quickstarts.md docs/troubleshooting.md pkg/llmproxy`
`go test ./pkg/llmproxy/translator/codex/... -run 'Reasoning|Effort|XHigh|tool|schema' -count=1`
`go test ./pkg/llmproxy/auth/iflow -run 'Token|Refresh|Access' -count=1`
`go test ./pkg/llmproxy/config -run 'Provider|Tier|Priority|Migration' -count=1`
`go test ./pkg/llmproxy/registry -run 'Gemini|Codex|Metadata|Alias' -count=1`
