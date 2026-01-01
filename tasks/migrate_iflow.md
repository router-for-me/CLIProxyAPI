# Task: Migrate iFlow Executor to Canonical IR

**Status**: [ ] Todo

## Context
Migrate `iflow_executor.go` to use `translator_new` architecture, similar to the Qwen migration.

## Sub-Tasks
- [ ] Create `from_ir/iflow.go` (if needed) or reuse OpenAI emitter if fully compatible.
- [ ] Refactor `internal/runtime/executor/iflow_executor.go`:
  - Replace `sdktranslator` with `to_ir` -> `from_ir` pipeline.
  - Ensure any iFlow-specific headers/auth handling is preserved.
- [ ] Verify with unit test.
