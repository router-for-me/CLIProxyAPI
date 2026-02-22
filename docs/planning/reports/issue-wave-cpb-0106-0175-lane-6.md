# Issue Wave CPB-0106..0175 Lane 6 Report

## Scope
- Lane: 6
- Worktree: `/Users/kooshapari/temp-PRODVERCEL/485/kush/cliproxyapi-plusplus-wave-cpb3-6`
- Assigned items in this pass: `CPB-0156..CPB-0165`
- Commit status: no commits created

## Summary
- Triaged all 10 assigned items.
- Implemented 2 safe quick wins with focused regression coverage:
  - `CPB-0160`: added unit tests for Vertex Imagen routing/conversion helpers.
  - `CPB-0165`: added chat-completions regression coverage for nullable type arrays in tool schemas.
- Remaining items were triaged as either already covered by existing code/tests or blocked for this lane because they require broader cross-repo/product changes and/or reproducible upstream fixtures.

## Per-Item Status

### CPB-0156 - `Invalid JSON payload received: Unknown name "deprecated"`
- Status: triaged as likely already mitigated in Gemini tool sanitation path; no new code change.
- What was found:
  - Gemini chat-completions translation sanitizes Google Search tool fields and has regression tests ensuring unsupported keys are removed.
- Lane action:
  - No patch (existing behavior/tests already cover this class of upstream schema-key rejection).
- Evidence:
  - `pkg/llmproxy/translator/gemini/openai/chat-completions/gemini_openai_request.go:369`
  - `pkg/llmproxy/translator/gemini/openai/chat-completions/gemini_openai_request_test.go:10`

### CPB-0157 - `proxy_ prefix applied to tool_choice.name but not tools[].name`
- Status: triaged as already covered.
- What was found:
  - Prefix logic applies to both `tool_choice.name` and tool declarations/history.
  - Existing tests assert both surfaces.
- Lane action:
  - No patch.
- Evidence:
  - `pkg/llmproxy/runtime/executor/claude_executor.go:796`
  - `pkg/llmproxy/runtime/executor/claude_executor.go:831`
  - `pkg/llmproxy/runtime/executor/claude_executor_test.go:14`

### CPB-0158 - `Windows startup auto-update command`
- Status: triaged, blocked for safe quick win in this lane.
- What was found:
  - No explicit CLI command surface for a Windows startup auto-update command was identified.
  - There is management asset auto-updater logic, but this does not map to the requested command-level feature.
- Lane action:
  - No code change.
- Evidence:
  - `pkg/llmproxy/managementasset/updater.go:62`

### CPB-0159 - `反重力逻辑加载失效` rollout safety
- Status: triaged as partially addressed by existing fallback/retry safeguards.
- What was found:
  - Antigravity executor already has base URL fallback and no-capacity retry logic.
- Lane action:
  - No code change.
- Evidence:
  - `pkg/llmproxy/executor/antigravity_executor.go:153`
  - `pkg/llmproxy/executor/antigravity_executor.go:209`
  - `pkg/llmproxy/executor/antigravity_executor.go:1543`

### CPB-0160 - `support openai image generations api(/v1/images/generations)`
- Status: quick-win hardening completed (unit coverage added for existing Imagen path).
- What was found:
  - Vertex executor has dedicated Imagen handling (`predict` action, request conversion, response conversion), but had no direct unit tests for these helpers.
- Safe fix implemented:
  - Added tests for Imagen action selection, request conversion from content text and options, and response conversion shape.
- Changed files:
  - `pkg/llmproxy/executor/gemini_vertex_executor_test.go`
- Evidence:
  - Runtime helper path: `pkg/llmproxy/executor/gemini_vertex_executor.go:38`
  - New tests: `pkg/llmproxy/executor/gemini_vertex_executor_test.go:10`

### CPB-0161 - `account has available credit but 503/429 occurs` integration path
- Status: triaged, blocked for lane-safe implementation.
- What was found:
  - Existing docs and executors already cover retry/cooldown behavior for `429/5xx`, but the requested non-subprocess integration contract is broader architectural work.
