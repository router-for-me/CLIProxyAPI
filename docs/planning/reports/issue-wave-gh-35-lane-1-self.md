# Issue Wave GH-35 – Lane 1 (Self) Report

## Scope
- Source file: `docs/planning/issue-wave-gh-35-2026-02-22.md`
- Items assigned to self lane:
  - #258 Support `variant` parameter as fallback for `reasoning_effort` in codex models
  - #254 请求添加新功能：支持对Orchids的反代
  - #253 Codex support
  - #251 Bug thinking
  - #246 fix(cline): add grantType to token refresh and extension headers

## Work completed
- Implemented `#258` in `pkg/llmproxy/translator/codex/openai/chat-completions/codex_openai_request.go`
  - Added `variant` fallback when `reasoning_effort` is absent.
  - Preferred existing behavior: `reasoning_effort` still wins when present.
- Added regression tests in `pkg/llmproxy/translator/codex/openai/chat-completions/codex_openai_request_test.go`
  - `TestConvertOpenAIRequestToCodex_UsesVariantFallbackWhenReasoningEffortMissing`
  - `TestConvertOpenAIRequestToCodex_UsesReasoningEffortBeforeVariant`
- Implemented `#253`/`#251` support path in `pkg/llmproxy/thinking/apply.go`
  - Added `variant` fallback parsing for Codex thinking extraction (`thinking` compatibility path) when `reasoning.effort` is absent.
- Added regression coverage in `pkg/llmproxy/thinking/apply_codex_variant_test.go`
  - `TestExtractCodexConfig_PrefersReasoningEffortOverVariant`
  - `TestExtractCodexConfig_VariantFallback`
- Implemented `#258` in responses path in `pkg/llmproxy/translator/codex/openai/responses/codex_openai-responses_request.go`
  - Added `variant` fallback when `reasoning.effort` is absent.
- Added regression coverage in `pkg/llmproxy/translator/codex/openai/responses/codex_openai-responses_request_test.go`
  - `TestConvertOpenAIResponsesRequestToCodex_UsesVariantAsReasoningEffortFallback`
  - `TestConvertOpenAIResponsesRequestToCodex_UsesReasoningEffortOverVariant`

## Not yet completed
- #254, #246 remain queued for next execution pass (lack of actionable implementation details in repo/issue text).

## Validation
- `go test ./pkg/llmproxy/translator/codex/openai/chat-completions`
- `go test ./pkg/llmproxy/translator/codex/openai/responses`
- `go test ./pkg/llmproxy/thinking`

## Risk / open points
- #254 may require provider registration/model mapping work outside current extracted evidence.
- #246 requires issue-level spec for whether `grantType` is expected in body fields vs headers in a specific auth flow.
