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
