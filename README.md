# CLI Proxy API

English | [简体中文](README_CN.md) | [日本語](README_JA.md)

CLIProxyAPI is a self-hosted proxy that exposes OpenAI-, Anthropic-, and Gemini-compatible APIs for CLI subscriptions, OAuth accounts, API keys, and compatible upstream providers. It supports multiple credentials, round-robin load balancing, streaming, tools, multimodal input, a web management console, and bounded usage analytics.

## Features

- OpenAI-compatible `/v1/models`, Chat Completions, Responses, images, video, and realtime routes
- Anthropic-compatible `/v1/messages` and token-counting routes
- Gemini-compatible `/v1beta/models`, `generateContent`, and Interactions routes
- OAuth login for Codex, Claude, Antigravity, Kimi, and xAI
- Vertex service-account import and configurable OpenAI-compatible upstreams
- Multiple credentials with round-robin scheduling, retries, and cooldown handling
- Streaming, non-streaming, WebSocket, tool calling, and text/image input where supported
- Disk-served management UI whose HTML, CSS, and JavaScript can be changed without rebuilding the executable
- Per-client-Key aliases, enable/disable controls, usage statistics, exports, and estimated pricing
- Embeddable Go SDK and optional dynamic plugins

## Supported credentials

| Provider or source | Credential method | Setup command or location |
|---|---|---|
| OpenAI Codex | OAuth or device flow | `--codex-login` / `--codex-device-login` |
| Anthropic Claude | OAuth | `--claude-login` |
| Google Antigravity | OAuth | `--antigravity-login` |
| Kimi | OAuth device flow | `--kimi-login` |
| xAI / Grok | OAuth device flow | `--xai-login` |
| Google Vertex AI | Service-account JSON | `--vertex-import FILE` |
| Gemini API keys and compatible upstreams | YAML configuration | `config.yaml` |

OAuth credentials are stored under `auth-dir`, which defaults to `~/.cli-proxy-api`.

## Before you start

You need one of the following:

