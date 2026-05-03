# Phase C / Stage 1 exit punch list

Codex CLI gpt-5.5 review iterated 6 rounds against the BE diff during
Phase C, plus 3 rounds of full-diff Stage 1 exit review against
`refactor/upstream-bound vs upstream/dev` (BE + FE). All BLOCKERs and
all IMPORTANTs identified by Codex have been closed. The 4 NITs flagged
in Stage 1 round 3 are deliberately deferred — see "Deferred" below.

The original Phase C goals (atomic AMP routing, async logging with
priority lane, atomic Server config, clone-modify-persist-swap mgmt
writes, LRU signature cache + bounded outer group map, exponential
refresh backoff, ReleaseURLProvider seam) are all delivered, tested
under `-race`, and meeting their plan-defined bench targets. Phase D
(antigravity payload-builder + ProviderEditorShell) is delivered.
FE-1 (OpenAISection virtualisation) is delivered with documented
trade-offs.

## Deferred — to address in a focused follow-up

### Stage 1 exit round 3 NITs (deferred — accepted as long-tail)

User explicitly accepted these as deferred at Stage 1 exit (option 2:
fix IMPORTANTs, defer NITs). All 4 are quality concerns, not Stage 1
gating issues per Codex's verdict.

#### BE-R3-2 — `cloneErrorMessages` Addon test gap
- **File:** `internal/logging/async_emitter_test.go`
- **Concern:** the round-2 BE-R2-1 deep-clone of `Addon http.Header`
  is functionally correct but the existing mutate-after-enqueue test
  doesn't directly assert it (writeLogRequest never reads `Addon`).
  Codex round 3 NIT: add a unit test on `cloneErrorMessages` that
  mutates the original header and asserts the clone is independent.
- **Fix shape:** ~10 LOC unit test on the helper directly.
- **Effort:** ~10 min.

#### FE-R3-2 — virtualised path loses inter-card gap
- **File:** `src/components/providers/OpenAISection/OpenAISection.tsx`
- **Concern:** below threshold, `AiProvidersPage.module.scss`'s grid
  `gap` applies; above threshold, absolute-positioned virtualised rows
  have no margin/padding so cards appear flush together.
- **Fix shape:** add a `paddingBottom` (e.g. `$spacing-md`) to each
  virtualised row's wrapper, or include the gap in `estimateSize()`
  and `measureElement` calculations.
- **Effort:** ~10 min.
- **Real-world impact:** only visible at 50+ providers (the bench
  fixture, not typical use).

#### FE-R3-3 — `isAbortError` home in `usePoll.ts` is awkward
- **File:** `src/hooks/usePoll.ts` (currently exports `isAbortError`)
- **Concern:** non-poller callers (`useAuthFilesData`, `LogsPage`) now
  import the helper from a hook file. Cleaner to move to
  `src/utils/errors.ts` (or similar neutral location).
- **Fix shape:** move + update 3 import sites.
- **Effort:** ~5 min.

#### FE-R3-4 — virtualisation threshold of 50 is unverified
- **File:** `src/components/providers/OpenAISection/OpenAISection.tsx`
- **Concern:** the cutoff (`OPENAI_VIRTUALIZATION_THRESHOLD = 50`) is
  picked without a browser FPS measurement to validate that 50 is the
  right boundary. A 50-card grid may render fine without
  virtualisation, in which case the threshold could be raised (avoiding
  the inter-card-gap regression for more cases).
- **Fix shape:** browser FPS measurement on a fixture-seeded page,
  then tune the constant. Same fixture work blocks the Playwright
  keyboard-nav test.
- **Effort:** ~2-4 hours including the fixture seam.
- **Real-world impact:** unknown without measurement.

### Multi-cfg snapshot reads in OAuth-flow goroutines
- **Files:** `internal/api/handlers/management/oauth_callback.go`,
  `internal/api/handlers/management/auth_files.go` (Anthropic/Gemini/
  Codex/Antigravity/Kimi OAuth flows around lines 1437-1472, 1562-1608,
  1841-1876).
- **Concern (Codex round 5 BLOCKER):** these handlers/goroutines call
  `h.cfg()` multiple times across the OAuth callback wait loop and the
  post-callback persist step. A hot-reload between the validation and
  the persist could mix `AuthDir` from snapshot N with proxy / TLS
  settings from N+1.
- **Real-world risk:** low. OAuth login flows are interactive (operator-
  driven) and rarely overlap with config hot-reloads in practice.
- **Fix shape:** snapshot `cfg := h.cfg()` once at flow entry, plumb
  `cfg`, `authDir`, and proxy values explicitly through the goroutine
  closures and helpers (the OAuth services constructed mid-flow take
  these as direct args, so the plumbing is mechanical).
- **Effort:** ~1-2 hours. Mostly mechanical; the only risk is missing
  a goroutine that reaches back to `h.cfg()` indirectly.

## Future-watch (would surface in round 6+)

These are likely findings the loop would have surfaced if continued —
flagged here so they're not a surprise on the eventual upstream PR.

### `flush()` lock-protected counter still racy with worker
- **File:** `internal/logging/async_emitter.go`
- **State:** round-5 fix moved `pending.Add(1)` BEFORE the channel send
  inside `closeMu`. That closes the dequeue-to-execute race for the
  flush invariant (the worker can't observe a task in the channel
  before pending is bumped, because the send is what makes the task
  visible). A round-6 review would likely re-verify and pass it, but
  if it doesn't the next refinement is a second mutex bracketing
  channel-receive and execute-entry on the worker side.
