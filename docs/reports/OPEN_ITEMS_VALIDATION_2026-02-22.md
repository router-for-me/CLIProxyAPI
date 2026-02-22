# Open Items Validation (2026-02-22)

Scope audited against `upstream/main` (`af8e9ef45806889f3016d91fb4da764ceabe82a2`) for:
- Issues: #198, #206, #210, #232, #241, #258
- PRs: #259, #11

## Already Implemented

- PR #11 `fix: handle unexpected 'content_block_start' event order (fixes #4)`
  - Status: Implemented on `main` (behavior present even though exact PR commit is not merged).
  - Current `main` emits `message_start` before any content/tool block emission on first delta chunk.

## Partially Implemented

- Issue #198 `Cursor CLI \ Auth Support`
  - Partial: Cursor-related request-format handling exists for Kiro thinking tags, but no Cursor auth/provider implementation exists.
- Issue #232 `Add AMP auth as Kiro`
  - Partial: AMP module and AMP upstream config exist, but no AMP auth provider/login flow in `internal/auth`.
- Issue #241 `copilot context length should always be 128K`
  - Partial: Some GitHub Copilot models are 128K, but many remain 200K (and Gemini entries at 1,048,576).
- Issue #258 `Support variant fallback for reasoning_effort in codex models`
  - Partial: Codex reasoning extraction supports `reasoning.effort`, but there is no fallback from `variant`.
- PR #259 `Normalize Codex schema handling`
  - Partial: `main` already has some Codex websocket normalization (`response.done` -> `response.completed`), but the proposed schema-normalization functions/tests and install flow are not present.

## Not Implemented

- Issue #206 `Nullable type arrays in tool schemas cause 400 on Antigravity/Droid Factory`
  - Not implemented on `main`; the problematic uppercasing path for tool parameter `type` is still present.
- Issue #210 `Kiro x Ampcode Bash parameter incompatibility`
  - Not implemented on `main`; truncation detector still requires `Bash: {"command"}` instead of `cmd`.

## Evidence (commit/file refs)

- Baseline commit:
  - `upstream/main` -> `af8e9ef45806889f3016d91fb4da764ceabe82a2`

- PR #11 implemented behavior:
  - `internal/translator/openai/claude/openai_claude_response.go:130` emits `message_start` immediately on first `delta`.
  - `internal/translator/openai/claude/openai_claude_response.go:156`
  - `internal/translator/openai/claude/openai_claude_response.go:178`
  - `internal/translator/openai/claude/openai_claude_response.go:225`
  - File history on `main`: commit `cbe56955` (`Merge pull request #227 from router-for-me/plus`) contains current implementation.

- Issue #206 not implemented:
  - `internal/translator/gemini/openai/responses/gemini_openai-responses_request.go:357`
  - `internal/translator/gemini/openai/responses/gemini_openai-responses_request.go:364`
  - `internal/translator/gemini/openai/responses/gemini_openai-responses_request.go:365`
  - `internal/translator/gemini/openai/responses/gemini_openai-responses_request.go:371`
  - These lines still uppercase and rewrite schema types, matching reported failure mode.

- Issue #210 not implemented:
  - `internal/translator/kiro/claude/truncation_detector.go:66` still has `"Bash": {"command"}`.

- Issue #241 partially implemented:
  - 128K examples: `internal/registry/model_definitions.go:153`, `internal/registry/model_definitions.go:167`
  - 200K examples still present: `internal/registry/model_definitions.go:181`, `internal/registry/model_definitions.go:207`, `internal/registry/model_definitions.go:220`, `internal/registry/model_definitions.go:259`, `internal/registry/model_definitions.go:272`, `internal/registry/model_definitions.go:298`
  - 1M examples: `internal/registry/model_definitions.go:395`, `internal/registry/model_definitions.go:417`
  - Relevant history includes `740277a9` and `f2b1ec4f` (Copilot model definition updates).

- Issue #258 partially implemented:
  - Codex extraction only checks `reasoning.effort`: `internal/thinking/apply.go:459`-`internal/thinking/apply.go:467`
  - Codex provider applies only `reasoning.effort`: `internal/thinking/provider/codex/apply.go:64`, `internal/thinking/provider/codex/apply.go:85`, `internal/thinking/provider/codex/apply.go:120`
  - Search on `upstream/main` for codex `variant` fallback returned no implementation in codex execution/thinking paths.

- Issue #198 partial (format support, no provider auth):
  - Cursor-format mention in Kiro translator comments: `internal/translator/kiro/claude/kiro_claude_request.go:192`, `internal/translator/kiro/claude/kiro_claude_request.go:443`
  - No `internal/auth/cursor` provider on `main`; auth providers under `internal/auth` are: antigravity/claude/codex/copilot/gemini/iflow/kilo/kimi/kiro/qwen/vertex.

- Issue #232 partial (AMP exists but not as auth provider):
  - AMP config exists: `internal/config/config.go:111`-`internal/config/config.go:112`
  - AMP module exists: `internal/api/modules/amp/routes.go:1`
  - `internal/auth` has no `amp` auth provider directory on `main`.

- PR #259 partial:
  - Missing from `main`: `install.sh` (file absent on `upstream/main`).
  - Missing from `main`: `internal/runtime/executor/codex_executor_schema_test.go` (file absent).
  - Missing from `main`: `normalizeCodexToolSchemas` / `normalizeJSONSchemaArrays` symbols (no matches in `internal/runtime/executor/codex_executor.go`).
  - Already present adjacent normalization: `internal/runtime/executor/codex_websockets_executor.go:979` (`normalizeCodexWebsocketCompletion`).

## Recommended Next 5

1. Implement #206 exactly as proposed: remove per-property type uppercasing in Gemini responses translator and pass tool schema raw JSON (with tests for `["string","null"]` and nested schemas).
2. Implement #210 by supporting `Bash: {"cmd"}` in Kiro truncation required-fields map (or dual-accept with explicit precedence), plus regression test for Ampcode loop case.
3. Land #258 by mapping `variant` -> `reasoning.effort` for Codex requests when `reasoning.effort` is absent; include explicit mapping for `high`/`x-high`.
4. Resolve #259 as a focused split: (a) codex schema normalization + tests, (b) install flow/docs as separate PR to reduce review risk.
5. Decide policy for #241 (keep provider-native context lengths vs force 128K), then align `internal/registry/model_definitions.go` and add a consistency test for Copilot context lengths.
