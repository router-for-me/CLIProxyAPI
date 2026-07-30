# Deployment

The usage dashboard is an **optional** sidecar. CLIProxyAPI runs exactly the same
without it; the dashboard only reads the management usage queue and never enters
the inference request path.

## 1. Configure CLIProxyAPI

Enable usage statistics and raise the queue retention to the supported maximum so
the collector can survive short outages:

```yaml
usage-statistics-enabled: true
redis-usage-queue-retention-seconds: 3600
```

Restart CPA after changing these. Without `usage-statistics-enabled: true`, no
records are emitted and the dashboard will stay empty.

## 2. Install dependencies

Requires Python >= 3.10 and `uv` (https://docs.astral.sh/uv/).

```bash
cd /opt/usage-dashboard
uv sync
```

To build the frontend from source (optional — `frontend/dist/` may be
committed and used directly), you also need Node.js >= 18 and pnpm:

```bash
pnpm build
```

## 3. Configure the dashboard

```bash
export CLIPROXY_MANAGEMENT_KEY='<your CPA management secret>'
python3 usage_dashboard.py init
```

This creates `~/.cli-proxy-api/usage-dashboard/` with `config.json` (mode 0600)
and `usage.sqlite`. Edit `config.json` for your environment.

## 4. Local (loopback) access — default

```bash
python3 usage_dashboard.py run
```

Open `http://127.0.0.1:8321` on the same machine. No token is required because
the default bind is loopback only.

## 5. LAN access

To reach the dashboard from other machines on your LAN:

```json
{
  "dashboard_host": "0.0.0.0",
  "dashboard_port": 8321,
  "dashboard_token": "<a-long-random-token>"
}
```

A non-loopback bind **requires** `dashboard_token`; the server refuses to start
otherwise. Open `http://<server-LAN-IP>:8321` and authenticate with the token
(the dashboard will prompt for it).

Recommended hardening for LAN exposure:

- **Firewall**: allow the dashboard port only from trusted LAN CIDRs, e.g.
  `ufw allow from 192.168.1.0/24 to any port 8321`.
- **TLS reverse proxy**: put nginx/Caddy in front for HTTPS when traffic crosses
  an untrusted network. The dashboard speaks HTTP; terminate TLS at the proxy.
- **Unique token**: generate a long random token, e.g.
  `python3 -c "import secrets; print(secrets.token_urlsafe(32))"`.

## 6. Lifecycle

| Command | Purpose |
|---------|---------|
| `init` | Create data dir, DB, default config |
| `collect` | Run only the collector loop |
| `serve` | Run only the dashboard server (uvicorn) |
| `run` | Run collector + server together (local convenience) |
| `report <range\|from to>` | Print a JSON summary |
| `compact --days N` | Delete rows older than N days |
| `quota --force` | Refresh Codex quota (disabled by default) |

### systemd

Create `/etc/usage-dashboard/env` (mode 0600, owned by the dashboard user):

```
CLIPROXY_MANAGEMENT_KEY=<your CPA management secret>
```

Then use:

```ini
[Unit]
Description=CLIProxyAPI Usage Dashboard
After=network.target

[Service]
ExecStart=uv run python /opt/usage-dashboard/usage_dashboard.py run
EnvironmentFile=/etc/usage-dashboard/env
WorkingDirectory=/opt/usage-dashboard
Restart=on-failure
User=usage-dashboard

[Install]
WantedBy=multi-user.target
```

Collector state is persisted in the SQLite database, so the `serve` process
reflects collector health even when run as a separate process.

### Docker Compose

```yaml
services:
  usage-dashboard:
    image: python:3.12-slim
    command: >
      sh -c "
        pip install uv &&
        cd /app &&
        uv sync &&
        uv run python /app/usage_dashboard.py run
      "
    environment:
      CLIPROXY_MANAGEMENT_KEY: ${CPA_MANAGEMENT_KEY}
      CLIPROXY_BASE_URL: http://cliproxyapi:8317
      DASHBOARD_HOST: 0.0.0.0
      DASHBOARD_TOKEN: ${DASHBOARD_TOKEN}
    volumes:
      - ./usage-dashboard:/app
      - usage-data:/root/.cli-proxy-api/usage-dashboard
    ports:
      - "8321:8321"
volumes:
  usage-data:
```

For a build-time approach with a pre-built frontend, add a build stage:

```dockerfile
FROM python:3.12-slim AS builder
RUN pip install uv
COPY . /app
WORKDIR /app
RUN uv sync && cd frontend && pnpm build

FROM python:3.12-slim
RUN pip install uv
COPY --from=builder /app /app
WORKDIR /app
CMD ["uv", "run", "python", "/app/usage_dashboard.py", "run"]
```

## 7. Limitations and data safety

- **Bounded queue delivery**: CPA's usage queue is memory-backed, destructive on
  read, and capped at `redis-usage-queue-retention-seconds` (max 3600). If the
  collector is down longer than that window, those records are permanently lost.
  Run the collector continuously.
- **CPA restart**: the in-memory queue is cleared on CPA restart; records still
  in the queue at that moment are lost.
- **Backups**: the SQLite database lives at `<data_dir>/usage.sqlite`. Back it up
  when CPA is running and the collector is active; WAL mode makes online copies
  safe via `sqlite3 usage.sqlite '.backup /path/to/backup.sqlite'`.
- **Schema upgrades**: migrations run automatically on startup and are
  transactional. If a migration fails, the previous schema is left intact and
  the server refuses to operate.
- **Retention**: use `compact --days N` periodically to bound database growth.
- **Cost estimates** are derived from `pricing.json` and are **not** authoritative
  billing.
- **File permissions**: ensure `<data_dir>` and `config.json` are readable only
  by the dashboard user (`chmod 600 config.json` is applied on `init`).

## 8. Rollback

Stopping or removing the dashboard does not affect CLIProxyAPI. To roll back:

1. Stop the dashboard process.
2. (Optional) remove its systemd unit / compose entry.
3. The SQLite database is retained — keep it for later export/recovery, or delete
   it to start clean.

CPA's inference behavior is unchanged regardless of the dashboard's state.