- **Real-world risk:** low. The bench teardown case the original
  IMPORTANT covered now waits for `pending == 0` correctly under
  observed schedules.

### Future Codex passes will likely find more multi-cfg patterns
- The pattern of "snapshot once at handler/flow entry" is now
  established. Where it isn't applied yet (OAuth flow goroutines,
  any helper Codex hasn't called out), the same fix shape applies.
  When future merges or fork features touch these files, apply the
  pattern proactively rather than waiting for a review to surface it.

## What's NOT on this list

These findings from rounds 1-6 are fully fixed and verified under
`-race`:

  - Service-level config snapshot races (round 6 BLOCKER #1) —
    `Service.configSnapshot()` accessor + `ensureExecutorsForAuthWithMode`,
    `registerModelsForAuth`, `oauthExcludedModels` snapshot once at
    function entry; `resolveConfig*Key` helpers converted to
    package-level functions taking `cfg *config.Config` so the caller's
    snapshot covers the full resolution. New
    `TestService_ConfigSnapshot_RaceFree` pins the invariant.
  - `Server.Stop` async logger flush (round 6 IMPORTANT #2) — type-
    assert `requestLogger` to `interface{ Close() }` and call after
    `s.server.Shutdown` so queued normal logs and forced-error logs are
    drained before return.
  - Outer signature-cache group map bound (round 6 IMPORTANT #3) —
    `MaxGroupCount` cap (64) with LRU-by-write-time eviction;
    `evictLRUGroup()` runs on cold-path inserts under `groupEvictMu`.
    `TestSignatureCache_OuterMapBoundedByMaxGroupCount` pins the cap.
    Read path stays sync.Map.Load fast-path (no atomic.Int64 contention
    on lastAccess; eviction policy degrades to "least recently written"
    which is fine because production model surfaces stay well under the
    cap).
  - Async emitter drop-oldest semantics (round 6 NIT #4) — normal-queue
    overflow now does receive-then-send under `closeMu` so the OLDEST
    queued task is evicted, matching plan §Behavior Contract.
  - Hot-reload race test coverage (round 6 NIT #5) — new
    `TestServer_MgmtHandlers_HotReloadRace` drives `PutDebug`,
    `PutAmpUpstreamURL`, `PutAmpModelMappings` directly against the
    Handler concurrently with `Server.Config()` readers; new service-
    level `TestService_ConfigSnapshot_RaceFree` covers the helper
    snapshot pattern.

  - Mgmt deadlock (round 1) — `authManager` atomic.Pointer, h.mu
    released before commit (round 1) then re-held safely (round 2).
  - `BaseAPIHandler.Cfg` race — atomic.Pointer + `Config()` accessor
    (round 1), plus per-handler snapshot capture (round 2).
  - Async logger forced-after-close drop — `closed` flag protected by
    `closeMu` mutex (rounds 2 + 3).
  - AMP `MultiSourceSecret.explicitKey` race — atomic.Pointer (round 1).
  - Mgmt index pre-resolution — moved into `applyConfigChange` closure;
    `c.Writer.Written()` short-circuit (round 1).
  - `UpdateClients` fan-out serialization — `Server.updateMu` (round 1).
  - YAML clone preserves `json:"-"` fields — reverted from JSON
    round-trip after round-2 surfaced data loss (round 2).
  - `errorLogsMaxFiles` — `atomic.Int64` (round 1).
  - Mgmt commit reorder — h.mu held through commit (round 2).
  - Delete no-match returning 200 — explicit 404 in every Delete*
    handler + `deleteFromStringList` helper (rounds 2-4).
  - Patch index out-of-range returning 200 — `patchStringList` 404
    (round 4).
  - Patch Amp{Model,Upstream}APIKeys empty payload returning 200 —
    400 validation up front (round 5).
  - Mgmt commit skipped service-level fan-out — Service-level
    `buildReloadCallback` wired via `Server.SetManagementCommitter`
    BEFORE server.Start (rounds 3 + 4).
  - Mgmt commit skipped watcher auth refresh —
    `watcher.SetConfig(newCfg)` + `watcher.RefreshAuthState(true)`
    in the reload callback (rounds 4 + 5).
  - Mgmt getter helpers in `config_auth_index.go`, `auth_files.go`
    (3 helpers), `logs.go` (4 handlers + helper), `vertex_import.go`,
    `api_tools.go` — all snapshot once (rounds 3 + 4 + 5).

## Codex review session IDs

  - Round 1: 019deac7-c516-77a2-89df-c9d3fa4e6db5 → e9b8bbe3
  - Round 2: 019deaf0-48e5-7371-b7a4-565b0ada0abb → 7ea1346f
  - Round 3: 019deafe-8819-7070-9472-52917f86cf05 → 9e079689
  - Round 4: 019deb1a-8561-7aa0-9a12-faf0e11067a7 → 94a9500c
  - Round 5: 019deb2e-ff4b-70a1-a480-643322f1e9b1 → dcaea756
  - Round 6: 019deb54-496e-7b51-8dfc-8e8691ab80e8 (BLOCKER #1, IMPORTANT
    #2-3, NIT #4-5 — all closed in this round)
