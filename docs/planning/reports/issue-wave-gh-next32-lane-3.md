# Issue Wave Next32 - Lane 3 Report

Scope: `router-for-me/CLIProxyAPIPlus` issues `#147 #146 #145 #136 #133 #129`
Worktree: `cliproxyapi-plusplus-wave-cpb-3`

## Per-Issue Status

### #147
- Status: `done`
- Notes: ARM64 deployment guidance and build path are validated.
- Code/docs surface:
  - `docs/install.md`
  - `Dockerfile`
- Acceptance command:
  - `rg -n "platform linux/arm64|uname -m|arm64" docs/install.md`
  - `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o /tmp/cliproxy_arm64_check ./cmd/server`

### #146
- Status: `blocked`
- Notes: no deterministic failing fixture in current repo state that maps to a safe bounded patch; deferred to dedicated repro lane.

### #145
- Status: `done`
- Notes: issue is still `OPEN` upstream, but deterministic regression coverage for the exact OpenAI-compat payload path exists and passes in this tree.
- Code/test surface:
  - `pkg/llmproxy/translator/kiro/claude/kiro_claude_request.go`
  - `pkg/llmproxy/translator/kiro/claude/kiro_claude_request_test.go`
- Evidence command:
  - `go test ./pkg/llmproxy/translator/kiro/claude -run 'TestBuildKiroPayload_OpenAICompatIssue145Payload' -count=1`
  - Result: `ok   github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/translator/kiro/claude	0.523s`

### #136
- Status: `blocked`
- Notes: low-risk refresh hardening exists, but full "no manual refresh needed" closure requires dedicated product status surface/API workflow not present in this repo lane.
- Code surface validated:
  - `pkg/llmproxy/auth/kiro/sso_oidc.go`
- Acceptance command:
  - `go test ./pkg/llmproxy/auth/kiro -run 'RefreshToken|SSOOIDC|Token|OAuth' -count=1`
  - Result: `ok   github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/auth/kiro`

### #133
- Status: `blocked`
- Notes: issue is still `OPEN`; current deterministic evidence shows config/normalization support for `fill-first`, but no direct request-routing behavior proof in this lane for the reported runtime symptom.
- Code/test surface:
  - `pkg/llmproxy/api/handlers/management/config_basic.go`
  - `pkg/llmproxy/api/handlers/management/config_basic_routing_test.go`
- Evidence command:
  - `rg -n "fill-first|Test.*Fill|TestNormalizeRoutingStrategy_AcceptsFillFirstAliases" pkg/llmproxy | head -n 80`
  - Result: shows `fill-first` normalization/config coverage (for example `config_basic_routing_test.go:5`) but no deterministic end-to-end routing-behavior proof.

### #129
- Status: `done`
- Notes: cloud deploy config-path fallback support is present and passing focused package tests.
- Code surface validated:
  - `cmd/server/config_path.go`
  - `cmd/server/config_path_test.go`
  - `cmd/server/main.go`
- Acceptance command:
  - `go test ./cmd/server -count=1`
  - Result: `ok   github.com/router-for-me/CLIProxyAPI/v6/cmd/server`

### Wave2 #221 - `kiro账号被封`
- Status: `implemented`
- Source mapping:
  - Source issue: `router-for-me/CLIProxyAPIPlus#221` (Kiro account banned handling)
  - Fix: broaden Kiro 403 suspension detection to case-insensitive suspended/banned signals so banned accounts consistently trigger cooldown + remediation messaging in both non-stream and stream paths.
  - Code: `pkg/llmproxy/runtime/executor/kiro_executor.go`
  - Tests: `pkg/llmproxy/runtime/executor/kiro_executor_extra_test.go`
- Test commands:
  - `go test ./pkg/llmproxy/runtime/executor -run 'Test(IsKiroSuspendedOrBannedResponse|FormatKiroCooldownError|FormatKiroSuspendedStatusMessage)' -count=1`
  - Result: blocked by pre-existing package build failures in `pkg/llmproxy/runtime/executor/codex_websockets_executor.go` (`unused imports`, `undefined: authID`, `undefined: wsURL`).

## Focused Checks

- `rg -n "platform linux/arm64|uname -m|arm64" docs/install.md`
- `go test ./pkg/llmproxy/auth/kiro -run 'RefreshToken|SSOOIDC|Token|OAuth' -count=1`
- `go test ./cmd/server -count=1`

## Blockers

- `#133`: missing deterministic runtime proof for fill-first behavior beyond normalization-level coverage.
