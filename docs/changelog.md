# Changelog

## 2026-02-22

### CPB-0781 — Claude beta header ingestion hardening

- Hardened `betas` ingestion in both Claude executor paths (`pkg/llmproxy/executor` and `pkg/llmproxy/runtime/executor`):
  - ignore malformed non-string items in `betas` arrays
  - support comma-separated string payloads for tolerant legacy ingestion
  - always remove `betas` from upstream body after extraction
- Added regression tests in:
  - `pkg/llmproxy/executor/claude_executor_betas_test.go`
  - `pkg/llmproxy/runtime/executor/claude_executor_betas_test.go`

### CPB-0784 — Provider-agnostic web search translator utility

- Extracted shared web-search detection into:
  - `pkg/llmproxy/translator/util/websearch.go`
- Rewired Kiro and Codex translators to consume that shared helper.
- Added regression tests in:
  - `pkg/llmproxy/translator/util/websearch_test.go`
  - `pkg/llmproxy/translator/kiro/claude/kiro_websearch_test.go`
  - `pkg/llmproxy/translator/codex/claude/codex_claude_request_test.go`

### CPB-0782 / CPB-0783 / CPB-0786 — documentation bootstrap

- Added Opus 4.5 quickstart and Nano Banana quickstart docs:
  - `docs/features/providers/cpb-0782-opus-4-5-quickstart.md`
  - `docs/features/providers/cpb-0786-nano-banana-quickstart.md`
- Added deterministic HMR/runbook guidance for gemini 3 pro preview tool failures:
  - `docs/operations/cpb-0783-gemini-3-pro-preview-hmr.md`

## 2026-02-23

### CPB-0600 — iFlow model metadata naming standardization

- Standardized the `iflow-rome-30ba3b` static model metadata:
  - `display_name` is now `iFlow-ROME-30BA3B`
  - `description` is now `iFlow ROME-30BA3B model`
- Adjacent cleanup: added a targeted regression test in
  `pkg/llmproxy/registry/model_definitions_test.go` to lock this naming contract.

Compatibility guarantees:

- **Request/response contracts:** the model identifier remains `iflow-rome-30ba3b`.
- **Routing behavior:** no runtime routing, auth, or request-handling logic changed.
- **Downstream impact:** only `/v1/models` metadata shape/values for this model are adjusted.

Caveats:

- Existing clients that display-matched hard-coded `DisplayName` strings should update to match the new
  `iFlow-ROME-30BA3B` value.
