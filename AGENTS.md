# AGENTS.md

Go 1.26+ proxy server providing OpenAI/Gemini/Claude/Codex compatible APIs with OAuth and round-robin load balancing.

## Repository
- GitHub: https://github.com/router-for-me/CLIProxyAPI

## Commands
```bash
gofmt -w . # Format (required after Go changes)
go build -o cli-proxy-api ./cmd/server # Build
go run ./cmd/server # Run dev server
go test ./... # Run all tests
go test -v -run TestName ./path/to/pkg # Run single test
go build -o test-output ./cmd/server && rm test-output # Verify compile (REQUIRED after changes)
```
- Common flags: `--config <path>`, `--tui`, `--standalone`, `--local-model`, `--no-browser`, `--oauth-callback-port <port>`

## Config
- Default config: `config.yaml` (template: `config.example.yaml`)
- `.env` is auto-loaded from the working directory
- Auth material defaults under `auths/`
- Storage backends: file-based default; optional Postgres/git/object store (`PGSTORE_*`, `GITSTORE_*`, `OBJECTSTORE_*`)

## Architecture
- `cmd/server/` — Server entrypoint
- `internal/api/` — Gin HTTP API (routes, middleware, modules)
- `internal/api/modules/amp/` — Amp integration (Amp-style routes + reverse proxy)
- `internal/thinking/` — Main thinking/reasoning pipeline. `ApplyThinking()` (apply.go) parses suffixes (`suffix.go`, suffix overrides body), normalizes config to canonical `ThinkingConfig` (`types.go`), normalizes and validates centrally (`validate.go`/`convert.go`), then applies provider-specific output via `ProviderApplier`. Do not break this "canonical representation → per-provider translation" architecture.
- `internal/runtime/executor/` — Per-provider runtime executors (incl. Codex WebSocket)
- `internal/translator/` — Provider protocol translators (and shared `common`)
- `internal/registry/` — Model registry + remote updater (`StartModelsUpdater`); `--local-model` disables remote updates
- `internal/store/` — Storage implementations and secret resolution
- `internal/managementasset/` — Config snapshots and management assets
- `internal/cache/` — Request signature caching
- `internal/watcher/` — Config hot-reload and watchers
- `internal/wsrelay/` — WebSocket relay sessions
- `internal/usage/` — Usage and token accounting
- `internal/tui/` — Bubbletea terminal UI (`--tui`, `--standalone`)
- `sdk/cliproxy/` — Embeddable SDK entry (service/builder/watchers/pipeline)
- `test/` — Cross-module integration tests

## Code Conventions
- Keep changes small and simple (KISS)
- Comments in English only
- If editing code that already contains non-English comments, translate them to English (don’t add new non-English comments)
- For user-visible strings, keep the existing language used in that file/area
- New Markdown docs should be in English unless the file is explicitly language-specific (e.g. `README_CN.md`)
- As a rule, do not make standalone changes to `internal/translator/`. You may modify it only as part of broader changes elsewhere.
- If a task requires changing only `internal/translator/`, run `gh repo view --json viewerPermission -q .viewerPermission` to confirm you have `WRITE`, `MAINTAIN`, or `ADMIN`. If you do, you may proceed; otherwise, file a GitHub issue including the goal, rationale, and the intended implementation code, then stop further work.
- `internal/runtime/executor/` should contain executors and their unit tests only. Place any helper/supporting files under `internal/runtime/executor/helps/`.
- Follow `gofmt`; keep imports goimports-style; wrap errors with context where helpful
- Do not use `log.Fatal`/`log.Fatalf` (terminates the process); prefer returning errors and logging via logrus
- Shadowed variables: use method suffix (`errStart := server.Start()`)
- Wrap defer errors: `defer func() { if err := f.Close(); err != nil { log.Errorf(...) } }()`
- Use logrus structured logging; avoid leaking secrets/tokens in logs
- Avoid panics in HTTP handlers; prefer logged errors and meaningful HTTP status codes
- Timeouts are allowed only during credential acquisition; after an upstream connection is established, do not set timeouts for any subsequent network behavior. Intentional exceptions that must remain allowed are the Codex websocket liveness deadlines in `internal/runtime/executor/codex_websockets_executor.go`, the wsrelay session deadlines in `internal/wsrelay/session.go`, the management APICall timeout in `internal/api/handlers/management/api_tools.go`, and the `cmd/fetch_antigravity_models` utility timeouts

## Cursor Composer provider (`feat/cursor-composer-provider`)

Provider id: `cursor-composer`. Executor: `internal/runtime/executor/cursor_composer_executor.go`. SDK bridge client: `internal/runtime/executor/cursor_composer_sdk_bridge.go`. Shared defaults/helpers: `internal/cursorcomposer/`.

### How it works

- **API keys** (`crsr_…` from Cursor Dashboard → Integrations) are the primary auth mode.
- When `sdk-bridge-url` is set (recommended for `crsr_` keys), CLIProxy forwards chat to a local **Cursor SDK bridge** (`POST {sdk-bridge-url}/v1/chat/completions`) instead of connect-proto alone.
- When `sdk-bridge-url` is empty, the executor uses Cursor connect-proto (`backend-base-url` + `chat-endpoint`) after exchanging the API key for a session token.
- Model registration runs in `sdk/cliproxy/service.go` (`registerModelsForAuth`, `defaultCursorComposerModels`, `mergeCursorComposerDefaultModels`). Always merge defaults so upgrades/reloads do not leave stale in-memory model sets (symptom: `composer-2.5` works but `composer-2.5-fast` returns `auth_unavailable: no auth available` until process restart).

