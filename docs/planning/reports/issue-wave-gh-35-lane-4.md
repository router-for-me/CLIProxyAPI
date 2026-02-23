# Issue Wave GH-35 Lane 4 Report

## Scope
- Lane: `workstream-cpb-4`
- Target issues: `#198`, `#183`, `#179`, `#178`, `#177`
- Worktree: `cliproxyapi-plusplus-worktree-4`
- Date: 2026-02-22

## Per-Issue Status

### #177 Kiro Token import fails (`Refresh token is required`)
- Status: `fixed (safe, implemented)`
- What changed:
  - Kiro IDE token loader now checks both default and legacy token file paths.
  - Token parsing now accepts both camelCase and snake_case key formats.
  - Custom token-path loader now uses the same tolerant parser.
- Changed files:
  - `pkg/llmproxy/auth/kiro/aws.go`
  - `pkg/llmproxy/auth/kiro/aws_load_token_test.go`

### #178 Claude `thought_signature` forwarded to Gemini causes Base64 decode errors
- Status: `hardened with explicit regression coverage`
- What changed:
  - Added translator regression tests to verify model-part thought signatures are rewritten to `skip_thought_signature_validator` in both Gemini and Gemini-CLI request paths.
- Changed files:
  - `pkg/llmproxy/translator/gemini/gemini/gemini_gemini_request_test.go`
  - `pkg/llmproxy/translator/gemini-cli/gemini/gemini-cli_gemini_request_test.go`

### #183 why no Kiro in dashboard
- Status: `partially fixed (safe, implemented)`
- What changed:
  - AMP provider model route now serves dedicated static model inventories for `kiro` and `cursor` instead of generic OpenAI model listing.
  - Added route-level regression test for dedicated-provider model listing.
- Changed files:
  - `pkg/llmproxy/api/modules/amp/routes.go`
  - `pkg/llmproxy/api/modules/amp/routes_test.go`

### #198 Cursor CLI/Auth support
- Status: `partially improved (safe surface fix)`
- What changed:
  - Cursor model visibility in AMP provider alias models endpoint is now dedicated and deterministic (same change as #183 path).
- Changed files:
  - `pkg/llmproxy/api/modules/amp/routes.go`
  - `pkg/llmproxy/api/modules/amp/routes_test.go`
- Note:
  - This does not implement net-new Cursor auth flows; it improves discoverability/compatibility at provider model listing surfaces.

### #179 OpenAI-MLX-Server and vLLM-MLX support
- Status: `docs-level support clarified`
- What changed:
  - Added explicit provider-usage documentation showing MLX/vLLM-MLX via `openai-compatibility` block and prefixed model usage.
- Changed files:
  - `docs/provider-usage.md`

## Test Evidence

### Executed and passing
- `go test ./pkg/llmproxy/auth/kiro -run 'TestLoadKiroIDEToken_FallbackLegacyPathAndSnakeCase|TestLoadKiroIDEToken_PrefersDefaultPathOverLegacy' -count=1`
  - Result: `ok github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/auth/kiro 0.714s`
- `go test ./pkg/llmproxy/auth/kiro -count=1`
  - Result: `ok github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/auth/kiro 2.064s`
- `go test ./pkg/llmproxy/api/modules/amp -run 'TestRegisterProviderAliases_DedicatedProviderModels' -count=1`
  - Result: `ok github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/api/modules/amp 2.427s`
- `go test ./pkg/llmproxy/translator/gemini/gemini -run 'TestConvertGeminiRequestToGemini|TestConvertGeminiRequestToGemini_SanitizesThoughtSignatureOnModelParts' -count=1`
  - Result: `ok github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/translator/gemini/gemini 4.603s`
- `go test ./pkg/llmproxy/translator/gemini-cli/gemini -run 'TestConvertGeminiRequestToGeminiCLI|TestConvertGeminiRequestToGeminiCLI_SanitizesThoughtSignatureOnModelParts' -count=1`
  - Result: `ok github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/translator/gemini-cli/gemini 1.355s`

### Attempted but not used as final evidence
- `go test ./pkg/llmproxy/api/modules/amp -count=1`
  - Observed as long-running/hanging in this environment; targeted amp tests were used instead.

## Blockers / Limits
- #198 full scope (Cursor auth/storage protocol support) is broader than a safe lane-local patch; this pass focuses on model-listing visibility behavior.
- #179 full scope (new provider runtime integrations) was not attempted in this lane due risk/scope; docs now clarify supported path through existing OpenAI-compatible integration.
- No commits were made.
