# Open Items Validation (Fork Main) - 2026-02-22

Scope audited against local `main` (fork) for:
- Issues: #198, #206, #210, #232, #241, #258
- PRs: #259, #11

## Already Implemented on Fork Main

- #206 Nullable schema arrays in Gemini responses translator
  - Evidence: commit `9b25e954` (`fix(gemini): sanitize nullable tool schema types in responses translator (#206)`)
- #210 Kiro/Amp Bash `cmd` compatibility
  - Evidence: commit `e7c20e4f` (`fix(kiro): accept Bash cmd alias to prevent amp truncation loops (#210)`)
- #232 AMP auth as Kiro-compatible flow
  - Evidence: commit `322381d3` (`feat(amp): add kiro-compatible amp auth flow and tests (#232)`)
- #241 Copilot context windows normalized to 128k
  - Evidence: commit `94c086e2` (`fix(registry): normalize github-copilot context windows to 128k (#241)`)
- #258 Codex `variant` fallback for thinking/reasoning
  - Evidence: `pkg/llmproxy/thinking/apply.go` in `extractCodexConfig` handles `variant` fallback

## Implemented Behavior Also Relevant to Open PRs

- PR #11 unexpected `content_block_start` order
  - Behavior appears present in current translator flow and was already audited as functionally addressed.

## Still Pending / Needs Decision

- #198 Cursor CLI/Auth support
  - Cursor-related model/routing references exist, but complete end-to-end Cursor auth onboarding should be validated with a dedicated E2E matrix.
- PR #259 Normalize Codex schema handling
  - Some normalization behavior exists, but parity with PR scope (including exact install/schema expectations) still needs targeted gap closure.

## Recommended Next 3

1. Add Cursor auth E2E coverage + quickstart parity checklist (#198).
2. Extract PR #259 into a test-first patch in codex executor schema normalization paths.
3. Close issue statuses on upstream/fork tracker with commit links from this report.
