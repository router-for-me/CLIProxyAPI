# Issue Wave GH-35 - Lane 2 Report

Scope: `router-for-me/CLIProxyAPIPlus` issues `#245 #241 #232 #221 #219`
Worktree: `cliproxyapi-plusplus-worktree-2`

## Per-Issue Status

### #245 - `fix(cline): add grantType to token refresh and extension headers`
- Status: `fix`
- Summary:
  - Hardened Kiro IDC refresh payload compatibility by sending both camelCase and snake_case token fields (`grantType` + `grant_type`, etc.).
  - Unified extension header behavior across `RefreshToken` and `RefreshTokenWithRegion` via shared helper logic.
- Code paths inspected:
  - `pkg/llmproxy/auth/kiro/sso_oidc.go`

### #241 - `context length for models registered from github-copilot should always be 128K`
- Status: `fix`
- Summary:
  - Enforced a uniform `128000` context length for all models returned by `GetGitHubCopilotModels()`.
  - Added regression coverage to assert all Copilot models remain at 128K.
- Code paths inspected:
  - `pkg/llmproxy/registry/model_definitions.go`
  - `pkg/llmproxy/registry/model_definitions_test.go`

### #232 - `Add AMP auth as Kiro`
- Status: `feature`
- Summary:
  - Existing AMP support is routing/management oriented; this issue requests additional auth-mode/product behavior across provider semantics.
  - No safe, narrow, high-confidence patch was applied in this lane without widening scope into auth architecture.
- Code paths inspected:
  - `pkg/llmproxy/api/modules/amp/*`
  - `pkg/llmproxy/config/config.go`
  - `pkg/llmproxy/config/oauth_model_alias_migration.go`

### #221 - `kiro账号被封`
- Status: `external`
- Summary:
  - Root symptom is account suspension by upstream provider and requires provider-side restoration.
  - No local code change can clear a suspended account state.
- Code paths inspected:
  - `pkg/llmproxy/runtime/executor/kiro_executor.go` (suspension/cooldown handling)

### #219 - `Opus 4.6` (unknown provider paths)
- Status: `fix`
- Summary:
  - Added static antigravity alias coverage for `gemini-claude-opus-thinking` to prevent `unknown provider` classification.
  - Added migration/default-alias support for that alias and improved migration dedupe to preserve multiple aliases per same upstream model.
- Code paths inspected:
  - `pkg/llmproxy/registry/model_definitions_static_data.go`
  - `pkg/llmproxy/config/oauth_model_alias_migration.go`
  - `pkg/llmproxy/config/oauth_model_alias_migration_test.go`

## Files Changed

- `pkg/llmproxy/auth/kiro/sso_oidc.go`
- `pkg/llmproxy/auth/kiro/sso_oidc_test.go`
- `pkg/llmproxy/registry/model_definitions.go`
- `pkg/llmproxy/registry/model_definitions_static_data.go`
- `pkg/llmproxy/registry/model_definitions_test.go`
- `pkg/llmproxy/config/oauth_model_alias_migration.go`
- `pkg/llmproxy/config/oauth_model_alias_migration_test.go`
- `docs/planning/reports/issue-wave-gh-35-lane-2.md`

## Focused Tests Run

- `go test ./pkg/llmproxy/auth/kiro -run 'TestRefreshToken|TestRefreshTokenWithRegion'`
- `go test ./pkg/llmproxy/registry -run 'TestGetGitHubCopilotModels|TestGetAntigravityModelConfig'`
- `go test ./pkg/llmproxy/config -run 'TestMigrateOAuthModelAlias_ConvertsAntigravityModels'`
- `go test ./pkg/llmproxy/auth/kiro ./pkg/llmproxy/registry ./pkg/llmproxy/config`

Result: all passing.

## Blockers

- `#232` needs product/auth design decisions beyond safe lane-scoped bugfixing.
- `#221` is externally constrained by upstream account suspension workflow.
