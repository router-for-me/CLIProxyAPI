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
- Usage comes from the hub's own traffic and, when the usage refresher is on, from the providers'
  usage endpoints for idle accounts (see below). Without the refresher the snapshot is in memory
  only, so after a restart each account is "unknown" until it serves a request.
- Codex per-model "additional" limits (for example Spark) are not ranked on.

## Usage refresher

Quota headers only arrive while an account serves traffic. The refresher asks each provider's usage
endpoint for accounts that are idle, and keeps the last reading across restarts:

```yaml
routing:
  usage-refresh:
    interval: "10m"   # empty or 0 turns it off (the default)
    providers: []     # claude, codex, kimi, xai, antigravity, meta, deepseek, commandcode, zai,
                      # opencode-go; empty means all of them
```

- An account is polled when its last poll is older than `interval` and, for Claude and Codex, it has
  not returned quota headers within `interval` either. A Codex account that keeps serving traffic is
  still polled once an hour, because its banked resets only come from the poll. Disabled accounts are
  not polled. Muse is polled at most every three hours whatever the interval: Meta locks out a login
  that is polled often.
- API keys from the config file (`openai-compatibility`) are polled when their `base-url` points at
  a service with a usage endpoint: `api.deepseek.com` (`deepseek`), `*.commandcode.ai`
  (`commandcode`), `api.z.ai` or `open.bigmodel.cn` (`zai`, the GLM coding plan) and
  `opencode.ai/zen/go` (`opencode-go`). The name given to the entry does not matter. Any other API
  key, including Claude, Codex and Gemini keys, is not polled.
- A failed poll is retried after `interval`, not sooner. The dashboard shows the error.
- Claude and Codex readings are stored as the same quota headers their responses carry, so
  consume-first ranks idle accounts on them. A poll never replaces newer headers from traffic.
- The last reading of every account is written to `<auth-dir>/usage-snapshot.cache` (not `.json`,
  so the watcher never loads it as a credential). It holds figures only, no tokens. After a
  restart it is read back, so the router order is known immediately. The Postgres store clears
  the auth directory at startup, so there the snapshot does not survive a restart.
- The interval and provider list are read on every one-minute tick, so a config reload applies
  without a restart.

| Provider | Endpoint (as CodexBar reads it) | What it reports |
|---|---|---|
| Claude | `GET api.anthropic.com/api/oauth/usage`, `anthropic-beta: oauth-2025-04-20` | 5-hour and weekly utilization (percent) and resets; Opus, Sonnet and model-scoped weekly limits (for example "Fable only") |
| Codex | `GET chatgpt.com/backend-api/wham/usage`, `ChatGPT-Account-Id` | plan, 5-hour and weekly used percent, window length, reset; per-model additional limits |
| Codex banked resets | `GET chatgpt.com/backend-api/wham/rate-limit-reset-credits`, same headers plus `OpenAI-Beta: codex-1` | each available banked reset and its expiry. A failed read keeps the last list |
| Kimi | `GET api.kimi.com/coding/v1/usages` | membership level (plan); 5-hour, weekly and monthly pools as used ratios, or the older used/limit counts |
| Grok (`xai`) | `GET cli-chat-proxy.grok.com/v1/billing?format=credits`, `x-xai-token-auth: xai-grok-cli` | subscription tier; credit used percent for the billing period and its end |
| Antigravity | `POST daily-cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary` with the project | remaining fraction and reset per pool (Gemini, Claude and GPT) for the 5-hour and weekly buckets |
| Devin | not polled: its executor already records daily and weekly remaining percent and resets | shown as-is |
| Muse (`meta`) | `POST api.meta.ai/muse-code/key` with the device (`dca:`) token | tier name; short window and weekly used percent and resets. The key this call mints is discarded |
| DeepSeek (API key) | `GET api.deepseek.com/user/balance` | prepaid balance and its currency |
| CommandCode (API key) | `GET api.commandcode.ai/alpha/billing/credits` and `/alpha/billing/subscriptions` | 5-hour and weekly caps; USD left in the monthly grant, the grant's size (from the plan when the reply omits it) and the period end |
| GLM coding plan (`zai`, API key) | `GET <api.z.ai or open.bigmodel.cn>/api/monitor/usage/quota/limit` | plan; 5-hour and weekly token windows; the monthly count of web search and reader calls ("MCP") |
| OpenCode Go (API key) | `GET opencode.ai/zen/go/v1/usage` | rolling 5-hour, weekly and monthly used percent and resets |

## Dashboard

The hub serves its own dashboard at **`/hub`** (for example
`https://<hub>:8317/hub`). It asks for the management key once and keeps it in the browser's local
storage, as the stock panel does. It is one HTML file embedded in the binary, with no build step,
and it is turned off with the stock panel (`management.disable-control-panel: true`) or when the
management API is off.

The page answers two questions first. **Spend next** lists, per provider, the account consume-first
will use next, with its tightest window's percent left (or its balance) and a pace mark. **Running
low** lists every usable account whose tightest window is at 15% or less, or below half and on
course to run out before it resets. **Banked resets** lists Codex accounts holding banked resets and
when the first expires; they are redeemed in Codex itself.

The pace mark is CodexBar's arithmetic on the figures the page already has: the window is assumed to
keep being spent at its average rate so far. It reads `+12 spare` (12 points less used than an even
spend would have used by now), `on pace`, `runs out Tue 15:18` (the projected time it runs dry, before
its reset), `back Wed 00:07` (spent; when it resets) or `unused`. An account's detail spells it out,
for example "5 over, runs out in 17h 30m" or "12 spare, lasts to reset".

**Accounts** lists every credential and API key in the router's order within each provider. A row
shows the tightest window, the others in brief, and marks for next, held, disabled, spent and a
failed poll. Opening a row shows each window with its reset and pace, then plan, login, balance,
banked resets, groups, cooldowns, recent requests and the age of the figures, then the actions: hold
(1 hour, 5 hours, 1 day or until its weekly reset), release, disable or enable, and set its name
(`note`) and groups, through the management API calls below. Config-file API keys are shown but are
edited in the config. The page refreshes every 30 seconds while it is visible and keeps open rows open.


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

`GET /v8/management/credentials/usage` is the dashboard's single source: `strategy`,
`usage_refresh`, and `accounts`, which holds every credential file entry (as below) plus the
config-file API keys (`source: "config"`), each with `cooldowns` and `usage`.

`GET /v8/management/credentials` (management key required) returns one entry per account with:

- `id`, `name`, `label`, `email`, `provider`, `priority`
- `disabled`, `status`, `unavailable`, `next_retry_after` (cooldown end)
- `hold_until` and `group` when set
- `quota.observed_at` and `quota.signals`: the raw quota headers from the last response
- `consume_first`: the figures the router ranks on, so a dashboard can show the same order:
  `reset_at` (omitted when unknown), `used_percent` (highest live window), `spent`
- `usage`: `windows` (each `used_percent`, `window_minutes`, `reset_at`, and a `label` for
  extra windows), `plan`, `fetched_at` and the last poll `error`. Claude and Codex main windows
  come from the live quota headers; the rest from the last refresher poll. `balance` (`left`,
  `total` when known, `currency`) for DeepSeek and CommandCode; `banked_resets` (each `expires_at`)
  for Codex

Sort by `spent`, then entries with `reset_at` before those without, then `reset_at`, then
`used_percent`, then `id` to reproduce the router's order within a priority tier.
