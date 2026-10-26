# Soonest-reset-first routing

The `soonest-reset-first` strategy assigns new conversations to the available account with the earliest known reset. Manual priority remains the first selection rule. Existing session affinity remains authoritative until the bound account becomes unavailable.

```yaml
routing:
  strategy: soonest-reset-first
  session-affinity: true
  session-affinity-ttl: 10m
```

## Selection rules

1. Exclude disabled credentials, exhausted credentials with zero weight, and credentials in cooldown for the requested model.
2. Keep the highest available manual priority tier.
3. Keep the earliest future reset with an observation no older than 15 minutes.
4. Use weighted round robin for equal reset timestamps.
5. If no candidate has a usable reset, use weighted round robin for all remaining candidates.

Accounts without usable reset data remain fallback candidates. A known reset takes precedence over an unknown reset in the same priority tier. The strategy does not interrupt streams or move healthy bound sessions.

The selector uses the route-aware selection path. Model aliases and retry exclusions remain active. The built-in scheduler fast path does not replace this selector.

## Collector contract

An authenticated collector sends a complete snapshot to `PUT /v8/management/routing/reset-hints`:

```json
{
  "accounts": [
    {
      "auth_index": "runtime-account-index",
      "observed_at": "2026-10-26T12:00:00Z",
      "reset_at": "2026-10-27T13:00:00Z"
    }
  ]
}
```

Timestamps use RFC 3339. The handler accepts at most 1,024 observations in a 256 KiB body. Duplicate indexes, unknown fields, future observations, and invalid timestamps reject the complete update. Missing accounts and expired observations do not enter the snapshot.

An empty `accounts` array clears all hints. `null` is not an empty snapshot. The handler returns HTTP 409 if the active selector is not `soonest-reset-first`.

Hints stay in memory. They do not change OAuth files, manual priorities, weights, or cooldowns. A restart or selector replacement clears them. The collector must publish again after a blue/green cutover. Until then, selection uses quota weights.

The limits exporter publishes hints every quota reconciliation interval when `QUOTA_RESET_HINTS_ENABLED=1`. It uses the longest primary quota window, usually the weekly window. It excludes model-scoped windows and last-known error snapshots. Shorter windows still determine exhaustion through quota weights.

If the longest window has no usable future timestamp, the exporter sends no hint for that account. It does not substitute a five-hour reset for an unknown weekly reset. Accounts with only a short primary window can use that window.

## Activation and rollback

1. Release the proxy with this selector and the v8 endpoint.
2. Release the limits exporter with reset-hint support.
3. Set `routing.strategy` to `soonest-reset-first`.
4. Keep `QUOTA_ROUTING_ENABLED=1` for quota weights and exhaustion checks.
5. Set `QUOTA_RESET_HINTS_ENABLED=1` on the exporter.
6. Use the established deployment procedure.
7. Check the management response and exporter errors after the first quota reconciliation.

For rollback, disable `QUOTA_RESET_HINTS_ENABLED`. Restore `routing.strategy: weighted-round-robin`. Manual priorities and credential files need no restoration.

The legacy v0 management API does not expose this endpoint. Use the v8 config API or the configuration file to select this strategy.
