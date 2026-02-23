# Issue Wave CPB-0731-0780 Lane B Report

- Lane: `B (cliproxyapi-plusplus)`
- Window slice covered in this report: `CPB-0739` to `CPB-0746`
- Scope: triage-only report (no code changes)

## Triage Entries

### CPB-0739 — OpenRouter 200 OK but invalid JSON response handling
- Title focus: rollout-safe parsing/guardrails for OpenAI-compatible responses that return invalid JSON despite HTTP `200`.
- Likely impacted paths:
  - `pkg/llmproxy/executor/openai_compat_executor.go`
  - `pkg/llmproxy/translator/openai/openai/responses/openai_openai-responses_response.go`
  - `pkg/llmproxy/translator/openai/openai/chat-completions/openai_openai_response.go`
- Validation command: `rg -n "openrouter|OpenRouter|invalid json|json" pkg/llmproxy/executor/openai_compat_executor.go pkg/llmproxy/translator/openai/openai/responses/openai_openai-responses_response.go pkg/llmproxy/translator/openai/openai/chat-completions/openai_openai_response.go`

### CPB-0740 — Claude tools `input_schema` required error normalization
- Title focus: metadata/schema naming consistency for Claude tool definitions, especially `tools.*.custom.input_schema` handling.
- Likely impacted paths:
  - `pkg/llmproxy/translator/openai/claude/openai_claude_request.go`
  - `pkg/llmproxy/executor/claude_executor.go`
  - `pkg/llmproxy/translator/openai/claude/openai_claude_request_test.go`
- Validation command: `rg -n "input_schema|tool|tools|custom" pkg/llmproxy/translator/openai/claude/openai_claude_request.go pkg/llmproxy/executor/claude_executor.go pkg/llmproxy/translator/openai/claude/openai_claude_request_test.go`

### CPB-0741 — Gemini CLI exhausted-capacity fallback model drift
- Title focus: prevent fallback to deprecated/nonexistent Gemini model IDs after quota/rate-limit events.
- Likely impacted paths:
  - `pkg/llmproxy/executor/gemini_cli_executor.go`
  - `pkg/llmproxy/executor/gemini_cli_executor_model_test.go`
  - `pkg/llmproxy/executor/gemini_cli_executor_retry_delay_test.go`
- Validation command: `go test ./pkg/llmproxy/executor -run 'GeminiCLI|gemini' -count=1`

### CPB-0742 — `max_tokens` vs `thinking.budget_tokens` validation hardening
- Title focus: enforce reasoning budget/token constraints with clearer validation and safer defaults.
- Likely impacted paths:
  - `pkg/llmproxy/executor/thinking_providers.go`
  - `pkg/llmproxy/translator/openai/common/reasoning.go`
  - `pkg/llmproxy/executor/codex_executor.go`
- Validation command: `rg -n "max_tokens|budget_tokens|reasoning" pkg/llmproxy/executor/thinking_providers.go pkg/llmproxy/translator/openai/common/reasoning.go pkg/llmproxy/executor/codex_executor.go`

### CPB-0743 — Antigravity CLI support observability/runbook coverage
- Title focus: define which CLIs support Antigravity and operationalize with logging/alert/runbook checks.
- Likely impacted paths:
  - `docs/provider-quickstarts.md`
  - `docs/provider-operations.md`
  - `pkg/llmproxy/executor/antigravity_executor.go`
- Validation command: `rg -n "Antigravity|antigravity|CLI|runbook|logging" docs/provider-quickstarts.md docs/provider-operations.md pkg/llmproxy/executor/antigravity_executor.go`

### CPB-0744 — Dynamic model mapping + custom param injection (iflow /tab)
- Title focus: provider-agnostic model remapping and custom parameter injection path for iflow-style requests.
- Likely impacted paths:
  - `pkg/llmproxy/executor/iflow_executor.go`
  - `pkg/llmproxy/registry/model_registry.go`
  - `pkg/llmproxy/util/translator.go`
- Validation command: `go test ./pkg/llmproxy/executor -run 'IFlow|iflow' -count=1`

### CPB-0745 — iFlow Google-login cookie usability regression
- Title focus: improve auth/cookie DX so cookie-based login state is consumed reliably by iFlow flows.
- Likely impacted paths:
  - `pkg/llmproxy/auth/iflow/iflow_auth.go`
  - `pkg/llmproxy/auth/iflow/cookie_helpers.go`
  - `pkg/llmproxy/executor/iflow_executor.go`
- Validation command: `go test ./pkg/llmproxy/auth/iflow -run 'Cookie|Exchange|Refresh' -count=1`

### CPB-0746 — Antigravity quickstart/troubleshooting expansion
- Title focus: improve docs/examples for "Antigravity not working" with copy-paste diagnostics and troubleshooting.
- Likely impacted paths:
  - `docs/provider-quickstarts.md`
  - `docs/provider-operations.md`
  - `pkg/llmproxy/executor/antigravity_executor_error_test.go`
- Validation command: `rg -n "Antigravity|troubleshoot|troubleshooting|quickstart|/v1/models" docs/provider-quickstarts.md docs/provider-operations.md pkg/llmproxy/executor/antigravity_executor_error_test.go`

## Validation Block

```bash
rg -n "openrouter|OpenRouter|invalid json|json" pkg/llmproxy/executor/openai_compat_executor.go pkg/llmproxy/translator/openai/openai/responses/openai_openai-responses_response.go pkg/llmproxy/translator/openai/openai/chat-completions/openai_openai_response.go
rg -n "input_schema|tool|tools|custom" pkg/llmproxy/translator/openai/claude/openai_claude_request.go pkg/llmproxy/executor/claude_executor.go pkg/llmproxy/translator/openai/claude/openai_claude_request_test.go
go test ./pkg/llmproxy/executor -run 'GeminiCLI|gemini' -count=1
rg -n "max_tokens|budget_tokens|reasoning" pkg/llmproxy/executor/thinking_providers.go pkg/llmproxy/translator/openai/common/reasoning.go pkg/llmproxy/executor/codex_executor.go
rg -n "Antigravity|antigravity|CLI|runbook|logging" docs/provider-quickstarts.md docs/provider-operations.md pkg/llmproxy/executor/antigravity_executor.go
go test ./pkg/llmproxy/executor -run 'IFlow|iflow' -count=1
go test ./pkg/llmproxy/auth/iflow -run 'Cookie|Exchange|Refresh' -count=1
rg -n "Antigravity|troubleshoot|troubleshooting|quickstart|/v1/models" docs/provider-quickstarts.md docs/provider-operations.md pkg/llmproxy/executor/antigravity_executor_error_test.go
```
