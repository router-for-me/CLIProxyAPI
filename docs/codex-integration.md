# Codex desktop and CLI integration

CLIProxyAPI can provide third-party subscription models to Codex desktop and the Codex CLI while preserving Codex's built-in `openai` provider identity. The integration uses the existing CLIProxyAPI process; it does not start a second proxy, a sidecar, or a credential watcher.

```text
Codex desktop / CLI
        |
        | loopback /v1
        v
CLIProxyAPI
   |-- Codex OAuth -> official GPT models
   |-- xAI OAuth -> Grok models
   `-- AntiGravity OAuth -> Gemini and Claude models
```

The lifecycle commands manage one marked root block in Codex's `config.toml`, one complete model catalog, a restore journal, and catalog-cache invalidation. Setup, sync, and restore are previews unless `-apply` is present.

## Model surface

The first five entries are the featured multi-agent models, in this order:

| Codex-visible slug | Upstream route | Featured | Tools | Image input | Hosted web search |
| --- | --- | --- | --- | --- | --- |
| `gpt-5.6-sol` | built-in `openai` provider | yes | yes | provider-defined | yes |
| `xai/grok-4.5` | `xai` / `grok-4.5` | yes | yes | yes | yes |
| `antigravity/gemini-3.6-flash` | `antigravity` / `gemini-3.6-flash-high` | yes | yes | yes | no |
| `antigravity/gemini-3.1-pro` | `antigravity` / `gemini-pro-agent` | yes | yes | yes | no |
| `antigravity/claude-opus-4-6-thinking` | `antigravity` / `claude-opus-4-6-thinking` | yes | yes | yes | no |
| `xai/grok-build-0.1` | `xai` / `grok-build-0.1` | no | yes | yes | yes |

Provider-qualified slugs are strict routes. For example, `antigravity/gemini-3.1-pro` fails closed when AntiGravity is unavailable; it is never sent to a same-named model on another provider. The stable slug remains visible in responses even though its upstream target is `gemini-pro-agent`.

The catalog also retains the complete official GPT model surface. Official tasks and models continue to use Codex's built-in `openai` provider rather than a replacement provider name.

## Prerequisites

1. Use a strict loopback listener: `127.0.0.1`, `::1`, or `localhost`.
2. Log in to every required provider with the same CLIProxyAPI config:

   ```bash
   ./cli-proxy-api -config config.yaml -codex-login
   ./cli-proxy-api -config config.yaml -xai-login
   ./cli-proxy-api -config config.yaml -antigravity-login
   ```

3. Enable the integration in `config.yaml`. The complete default mapping is in [`config.example.yaml`](../config.example.yaml).

   ```yaml
   host: "127.0.0.1"
   port: 8317

   codex-integration:
     enabled: true
     loopback-access: true
     auto-sync: true
     catalog-file: "cliproxyapi-catalog.json"
     multi-agent-mode: "v1"
   ```

When `models` is omitted, the stable default mapping above is used. Keep an explicit `models` list when you want the deployed policy to be self-documenting.

## Setup and migration

Start CLIProxyAPI with the integration-enabled config, then preview setup:

```bash
./cli-proxy-api -config config.yaml -codex-setup -json
```

Apply only after reviewing the resolved `config_file` and `catalog_file`:

```bash
./cli-proxy-api -config config.yaml -codex-setup -apply -json
```

The command writes only inside the resolved Codex home. Resolution order is `-codex-home`, `codex-integration.codex-home`, `CODEX_HOME`, then the platform default. Use `-codex-home` for isolated rehearsal:

```bash
temporary_codex_home="$(mktemp -d)"
./cli-proxy-api -config config.yaml -codex-setup -codex-home "$temporary_codex_home" -apply -json
CODEX_HOME="$temporary_codex_home" codex debug models
./cli-proxy-api -config config.yaml -codex-restore -codex-home "$temporary_codex_home" -apply -json
```

Setup refuses user-owned `openai_base_url` or `model_catalog_json` root keys. To migrate a recognized OpenCodex block, preview and then explicitly apply:

```bash
./cli-proxy-api -config config.yaml -codex-setup -codex-migrate-opencodex -json
./cli-proxy-api -config config.yaml -codex-setup -codex-migrate-opencodex -apply -json
```

This migration recognizes only OpenCodex's known marker. It does not guess ownership of arbitrary configuration. Stop OpenCodex's config writer after the CLIProxyAPI block is applied, then restart Codex so its model cache is rebuilt.

## Routine operations

Preview and apply a catalog refresh:

```bash
./cli-proxy-api -config config.yaml -codex-sync -json
./cli-proxy-api -config config.yaml -codex-sync -apply -json
```

`auto-sync: true` updates an already-installed catalog when the model registry or mapping revision changes. A compile failure or missing mapped model leaves the last-good catalog in place.

Run read-only diagnostics:

```bash
./cli-proxy-api -config config.yaml -codex-doctor -json
```

Add explicit, low-output provider requests only during a maintenance window:

```bash
./cli-proxy-api -config config.yaml -codex-doctor -probe-models -json
```

Doctor exits with `0` for clean, `1` for warnings, and `2` for a blocking problem. It reports credential metadata and failure categories without printing tokens, email addresses, prompts, or upstream error bodies.

## Failure guide

| Doctor or runtime symptom | Meaning | Action |
| --- | --- | --- |
| `config.unmanaged` or ownership conflict | Codex root keys are owned by another configuration | Restore or explicitly migrate the known OpenCodex block; do not delete unrelated keys |
| `catalog.source_unavailable` | A configured upstream model is absent for its required provider | Refresh that provider login or correct the fixed mapping; the last-good catalog remains active |
| `catalog.stale` / `cache.restart_required` | Disk catalog or Codex cache predates the current mapping | Run sync with `-apply`, then restart Codex |
| `oauth.<provider>_missing` | No safe credential metadata was found | Run the corresponding provider login and re-run doctor |
| `endpoint.models_rejected` | Loopback data-plane authentication or binding is wrong | Confirm `host` is strict loopback, enable `loopback-access`, and restart CLIProxyAPI |
| HTTP `401` | Provider credential expired or was rejected | Refresh that provider login; do not route the slug to another provider |
| HTTP `429` | Subscription quota or concurrent connection limit | Wait for reset or reduce concurrency; preserve the provider-qualified route |
| HTTP `5xx`, first-event timeout, or idle timeout | Upstream or network failure | Retry a read-only request after connectivity recovers; inspect request ID and provider health |
| WebSocket upgrade unavailable | No request payload was accepted over WebSocket | CLIProxyAPI safely uses the HTTP Responses transport |
| WebSocket disconnect after request write | Acceptance is ambiguous | CLIProxyAPI returns the error and does not automatically resend, preventing duplicate tool execution |
| `opencodex.listener_detected` | The old proxy is still running | Finish the cutover checks, then stop its daemon and config watcher |

## Restore

Preview restore before changing the real Codex home:

```bash
./cli-proxy-api -config config.yaml -codex-restore -json
./cli-proxy-api -config config.yaml -codex-restore -apply -json
```

Restore removes the CLIProxyAPI-owned marker block, restores a pre-existing catalog when one was backed up, and preserves user edits made outside the managed block. It moves integration state aside rather than silently overwriting it. Restart Codex after an applied restore.

## OpenCodex retirement checklist

1. Record the current config and catalog hashes, file modes, listener ports, service labels, and credential-file metadata. Never record credential contents.
2. Complete setup and doctor in a temporary Codex home.
3. Apply the explicit OpenCodex migration to the real Codex home.
4. Stop OpenCodex's daemon and credential/config watcher; keep its installation and backups during the observation period.
5. Restart Codex and verify the five featured models, Grok Build, official GPT history, tools, image input, compact, and a long-running task.
6. After the observation gate passes, unload the old service permanently. Retain the CLIProxyAPI restore journal and backups for one release cycle.

## Security and capability boundaries

- Loopback bearer access applies only to data-plane model routes. It does not authenticate management routes and is rejected for non-loopback peers, forwarded-header spoofing, wildcard binds, and LAN access.
- Setup rejects symlinks, uses an exclusive lifecycle lock, writes atomically, and creates new sensitive files with mode `0600`.
- The first release exposes the explicit stable mappings above. It does not automatically publish every provider model.
- AntiGravity models do not advertise Codex hosted `web_search`; local browser or MCP tools remain available to the Codex agent.
- Official GPT image generation and image edit stay on the official Codex path. Third-party catalog entries advertise image input only where configured.
- Remote Codex clients and OpenCodex sidecar search behavior are outside this integration.
- Provider subscriptions and OAuth-backed CLI access are governed by each provider's terms and usage limits. Confirm that proxying a subscription into Codex is permitted for your account and organization.
