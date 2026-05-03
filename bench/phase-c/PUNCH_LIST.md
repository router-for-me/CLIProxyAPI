# Phase C exit punch list

Codex CLI gpt-5.5 review iterated 5 rounds against the BE diff
`refactor/upstream-bound vs upstream/dev`. Each round closed all
BLOCKERs/IMPORTANTs Codex flagged at that round, but the loop didn't
fully converge — round 5 still surfaced 4 findings, of which 3.5 were
fixed and 1 was deferred. Phase C exits with the 1 deferred item plus
2 future-watch items below.

The decision to ship with this punch list rather than continue iterating
was a deliberate user call after weighing remaining-findings impact vs
diminishing-returns review cost. The original Phase C goals (atomic AMP
routing, async logging with priority lane, atomic Server config,
clone-modify-persist-swap mgmt writes, LRU signature cache, exponential
refresh backoff, ReleaseURLProvider seam) are all delivered, tested
under `-race`, and meeting their plan-defined bench targets.

## Deferred — to address in a focused follow-up

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

These findings from rounds 1-5 are fully fixed and verified under
`-race`:

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
