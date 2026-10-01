# AGENTS.md

Go 1.26+ proxy server providing OpenAI/Gemini/Claude/Codex compatible APIs with OAuth and round-robin load balancing.

## Repository
- GitHub: https://github.com/router-for-me/CLIProxyAPI

## Commands
```bash
gofmt -w . # Format (required after Go changes)
go build -o nixllm ./cmd/server # Build
go run ./cmd/server # Run dev server
go test ./... # Run all tests
go test -v -run TestName ./path/to/pkg # Run single test
go build -o test-output ./cmd/server && rm test-output # Verify compile (REQUIRED after changes)
# PG-first import-config (requires PGSTORE_DSN):
nixllm -import-config config.yaml # One-shot YAML → PostgreSQL control-plane import
nixllm -import-config config.yaml -import-config-dry-run # Plan and validate only; no DB writes
# NixLLM dashboard (React + Vite SPA under web/dashboard/):
cd web/dashboard && npm install && npm run dev  # Dev SPA on :9173 (proxies /v0 to the Go API)
cd web/dashboard && npm run build                # Build dist/ (embedded via internal/dashboardasset)
make dash-embed                                  # Build SPA + rebuild Go binary so /dashboard serves it
```
- Common flags: `--config <path>`, `--tui`, `--standalone`, `--local-model`, `--no-browser`, `--oauth-callback-port <port>`
- Dashboard env vars: `MANAGEMENT_PASSWORD` (or `NIXLLM_DASHBOARD_PASSWORD` alias) gates `/v0/management` and the dashboard login screen; `PGSTORE_DSN` is required for the PG-backed routes the dashboard surfaces. `PGSTORE_ENCRYPTION_KEY` (optional) AES-GCM-seals the `api_key_principal` column of `usage_events` at rest; unset = plaintext (legacy rows remain readable).

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
- `internal/configsnapshot/` — PG-first control plane planner. `BuildResourcePlan` converts all supported config sections into a `NormalizedResourcePlan` (pure, no DB, no mutation of the caller's config); `MarshalYAML`/`UnmarshalYAML` give deterministic snapshot projection; `Checksum` gives the canonical SHA-256 identity. See `docs/plans/2026-09-14-nixllm-pg-first-design.md`.
- `internal/configvalidation/` — Shared validation pipeline; `Validate(snapshot)` round-trips a snapshot through `config.ParseConfigBytes` so dashboard saves and CLI imports share the same defaults and sanitization.
- `internal/configstore/` — `Repository` interface + `RevisionConflictError`; the PG-backed implementation (`configstore/pg`) gates every write on expected revision inside one tx and appends a `config_revisions` row.
- `internal/store/runtimeconfig/` — Transaction-aware SQL for `LoadRuntimeConfig`, `RollbackRuntimeConfig`, `ListRevisions`, `ListImports`, `ActiveRevision` over the `runtime_config` / `config_revisions` / `config_imports` tables. Depends on both `store` and `configsnapshot`, so those two must never depend on each other.
- `internal/store/pg_normalized_import.go` — `ApplyNormalizedResourcePlan` applies the planner output in ONE transaction: provider parent upsert + child collections (models/headers/excluded/entries with stable child identity matching), then client API keys. Any error rolls back everything.
- `internal/api/handlers/management/runtime_config.go` — Management routes `/v0/management/runtime-config` (GET/POST with expected_revision), `/runtime-config/rollback`, `/config-revisions`, `/config-imports`. Return 503 without `PGSTORE_DSN`.
- `internal/policy/` — Per-API-key + per-Internal-User policy enforcement (RPM, TPM, hourly rate, max_parallel_requests, budget caps, model access). Per-user caps act as fallback when per-key caps are unset, mirroring the LiteLLM user→key hierarchy.
- `internal/api/handlers/management/internal_users.go` — LiteLLM `/user/*` equivalence class under `/v0/management/internal-users/*` (path kept for backward compatibility; see the file-level comment for the route-by-route mapping). Implements auto-create-key on user creation, per-user TPM/parallel caps, per-model spend via on-the-fly SELECT, and spend reconciliation.
- `internal/api/handlers/management/alerts.go` + `alerts_runner.go` — Alert/notification feed (Analysis → Alerts) and the background detection sweep (max-spend for internal users + API keys via `usage_windows`/`policy.WindowFor`, error-rate from `usage_errors`, provider cooldowns via `authManager.CooldownStateSnapshot()`). Deduplicated by fingerprint + suppression window; settings live in `alert_settings`.
- `internal/api/handlers/management/proxy_pools.go` + `proxy_pools_relaydeploy.go` — Named egress-proxy pools (PG `proxy_pools` table + `/v0/management/proxy-pools*` routes; 503 without `PGSTORE_DSN`). Pool test is status-only (never flips `is_active`); batch import parses server-side; relay-deploy deploys a one-shot relay worker to Vercel/Cloudflare/Deno. Binding lives on `upstream_providers(.proxy_pool_id)` rows/entries and is resolved at render time in `internal/upstreamsync` into a composite `proxy_url` (`?no_proxy=…&strict=…`, parsed by `sdk/proxyutil`) or `Auth.RelayBaseURL` (x-relay-target/x-relay-path header contract, transport built in `internal/runtime/executor/helps`). Executors/conductor are untouched.
- `internal/dashboardasset/` — Embeds the NixLLM dashboard SPA (served at `/dashboard`)
- `internal/managementasset/` — Config snapshots and management assets
- `internal/cache/` — Request signature caching
- `internal/watcher/` — Config hot-reload and watchers
- `internal/wsrelay/` — WebSocket relay sessions
- `internal/usage/` — Usage and token accounting
- `internal/tui/` — Bubbletea terminal UI (`--tui`, `--standalone`)
- `sdk/cliproxy/` — Embeddable SDK entry (service/builder/watchers/pipeline)
- `web/dashboard/` — NixLLM dashboard SPA (React + Vite, served at `/dashboard`)
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
- Timeouts are allowed only during credential acquisition; after an upstream connection is established, do not set timeouts for any subsequent network behavior. Intentional exceptions that must remain allowed are the Codex websocket liveness deadlines in `internal/runtime/executor/codex_websockets_executor.go`, the wsrelay session deadlines in `internal/wsrelay/session.go`, the management APICall timeout in `internal/api/handlers/management/api_tools.go`, the opencode-go quota fetch and models-refresh timeouts in `internal/api/handlers/management/upstream_providers_quota.go` and `upstream_providers_refreshmodels.go`, the auto-router vision bridge deadline in `sdk/api/handlers/vision_bridge.go`, the Jev AI classifier gate deadline in `internal/autorouter/jevgate/gate.go` (both run before any upstream model connection exists), and the `cmd/fetch_antigravity_models` utility timeouts

## Release tags

Version tags use the pattern `v<base>-<fork>.<patch>`, e.g. `v7.2.138-0.1.39`. The prefix `v7.2.138-0.1.` is fixed; only the final patch number increments. When asked to push with the latest patch version tag, find the highest existing tag matching `v7.2.138-0.1.*`, bump its final segment by one (e.g. `v7.2.138-0.1.39` -> `v7.2.138-0.1.40`), and create an **annotated** tag on `HEAD` whose message is `<tag> Release <tag>` (matching the existing tags). Do not use plain semver tags like `v7.3.9` for releases.
