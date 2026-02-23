# Issue Wave GH Next32 Merge Report - Wave 2 (2026-02-23)

## Scope
- Wave 2, one item per lane (6 lanes total).
- Base: `origin/main` @ `f7e56f05`.

## Merged Commits
- `f1ab6855` - `fix(#253): support endpoint override for provider-pinned codex models`
- `05f894bf` - `fix(registry): enforce copilot context length 128K at registration (#241)`
- `947883cb` - `fix(kiro): handle banned account 403 payloads (#221)`
- `9fa8479d` - `fix(kiro): broaden cmd alias handling for command tools (#210)`
- `d921c09b` - `fix(#200): honor Gemini quota reset durations for cooldown`
- `a2571c90` - `fix(#179): honor openai-compat models-endpoint overrides`

## Issue Mapping
- `#253` -> `f1ab6855`
- `#241` -> `05f894bf`
- `#221` -> `947883cb`
- `#210` -> `9fa8479d`
- `#200` -> `d921c09b`
- `#179` -> `a2571c90`

## Validation
- `go test ./sdk/api/handlers/openai -run 'TestResolveEndpointOverride_' -count=1`
- `go test ./pkg/llmproxy/registry -run 'TestRegisterClient_NormalizesCopilotContextLength|TestGetGitHubCopilotModels' -count=1`
- `go test ./pkg/llmproxy/translator/kiro/claude -run 'TestDetectTruncation|TestBuildSoftFailureToolResult' -count=1`
- `go test pkg/llmproxy/executor/openai_models_fetcher.go pkg/llmproxy/executor/proxy_helpers.go pkg/llmproxy/executor/openai_models_fetcher_test.go -count=1`
- `go test pkg/llmproxy/runtime/executor/openai_models_fetcher.go pkg/llmproxy/runtime/executor/proxy_helpers.go pkg/llmproxy/runtime/executor/openai_models_fetcher_test.go -count=1`
