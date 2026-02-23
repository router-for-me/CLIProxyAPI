# Issue Wave CPB-0731-0780 Lane D Report

- Lane: `D (cliproxyapi-plusplus)`
- Window: `CPB-0755` to `CPB-0762`
- Worktree: `/Users/kooshapari/temp-PRODVERCEL/485/kush/cliproxyapi-plusplus`
- Scope: triage-only report (no code edits).

## Per-Item Triage

### CPB-0755
- Title focus: DX polish for AMP web-search behavior with faster validation loops.
- Likely impacted paths:
  - `pkg/llmproxy/api/modules/amp/routes.go`
  - `pkg/llmproxy/translator/openai/openai/responses/openai_openai-responses_request.go`
- Validation command: `rg -n "web_search|googleSearch|amp" pkg/llmproxy/api/modules/amp/routes.go pkg/llmproxy/translator/openai/openai/responses/openai_openai-responses_request.go`

### CPB-0756
- Title focus: docs/examples expansion for `1006` handling with copy-paste remediation.
- Likely impacted paths:
  - `docs/troubleshooting.md`
  - `docs/provider-quickstarts.md`
- Validation command: `rg -n "1006|websocket|close code" docs/troubleshooting.md docs/provider-quickstarts.md`

### CPB-0757
- Title focus: QA parity scenarios for Kiro OAuth support (stream/non-stream + edge payloads).
- Likely impacted paths:
  - `pkg/llmproxy/auth/kiro/oauth.go`
  - `pkg/llmproxy/translator/kiro/openai/kiro_openai_request_test.go`
- Validation command: `go test ./pkg/llmproxy/auth/kiro -run 'Test.*OAuth|Test.*SSO' -count=1`

### CPB-0758
- Title focus: simplify Antigravity configuration flow and isolate auth/transform boundaries.
- Likely impacted paths:
  - `pkg/llmproxy/auth/antigravity/auth.go`
  - `pkg/llmproxy/api/handlers/management/auth_files.go`
- Validation command: `go test ./pkg/llmproxy/auth/antigravity -run 'Test.*' -count=1`

### CPB-0759
- Title focus: non-subprocess integration path for `auth_unavailable` + `/v1/models` stability.
- Likely impacted paths:
  - `pkg/llmproxy/api/handlers/management/api_tools.go`
  - `pkg/llmproxy/api/handlers/management/model_definitions.go`
- Validation command: `rg -n "auth_unavailable|/v1/models|model" pkg/llmproxy/api/handlers/management/api_tools.go pkg/llmproxy/api/handlers/management/model_definitions.go`

### CPB-0760
- Title focus: port Claude Code web-search recovery flow into first-class Go CLI command(s).
- Likely impacted paths:
  - `cmd/cliproxyctl/main.go`
  - `cmd/cliproxyctl/main_test.go`
- Validation command: `go test ./cmd/cliproxyctl -run 'Test.*(login|provider|ampcode)' -count=1`

### CPB-0761
- Title focus: close auto-compact compatibility gaps and lock regressions.
- Likely impacted paths:
  - `pkg/llmproxy/translator/kiro/common/message_merge.go`
  - `pkg/llmproxy/translator/kiro/claude/truncation_detector.go`
- Validation command: `go test ./pkg/llmproxy/translator/kiro/... -run 'Test.*(Truncation|Merge|Compact)' -count=1`

### CPB-0762
- Title focus: harden Gemini business-account support with safer defaults and fallbacks.
- Likely impacted paths:
  - `pkg/llmproxy/auth/gemini/gemini_auth.go`
  - `pkg/llmproxy/config/config.go`
- Validation command: `go test ./pkg/llmproxy/auth/gemini -run 'Test.*Gemini' -count=1`

## Validation Block

```bash
rg -n "web_search|googleSearch|amp" pkg/llmproxy/api/modules/amp/routes.go pkg/llmproxy/translator/openai/openai/responses/openai_openai-responses_request.go
rg -n "1006|websocket|close code" docs/troubleshooting.md docs/provider-quickstarts.md
go test ./pkg/llmproxy/auth/kiro -run 'Test.*OAuth|Test.*SSO' -count=1
go test ./pkg/llmproxy/auth/antigravity -run 'Test.*' -count=1
rg -n "auth_unavailable|/v1/models|model" pkg/llmproxy/api/handlers/management/api_tools.go pkg/llmproxy/api/handlers/management/model_definitions.go
go test ./cmd/cliproxyctl -run 'Test.*(login|provider|ampcode)' -count=1
go test ./pkg/llmproxy/translator/kiro/... -run 'Test.*(Truncation|Merge|Compact)' -count=1
go test ./pkg/llmproxy/auth/gemini -run 'Test.*Gemini' -count=1
```
