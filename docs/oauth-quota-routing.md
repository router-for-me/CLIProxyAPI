# OAuth Quota Routing

This repository now supports two quota-oriented routing strategies for OAuth-backed accounts:

- `oauth-quota-burst-sync-sticky`
- `oauth-quota-reserve-staggered`
- `oauth-quota-weekly-guarded-sticky`

They are designed for providers that expose rolling quota windows such as "5-hour" and "7-day" limits.

## Why these strategies exist

`oauth-quota-burst-sync-sticky` is for operators who value current burst capacity more than future continuity.
It tries to activate unstarted windows first, then keeps using the account whose short reset is closest.

`oauth-quota-reserve-staggered` is the conservative alternative.
It prefers already-triggered / older accounts first and keeps fresher accounts in reserve for later demand spikes.

`oauth-quota-weekly-guarded-sticky` is for operators who still want refresh-aware sticky usage,
but do not want a single account's 7-day window to be drained too early.
It keeps consuming already-active healthy accounts first, starts reserve accounts only when needed,
and demotes accounts whose weekly remaining quota falls below a dynamic guard floor.

## Config

```yaml
routing:
  strategy: "oauth-quota-burst-sync-sticky"
```

The existing management endpoint can switch these values directly:

- `round-robin`
- `fill-first`
- `oauth-quota-burst-sync-sticky`
- `oauth-quota-reserve-staggered`
- `oauth-quota-weekly-guarded-sticky`

Accepted aliases for convenience:

- `burst-sync-sticky`
- `reserve-staggered`
- `weekly-guarded-sticky`

## Metadata schema

The selector is most useful when auth metadata contains quota window snapshots.
It reads any of these metadata keys:

- `oauth_quota_windows`
- `quota_windows`
- `routing_windows`
- `quotaWindows`

Each entry may look like this:

```yaml
oauth_quota_windows:
  - label: "5-Hour Window"
    scope: "short"
    triggered: true
    used_percent: 68
    reset_at: "2026-04-17T05:00:00Z"
    window_duration_seconds: 18000
  - label: "7-Day Window"
    scope: "long"
    triggered: true
    used_percent: 35
    reset_at: "2026-04-22T09:30:00Z"
    window_duration_seconds: 604800
```

Supported field names are intentionally loose so external quota collectors do not need an exact schema.
The selector understands common variants such as:

- `used_percent` / `usedPercent` / `utilization`
- `reset_at` / `resets_at`
- `reset_after_seconds` / `resetAfterSeconds`
- `limit_reached` / `limitReached`
- `scope` / `window_scope`
- `triggered` / `active` / `started`

It also recognizes top-level Claude/Codex-style window keys such as `five_hour`, `seven_day`, `primary_window`, and `secondary_window`.

## Selection rules

For `oauth-quota-burst-sync-sticky`:

1. Prefer accounts with quota window signals over accounts with no signals.
2. Prefer accounts that still have untriggered relevant windows, so real traffic starts those clocks.
3. Then prefer the account whose short window resets sooner.
4. Then prefer the account whose long window resets sooner.
5. Then prefer the account with more remaining quota.

For `oauth-quota-reserve-staggered`:

1. Prefer accounts with quota window signals over accounts with no signals.
2. Prefer accounts whose relevant windows are already triggered.
3. Then prefer the account whose short window resets sooner.
4. Then prefer the account whose long window resets sooner.
5. Then prefer the account with less long-window quota remaining, preserving fresher accounts.

For `oauth-quota-weekly-guarded-sticky`:

1. Prefer accounts with quota window signals over accounts with no signals.
2. Prefer already-triggered accounts whose weekly remaining quota is still above a dynamic soft guard floor.
3. Then prefer already-triggered accounts that are between the soft and hard weekly guard floors.
4. Preserve accounts with untriggered windows as reserve unless the healthier active pool is insufficient.
5. Only fall back to weekly-depleted accounts once the healthier and reserve pools are exhausted.
6. Within the same tier, still prefer accounts whose short window resets sooner.

Both selectors remain deterministic, so they behave like a sticky fill-first strategy after ranking.
If `session-affinity` is enabled, the quota selector is wrapped by the existing session-affinity layer.

## Fallback behavior

CPA does not currently know the exact remaining token balance for every provider.
If window metadata is missing, the selector falls back to existing cooldown hints such as:

- `quota.next_recover_at`
- `next_retry_after`
- model-level cooldown state

That means these strategies are heuristic unless an external quota collector keeps window metadata fresh.
