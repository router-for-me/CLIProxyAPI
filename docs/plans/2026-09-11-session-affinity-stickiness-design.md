# Session Affinity Stickiness — Diagnosability + Defensive Fixes

Date: 2026-09-11
Status: Design validated

## Problem

With the default (global) routing strategy, session affinity occasionally loses
bindings. Operator logs show "cache hit" lines mixed with unexpected
"cache miss, new binding" lines for what appears to be the same session. Goal:
keep sessions sticky and make every binding loss explainable from logs.

## Diagnosis summary (from 2026-09-11 production log + code trace)

1. **Logs are unreadable across sessions.** `truncateSessionID`
   (`sdk/cliproxy/auth/selector.go:771`) keeps only the first 8 chars. Every
   derived session starts with `derived:`, so all distinct sessions log as
   `derived:...`. Apparent anomalies (e.g. two different auths "cache hit" for
   the same session seconds apart) may simply be different sessions.
2. **Legitimate failover exists** — "cache hit but auth unavailable,
   reselected" (`selector.go:728`) moves a binding permanently. Not a bug.
3. **Mass-wipe path A — auth removal.** `Manager.Remove` →
   `invalidateSessionAffinity` (`conductor_lifecycle.go:210`) drops every
   binding for an auth ID. Upstream provider re-renders that change an auth's
   identity (API key / base URL edit) produce Remove(old) + Register(new);
   logically the same credential, but all sessions bound to the old ID are
   wiped.
4. **Mass-wipe path B — selector swap.** A config update that changes
   `routingRuntimeState` (`sdk/cliproxy/service_config.go:206-208`) builds a
   fresh `SessionAffinitySelector` with an empty cache.
5. Other paths (TTL expiry with refresh-on-hit, 10k entry eviction) are not
   plausible at the observed traffic.

Root cause not yet pinned between 1/3/4 because logs cannot distinguish
sessions. Design therefore ships diagnosability first, plus defensive fixes
for the two confirmed mass-wipe paths.

## Design

### Phase 1 — Diagnosability (sdk/cliproxy/auth)

- `truncateSessionID`: strip the session prefix (`derived:`, `conv:`, `pck:`,
  `session:`, `user:`, `execution:`, `msg:`), keep first 16 chars of the
  unique part + `...`.
- Every binding log line (`cache hit`, `cache miss, new binding`,
  `cache hit but auth unavailable, reselected`) gains a `key=` field: 8-hex
  short hash of the full cache key `provider::sessionID::model`.
- `SessionCache.InvalidateAuth` returns `int` (deleted entry count).
- `Manager.invalidateSessionAffinity` logs
  `session-affinity: invalidated N binding(s) for auth <id>` (warn) when N > 0.
- `SetSelector` (`conductor_selection.go`): when the outgoing selector is a
  `SessionAffinitySelector` being replaced, log how many bindings it held.

Outcome: a future stickiness loss is immediately classifiable as
different-session, invalidation wipe, selector-swap wipe, or true miss.

### Phase 2 — Defensive fixes

**A. Binding migration on re-render.** When a remove+add cycle in the same
upstreamsync render produces a new auth equivalent to the removed one (same
`source` / `base_url` / `compat_name` attributes), migrate bindings
old ID → new ID via `SessionCache.Set` preserving the original TTL/expiry.
Genuinely removed auths keep the existing invalidation behavior. No new
persistent state; only in-memory entry copies; cache key format unchanged.

**B. Selector swap handoff.** `SetSelector` gains a cache handoff: an
outgoing `SessionAffinitySelector` transfers its unexpired entries to an
incoming `SessionAffinitySelector` (TTLs preserved). Swap to a non-affinity
selector discards the cache (with the Phase-1 log). Config updates that do
not touch routing no longer empty bindings.

### Explicitly unchanged

- Failover on unavailable bound auth (correct behavior).
- Derived session identity (`sdk/cliproxy/session/identity.go`) — revisit
  after Phase-1 logs reveal whether derived IDs are unstable per turn.
- Per-model route strategy vs. established bindings semantics.

## Data flow

```
Request → Pick() → cache hit? ── ya → auth tersedia? ── ya → pakai (refresh TTL)
                          │                    └─ tidak → reselect + bind baru (failover sah)
                          └─ tidak → fallback selector → bind baru
                                    ▲
   wipe paths, now lifecycle-logged: InvalidateAuth ── count + warn log
                                     SetSelector swap ── cache handoff
                                     Remove(re-render) ── migrate old→new binding
```

State remains in-memory only; entries are `authID + expiresAt + aliases`;
key format `provider::sessionID::model` unchanged.

**Implementation placement note (as built):** the handoff lives inside
`Manager.SetSelector` (`sdk/cliproxy/auth/conductor_selection.go`), and the
migration lives in `Manager.Remove`/`Register`
(`sdk/cliproxy/auth/conductor_lifecycle.go`) via the
`pendingAffinityMigrations` stash — no changes to
`sdk/cliproxy/service_config.go` or upstreamsync were needed. The migration
equivalence key is `base_url|compat_name|config_index` (config_index added
during review to disambiguate multi-entry pools).

## Testing

| Test | Locks |
|---|---|
| `truncateSessionID` | Two distinct `derived:` sessions render different log strings; prefix stripped |
| `InvalidateAuth` count | 3 bindings → returns 3, warn log emitted |
| `SetSelector` handoff | affinity→affinity preserves entries (TTL preserved); affinity→non-affinity discards without panic |
| Re-render migration | Remove(old)+Register(new) with matching `source` migrates bindings; operator delete (no re-add) still invalidates |
| Regression | Existing `session_affinity_*_test.go`, `session_cache_test.go` stay green |

All unit tests, no PG. Touch points: `sdk/cliproxy/auth/` (selector, session
cache, conductor_lifecycle, conductor_selection), `sdk/cliproxy/service_config.go`
(handoff), and the upstreamsync re-render path for migration.
