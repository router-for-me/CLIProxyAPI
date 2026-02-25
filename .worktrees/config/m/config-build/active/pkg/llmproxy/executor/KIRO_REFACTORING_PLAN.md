# kiro_executor.go Refactoring Plan

## Current State
- **File:** `pkg/llmproxy/executor/kiro_executor.go`
- **Size:** 4,676 lines (189KB)
- **Problem:** Monolithic file violates single responsibility principle

## Identified Logical Modules

### Module 1: Constants & Config (Lines ~1-150)
**File:** `kiro_constants.go`
- Constants: kiroContentType, kiroAcceptStream, retry configs
- Event stream frame size constants
- User-Agent constants
- Global fingerprint manager

### Module 2: Retry Logic (Lines ~150-350)
**File:** `kiro_retry.go`
- `retryConfig` struct
- `defaultRetryConfig()`
- `isRetryableError()`
- `isRetryableHTTPStatus()`
- `calculateRetryDelay()`
- `logRetryAttempt()`

### Module 3: HTTP Client (Lines ~350-500)
**File:** `kiro_client.go`
- `getKiroPooledHTTPClient()`
- `newKiroHTTPClientWithPooling()`
- `kiroEndpointConfig`
- Endpoint resolution functions

### Module 4: KiroExecutor Core (Lines ~500-1200)
**File:** `kiro_executor.go` (simplified)
- `KiroExecutor` struct
- `NewKiroExecutor()`
- `Identifier()`
- `PrepareRequest()`
- `HttpRequest()`
- `mapModelToKiro()`

### Module 5: Execution Logic (Lines ~1200-2500)
**File:** `kiro_execute.go`
- `Execute()`
- `executeWithRetry()`
- `kiroCredentials()`
- `determineAgenticMode()`
- `buildKiroPayloadForFormat()`

### Module 6: Streaming (Lines ~2500-3500)
**File:** `kiro_stream.go`
- `ExecuteStream()`
- `executeStreamWithRetry()`
- `EventStreamError`
- `eventStreamMessage`
- `parseEventStream()`
- `readEventStreamMessage()`
- `streamToChannel()`

### Module 7: Token & Auth (Lines ~3500-4200)
**File:** `kiro_auth.go`
- `CountTokens()`
- `Refresh()`
- `persistRefreshedAuth()`
- `reloadAuthFromFile()`
- `isTokenExpired()`

### Module 8: WebSearch (Lines ~4200-4676)
**File:** `kiro_websearch.go`
- `webSearchHandler`
- `newWebSearchHandler()`
- MCP integration functions

## Implementation Steps

1. **Phase 1:** Create new modular files with package-level functions (no public API changes)
2. **Phase 2:** Update imports in kiro_executor.go to use new modules
3. **Phase 3:** Run full test suite to verify no regressions
4. **Phase 4:** Deprecate old functions with redirects

## Estimated LOC Reduction
- Original: 4,676 lines
- After refactor: ~800 lines (kiro_executor.go) + ~600 lines/module × 7 modules
- **Net reduction:** ~30% through better organization and deduplication

## Risk Assessment
- **Medium Risk:** Requires comprehensive testing
- **Mitigation:** All existing tests must pass; add integration tests for each module
- **Timeline:** 2-3 hours for complete refactor

## Dependencies to Consider
- Other executors in `executor/` package use similar patterns
- Consider creating shared `executorutil` package for common retry/logging patterns
