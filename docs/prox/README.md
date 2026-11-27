# prox features

prox adds quota-aware routing, session affinity, and a quota dashboard to
CLIProxyAPI. Everything is off until you enable it. See [ROADMAP.md](ROADMAP.md)
for what is planned and [OPERATIONS.md](OPERATIONS.md) for the image, CI, and upstream merges.

## Enable

Add this to `config.yaml`. `plugins.enabled` does not need to be on; prox is
built in and only borrows the `plugins.configs` block so the settings survive
v8 config migration and hot reload.

```yaml
plugins:
  configs:
    prox:
      enabled: true
      mode: shadow            # record decisions only; switch to active after review
      strategy: reset_first   # reset_first | quota_pressure | balanced | manual_priority
      state_dir: prox-data    # bindings, quota cache, usage history
```

Every other key from quota-router works unchanged (`poll_interval`,
`affinity_ttl`, `affinity_scope`, `rebind_policy`, `new_session_min_remaining`,
`schedule_across_priorities`, `account_priority`, `account_alias`, ...). The
dashboard's Settings page lists all of them with descriptions.

## Window ignition

A five-hour window only starts with the first request after a reset. With
ignition on, prox sends one minimal request ("Reply with OK.") to each idle
Claude/Codex account a few seconds after its five-hour window resets, so the
next window starts right away instead of when you next use the account.

```yaml
plugins:
  configs:
    prox:
      ignition:
        enabled: true          # off by default
        start_hour: 7          # first ignition of the day (local time or `timezone`)
        end_hour: 22           # last hour; end_grace_minutes (30) extends it
        timezone: America/New_York
        providers:
          claude: {enabled: true}             # newest Haiku unless `model` is set
          codex:  {enabled: true, model: ""}  # newest Luna/mini unless `model` is set
```

- The request goes through CPA's own executor pinned to the account, in the
  provider's native format, so payload rules and cloaking apply as usual. prox
  stops reading at the first streamed delta.
- Success means a later quota poll shows a five-hour window with a future reset.
  If none appears within `confirm_seconds` (240), ignition pauses until the next day.
- 401/403 stops ignition for the account until its credential changes. 429 pauses
  until the next day. 400/404/422 skips that model for 24 h and tries the next.
  Other errors retry after 5 and 15 minutes, then pause until the next day.
  A CPA cooldown retries when the cooldown ends, without counting a failure.
- Accounts whose weekly (or other account-wide long) window is used up are skipped.
- `GET /prox/api/ignition` lists plans and events; `POST /prox/api/ignition/fire`
  with `{"auth_id": "..."}` ignites one account now.

## Notifications

```yaml
plugins:
  configs:
    prox:
      notify:
        bark_url: https://api.day.app/<device key>
        webhook:
          url: https://ntfy.sh/my-topic   # or Feishu, Telegram, your own endpoint
          method: POST                    # GET, POST, PUT
          headers: {Authorization: "Bearer <token>"}
          body: ""                        # empty: JSON {title, body, text, level, source}
          success_json: {}                # e.g. {code: 0}
        thresholds: [50, 20, 10]          # remaining % alert levels
        recovery: {five_hour: after_exhausted, weekly: all}   # off | all | after_exhausted
        reset_reminder: {five_hour: off, weekly: has_remaining} # off | all | has_remaining
        ignition_events: true
```

Alerts: remaining quota crossing each threshold and zero (once per cycle),
window resets, quota restored before its reset time, reset reminders (1 h before
a five-hour reset, 1 day before a weekly one), plan changes, quota polls rejected
3 times over at least 30 minutes, and ignition problems. The first sighting of an
account or window only records a baseline. A webhook `body` may use `{{title}}`,
`{{body}}`, `{{text}}` and `{{level}}`; values are JSON-escaped when the body is
JSON. `POST /prox/api/notify/test` sends a test message and
`GET /prox/api/notify/log` lists recent deliveries.

## Kimi and Grok monitoring

Kimi and Grok (CLI subscription) accounts are polled every 5 minutes with
read-only GETs against the same endpoints the CPA Management Center uses, so
they appear in the pool, history, and notifications. prox does not route them.
API-billed Grok accounts have no quota and show `api_billing_no_quota`.
Antigravity is intentionally not monitored.

```yaml
plugins:
  configs:
    prox:
      monitor: {enabled: true, poll_interval: 5m, kimi: true, xai: true}
```

`GET /prox/api/monitor` lists them; `POST /prox/api/monitor/refresh` polls now.

## Usage and cost

