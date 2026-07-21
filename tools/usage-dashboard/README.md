# CLIProxyAPI Usage Dashboard

A lightweight, standard-library Python sidecar that collects per-request usage
records from CLIProxyAPI, persists them in SQLite, and renders a local web
dashboard with historical token, model, latency, and estimated-cost analytics.

It sits **beside** CLIProxyAPI and reads the management usage queue. It never
enters the inference request path, so stopping it does not affect model
requests.

## Quick start

1. Enable usage statistics in CPA and raise the queue retention to the maximum:

   ```yaml
   usage-statistics-enabled: true
   redis-usage-queue-retention-seconds: 3600
   ```

2. Create the dashboard config (placeholders only — real keys come from env or a
   permission-restricted file):

   ```bash
   export CLIPROXY_MANAGEMENT_KEY='<your CPA management secret>'
   python3 usage_dashboard.py init
   ```

3. Run collector + dashboard together for local use:

   ```bash
   python3 usage_dashboard.py run
   # dashboard listening on http://127.0.0.1:8320
   ```

For LAN access, edit `~/.cli-proxy-api/usage-dashboard/config.json`:

```json
{
  "dashboard_host": "0.0.0.0",
  "dashboard_port": 8320,
  "dashboard_token": "<a-long-random-token>"
}
```

Then open `http://<server-LAN-IP>:8320` and authenticate with the token.

## Commands

- `init` — create the data directory, SQLite database, and default config.
- `collect` — run only the collector loop.
- `serve` — run only the dashboard server.
- `run` — run collector and server together (local convenience).
- `report <range|from to>` — print a JSON summary. Range presets: `today 1h 5h
  24h 7d 30d`. Explicit RFC 3339 `from`/`to` overrides the preset.
- `compact --days N` — delete usage rows older than N days.
- `quota --force` — refresh the optional Codex quota snapshots (disabled by
  default; requires Codex OAuth files).

## Configuration

Config lives at `~/.cli-proxy-api/usage-dashboard/config.json` (mode 0600) and
is merged over defaults. Environment variables override file values:

| Key | Env var | Default | Notes |
|-----|---------|---------|-------|
| `cliproxy_base_url` | `CLIPROXY_BASE_URL` | `http://127.0.0.1:8317` | CPA base URL |
| `management_key` | `CLIPROXY_MANAGEMENT_KEY` | `""` | CPA management secret (required for collect) |
| `poll_interval_seconds` | `POLL_INTERVAL_SECONDS` | `2` | Queue poll interval |
| `batch_size` | `BATCH_SIZE` | `100` | Records per poll |
| `dashboard_host` | `DASHBOARD_HOST` | `127.0.0.1` | Bind address; non-loopback requires `dashboard_token` |
| `dashboard_port` | `DASHBOARD_PORT` | `8320` | Bind port |
| `dashboard_token` | `DASHBOARD_TOKEN` | `""` | Bearer token for dashboard access |
| `quota_enabled` | `QUOTA_ENABLED` | `false` | Codex OAuth quota feature |
| `data_dir` | `USAGE_DASHBOARD_DATA_DIR` | `~/.cli-proxy-api/usage-dashboard` | SQLite + config location |

## Pricing

Optional effective-dated pricing lives at `<data_dir>/pricing.json`. Example:

```json
{
  "currency": "USD",
  "models": {
    "ts-gpt-56": [
      {
        "effective_from": "2026-07-01T00:00:00Z",
        "input_per_million": 1.25,
        "output_per_million": 10.0,
        "cached_input_per_million": 0.15
      }
    ]
  }
}
```

Models without a price entry are reported as unpriced; their token totals are
still counted, and cost coverage is marked incomplete. All monetary values are
**estimates**, not authoritative billing.

## API

All analytics endpoints accept `from`/`to` (RFC 3339, UTC) and/or `range`
preset, and an optional repeated `model` filter. Explicit `from`/`to` wins.

- `GET /api/v1/health`
- `GET /api/v1/summary?range=24h&model=ts-gpt-56`
- `GET /api/v1/timeseries?from=...&to=...&group_by=model|provider|day|hour`
- `GET /api/v1/models?range=7d`
- `GET /api/v1/requests?range=24h&model=...&limit=100&cursor=...`

Legacy endpoints (`/api/summary`, `/api/requests`, `/api/health`) remain for the
embedded HTML dashboard.

## Limitations

- The CPA usage queue is memory-backed, destructive on read, and bounded by
  `redis-usage-queue-retention-seconds` (max 3600). If the collector is down
  longer than the retention window, those records are lost. Run the collector
  continuously.
- Cost values are estimates derived from your `pricing.json`; they are not
  authoritative billing.
- Non-loopback dashboard access requires a bearer token and should be firewalled
  to trusted LAN CIDRs; use a TLS reverse proxy for untrusted networks.

## Layout

- `usage_dashboard/` — package (config, storage, collector, pricing, query, server).
- `usage_dashboard.py` — thin shim that runs the package CLI.
- `dashboard.html` — the served dashboard, embedded by the server.
