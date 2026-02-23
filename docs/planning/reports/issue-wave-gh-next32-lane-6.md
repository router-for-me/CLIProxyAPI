# Issue Wave Next32 - Lane 6 Report

Scope: `router-for-me/CLIProxyAPIPlus` issues `#83 #81 #79 #78 #72`
Worktree: `cliproxyapi-plusplus-wave-cpb-6`

## Per-Issue Status

### #83
- Status: `pending`
- Notes: lane-started

### #81
- Status: `pending`
- Notes: lane-started

### #79
- Status: `pending`
- Notes: lane-started

### #78
- Status: `pending`
- Notes: lane-started

### #72
- Status: `pending`
- Notes: lane-started

## Focused Checks

- `task quality:fmt:check`
- `QUALITY_PACKAGES='./pkg/llmproxy/api ./sdk/api/handlers/openai' task quality:quick`

## Blockers

- None recorded yet; work is in planning state.

## Wave2 Entries

### 2026-02-23 - #179 OpenAI-MLX/vLLM-MLX support
- Status: `done`
- Mapping:
  - Source issue: `router-for-me/CLIProxyAPIPlus#179`
  - Implemented fix: OpenAI-compatible model discovery now honors `models_endpoint` auth attribute (emitted from `models-endpoint` config), including absolute URL and absolute path overrides.
  - Why this is low risk: fallback/default `/v1/models` behavior is unchanged; only explicit override handling is added.
- Files:
  - `pkg/llmproxy/executor/openai_models_fetcher.go`
  - `pkg/llmproxy/executor/openai_models_fetcher_test.go`
  - `pkg/llmproxy/runtime/executor/openai_models_fetcher.go`
  - `pkg/llmproxy/runtime/executor/openai_models_fetcher_test.go`
- Tests:
  - `go test pkg/llmproxy/executor/openai_models_fetcher.go pkg/llmproxy/executor/proxy_helpers.go pkg/llmproxy/executor/openai_models_fetcher_test.go`
  - `go test pkg/llmproxy/runtime/executor/openai_models_fetcher.go pkg/llmproxy/runtime/executor/proxy_helpers.go pkg/llmproxy/runtime/executor/openai_models_fetcher_test.go`
- Verification notes:
  - Added regression coverage for `models_endpoint` path override and absolute URL override in both mirrored executor test suites.
- Blockers:
  - Package-level `go test ./pkg/llmproxy/executor` and `go test ./pkg/llmproxy/runtime/executor` are currently blocked by unrelated compile errors in existing lane files (`kiro_executor.go`, `codex_websockets_executor.go`).
