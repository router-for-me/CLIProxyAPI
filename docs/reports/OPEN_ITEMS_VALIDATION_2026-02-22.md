# Open Items Validation (2026-02-23)

Scope revalidated on local `main` at commit `62fd80c23283e362b2417ec0395e8bc91743c844` for:
- Issues: #198, #206, #210, #232, #241, #258
- PRs: #259, #11

## Status Revalidation

- #198 `Cursor CLI / Auth Support` -> Implemented
  - Evidence: cursor login flow in `pkg/llmproxy/cmd/cursor_login.go`, cursor auth synthesis in `pkg/llmproxy/auth/synthesizer/config.go:405`, executor registration for cursor in `sdk/cliproxy/service.go:429`.
- #206 `Nullable type arrays in tool schemas` -> Implemented
  - Evidence: nullable handling regression test in `pkg/llmproxy/translator/gemini/openai/responses/gemini_openai-responses_request_test.go:91`.
- #210 `Kiro x Ampcode Bash parameter incompatibility` -> Implemented
  - Evidence: Bash required field map accepts both keys in `pkg/llmproxy/translator/kiro/claude/truncation_detector.go:68`; regression in `pkg/llmproxy/translator/kiro/claude/truncation_detector_test.go:48`.
- #232 `Add AMP auth as Kiro` -> Implemented
  - Evidence: AMP auth routes proxied for CLI login flow in `pkg/llmproxy/api/modules/amp/routes.go:226`; provider aliases include `kiro`/`cursor` model routing in `pkg/llmproxy/api/modules/amp/routes.go:299` with coverage in `pkg/llmproxy/api/modules/amp/routes_test.go:176`.
- #241 `Copilot context length should always be 128K` -> Implemented
  - Evidence: enforced 128K normalization in `pkg/llmproxy/registry/model_definitions.go:495`; invariant test in `pkg/llmproxy/registry/model_definitions_test.go:52`.
- #258 `Variant fallback for codex reasoning_effort` -> Implemented
  - Evidence: fallback in chat-completions translator `pkg/llmproxy/translator/codex/openai/chat-completions/codex_openai_request.go:56` and responses translator `pkg/llmproxy/translator/codex/openai/responses/codex_openai-responses_request.go:49`.
- PR #259 `Normalize Codex schema handling` -> Implemented
  - Evidence: schema normalization functions in `pkg/llmproxy/runtime/executor/codex_executor.go:597` and regression coverage in `pkg/llmproxy/runtime/executor/codex_executor_schema_test.go:10`.
- PR #11 `content_block_start ordering` -> Implemented
  - Evidence: stream lifecycle test asserts `message_start` then `content_block_start` in `pkg/llmproxy/runtime/executor/github_copilot_executor_test.go:238`.

## Validation Commands and Outcomes

- `go test ./pkg/llmproxy/translator/gemini/openai/responses -run 'TestConvertOpenAIResponsesRequestToGeminiHandlesNullableTypeArrays' -count=1` -> pass
- `go test ./pkg/llmproxy/translator/kiro/claude -run 'TestDetectTruncation' -count=1` -> pass
- `go test ./pkg/llmproxy/registry -run 'TestGetGitHubCopilotModels' -count=1` -> pass
- `go test ./pkg/llmproxy/runtime/executor -run 'TestNormalizeCodexToolSchemas' -count=1` -> pass
- `go test ./pkg/llmproxy/runtime/executor -run 'TestTranslateGitHubCopilotResponsesStreamToClaude_TextLifecycle' -count=1` -> pass
- `go test ./pkg/llmproxy/translator/codex/openai/chat-completions -run 'Test.*Variant|TestConvertOpenAIRequestToCodex' -count=1` -> pass
- `go test ./pkg/llmproxy/translator/codex/openai/responses -run 'Test.*Variant|TestConvertOpenAIResponsesRequestToCodex' -count=1` -> pass
- `go test ./pkg/llmproxy/api/modules/amp -run 'TestRegisterProviderAliases_DedicatedProviderModels|TestRegisterProviderAliases_DedicatedProviderModelsV1' -count=1` -> pass
- `go test ./pkg/llmproxy/auth/synthesizer -run 'TestConfigSynthesizer_SynthesizeCursorKeys_' -count=1` -> pass
- `go test ./pkg/llmproxy/cmd -run 'TestDoCursorLogin|TestSetupOptions_ContainsCursorLogin' -count=1` -> fail (blocked by `sdk/cliproxy/service.go` ProviderExecutor interface mismatch in unrelated compilation unit)
- `go vet ./...` -> fail (multiple import/type drifts, including stale `internal/...` references and interface/symbol mismatches)

## Current `task quality` Boundary

Current boundary is `go vet ./...` failing on repo-wide import/type drift (notably stale `internal/...` references and interface mismatches), so full `task quality` cannot currently pass end-to-end even though the targeted open-item validations above pass.

## Recommended Next (Unresolved Only)

1. Fix repo-wide `go vet` blockers first (`internal/...` stale imports and ProviderExecutor interface mismatches), then rerun full `task quality`.
2. After the vet/build baseline is green, rerun the cursor CLI test slice under `pkg/llmproxy/cmd` to remove the remaining validation gap.
