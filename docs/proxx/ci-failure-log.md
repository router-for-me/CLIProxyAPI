# CI failure log

Every test failure that gets fixed is logged here, with one of these classes:

- **caught regression**: the test found a real bug in product code.
- **test bug**: the test itself was wrong (leaked state, raced, timed wall clock). Fixing it made it keep testing the same thing.
- **change detector**: the test failed because something changed on purpose, such as vendor data or a refactor, and nothing was broken.

Review on 2026-12-06, after two weeks of entries.
- Delete test files whose only failures are change detectors, and ban their pattern in AGENTS.md.
- Never delete tests for authorization, privacy, migrations or concurrency.

| Date | Test | Class | Cause | PR |
|---|---|---|---|---|
| 2026-11-21 | internal/runtime/executor `TestClaudeExecutorSharedCredentialMetadataMixedAccess` | caught regression | unlocked read of shared credential metadata (data race) | #23 |
| 2026-11-21 | internal/cache `TestIssue6332ReplayCommitPrecedesCompletion` | caught regression | executor goroutine wrote the pooled gin context after the handler returned | #24 |
| 2026-11-21 | internal/api `TestModelsWithClientVersionReturnsCodexCatalog` | change detector | asserted gpt-5.5's vendor `service_tiers`; rewritten to assert pass-through behavior | #19 |
| 2026-11-21 | internal/translator/interactions `TestInteractionsTranslatorsDoNotImportGeminiTranslators` | test bug | regex pinned the old `/v7` module path, so it could never fail | #15 |
| 2026-11-21 | internal/runtime/executor `TestClaudeCacheControlDocChecksDoNotMaterializeLargeMessagesArray` | test bug | 20ms wall-clock bound | #22 |
| 2026-11-21 | internal/api/handlers/management `TestStartOAuthV8TargetedSessionRegistrationAcrossBuiltinProviders` | test bug | swapped `http.DefaultTransport` and session store globals | #25 |
| 2026-11-21 | sdk/api/handlers/openai `TestCodexClientModelsApplyPatchRouting` | test bug | another test leaked a registry client | #20 |
| 2026-11-21 | internal/api `TestOAuthCallbackRouteSkipsManagementKeyMiddleware` | test bug | fixed OAuth state reused across runs | #21 |
| 2026-11-21 | sdk/cliproxy `TestEndToEndAuthFile*` (2 tests) | test bug | two tests leaked registry clients | #26 |
| 2026-11-21 | sdk/cliproxy same-revision batch wait test | test bug | race inside the test on which auth reached the hook | #26 |
| 2026-11-21 | sdk/cliproxy `TestServiceApplyKVStoreConfig_HomeModeNeverOpensBBolt` | test bug | leaked global Home client | #26 |
| 2026-11-21 | sdk/cliproxy `TestServiceHeartbeatFailoverDoesNotDrainBlockingScope` | test bug | random retry jitter against a fixed 1s wait | #26 |
| 2026-11-22 | internal/wstunnel `TestHubAuth_MethodAndCredentials` | test bug | read one server_list frame right after the handshake, before the second server was published | this PR |
| 2026-11-22 | sdk/api/handlers/openai `TestResponsesWebsocketSameSessionLaterCreate429DoesNotReplayAfterDownstreamCommit` | open | the 429 error frame is dropped when a data write still holds the writer lock; the code closes without waiting by design, so a fix needs a decision (see below) | open |

Change detectors so far: one, and it was rewritten to test behavior rather than deleted.
Candidate AGENTS.md ban, if the pattern recurs: "tests must not assert on vendor catalog contents."
Six more tests pin vendor catalog data. They are listed in #19.

## Open: dropped terminal error frame on the responses websocket

`closeWithPayload` in `sdk/api/handlers/openai/openai_responses_websocket.go` hard-closes the
connection when another goroutine holds `writeMu`, so the client sees an abnormal closure
instead of the 429. Under load it fails about 1 in 200 runs. A probe showed the holder is the
writer of the previous frame, after its bytes reached the client but before `WriteMessage`
returned. That state cannot be told apart from a write blocked on a client that stopped
reading without a clock, and AGENTS.md forbids timeouts there. The choices are a short bounded
wait (an AGENTS.md exception), or relaxing the test to assert only its stated purpose (no
replay to the second credential).

