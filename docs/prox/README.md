# prox features

prox adds quota-aware routing, session affinity, and a quota dashboard to
CLIProxyAPI. Everything is off until you enable it. See [ROADMAP.md](ROADMAP.md)
for what is planned.

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

These features are written from scratch based on the documented behavior of
[lamplighter](https://github.com/PosvdM/cpa-plugin-lamplighter); no lamplighter
code is included.

## Dashboard

Open `http://<host>:8317/prox/ui/index.html` and enter the management key
(`remote-management.secret-key`). The JSON API is under `/prox/api/*` and
requires the same key. Settings saved from the dashboard go through
`PATCH /v0/management/plugins/prox/config` and apply via hot reload.

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
| `internal/api/server_management.go` | register `/prox` routes |

## Dashboard development

```sh
cd web/prox
bun install
bun run dev     # proxies /prox/api to 127.0.0.1:8317
bun run build   # writes internal/prox/mgmt/dist (embedded in the binary)
```