- Lane action:
  - No code change.
- Evidence:
  - `pkg/llmproxy/executor/gemini_executor.go:288`
  - `pkg/llmproxy/executor/kiro_executor.go:824`
  - `docs/provider-operations.md:48`

### CPB-0162 - `openclaw调用CPA中的codex5.2报错`
- Status: triaged, blocked (no deterministic local repro).
- What was found:
  - Codex executor and `gpt-5.2-codex` model definitions exist in this worktree, but no failing fixture/test tied to the reported `openclaw` path was present.
- Lane action:
  - No code change to avoid speculative behavior.
- Evidence:
  - `pkg/llmproxy/runtime/executor/codex_executor.go:86`
  - `pkg/llmproxy/registry/model_definitions.go:317`

### CPB-0163 - `opus4.6 1m context vs 280K request-size limit`
- Status: triaged, blocked for safe quick win.
- What was found:
  - No single explicit `280KB` hard-limit constant/path was isolated in this worktree for a safe local patch.
  - Related payload-sizing behavior appears distributed (for example token estimation/compression helpers), requiring broader validation.
- Lane action:
  - No code change.
- Evidence:
  - `pkg/llmproxy/executor/kiro_executor.go:3624`
  - `pkg/llmproxy/translator/kiro/claude/tool_compression.go:1`

### CPB-0164 - `iflow token refresh generic 500 "server busy"`
- Status: triaged as already covered.
- What was found:
  - iFlow token refresh already surfaces provider error payload details, including `server busy`, and has targeted regression coverage.
- Lane action:
  - No code change.
- Evidence:
  - `pkg/llmproxy/auth/iflow/iflow_auth.go:165`
  - `pkg/llmproxy/auth/iflow/iflow_auth_test.go:87`

### CPB-0165 - `Nullable type arrays in tool schemas cause 400 on Antigravity/Droid Factory`
- Status: quick-win hardening completed.
- What was found:
  - Responses-path nullable schema handling had coverage; chat-completions Gemini path lacked a dedicated regression assertion for nullable arrays.
- Safe fix implemented:
  - Added chat-completions test asserting nullable `type` arrays are not stringified during tool schema conversion.
- Changed files:
  - `pkg/llmproxy/translator/gemini/openai/chat-completions/gemini_openai_request_test.go`
- Evidence:
  - Existing conversion path: `pkg/llmproxy/translator/gemini/openai/chat-completions/gemini_openai_request.go:323`
  - New test: `pkg/llmproxy/translator/gemini/openai/chat-completions/gemini_openai_request_test.go:91`

## Test Evidence

Commands run (focused):

1. `go test ./pkg/llmproxy/translator/gemini/openai/chat-completions -run 'NullableTypeArrays|GoogleSearch|SkipsEmptyAssistantMessage' -count=1`
- Result: `ok   github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/translator/gemini/openai/chat-completions 0.667s`

2. `go test ./pkg/llmproxy/executor -run 'GetVertexActionForImagen|ConvertToImagenRequest|ConvertImagenToGeminiResponse|IFlowExecutorParseSuffix|PreserveReasoningContentInMessages' -count=1`
- Result: `ok   github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/executor 1.339s`

3. `go test ./pkg/llmproxy/runtime/executor -run 'ApplyClaudeToolPrefix|StripClaudeToolPrefix' -count=1`
- Result: `ok   github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/runtime/executor 1.164s`

4. `go test ./pkg/llmproxy/auth/iflow -run 'RefreshTokensProviderErrorPayload|ExchangeCodeForTokens|AuthorizationURL' -count=1`
- Result: `ok   github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/auth/iflow 0.659s`

## Files Changed In Lane 6
- `pkg/llmproxy/translator/gemini/openai/chat-completions/gemini_openai_request_test.go`
- `pkg/llmproxy/executor/gemini_vertex_executor_test.go`
- `docs/planning/reports/issue-wave-cpb-0106-0175-lane-6.md`
