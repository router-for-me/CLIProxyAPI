# 0005: Usage aggregator and management panel

Date: 2026-11-16. Status: accepted (implementation notes are appended when it lands).

## Usage aggregator

- Upstream has no token/cost aggregation. `GET /observability/usage/queue` **pops** records, so the
  panel must never use it (it would steal records from Home/RESP consumers).
- proxx registers its own usage plugin that keeps totals per day × model × provider × client API key
  (requests, failures, tokens by type, cost) plus a ring buffer of recent requests.
- It runs regardless of `usage-statistics-enabled` (that flag only gates the upstream queue)
  and can be turned off with `proxx.usage.disabled`.
- Totals are snapshotted to a JSON file (default `proxx-usage.json` next to the config) every
  minute and on shutdown, so they survive restarts.
- Exposed under `/v8/management/proxx/usage/*`.

## Panel

- Fork `router-for-me/Cli-Proxy-API-Management-Center` as `iamkunal9/proxx-panel`, cloned at `../proxx-panel`.
- "Overhaul" = a reskin, not a rewrite: a dark-first theme inspired by Linear (dense, neutral
  greys, one accent colour, subtle borders) done through the theme tokens and layout shell, so
  upstream panel updates still merge.
- New pages in new files: Spend & usage, Model groups, Pricing.
- Deploy: the backend downloads `management.html` from the latest GitHub release of
  `remote-management.panel-github-repository`; point it at `iamkunal9/proxx-panel` and tag a
  release there.
