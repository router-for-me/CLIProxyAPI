# Issue Wave Next32 - Lane 3 Report

Scope: `router-for-me/CLIProxyAPIPlus` issues `#147 #146 #145 #136 #133 #129`
Worktree: `cliproxyapi-plusplus-wave-cpb-3`

## Per-Issue Status

### #147
- Status: `pending`
- Notes: lane-started

### #146
- Status: `pending`
- Notes: lane-started

### #145
- Status: `pending`
- Notes: lane-started

### #136
- Status: `pending`
- Notes: lane-started

### #133
- Status: `pending`
- Notes: lane-started

### #129
- Status: `pending`
- Notes: lane-started

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

- `task quality:fmt:check`
- `QUALITY_PACKAGES='./pkg/llmproxy/api ./sdk/api/handlers/openai' task quality:quick`

## Blockers

- None recorded yet; work is in planning state.
