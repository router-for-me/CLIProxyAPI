# Issue Wave CPB-0731-0780 Lane A Triage Report

- Lane: `A (cliproxyapi-plusplus)`
- Window covered in this pass: `CPB-0731` to `CPB-0738`
- Scope: triage-only report (no code changes)

## Triage Entries

### CPB-0731
- Title focus: provider quickstart for Antigravity `thinking` block missing (`400 Invalid Argument`) with setup/auth/model/sanity flow.
- Likely impacted paths:
  - `docs/provider-quickstarts.md`
  - `docs/troubleshooting.md`
  - `docs/provider-usage.md`
- Validation command: `rg -n "thinking block|Invalid Argument|Antigravity" docs/provider-quickstarts.md docs/troubleshooting.md`

### CPB-0732
- Title focus: Gemini/OpenAI-format compatibility hardening with clearer validation and safer fallbacks.
- Likely impacted paths:
  - `pkg/llmproxy/executor/gemini_executor.go`
  - `pkg/llmproxy/runtime/executor/gemini_executor.go`
  - `pkg/llmproxy/util/translator.go`
- Validation command: `go test ./pkg/llmproxy/executor -run TestGemini -count=1`

### CPB-0733
- Title focus: persistent usage statistics operationalization (observability thresholds + runbook alignment).
- Likely impacted paths:
  - `pkg/llmproxy/executor/usage_helpers.go`
  - `pkg/llmproxy/runtime/executor/usage_helpers.go`
  - `docs/operations/provider-outage-triage-quick-guide.md`
- Validation command: `go test ./pkg/llmproxy/executor -run TestUsage -count=1`

### CPB-0734
- Title focus: provider-agnostic handling for Antigravity Claude thinking+tools streams that emit reasoning without assistant/tool calls.
- Likely impacted paths:
  - `pkg/llmproxy/executor/antigravity_executor.go`
  - `pkg/llmproxy/runtime/executor/antigravity_executor.go`
  - `pkg/llmproxy/util/translator.go`
- Validation command: `go test ./pkg/llmproxy/executor -run TestAntigravityBuildRequest -count=1`

### CPB-0735
- Title focus: DX improvements for `max_tokens > thinking.budget_tokens` guardrails and faster operator feedback.
- Likely impacted paths:
  - `pkg/llmproxy/executor/antigravity_executor.go`
  - `pkg/llmproxy/executor/antigravity_executor_error_test.go`
  - `docs/troubleshooting.md`
- Validation command: `rg -n "max_tokens|budget_tokens|thinking" pkg/llmproxy/executor/antigravity_executor.go docs/troubleshooting.md`

### CPB-0736
- Title focus: non-subprocess integration path for Antigravity permission-denied project errors, including HTTP fallback/version negotiation contract.
- Likely impacted paths:
  - `sdk/auth/antigravity.go`
  - `sdk/cliproxy/auth/conductor.go`
  - `pkg/llmproxy/executor/antigravity_executor.go`
- Validation command: `rg -n "permission|project|fallback|version" sdk/auth/antigravity.go sdk/cliproxy/auth/conductor.go pkg/llmproxy/executor/antigravity_executor.go`

### CPB-0737
- Title focus: QA parity coverage for extended thinking blocks during tool use (stream/non-stream + edge payloads).
- Likely impacted paths:
  - `pkg/llmproxy/executor/antigravity_executor_buildrequest_test.go`
  - `pkg/llmproxy/runtime/executor/antigravity_executor_buildrequest_test.go`
  - `pkg/llmproxy/executor/antigravity_executor_error_test.go`
- Validation command: `go test ./pkg/llmproxy/executor -run TestAntigravity -count=1`

### CPB-0738
- Title focus: refactor Antigravity browsing/tool-call transformation boundaries to isolate web-request path behavior.
- Likely impacted paths:
  - `pkg/llmproxy/executor/antigravity_executor.go`
  - `pkg/llmproxy/util/translator.go`
  - `sdk/api/handlers/handlers.go`
- Validation command: `rg -n "browse|web|tool_call|url_context|search" pkg/llmproxy/executor/antigravity_executor.go pkg/llmproxy/util/translator.go sdk/api/handlers/handlers.go`

## Validation Block

`rg -n "thinking block|Invalid Argument|Antigravity" docs/provider-quickstarts.md docs/troubleshooting.md`
`go test ./pkg/llmproxy/executor -run TestGemini -count=1`
`go test ./pkg/llmproxy/executor -run TestUsage -count=1`
`go test ./pkg/llmproxy/executor -run TestAntigravityBuildRequest -count=1`
`rg -n "max_tokens|budget_tokens|thinking" pkg/llmproxy/executor/antigravity_executor.go docs/troubleshooting.md`
`rg -n "permission|project|fallback|version" sdk/auth/antigravity.go sdk/cliproxy/auth/conductor.go pkg/llmproxy/executor/antigravity_executor.go`
`go test ./pkg/llmproxy/executor -run TestAntigravity -count=1`
`rg -n "browse|web|tool_call|url_context|search" pkg/llmproxy/executor/antigravity_executor.go pkg/llmproxy/util/translator.go sdk/api/handlers/handlers.go`
