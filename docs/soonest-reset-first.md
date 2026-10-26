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
3. Keep the earliest known future reset. A known reset stays in use until it passes or a newer observation replaces it.
4. Use weighted round robin for equal reset timestamps.
5. Candidates without a known reset come after all candidates with one and share by weight.

Accounts without usable reset data remain fallback candidates. A known reset takes precedence over an unknown reset in the same priority tier. The strategy does not interrupt streams or move healthy bound sessions.

The selector uses the route-aware selection path. Model aliases and retry exclusions remain active. The built-in scheduler fast path does not replace this selector.

## Collector contract

An authenticated collector sends its observations to `PUT /v8/management/routing/reset-hints`:

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

Timestamps use RFC 3339. The handler accepts at most 1,024 observations in a 256 KiB body. Duplicate indexes, unknown fields, future observations, and invalid timestamps reject the complete update. Unknown accounts and resets that already passed are ignored.

Updates merge into the retained observations. An observation replaces the retained one for the same credential unless it is older. Credentials missing from an update keep their known reset, so a failed quota read does not move an account behind others. A retained reset stops counting when it passes; removed credentials are dropped. An empty `accounts` array only prunes. `null` is rejected. The handler returns HTTP 409 if the active selector is not `soonest-reset-first`.

Hints stay in memory. They do not change OAuth files, manual priorities, weights, or cooldowns. A restart or selector replacement clears them, so the collector must publish again after a restart. Until then, selection uses quota weights.

A collector should publish on a fixed interval and use the longest primary quota window, usually the weekly window. Model-scoped windows should not rank the whole account. When a quota read fails, the collector should send the last successful observation with its original timestamp. Shorter windows still determine exhaustion through quota weights, for example by setting the weight of an exhausted credential to 0 until its window resets.

If the longest window has no usable future timestamp, the collector should send no hint for that account instead of substituting a shorter window's reset. Accounts with only a short primary window can use that window.

## Read-only routing status

`GET /v8/management/routing/status` uses the same management authentication. It reports the installed selector strategy, `session_affinity`, `generated_at`, the retained `hints` keyed by management auth index, and `providers`: the routing order per provider, computed by the selector's own rules. Each entry has `auth_index`, `rank` (1 receives new conversations; equal ranks share by weight), `shared`, `priority`, `weight`, `state` (`eligible`, `zero_weight`, `cooldown`, `unavailable`, `disabled`), `reason` (`only_in_tier`, `earliest_reset`, `later_reset`, `no_reset`), the retained `reset_at`/`observed_at`, and `next_retry_after` for blocked credentials. Dashboards should display this order instead of recomputing it. The returned snapshot cannot mutate selector state.

This is an account-level diagnostic, not a model-specific next-request promise. Model access, cooldowns, transport preference and existing conversation bindings still apply during selection.

## Activation and rollback

1. Release the proxy with this selector and the v8 endpoint.
2. Set `routing.strategy` to `soonest-reset-first`.
3. Start the collector, and keep any quota-weight updates it already performs.
4. Check `GET /v8/management/routing/status` after the first published update.

For rollback, stop publishing hints and restore the previous `routing.strategy`, for example `weighted-round-robin`. Manual priorities and credential files need no restoration.

The legacy v0 management API does not expose this endpoint. Use the v8 config API or the configuration file to select this strategy.
