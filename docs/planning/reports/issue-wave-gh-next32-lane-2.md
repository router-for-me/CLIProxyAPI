# Issue Wave Next32 - Lane 2 Report

Scope: `router-for-me/CLIProxyAPIPlus` issues `#169 #165 #163 #158 #160 #149`
Worktree: `cliproxyapi-plusplus-wave-cpb-2`

## Per-Issue Status

### #169
- Status: `implemented`
- Notes: verified OpenAI models URL/versioned-path behavior in runtime executor path.
  - Evidence: `go test ./pkg/llmproxy/runtime/executor -run 'TestResolveOpenAIModelsURL|TestFetchOpenAIModels_UsesVersionedPath' -count=1`

### #165
- Status: `implemented`
- Notes: tightened Kiro quota diagnostics/compatibility in management handler:
  - `auth_index` query now accepts aliases: `authIndex`, `AuthIndex`, `index`
  - error payloads now include `auth_index` and token-resolution detail when available
  - tests added/updated in `pkg/llmproxy/api/handlers/management/api_tools_test.go`

### #163
- Status: `implemented`
- Notes: hardened malformed/legacy tool-call argument normalization for Kiro OpenAI translation:
  - non-object JSON arguments preserved as `{ "value": ... }`
  - non-JSON arguments preserved as `{ "raw": "<literal>" }`
  - focused regression added in `pkg/llmproxy/translator/kiro/openai/kiro_openai_request_test.go`

### #158
- Status: `implemented`
- Notes: improved OAuth upstream key compatibility normalization:
  - channel normalization now handles underscore/space variants (`github_copilot` -> `github-copilot`)
  - sanitation + lookup use the same normalization helper
  - coverage extended in `pkg/llmproxy/config/oauth_upstream_test.go`

### #160
- Status: `blocked`
- Notes: blocked pending a reproducible failing fixture on duplicate-output streaming path.
  - Current stream/tool-link normalization tests already cover ambiguous/missing call ID and duplicate-reasoning guardrails in `pkg/llmproxy/runtime/executor/kimi_executor_test.go`.
  - No deterministic regression sample in this repo currently maps to a safe, bounded code delta without speculative behavior changes.

### #149
- Status: `implemented`
- Notes: hardened Kiro IDC token-refresh path:
  - prevents invalid fallback to social OAuth refresh when IDC client credentials are missing
  - returns actionable remediation text (`--kiro-aws-login` / `--kiro-aws-authcode` / re-import guidance)
  - regression added in `sdk/auth/kiro_refresh_test.go`

## Focused Checks

- `go test ./pkg/llmproxy/config -run 'OAuthUpstream' -count=1`
- `go test ./pkg/llmproxy/translator/kiro/openai -run 'BuildAssistantMessageFromOpenAI' -count=1`
- `go test ./sdk/auth -run 'KiroRefresh' -count=1`
- `go test ./pkg/llmproxy/api/handlers/management -run 'GetKiroQuotaWithChecker' -count=1`
- `go vet ./...`
- `task quality:quick` (started; fmt/preflight/lint and many package tests passed, long-running suite still active in shared environment session)

## Blockers

- #160 blocked on missing deterministic reproduction fixture for duplicate-output stream bug in current repo state.

## Wave2 Lane 2 Entry - #241

- Issue: `#241` copilot context length should always be `128K`
- Status: `implemented`
- Mapping:
  - normalization at runtime registration: `pkg/llmproxy/registry/model_registry.go`
  - regression coverage: `pkg/llmproxy/registry/model_registry_hook_test.go`
- Tests:
  - `go test ./pkg/llmproxy/registry -run 'TestRegisterClient_NormalizesCopilotContextLength|TestGetGitHubCopilotModels' -count=1`
