# CLIProxyAPI (self-use fork)

English | [中文](README_CN.md) | [日本語](README_JA.md)

Personal fork of [router-for-me/CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) — a proxy server that provides OpenAI / Gemini / Claude / Codex compatible API interfaces in front of CLI tools and their accounts. This fork adds self-hosted relay providers, per-account usage cost accounting, and bundles its management panel and the new-api gateway into a single repository.

## What this fork adds

- **Mirasim provider** — first-class `mirasim` API-key provider for Anthropic-protocol reverse proxies (Bearer key, mandatory `base-url`, models fall back to the Claude catalog, alias mapping, thinking config, full Management API and panel support).
- **Cline provider** — first-class `cline` OAuth provider: WorkOS device-flow login (`--cline-login`, Management API, TUI), rotating token refresh, and **first-import model detection** so each account only advertises the models it can actually use (probed from `api.cline.bot`, persisted into the credential file).
- **Usage cost accounting** — upstream-reported request cost (Cline `usage.cost`) is captured into usage details and exposed as `tokens.cost_usd` on the usage queue (`/v0/management/usage-queue`, needs `usage-statistics-enabled: true`). Token counters were already there; money is now there too.
- **In-repo management panel** — the Management Center frontend lives in `management-center/` (Vite single-file build) and gets served from `static/management.html` instead of the downloaded GitHub asset. It ships the Cline OAuth card and the Mirasim key family.
- **In-repo new-api** — [QuantumNous/new-api](https://github.com/QuantumNous/new-api) vendored under `new-api/` for the side-by-side gateway deployment (self-use branch `mellow-rolling-falcon`).

Upstream highlights that still apply: Gemini / Claude / Codex / Grok / Kimi OAuth pools with round-robin, OpenAI-compatible upstreams, streaming + tool calling, model registry with remote updates, hot-reload, management API + TUI, embeddable Go SDK.

## Quick start

```bash
# build and run
go build -o cli-proxy-api ./cmd/server
cp config.example.yaml config.yaml   # edit: access.api-keys, management.secret-key
./cli-proxy-api --config config.yaml

# account logins (OAuth)
./cli-proxy-api --cline-login        # Cline device flow
./cli-proxy-api --claude-login       # Claude subscription
```

Mirasim relay in `config.yaml`:

```yaml
api-keys:
  mirasim:
    - name: my-relay
      base-url: "https://your-relay.example.com"   # required
      keys:
        - api-key: "your-key"
      models:
        - name: "claude-opus-4-1-20250805"
          alias: "mira-opus"
```

The HTTP API listens on `server.port` (default 8317) with OpenAI `/v1/chat/completions`, Anthropic `/v1/messages`, and Gemini endpoints; authenticate with any key from `access.api-keys`.

## Management panel

The panel source lives in `management-center/` and follows its own `AGENTS.md` (React 19 + Vite + Bun). Build and stage it for the backend:

```bash
./management-center/build.sh   # bun install + build -> static/management.html
```

The server prefers the local `static/management.html` over the auto-downloaded asset; set `management.disable-auto-update-panel: true` to stop the updater from replacing it. Open `http://<host>:<port>/management.html` and log in with `management.secret-key`.

## new-api gateway

`new-api/` vendors the new-api gateway (own Go module, own `AGENTS.md`; branding and attribution belong to QuantumNous and are kept intact). Build and run it alongside this proxy:

```bash
cd new-api/web && bun install --frozen-lockfile && bun run build   # frontend (embedded)
cd .. && go build -o new-api .
./new-api --port 3000   # first run initializes an admin account; SQLite by default
```

Point its channels at `http://127.0.0.1:8317` with a CLIProxyAPI client key to chain billing/account management through this proxy's upstream pool.

## Docs

- Config template: [config.example.yaml](config.example.yaml)
- SDK: [docs/sdk-usage.md](docs/sdk-usage.md) · [docs/sdk-advanced.md](docs/sdk-advanced.md) · [docs/sdk-access.md](docs/sdk-access.md) · [docs/sdk-watcher.md](docs/sdk-watcher.md)
- Management API v8: [docs/management-api-v8.md](docs/management-api-v8.md)
- Upstream guides: https://help.router-for.me/

## Upstream and ecosystem

This fork tracks [router-for-me/CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI). Ecosystem projects, ports, and the contributor list moved to the upstream README — see there if you're looking for related tools.

## License

MIT — see [LICENSE](LICENSE).
