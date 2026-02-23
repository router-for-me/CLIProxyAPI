# Issue Wave CPB-0721-0730 Lane E5 Report

- Lane: `E5 (cliproxy)`
- Window: `CPB-0721` to `CPB-0730`
- Worktree: `/Users/kooshapari/temp-PRODVERCEL/485/kush/cliproxyapi-plusplus`
- Scope policy: lane-only scope; no unrelated edits.

## Implemented

### CPB-0721 - Antigravity API 400 compatibility gaps (`$ref` / `$defs`)
- Status: implemented.
- Outcome:
  - Added a schema post-clean step in Antigravity request construction to hard-remove all `"$ref"` and `"$defs"` keys from tool schemas after existing cleanup.
  - Applied the same hardening in both executor entrypoints:
    - `pkg/llmproxy/executor/antigravity_executor.go`
    - `pkg/llmproxy/runtime/executor/antigravity_executor.go`
  - Added shared utility helper to remove arbitrary key names from JSON bodies by recursive path walk.
- Evidence:
  - `pkg/llmproxy/util/translator.go` (`DeleteKeysByName`)
  - `pkg/llmproxy/executor/antigravity_executor.go`
  - `pkg/llmproxy/runtime/executor/antigravity_executor.go`

### CPB-0721 regression coverage - Antigravity tool schema key stripping
- Status: implemented.
- Outcome:
  - Added buildRequest regression tests with schemas containing `$defs` and `$ref` and recursive assertions that neither key survives final outgoing payload.
- Evidence:
  - `pkg/llmproxy/executor/antigravity_executor_buildrequest_test.go`
  - `pkg/llmproxy/runtime/executor/antigravity_executor_buildrequest_test.go`

## Validation Commands

- `go test ./pkg/llmproxy/executor -run TestAntigravityBuildRequest -count=1`
- `go test ./pkg/llmproxy/runtime/executor -run TestAntigravityBuildRequest -count=1`
- `go test ./pkg/llmproxy/util -run TestDeleteKeysByName -count=1`

## Docs and Notes

- Added docs hand-off notes for CPB-0721 schema-key cleanup and regression checks.
  - `docs/guides/cpb-0721-0730-lane-e5-notes.md`