- A matching archive from [GitHub Releases](https://github.com/router-for-me/CLIProxyAPI/releases), or
- Go 1.26 or newer to build from source, or
- Docker Engine with Docker Compose v2.

Release asset names:

| Platform | Asset |
|---|---|
| Windows x64 | `CLIProxyAPI_<version>_windows_amd64.zip` |
| Windows ARM64 | `CLIProxyAPI_<version>_windows_aarch64.zip` |
| macOS Intel | `CLIProxyAPI_<version>_darwin_amd64.tar.gz` |
| macOS Apple Silicon | `CLIProxyAPI_<version>_darwin_aarch64.tar.gz` |
| Linux x64 | `CLIProxyAPI_<version>_linux_amd64.tar.gz` |
| Linux ARM64 | `CLIProxyAPI_<version>_linux_aarch64.tar.gz` |
| musl Linux / OpenWrt | Add `_no-plugin` before `.tar.gz` |
| FreeBSD ARM64 | `CLIProxyAPI_<version>_freebsd_aarch64_no-plugin.tar.gz` |

The normal Linux builds support dynamic plugins and require GLIBC 2.17 or newer. The `_no-plugin` builds are portable static binaries without dynamic-plugin support.

## First configuration

Copy the example before starting the server:

```powershell
# Windows PowerShell
Copy-Item .\config.example.yaml .\config.yaml
```

```bash
# Linux and macOS
cp config.example.yaml config.yaml
```

At minimum, replace the example client Key. Example values such as `your-api-key-1` trigger safe mode and cause proxy API routes to return HTTP 403.

```yaml
host: "127.0.0.1"
port: 8317

remote-management:
  allow-remote: false
  secret-key: "replace-with-a-strong-management-password"
  web-directory: "web/management"

api-keys:
  - "replace-with-a-client-api-key"

usage-statistics-enabled: true
```

`host: "127.0.0.1"` is recommended for a local-only installation. The example file uses `host: ""`, which listens on all IPv4 and IPv6 interfaces.

## Start on Windows

Extract the Windows archive, open PowerShell in that directory, create `config.yaml`, and run:

```powershell
.\cli-proxy-api.exe --config .\config.yaml
```

Use `Ctrl+C` to stop it. This is a console program; it does not install itself as a Windows service. For unattended startup, use Task Scheduler, NSSM, or another process supervisor.

## Start on Linux

```bash
mkdir -p cliproxyapi
tar -xzf CLIProxyAPI_<version>_linux_amd64.tar.gz -C cliproxyapi
cd cliproxyapi
cp config.example.yaml config.yaml
chmod +x ./cli-proxy-api
./cli-proxy-api --config ./config.yaml
```

Choose the ARM64 archive on ARM systems. Use the `_no-plugin` archive on musl-based distributions or older systems that cannot run the GLIBC build.

For a long-running installation, manage the same command with systemd, OpenRC, supervisord, or your existing process supervisor. The repository does not ship a service unit.

## Start on macOS

```bash
mkdir -p cliproxyapi
tar -xzf CLIProxyAPI_<version>_darwin_aarch64.tar.gz -C cliproxyapi
cd cliproxyapi
cp config.example.yaml config.yaml
chmod +x ./cli-proxy-api
./cli-proxy-api --config ./config.yaml
```

Use `darwin_amd64` on Intel Macs and `darwin_aarch64` on Apple Silicon. If Gatekeeper blocks a trusted binary that you downloaded yourself, allow it in System Settings or remove its quarantine attribute explicitly.

## Start with Docker

From a source checkout, create `config.yaml` before starting Compose. A missing bind-mount source may otherwise be created as a directory.

```bash
cp config.example.yaml config.yaml
docker compose up -d --remove-orphans --no-build
docker compose logs -f cli-proxy-api
```

On Windows PowerShell, use `Copy-Item` instead of `cp`.

Useful commands:

```bash
docker compose restart cli-proxy-api
docker compose down
```

To build the image from the checked-out source, run `./docker-build.sh` on Linux/macOS or `.\docker-build.ps1` in PowerShell and choose option 2. These scripts prevent Compose's pull policy from replacing the local image.

The Compose file persists:

- `config.yaml` at `/CLIProxyAPI/config.yaml`
- `auths/` at `/root/.cli-proxy-api`
- `logs/` at `/CLIProxyAPI/logs`
- `plugins/` at `/CLIProxyAPI/plugins`

For local-only Docker access, change the port mapping from `8317:8317` to `127.0.0.1:8317:8317`.

The host `.env` file is used by Compose for `${...}` substitution, but is not automatically passed into the container while `env_file` remains commented out. Explicitly enable `env_file` or add environment values when using `PGSTORE_*`, `GITSTORE_*`, `OBJECTSTORE_*`, or `MANAGEMENT_PASSWORD`.

## Build from source

```bash
git clone https://github.com/router-for-me/CLIProxyAPI.git
cd CLIProxyAPI
cp config.example.yaml config.yaml
go build -o cli-proxy-api ./cmd/server
./cli-proxy-api --config ./config.yaml
```

Windows PowerShell:

```powershell
git clone https://github.com/router-for-me/CLIProxyAPI.git
Set-Location CLIProxyAPI
Copy-Item .\config.example.yaml .\config.yaml
go build -o cli-proxy-api.exe ./cmd/server
.\cli-proxy-api.exe --config .\config.yaml
```

The normal source build uses CGO for dynamic-plugin support and therefore needs a C toolchain. Build with `CGO_ENABLED=0` if you need a portable binary and do not need dynamic plugins.

## Provider login

Run one login command at a time, using the same configuration file as the server:

```bash
./cli-proxy-api --config ./config.yaml --codex-login
./cli-proxy-api --config ./config.yaml --codex-device-login
./cli-proxy-api --config ./config.yaml --claude-login
./cli-proxy-api --config ./config.yaml --antigravity-login
./cli-proxy-api --config ./config.yaml --kimi-login
./cli-proxy-api --config ./config.yaml --xai-login
```

Add `--no-browser` on a headless machine. Callback-based flows also accept `--oauth-callback-port PORT`. To import Vertex credentials:

```bash
./cli-proxy-api --config ./config.yaml --vertex-import ./service-account.json
```

Inside an already running container, the equivalent pattern is:

```bash
docker compose exec cli-proxy-api ./CLIProxyAPI \
  --config /CLIProxyAPI/config.yaml \
  --codex-login --no-browser
```

Replace the login flag as needed. The mounted `auths/` directory preserves the resulting credentials.

## Verify and use the API

Health check, no authentication required:

```bash
curl http://127.0.0.1:8317/healthz
```

List models with a client Key from `api-keys`:

```bash
curl http://127.0.0.1:8317/v1/models \
  -H "Authorization: Bearer YOUR_CLIENT_API_KEY"
```

Use a model ID returned by that endpoint:

```bash
curl http://127.0.0.1:8317/v1/chat/completions \
  -H "Authorization: Bearer YOUR_CLIENT_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"MODEL_ID","messages":[{"role":"user","content":"Hello"}]}'
```

Common base URLs:

| Client protocol | Base URL or route |
|---|---|
| OpenAI-compatible | `http://127.0.0.1:8317/v1` |
| Anthropic Messages | `http://127.0.0.1:8317/v1/messages` |
| Gemini-compatible | `http://127.0.0.1:8317/v1beta` |

For OpenAI-compatible clients:

```powershell
# Windows PowerShell
$env:OPENAI_BASE_URL = "http://127.0.0.1:8317/v1"
$env:OPENAI_API_KEY = "YOUR_CLIENT_API_KEY"
```

```bash
# Linux and macOS
export OPENAI_BASE_URL="http://127.0.0.1:8317/v1"
export OPENAI_API_KEY="YOUR_CLIENT_API_KEY"
```

Prefer authentication headers over query-string Keys so credentials do not enter URL or proxy logs.

## Web management

Open [http://127.0.0.1:8317/management.html](http://127.0.0.1:8317/management.html). The Management API is enabled only when `remote-management.secret-key` or `MANAGEMENT_PASSWORD` is set. With `allow-remote: false`, management remains localhost-only.

The source checkout contains the primary UI under `web/management`. Files are read from disk for every request, so UI changes only need a browser refresh; no executable rebuild is required. Relative `web-directory` paths are resolved from the directory containing the active configuration file.

Current release archives and the current Docker image do not include `web/management`. If that directory is absent, `/management.html` falls back to the classic single-file panel. To use the source dashboard with a release binary, copy `web/management` beside `config.yaml`. With Docker, also mount it:

```yaml
volumes:
  - ./web/management:/CLIProxyAPI/web/management:ro
```

The classic panel is also available at `/management/` and `/management/legacy`.

## Usage statistics

Enable `usage-statistics-enabled`, assign stable IDs and aliases with `api-key-metadata`, and configure optional `usage-pricing` rules for estimated cost. The web console provides per-Key summaries, input/cache/output token breakdowns, filters, JSON/CSV export, and Key enable/disable controls.

The counters have distinct meanings:

- `attempts`, `success`, and `failed` count one final result per inbound client request. Use these for user reporting and billing estimates.
- `upstream_attempts` and `upstream_failed_attempts` count every credential, model, or provider attempt. Use these for operations and retry analysis.

Usage snapshots are bounded by `usage-statistics-retention-days`. They do not store raw client Keys or request bodies. Estimated prices are informational and are not a payment, balance, invoice, or authoritative settlement system. Older snapshots created before the counter split cannot reconstruct historical retry relationships.

## Terminal UI

Start the terminal management interface with:

```bash
./cli-proxy-api --config ./config.yaml --tui
```

Add `--standalone` to run an embedded local server from TUI mode.

## SDK and advanced documentation

- [SDK usage](docs/sdk-usage.md)
- [Advanced executors and translators](docs/sdk-advanced.md)
- [Authentication and access](docs/sdk-access.md)
- [Credential loading and watchers](docs/sdk-watcher.md)
- [Custom provider example](examples/custom-provider)
- [Plugin examples](examples/plugin)
- [Management API reference](https://help.router-for.me/management/api)

## Security checklist

- Replace every example value in `api-keys` before exposing the proxy.
- Use `host: "127.0.0.1"` for local-only installations.
- Keep `remote-management.allow-remote: false` unless remote administration is required.
- Use a strong management password and HTTPS before sending it over an untrusted network.
- Do not commit `config.yaml`, `.env`, `auths/`, usage snapshots, or provider credentials.
- Restrict access to the OAuth callback ports when logging in remotely.

## Troubleshooting

| Symptom | Check |
|---|---|
| Proxy routes return 403 | Replace template values such as `your-api-key-1` in `api-keys` |
| Management routes return 404 | Set `remote-management.secret-key` or `MANAGEMENT_PASSWORD` |
| New dashboard is not shown | Ensure `web/management/index.html` exists relative to `config.yaml`, then hard-refresh the browser |
| OAuth opens on the wrong machine | Add `--no-browser` and follow the printed URL/device instructions |
| Linux binary does not start | Use the `_no-plugin` build or install a compatible GLIBC runtime |
| Docker ignores storage environment values | Enable `env_file` or pass those variables explicitly to the container |

## Contributing

1. Fork the repository.
2. Create a feature branch.
3. Run `gofmt -w .`, `go test ./...`, and a clean build for Go changes.
4. Commit and push the branch.
5. Open a pull request with a clear description and verification notes.

## License

This project is licensed under the [MIT License](LICENSE).
