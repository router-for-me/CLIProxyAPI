# Issue Wave CPB-0036..0105 Lane 6 Report

## Scope
- Lane: 6
- Worktree: `/Users/kooshapari/temp-PRODVERCEL/485/kush/cliproxyapi-plusplus-wave-cpb-6`
- Assigned items in this pass: `CPB-0086..CPB-0095`
- Commit status: no commits created

## Summary
- Triaged all 10 assigned items.
- Implemented 2 safe quick wins:
  - `CPB-0090`: fix log-dir size enforcement to include nested day subdirectories.
  - `CPB-0095`: add regression test to lock `response_format` -> `text.format` Codex translation behavior.
- Remaining items are either already covered by existing code/tests, or require broader product/feature work than lane-safe changes.

## Per-Item Status

### CPB-0086 - `codex: usage_limit_reached (429) should honor resets_at/resets_in_seconds as next_retry_after`
- Status: triaged, blocked for safe quick-win in this lane.
- What was found:
  - No concrete handling path was identified in this worktree for `usage_limit_reached` with `resets_at` / `resets_in_seconds` projection to `next_retry_after`.
  - Existing source mapping only appears in planning artifacts.
- Lane action:
  - No code change (avoided speculative behavior without upstream fixture/contract).
- Evidence:
  - Focused repo search did not surface implementation references outside planning board docs.

### CPB-0087 - `process-compose/HMR refresh workflow` for Gemini Web concerns
- Status: triaged, not implemented (missing runtime surface in this worktree).
- What was found:
  - No `process-compose.yaml` exists in this lane worktree.
  - Gemini Web is documented as supported config in SDK docs, but no local process-compose profile to patch.
- Lane action:
  - No code change.
- Evidence:
  - `ls process-compose.yaml` -> not found.
  - `docs/sdk-usage.md:171` and `docs/sdk-usage_CN.md:163` reference Gemini Web config behavior.

### CPB-0088 - `fix(claude): token exchange blocked by Cloudflare managed challenge`
- Status: triaged as already addressed in codebase.
- What was found:
  - Claude auth transport explicitly uses `utls` Firefox fingerprint to bypass Anthropic Cloudflare TLS fingerprint checks.
- Lane action:
  - No change required.
- Evidence:
  - `pkg/llmproxy/auth/claude/utls_transport.go:18-20`
  - `pkg/llmproxy/auth/claude/utls_transport.go:103-112`

### CPB-0089 - `Qwen OAuth fails`
- Status: triaged, partial confidence; no safe localized patch identified.
- What was found:
  - Qwen auth/executor paths are present and unit tests pass for current covered scenarios.
  - No deterministic failing fixture in local tests to patch against.
- Lane action:
  - Ran focused tests, no code change.
- Evidence:
  - `go test ./pkg/llmproxy/auth/qwen -count=1` -> `ok`

### CPB-0090 - `logs-max-total-size-mb` misses per-day subdirectories
- Status: fixed in this lane with regression coverage.
- What was found:
  - `enforceLogDirSizeLimit` previously scanned only top-level `os.ReadDir(dir)` entries.
  - Nested log files (for date-based folders) were not counted/deleted.
- Safe fix implemented:
  - Switched to `filepath.WalkDir` recursion and included all nested `.log`/`.log.gz` files in total-size enforcement.
  - Added targeted regression test that creates nested day directory and verifies oldest nested file is removed.
- Changed files:
  - `pkg/llmproxy/logging/log_dir_cleaner.go`
  - `pkg/llmproxy/logging/log_dir_cleaner_test.go`
- Evidence:
  - `pkg/llmproxy/logging/log_dir_cleaner.go:100-131`
  - `pkg/llmproxy/logging/log_dir_cleaner_test.go:60-85`

### CPB-0091 - `All credentials for model claude-sonnet-4-6 are cooling down`
- Status: triaged as already partially covered.
- What was found:
  - Model registry includes cooling-down models in availability listing when suspension is quota-only.
