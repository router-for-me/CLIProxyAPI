# Estate hub: consume-first routing, holds and account pins

This fork adds three things on top of upstream CLIProxyAPI so one hub can replace per-machine
account switchers (cswap for Claude, cxswap for Codex):

- **`consume-first` routing**: spend the account whose allowance resets soonest.
- **Holds**: take one account out of every selection path until a set time, without a restart.
- **Account pins**: a client names the account (or group of accounts) it must use, and is refused
  with a clear error instead of being rotated elsewhere when that account cannot serve it.

## Config

```yaml
routing:
  strategy: "consume-first"
  session-affinity: true      # keep each conversation on its account (cache reuse)
  session-affinity-ttl: "1h"
```

`consume-first` also accepts `consumefirst` and `cf`. It is hot-reloaded like the other strategies.
Everything else (holds, groups, pins) lives in the auth JSON files or the request, not in the config.

## How consume-first ranks accounts

The proxy already records the quota headers each account returned on its last response
(Claude `anthropic-ratelimit-unified-*`, Codex `x-codex-primary-*` / `x-codex-secondary-*`).
Among the accounts that can serve the request right now (not cooling down after a 429, not
disabled, not held, matching any pin), it picks the first by:

1. **Not spent before spent.** An account is spent when any observed window (5-hour or weekly) is
   at 95% or more. Spent accounts are still used as a last resort; the upstream 429 cooldown is
   what removes them entirely.
2. **Known reset before unknown.** The rank uses the account's longest window: weekly for Claude
   (`7d`), and for Codex whichever of primary/secondary has the longer `window-minutes`.
3. **Soonest weekly reset first.** That allowance is the one about to be lost.
4. **Least used, then credential ID** as tie-breaks.

A reset is **unknown** when the account has not returned quota headers since the hub started,
when its weekly window has already reset since it was observed, or when the window has not
started (0% used and the reset is a whole window away, which providers report as a moving
placeholder). Unknown accounts rank after every account with a known reset.

**When a provider has no reset data** (Gemini, Antigravity, API-key credentials, or any account
before its first response), every candidate is unknown and the order falls back to credential ID,
which is the same order as `fill-first`. Priority tiers (`priority` in the auth JSON) still apply
first, exactly as for the other strategies.

**Session affinity** wraps the strategy unchanged: a conversation that is bound to an account
stays on it while that account is available, even if another account now ranks higher. It moves
only when its account starts cooling down, is held, disabled, or excluded by a pin.

### Differences from cxswap / cswap

- Same ordering rule (viable first, soonest weekly reset, unknown last, most headroom), same 95%
  spent line and the same "window not started" rule.
- No 5-minute reset margin: the hub chooses per new conversation, not by moving a shared pointer,
  so there is nothing to ping-pong.
- Spent accounts are a last resort rather than refused; the hub only refuses on a real 429.
- Usage comes only from the hub's own traffic, not CodexBar or the providers' usage endpoints. The
  snapshot is in memory, so after a restart each account is "unknown" until it serves a request.
- Codex per-model "additional" limits (for example Spark) are not ranked on.

## Holds

A hold takes an account out of every routing path (all strategies, pinned or not) until a time:

```json
{ "hold_until": "2026-10-28T09:00:00Z" }
```

Set it in the account's auth JSON (the file watcher picks it up), or live through the management API:

```bash
curl -X PATCH "$HUB/v8/management/credentials/fields" \
  -H "Authorization: Bearer $MANAGEMENT_KEY" -H "Content-Type: application/json" \
  -d '{"name": "<auth file name>", "hold_until": "2026-10-28T09:00:00Z"}'
```

Clear it with `"hold_until": null` (or any time in the past). The value must be RFC3339; a value
that does not parse is ignored. For an open-ended hold use the existing `disabled` flag
(`PATCH /v8/management/credentials/status` with `{"name": "...", "disabled": true}`).

## Account pins

A client pins a request with one header:

```
X-CPA-Account: <account or group>
```

The value matches, case-insensitively, any of: the credential ID, its auth file name (with or
without `.json`), its label, its email, or one entry of its `group` field. Groups are a
comma-separated string in the auth JSON:

```json
{ "group": "personal,pro" }
```

With a pin, only matching accounts are considered (consume-first still orders them). The hub
refuses the request immediately, without trying another account, when:

| Situation | Status | Error code |
|---|---|---|
| No account matches the pin for this model | 400 | `account_not_found` |
| Every matching account is held or at its limit | 429 with `Retry-After` (soonest return) | `account_unavailable` |
| Every matching account is disabled or otherwise unusable | 503 | `account_unavailable` |

The message names the pin and the reason, for example
`account "team" cannot serve model "claude-opus-4-1": held until 2026-10-28T09:00:00Z`.
The header is read by the hub only; it is never forwarded upstream.

Client examples:

- Claude Code: `ANTHROPIC_CUSTOM_HEADERS="X-CPA-Account: personal"` in the `env` block of
  `~/.claude/settings.json` or the process environment (the `cswap run <slot>` equivalent).
- Codex: in `~/.codex/config.toml`, under `[model_providers.cliproxy]`, add
  `http_headers = { "X-CPA-Account" = "pro" }` (the `cxswap run <slot>` equivalent).

## Reading the state (for a dashboard)

`GET /v8/management/credentials` (management key required) returns one entry per account with:

- `id`, `name`, `label`, `email`, `provider`, `priority`
- `disabled`, `status`, `unavailable`, `next_retry_after` (cooldown end)
- `hold_until` and `group` when set
- `quota.observed_at` and `quota.signals`: the raw quota headers from the last response
- `consume_first`: the figures the router ranks on, so a dashboard can show the same order:
  `reset_at` (omitted when unknown), `used_percent` (highest live window), `spent`

Sort by `spent`, then entries with `reset_at` before those without, then `reset_at`, then
`used_percent`, then `id` to reproduce the router's order within a priority tier.
