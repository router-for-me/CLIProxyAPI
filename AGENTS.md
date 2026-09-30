# AGENTS.md

Go 1.26+ proxy server providing OpenAI/Gemini/Claude/Codex compatible APIs with OAuth and round-robin load balancing.

## Repository
- Upstream: https://github.com/router-for-me/CLIProxyAPI
- Our fork (source of truth): https://github.com/hrygo/CLIProxyAPI

## Maintenance Model
This repository is a self-maintained fork. `main` is the trunk; `codex/*` are task
branches that must merge back into `main`. Do not treat upstream as a merge target
and do not wait for upstream review before shipping our own work.

- **Remotes:** `origin` is our fork (`hrygo/CLIProxyAPI`), `upstream` is
  `router-for-me/CLIProxyAPI`. Our first release is `v1.0.0`.
- **Upstream intake is release-only.** Track `upstream` releases, not its `dev` or
  `main` branches. Fetch upstream tags into the local-only `refs/upstream/tags/*`
  namespace and never push them to `origin`: `release.yaml` triggers a full
  multi-platform build on any `v[0-9]+.[0-9]+.[0-9]+` tag push, so pushing upstream
  tags here would publish spurious releases from our fork.
- **Our releases carry our own `vX.Y.Z` tags**, decoupled from upstream numbering.
  A release tag on this fork builds a GitHub Release through the inherited
  `release.yaml`, which is fork-agnostic (it publishes to `${{ github.repository }}`).
- **Module path stays upstream.** `go.mod` keeps
  `module github.com/router-for-me/CLIProxyAPI` so upstream releases can be ported
  without rewriting imports across the tree. Do not rename it.
- **Installation consumes our release artifacts** through the Homebrew tap
  `hrygo/homebrew-cliproxyapi`, tapped as `hrygo/cliproxyapi`
  (`brew install hrygo/cliproxyapi/cli-proxy-api`), which
  pins the release tarball's `sha256`. Do not build a binary by hand and `mv` it
  over a Homebrew-managed path; that leaves a stale regular file outside Cellar
  management and defeats `brew upgrade` / `brew rollback`.
- See `docs/plans/2026-10-01-fork-maintenance-runbook.zh-CN.md` for the full
  cutover, upstream-intake, and rollback procedure.

### CI

Only `release.yaml` and `pr-test-build.yml` are kept. `pr-test-build.yml` runs on
pull requests and pushes to `main`, and gates on `gofmt`, `go vet ./...`,
`go test ./... -count=1`, and a server build.

Upstream-only workflows were removed and must not be reintroduced:
`docker-image.yml` (pushed to a third-party Docker Hub org),
`auto-retarget-main-pr-to-dev.yml`, `agents-md-guard.yml` (auto-closed PRs touching
`AGENTS.md`), and `pr-path-guard.yml` (blocked `internal/translator/` changes, a
restriction that no longer applies now that we own this fork).

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
- `internal/home/` — CLIProxyAPIHome control plane integration (bootstrap, RESP communication, dispatch coordination)
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
- Because this is our own fork, a standalone `internal/translator/` change is allowed when the task genuinely requires it; no upstream permission check or upstream issue is needed. Note the deviation in the commit message when the change is translator-only.
- `internal/runtime/executor/` should contain executors and their unit tests only. Place any helper/supporting files under `internal/runtime/executor/helps/`.
- Follow `gofmt`; keep imports goimports-style; wrap errors with context where helpful
- Do not use `log.Fatal`/`log.Fatalf` (terminates the process); prefer returning errors and logging via logrus
- Shadowed variables: use method suffix (`errStart := server.Start()`)
- Wrap defer errors: `defer func() { if err := f.Close(); err != nil { log.Errorf(...) } }()`
- Use logrus structured logging; avoid leaking secrets/tokens in logs
- Avoid panics in HTTP handlers; prefer logged errors and meaningful HTTP status codes
- Timeouts are allowed only during credential acquisition; after an upstream connection is established, do not set timeouts for any subsequent network behavior. Intentional exceptions that must remain allowed are the Codex websocket liveness deadlines in `internal/runtime/executor/codex_websockets_executor.go`, the wsrelay session deadlines in `internal/wsrelay/session.go`, the management APICall timeout in `internal/api/handlers/management/api_tools.go`, and the `cmd/fetch_antigravity_models` utility timeouts
- Avoid wall-clock `time.Sleep` in TTL, expiration, ordering, or cache-eviction unit tests due to platform timer granularity (e.g. Windows default timer resolution of ~15.6ms) and CI jitter under load; prefer controllable clocks (`nowFunc` / mock clock), explicit timestamp manipulation, or deterministic synchronization primitives.
- Note: if modifying features that involve CLIProxyAPIHome, check if corresponding updates are needed in the CLIProxyAPIHome repository.
- Endpoints under the `/v0/management` base URL are deprecated and no longer maintained. For any feature changes, do not modify endpoints under `/v0/management` unless necessary to fix compilation errors.