Every upstream attempt (all providers) is recorded per hour, provider, account,
model, and client (`claude`, `codex`, ... from the session headers) with tokens
and API-equivalent cost. Hourly rows roll into days after a week and are kept
90 days. Defaults cover current Claude models and the GPT-5 family; add the
rest (for example GPT-6, Kimi, Grok) under `usage.pricing`, USD per 1M tokens:

```yaml
plugins:
  configs:
    prox:
      usage:
        retention: 2160h
        pricing:
          gpt-6-luna: {input: 1.25, output: 10, cache_read: 0.125}
          kimi-k3:    {input: 0.60, output: 2.50}
```

`cache_read` defaults to 0.1x and `cache_write` to 1.25x the input price. Rows
for unpriced models count tokens and report `unpriced` requests.

- `GET /prox/api/costs?range=24h|7d|30d&group=provider,auth,model,client&step=1h`
  returns grouped rows, totals, and the cache-read share of input tokens (a low
  share points at affinity or prompt-cache problems).
- `GET /prox/api/pool` returns per provider: accounts, usable accounts, the
  plan-weighted remaining percent per window (Max 20x counts 20x a Pro account),
  next reset, the next new-session account, and today's and 7-day cost.

## ClickHouse

```yaml
plugins:
  configs:
    prox:
      clickhouse:
        url: http://nas.tailnet.ts.net:8123
        database: prox
        table: requests
        username: default
        password_env: CLICKHOUSE_PASSWORD
```

prox creates the database and a MergeTree table on first write and inserts one
row per upstream attempt (time, request and trace IDs, provider, account,
model, client, hashed session keys, status, latency, TTFT, tokens, cost,
reasoning effort, service tier) through ClickHouse's HTTP interface, batched
every 5 s. Request and response bodies are not stored. If ClickHouse is down,
rows queue in memory (20k by default) and are then dropped with a warning;
requests are never delayed. `GET /prox/api/sink` shows counters.

These features are written from scratch based on the documented behavior of
[lamplighter](https://github.com/PosvdM/cpa-plugin-lamplighter); no lamplighter
code is included.

## Dashboard

The dashboard is the CLIProxyAPI Management Center (vendored in `web/panel`,
MIT) with prox additions, embedded in the binary and served at
`http://<host>:8317/management.html`. Log in with the management key.

- **Quota Management** gains a live strip (routing mode, next-session account
  per provider, traffic, cache-read share, burn, cost today), a pooled card
  per provider on the All tab, and on a provider tab a pool chart: recorded
  pool up to now, projected pool after it (last-24h burn, a full window after
  each reset), the gain each reset returns, pool empty, first account empty,
  next reset, burn over the last 3 hours, and a drain-rate view. Each account
  counts 100%.
- A **Ledger** view (default) shows one row per credential, grouped by
  provider and sorted by soonest recovery, with unused windows muted. The
  upstream card layouts are still under View > Cards.
- New pages: **Scheduler** (next pick and why, live decisions, sticky
  sessions), **Cost & Tokens**, and **Ignition & Alerts**.

Set `PROX_PANEL=upstream` to serve CPA's downloaded panel instead. The JSON
API is under `/prox/api/*` with the same key (`GET /prox/api/pool/chart?window=weekly|five_hour`
returns the chart data).

## How it plugs in

- `internal/prox/engine` puts the prox scheduler in front of CPA's plugin host.
  When prox declines a pick (no fresh quota, mixed providers, shadow mode), the
  plugin host and then CPA's built-in routing decide, so prox never makes a worse
  choice than stock CPA.
- Request outcomes, rate-limit headers, and token counts arrive through CPA's
  usage plugin interface (`usage.RegisterNamedPlugin("prox", ...)`).
- Credentials come from CPA's in-memory auth manager, so tokens CPA refreshes are
  used without re-reading files. prox never refreshes or writes credentials.

Upstream files touched (each marked `// prox:`):

| File | Change |
|---|---|
| `sdk/cliproxy/service_plugins.go` | apply prox config, wrap the plugin scheduler |
| `sdk/cliproxy/service_lifecycle.go` | flush prox state on shutdown |
| `internal/api/server_management.go` | register `/prox` routes, serve the embedded panel |

## Dashboard development

```sh
cd web/panel
bun install
bun run dev         # Vite dev server; log in against a running prox
bun run build:prox  # writes internal/prox/panel/dist/management.html.gz
```

prox code lives in `web/panel/src/prox/`; edits to upstream panel files are
marked `prox:`. `web/panel/UPSTREAM` records the vendored Management Center
commit. A demo with synthetic data:
`go test -tags proxdemo -run TestDemoServer -timeout 0 ./internal/prox/engine`,
then open `http://127.0.0.1:18420/management.html` with any key.
