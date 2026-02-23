# Issue Wave GH-35 Lane 7 Report

## Scope
- Lane: 7 (`cliproxyapi-plusplus-worktree-7`)
- Issues: #133, #129, #125, #115, #111
- Objective: inspect, implement safe fixes where feasible, run focused Go tests, and record blockers.

## Per-Issue Status

### #133 Routing strategy "fill-first" is not working as expected
- Status: `PARTIAL (safe normalization + compatibility hardening)`
- Findings:
  - Runtime selector switching already exists in `sdk/cliproxy` startup/reload paths.
  - A common config spelling mismatch (`fill_first` vs `fill-first`) was not normalized consistently.
- Fixes:
  - Added underscore-compatible normalization for routing strategy in management + runtime startup/reload.
- Changed files:
  - `pkg/llmproxy/api/handlers/management/config_basic.go`
  - `sdk/cliproxy/builder.go`
  - `sdk/cliproxy/service.go`
- Notes:
  - This improves compatibility and removes one likely reason users observe "fill-first not applied".
  - Live behavioral validation against multi-credential traffic is still required.

### #129 CLIProxyApiPlus ClawCloud cloud deploy config file not found
- Status: `DONE (safe fallback path discovery)`
- Findings:
  - Default startup path was effectively strict (`<wd>/config.yaml`) when `--config` is not passed.
  - Cloud/container layouts often mount config in nested or platform-specific paths.
- Fixes:
  - Added cloud-aware config discovery helper with ordered fallback candidates and env overrides.
  - Wired main startup path resolution to this helper.
- Changed files:
  - `cmd/server/main.go`
  - `cmd/server/config_path.go`
  - `cmd/server/config_path_test.go`

### #125 Error 403 (Gemini Code Assist license / subscription required)
- Status: `DONE (actionable error diagnostics)`
- Findings:
  - Antigravity upstream 403 bodies were returned raw, without direct remediation guidance.
- Fixes:
  - Added Antigravity 403 message enrichment for known subscription/license denial patterns.
  - Added helper-based status error construction and tests.
- Changed files:
  - `pkg/llmproxy/executor/antigravity_executor.go`
  - `pkg/llmproxy/executor/antigravity_executor_error_test.go`

### #115 -kiro-aws-login 登录后一直封号
- Status: `PARTIAL (safer troubleshooting guidance)`
- Findings:
  - Root cause is upstream/account policy behavior (AWS/Identity Center), not locally fixable in code path alone.
- Fixes:
  - Added targeted CLI troubleshooting branch for AWS access portal sign-in failure signatures.
  - Guidance now recommends cautious retry and auth-code fallback to reduce repeated failing attempts.
- Changed files:
  - `pkg/llmproxy/cmd/kiro_login.go`
  - `pkg/llmproxy/cmd/kiro_login_test.go`

### #111 Antigravity authentication failed (callback server bind/access permissions)
- Status: `DONE (clear remediation hint)`
- Findings:
  - Callback bind failures returned generic error text.
- Fixes:
  - Added callback server error formatter to detect common bind-denied / port-in-use cases.
  - Error now explicitly suggests `--oauth-callback-port <free-port>`.
- Changed files:
  - `sdk/auth/antigravity.go`
  - `sdk/auth/antigravity_error_test.go`

## Focused Test Evidence
- `go test ./cmd/server`
  - `ok   github.com/router-for-me/CLIProxyAPI/v6/cmd/server 2.258s`
- `go test ./pkg/llmproxy/cmd`
  - `ok   github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/cmd 0.724s`
- `go test ./sdk/auth`
  - `ok   github.com/router-for-me/CLIProxyAPI/v6/sdk/auth 0.656s`
- `go test ./pkg/llmproxy/executor ./sdk/cliproxy`
  - `ok   github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/executor 1.671s`
  - `ok   github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy 0.717s`

## All Changed Files
- `cmd/server/main.go`
- `cmd/server/config_path.go`
- `cmd/server/config_path_test.go`
- `pkg/llmproxy/api/handlers/management/config_basic.go`
- `pkg/llmproxy/cmd/kiro_login.go`
- `pkg/llmproxy/cmd/kiro_login_test.go`
- `pkg/llmproxy/executor/antigravity_executor.go`
- `pkg/llmproxy/executor/antigravity_executor_error_test.go`
- `sdk/auth/antigravity.go`
- `sdk/auth/antigravity_error_test.go`
- `sdk/cliproxy/builder.go`
- `sdk/cliproxy/service.go`

## Blockers / Follow-ups
- External-provider dependencies prevent deterministic local reproduction of:
  - Kiro AWS account lock/suspension behavior (`#115`)
  - Antigravity license entitlement state (`#125`)
- Recommended follow-up validation in staging:
  - Cloud deploy startup on ClawCloud with mounted config variants.
  - Fill-first behavior with >=2 credentials under same provider/model.
