# Feature: Migrate Qwen Provider to Canonical IR

## 1. Context
The project is migrating from a legacy many-to-many translation architecture to a unified **Canonical Intermediate Representation (IR)**. The Qwen provider (`qwen_executor.go`) currently relies on the legacy `sdktranslator` to convert inputs to OpenAI format, which is then sent to the Qwen API.

## 2. Goals
- **Remove Legacy Dependency**: Eliminate usage of `sdktranslator` in Qwen execution path.
- **Implement IR Interfaces**:
  - **Parsing**: Create `to_ir/qwen.go` (if Qwen format differs from OpenAI) or reuse `to_ir/openai.go` if compatible.
  - **Emission**: Create `from_ir/qwen.go` to convert Canonical IR -> Qwen API format.
- **Maintain Functionality**:
  - Chat completions (Req/Resp).
  - Streaming (SSE).
  - Auth token handling (Refresh, Headers).
  - Special logic ("poisoning" fix for Qwen3 empty tools).

## 3. Technical Approach

### 3.1 Input Parsing (`to_ir`)
Qwen uses an OpenAI-compatible API.
- **Plan**: Reuse `to_ir/openai.go` logic. If specific quirks exist, we might need a thin wrapper or configuration flag, but likely the standard OpenAI parser works for incoming requests if they mimic OpenAI.
- **Refinement**: Since the executor *receives* the raw request, and the goal is to use the Canonical IR pipeline, the `QwenExecutor` will receive a `UnifiedChatRequest` (IR) directly if the system is configured correctly, OR it will need to parse the incoming request into IR first.
- **Correction**: In the new architecture, the *Executor* receives the raw request and uses `to_ir` to get IR, then `from_ir` to send to upstream.
- **Decision**: Use `to_ir.ParseOpenAIRequest` (or similar) since input is typically OpenAI-compatible.

### 3.2 Output Emission (`from_ir`)
Create `translator_new/from_ir/qwen.go`.
- **Base**: Clone `from_ir/openai.go`.
- **Customization**:
  - **Headers**: `X-Goog-Api-Client`, `Client-Metadata`, `User-Agent` (from legacy implementation).
  - **Auth**: Bearer token handling.
  - **Payload quirks**: The "poisoning" fix for empty tools array (Qwen3 bug).

### 3.3 Executor Update (`internal/runtime/executor/qwen_executor.go`)
- Rewrite `Execute` and `ExecuteStream`.
- **Old Flow**: `Input -> sdktranslator(OpenAI) -> Http -> sdktranslator(Response) -> Output`
- **New Flow**:
  1. `to_ir`: Parse incoming `cliproxyexecutor.Request` -> `ir.UnifiedChatRequest`.
  2. `from_ir`: Convert `UnifiedChatRequest` -> Qwen JSON body (using new `from_ir/qwen.go`).
  3. **Http Request**: Send to Qwen API.
  4. **Response Parsing**: Convert Qwen response -> `ir.UnifiedChatResponse` (likely reusing OpenAI logic or specialized Qwen logic if response differs).
  5. `ir`: Convert `UnifiedChatResponse` -> Client format (e.g., SSE or JSON).

## 4. Acceptance Criteria
- [ ] Qwen provider works with `use-canonical-translator: true`.
- [ ] Chat completions work.
- [ ] Streaming works.
- [ ] Authentication headers are correctly preserved.
- [ ] The "empty tools" hack is preserved.
- [ ] Legacy `sdktranslator` imports are removed from `qwen_executor.go`.
