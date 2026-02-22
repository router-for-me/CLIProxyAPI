# Issue Wave GH-Next21 Lane 2 Report

Scope: OAuth/Auth reliability (`#246`, `#245`, `#177`)  
Worktree: `/Users/kooshapari/temp-PRODVERCEL/485/kush/wt/gh-next21-lane-2`  
Branch: `wave-gh-next21-lane-2`  
Date: 2026-02-22

## Status by Item

### #246 - fix(cline): add grantType to token refresh and extension headers
- Status: `done`
- Validation summary:
  - IDC refresh payload sends both camelCase and snake_case fields, including `grantType` and `grant_type`.
  - IDC refresh flow applies extension headers expected by Kiro IDE behavior.
- Evidence:
  - `pkg/llmproxy/auth/kiro/sso_oidc.go` (payload + header helpers)
  - `pkg/llmproxy/auth/kiro/sso_oidc_test.go` (regression coverage)
  - Implementation commit: `310c57a69`

### #245 - fix(cline): add grantType to token refresh and extension headers
- Status: `done`
- Validation summary:
  - Same auth reliability surface as `#246` is covered in both default and region-aware refresh code paths.
  - Tests assert both grant-type keys and extension header behavior.
- Evidence:
  - `pkg/llmproxy/auth/kiro/sso_oidc.go`
  - `pkg/llmproxy/auth/kiro/sso_oidc_test.go`
  - Implementation commit: `310c57a69`

### #177 - Kiro Token 导入失败: Refresh token is required
- Status: `done`
- Validation summary:
  - Token loader checks both default and legacy token-file paths.
  - Token parsing accepts both camelCase and snake_case token key formats.
  - Custom token-path loading reuses the tolerant parser.
- Evidence:
  - `pkg/llmproxy/auth/kiro/aws.go`
  - `pkg/llmproxy/auth/kiro/aws_load_token_test.go`
  - Implementation commits: `322381d38`, `219fd8ed5`

## Verification Commands

Executed on this lane worktree:
- `go test ./pkg/llmproxy/auth/kiro -run 'TestRefreshToken_IncludesGrantTypeAndExtensionHeaders|TestRefreshTokenWithRegion_UsesRegionHostAndGrantType' -count=1`
- `go test ./pkg/llmproxy/auth/kiro -run 'TestLoadKiroIDEToken_FallbackLegacyPathAndSnakeCase|TestLoadKiroIDEToken_PrefersDefaultPathOverLegacy' -count=1`
- `go test ./pkg/llmproxy/auth/kiro -count=1`

All commands passed.

## Remaining Gaps

- No lane-local gaps detected for `#246`, `#245`, or `#177` in current `main` state.
