# Task: Migrate Qwen Provider to Canonical IR

**Status**: [ ] Todo

## Context
Migrate `internal/runtime/executor/qwen_executor.go` to use the new Canonical IR architecture (`translator_new`), removing dependency on legacy `sdktranslator`.

## Sub-Tasks

### 1. Create Qwen Emitter (`from_ir/qwen.go`)
- [ ] Create `internal/translator_new/from_ir/qwen.go`.
- [ ] Implement `QwenProvider` struct implementing `Provider` interface.
- [ ] Copy OpenAI emitter logic as base.
- [ ] Add specific Qwen headers (`X-Goog-Api-Client`, etc.).
- [ ] Implement the "poisoning" fix: if `Tools` is empty array in IR, inject the dummy "do_not_call_me" tool.
- [ ] **Verification**: Unit test checking JSON output contains specific headers and dummy tool when tools are empty.

### 2. Update Qwen Executor (`qwen_executor.go`)
- [ ] Import `internal/translator_new` packages.
- [ ] Refactor `Execute`:
  - Parse input using `to_ir.ParseRequest`.
  - Generate upstream payload using `from_ir.NewQwenProvider().GenerateRequest`.
  - Handle response using `to_ir.ParseResponse` (assuming response is OpenAI-compatible).
  - specific response conversion if needed.
- [ ] Refactor `ExecuteStream`:
  - Similar flow.
  - Use `translator_new` streaming utilities.
- [ ] **Verification**: Run `go test ./internal/runtime/executor/...` (if tests exist) or create a manual test script mocking Qwen API.

### 3. Cleanup
- [ ] Remove `sdktranslator` imports.
- [ ] Remove legacy `applyQwenHeaders` (move logic to `from_ir/qwen.go`).

## Verification Plan
1. **Mock Test**: Create a small Go test file that:
   - Initializes `QwenExecutor`.
   - Mocks the HTTP transport.
   - Sends a request.
   - Verifies the outgoing request body (checking for dummy tool) and headers.
2. **Integration**: If credentials available, run `cliproxy chat --provider qwen --model qwen-coder-turbo "hello"`. (Optional, depends on env).

