# prox roadmap

prox is a fork of [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) that
natively includes the functionality of
[quota-router](https://github.com/kpatdev/quota-router) and
[lamplighter](https://github.com/PosvdM/cpa-plugin-lamplighter), plus a unified
quota/usage/cost dashboard.

## Principles

- Track upstream regularly (`git remote upstream`). All prox code lives in new
  packages under `internal/prox/`; edits to upstream files are limited to small
  wiring hooks, each marked with a `// prox:` comment so rebases are easy to audit.
- Follow the repo conventions in `AGENTS.md` (moego json/regexp/base64, ojson for
  writes, no post-connect timeouts, logrus, gofmt).
- One config block: `plugins.configs.prox` in `config.yaml` (a top-level key would be rejected by v8 validation), hot-reloaded through the existing watcher.
- One state directory (default `auths/../prox-data/`), atomic writes, advisory lock.

## Integration seams (already in upstream)

| Need | Seam | Upstream edit |
|---|---|---|
| Quota-aware pick + affinity | `auth.PluginScheduler` / `PluginSchedulerAcrossPriorities` via `Manager.SetPluginScheduler` | Wrap `pluginHost` in a composite at `sdk/cliproxy/builder.go` and `service_plugins.go` (native first, plugin host as fallback) |
| Failure + passive quota feedback, token usage | `usage.RegisterNamedPlugin` (`usage.Record`) | none |
| Response rate-limit headers | `sdk/cliproxy/auth/quota_signals.go` | small hook if headers are not already on the record |
| Ignition requests | core `Manager` execute with pinned auth | none (expose a narrow accessor if needed) |
| Dashboard + JSON API | embedded Management Center at `/management.html`, Gin routes `/prox/api/*` (management-key auth) | route registration and panel hook in `internal/api` |

## Package layout

```
internal/prox/
  config/     prox: config block, defaults, validation
  state/      persisted state file, lock, debounced atomic writes
  quota/      provider usage fetchers + parsers (Claude, Codex, Kimi, Grok, Antigravity),
              passive header parsing, window model, plan detection + weights
  poller/     poll schedule (interval, pre/post reset, passive-skip, on demand)
  session/    session key extraction (mirrors sdk/cliproxy/session), bindings, lineage
  router/     policies (reset_first, quota_pressure, balanced, manual_priority),
              health/circuit state, decision log, composite PluginScheduler
  ignite/     5h window ignition engine, schedule, failure protection, model choice
  notify/     alerts (thresholds, plan change, credential), Bark, webhook, codex-reset feed
  usage/      token + cost aggregation (per account/model/client), pricing table
  sink/       optional ClickHouse request/response logging
  api/        /prox/api/* JSON handlers, pooled limits endpoint
  web/        embedded dashboard build output
web/prox/     dashboard source (Bun + Vite + React)
```

## Phases

Status (2026-11-27): phases 1 and 2 done for Claude and Codex. quota-router's
config, quota, router, session, store, telemetry, and mgmt packages are ported
under `internal/prox/` with their tests; `internal/prox/engine` replaces the
plugin ABI adapter; the existing dashboard is served at `/prox/ui/`.

Status (2026-11-27, later): ignition (`internal/prox/ignite`) and notifications
(`internal/prox/notify`, Bark + webhook) done for Claude/Codex as a clean-room
implementation of lamplighter's documented behavior (lamplighter is AGPL; prox
stays MIT). Remaining from phase 3: Kimi/Grok/Antigravity monitoring (endpoints
from the MIT Management Center), plan weights. Dashboard pages for ignition and
notifications come with phase 6.

Status (2026-11-27, end of day): phases 3-6 done. Kimi/Grok monitor, usage and
cost ledger, pooled limits endpoint, ClickHouse usage sink, and dashboard pages
(pool strip, Cost & Tokens, Ignition & Alerts, monitored accounts). Open: request/
response body logging to ClickHouse (needs a request-logger hook), dashboard
settings forms for the ignition/notify/usage blocks, live verification against
real accounts.

Status (2026-11-27, UI): the quota-router dashboard is replaced by the
vendored Management Center (`web/panel`) with a pool chart, ledger view, live
strip, and Scheduler / Cost / Ignition pages, embedded and served at
`/management.html`.

1. **Quota core.** `config`, `state`, `quota` (Claude + Codex first), `poller`.
   Read-only polling and passive headers. Unit tests with fixtures from both repos.
2. **Router.** `session`, `router`, composite scheduler wiring, `shadow`/`active`
   modes, decision log, usage feedback (429/401/5xx handling, rebind policy).
   Regression test: native scheduler falls back to plugin host and to built-in routing.
3. **Monitor + alerts.** Kimi/Grok/Antigravity fetchers, plan weights, history store,
   `notify` (Bark/webhook), alert thresholds.
4. **Ignition.** Schedule, minimal streamed request with 2 s cutoff, failure protection,
   cooldown awareness. Payload-config ordering test per `AGENTS.md`.
5. **Usage, cost, logging.** Token/cost aggregation from `usage.Record`, pricing table
   (configurable overrides), pooled limits endpoint, optional ClickHouse sink.
6. **Dashboard.** After the UI option is chosen (see below).

## Dashboard options (decide before phase 6)

All options cover: per-account 5h/7d/monthly/per-model windows with reset countdowns,
burn rate and time-to-exhaustion, next-session pick and why, sessions and affinity,
decision explorer, ignition plans, alerts, tokens and **cost** by account/model/client/day,
cache read/write ratio (affinity health), settings.

- **A. Operator console (recommended).** Dense single-page layout like Theo's: a top
  strip of pooled headroom per provider, an account grid with only actionable
  buckets highlighted (unused buckets grayed), a live request feed, and tabs for
  Usage/Cost, Sessions, Activity, Ignition, Settings. Extends quota-router's React
  app (ported to Bun) and adds lamplighter's quota chart.
- **B. Two dashboards, one shell.** Keep quota-router's page structure for routing and
  lamplighter's chart-centric page for quota history, joined by a shared nav.
  Fastest to ship, less cohesive.
- **C. Grafana-first.** Minimal built-in status page; rich usage/cost/history views live
  in Grafana over ClickHouse. Best for deep analytics, needs ClickHouse running.

## Out of scope

- Header/entrypoint rewriting that makes headless or SDK traffic look like
  interactive Claude Code to get looser rate limits.