### Default models

| Client model id       | Notes |
|-----------------------|--------|
| `composer-2.5`        | Default alias |
| `composer-2.5-fast`   | Fast tier; must stay in default merge set |
| `composer-latest`     | Alias |

### Config (`config.yaml`)

```yaml
cursor-composer-api-key:
  - api-key: "crsr_…"
    sdk-bridge-url: "http://127.0.0.1:8792/sdk"   # required for SDK path; bridge listens on VPS localhost
    # optional: base-url, backend-base-url, chat-endpoint, client-version, proxy-url, models, excluded-models
```

- Template and comments: `config.example.yaml` (Cursor Composer section).
- Synthesizer copies `sdk_bridge_url` into auth attributes (`internal/watcher/synthesizer/config.go`); default `http://127.0.0.1:8792/sdk` when unset.
- Env overrides: `CURSOR_BACKEND_BASE_URL`, `CURSOR_CHAT_ENDPOINT`, `CURSOR_CLIENT_VERSION`.

### SDK bridge (VPS)

- Run **on the same host as CLIProxy** (bridge URL is loopback).
- Typical layout: `/opt/cursor-sdk-bridge/`, systemd unit `cursor-sdk-bridge.service`.
- ExecStart must point at the bridge script (e.g. `cursor-sdk-local-agent-bridge.mjs`), not a non-existent `scripts/…` path.
- **Model mapping:** Cursor SDK does not accept `composer-2.5-fast` as a native id. The bridge should map both `composer-2.5` and `composer-2.5-fast` to SDK selection `{ id: "default" }`. Sending `{ id: "composer-2.5-fast" }` to the SDK causes **502**.
- After deploying a new CLIProxy binary, **`systemctl restart cliproxyapi`** (and ensure `cursor-sdk-bridge` is active) so auth/model registration is fresh.

### Management API

Routes in `internal/api/server.go`, handlers in `internal/api/handlers/management/config_lists.go`:

- `GET/PUT/PATCH/DELETE /v0/management/cursor-composer-api-key`
- Alias: `/v0/management/cursor-api-key` (same payload; key name `cursor-api-key` on GET alias)

Official [Management Center](https://github.com/router-for-me/Cli-Proxy-API-Management-Center) may not list Cursor in the UI yet; use YAML or these API routes.

### Reference deployment (VPS + OpenCode on Mac)

**Architecture:** CLIProxy + SDK bridge on **VPS** (Tailscale IP e.g. `100.x.x.x:8317`). **OpenCode on Mac** uses an OpenAI-compatible provider pointing at the VPS, not a local Mac proxy.

| Component | Role |
|-----------|------|
| `cliproxyapi.service` | Listens `*:8317`, serves `/v1/*` |
| `cursor-sdk-bridge.service` | `127.0.0.1:8792` on VPS only |
| OpenCode `proxy` provider | `baseURL: http://<vps-tailscale-ip>:8317/v1`, proxy API key from VPS `config.yaml` |

**Do not** keep a separate OpenCode provider aimed at `http://127.0.0.1:8788` or `127.0.0.1:8317` on Mac after moving to VPS — that was the old local CLIProxy; stopped LaunchAgents free those ports. Use model ids like `proxy/composer-2.5` and `proxy/composer-2.5-fast` only.

OpenCode optional model icon: `file:///…/CLIProxyAPI/assets/cursor.webp`.

### Build & deploy (Linux VPS)

```bash
gofmt -w .
GOOS=linux GOARCH=amd64 go build -o CLIProxyAPI ./cmd/server
# install binary, restart cliproxyapi + cursor-sdk-bridge
```

Backup config before changes (e.g. under `/root/backup/cliproxy-deploy-<timestamp>/`).

### Troubleshooting

| Symptom | Likely cause | Action |
|---------|----------------|--------|
| `auth_unavailable` for `composer-2.5-fast` only | Stale in-memory models after upgrade | Restart `cliproxyapi`; ensure `mergeCursorComposerDefaultModels` is deployed |
| OpenCode `ConnectionRefused` → `127.0.0.1:8788` | Old `cursorapi` / local provider in OpenCode config | Remove local providers; point only at VPS `proxy` |
| `read ECONNRESET` / `[aborted]` in OpenCode | TCP closed mid-stream (VPN blip, long run, bridge/proxy restart, cancel) | Retry; check `systemctl status cliproxyapi cursor-sdk-bridge`; Tailscale |
| SDK bridge **502** | Wrong model id sent to Cursor SDK | Map fast/standard to `default` in bridge |
| `gpt-5.5` fails on same VPS | Codex OAuth pool / usage limits | Separate from Cursor; fix or disable bad Codex auths in `auths/` |

Logs: OpenCode `~/.local/share/opencode/log/`; VPS `journalctl -u cliproxyapi -u cursor-sdk-bridge`.

Never commit real `crsr_` keys or proxy API keys; use `config.yaml` on the server only.