- Lane action:
  - No code change.
- Evidence:
  - `pkg/llmproxy/registry/model_registry.go:745-747`

### CPB-0092 - `Add claude-sonnet-4-6 to registered Claude models`
- Status: triaged as already covered.
- What was found:
  - Default OAuth model-alias mappings include Sonnet 4.6 alias entries.
  - Related config tests pass.
- Lane action:
  - No code change.
- Evidence:
  - `pkg/llmproxy/config/oauth_model_alias_migration.go:56-57`
  - `go test ./pkg/llmproxy/config -run 'OAuthModelAlias' -count=1` -> `ok`

### CPB-0093 - `Claude Sonnet 4.5 models are deprecated - please remove from panel`
- Status: triaged, not implemented due compatibility risk.
- What was found:
  - Runtime still maps unknown models to Sonnet 4.5 fallback.
  - Removing/deprecating 4.5 from surfaced panel/model fallback likely requires coordinated migration and rollout guardrails.
- Lane action:
  - No code change.
- Evidence:
  - `pkg/llmproxy/runtime/executor/kiro_executor.go:1653-1655`

### CPB-0094 - `Gemini incorrect renaming of parameters -> parametersJsonSchema`
- Status: triaged as already covered with regression tests.
- What was found:
  - Existing executor regression tests assert `parametersJsonSchema` is renamed to `parameters` in request build path.
- Lane action:
  - No code change.
- Evidence:
  - `pkg/llmproxy/executor/antigravity_executor_buildrequest_test.go:16-18`
  - `go test ./pkg/llmproxy/runtime/executor -run 'AntigravityExecutorBuildRequest' -count=1` -> `ok`

### CPB-0095 - `codex 返回 Unsupported parameter: response_format`
- Status: quick-win hardening completed (regression lock).
- What was found:
  - Translator already maps OpenAI `response_format` to Codex Responses `text.format`.
  - Missing direct regression test in this file for the exact unsupported-parameter shape.
- Safe fix implemented:
  - Added test verifying output payload does not contain `response_format`, and correctly contains `text.format` fields.
- Changed files:
  - `pkg/llmproxy/translator/codex/openai/chat-completions/codex_openai_request_test.go`
- Evidence:
  - Mapping code: `pkg/llmproxy/translator/codex/openai/chat-completions/codex_openai_request.go:228-253`
  - New test: `pkg/llmproxy/translator/codex/openai/chat-completions/codex_openai_request_test.go:160-198`

## Test Evidence

Commands run (focused):

1. `go test ./pkg/llmproxy/logging -run 'LogDir|EnforceLogDirSizeLimit' -count=1`
- Result: `ok   github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/logging 4.628s`

2. `go test ./pkg/llmproxy/translator/codex/openai/chat-completions -run 'ConvertOpenAIRequestToCodex|ResponseFormat' -count=1`
- Result: `ok   github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/translator/codex/openai/chat-completions 1.869s`

3. `go test ./pkg/llmproxy/runtime/executor -run 'AntigravityExecutorBuildRequest|KiroExecutor_MapModelToKiro' -count=1`
- Result: `ok   github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/runtime/executor 1.172s`

4. `go test ./pkg/llmproxy/auth/qwen -count=1`
- Result: `ok   github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/auth/qwen 0.730s`

5. `go test ./pkg/llmproxy/config -run 'OAuthModelAlias' -count=1`
- Result: `ok   github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/config 0.869s`

## Files Changed In Lane 6
- `pkg/llmproxy/logging/log_dir_cleaner.go`
- `pkg/llmproxy/logging/log_dir_cleaner_test.go`
- `pkg/llmproxy/translator/codex/openai/chat-completions/codex_openai_request_test.go`
- `docs/planning/reports/issue-wave-cpb-0036-0105-lane-6.md`
