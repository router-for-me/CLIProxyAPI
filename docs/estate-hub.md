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
| Devin | `POST server.codeium.com/exa.seat_management_pb.SeatManagementService/GetUserStatus` with the CLI login's session token (the call the Devin executor makes; CodexBar instead reads app.devin.ai with a browser session) | plan; daily and weekly remaining percent and resets. These replace the windows the executor records |
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

The page is one list, phone first, in the order consume-first spends (Waseem, 28 Oct 2026: "one
table, shown graphically, with proper logos for each service, showing a bar, allowance left, in
spend-first order, resets shown alongside ... closer to what sysdash shows"). Above it is only the
title and the time of the last refresh: the table's order already says what is spent next.

Each row is one plan: the provider's logo, the plan's name, a smooth bar of the weekly
allowance left (or the longest window when there is no weekly one), and under it a thin bar for the
tightest other window, usually the 5-hour one. A bar's colour runs smoothly with what is left, on
sysdash's bands: green from 70% left, amber at 40%, red at 15% or less. On the right are the percent left (a USD balance for prepaid API keys) and
the time to the reset, with the exact time on hover. Tags mark **Next** (the router's next account
for a provider with several), **Spent**, **Held**, **Cooling**, **Poll failed** and **N banked**
(Codex banked resets; amber with the first expiry when it is within three days). Antigravity is one
row per model pool, as `allowances` shows it.

The order across all providers is consume-first's: usable rows by soonest reset, then rows with no
reset, then spent rows (any window at 95% or more used, showing when they are back), then held,
cooling and disabled rows at the bottom. Within a provider this is the router's own order.

Tapping a row opens each window with its reset and pace, then plan, login, balance, banked resets,
groups, cooldowns, recent requests and the age of the figures, then the actions: hold (1 hour,
5 hours, 1 day or until its weekly reset), release, disable or enable, and set its name (`note`) and
groups, through the management API calls below. Config-file API keys are shown but are edited in the
config. The pace line is CodexBar's arithmetic: the window is assumed to keep being spent at its
average rate so far ("12 spare, lasts to reset", "5 over, runs out in 17h 30m"). The page refreshes
every 30 seconds while it is visible and keeps open rows open. Logos are lobe-icons (MIT) inlined in
the page; a provider without one gets its initial.

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

**Pin-only accounts.** `"pin_only": true` in an account's auth JSON (or `PATCH
/v8/management/credentials/fields` with `{"name": "...", "pin_only": true}`, live) keeps it out of
unpinned routing entirely: no strategy, fallback or session binding of an unpinned request reaches
it, even when every other account is unusable. A pin that matches it is served as usual. Holds and
`disabled` still apply on top. This is cxswap's "disabled but `cxswap run` still works" seat
(Codex Team GDP on the estate hub). The credential listing shows `"pin_only": true`.

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
