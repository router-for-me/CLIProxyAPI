# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

Go 1.26+ proxy server providing OpenAI/Gemini/Claude/Codex compatible APIs with OAuth and round-robin load balancing.

## Repository
- GitHub: https://github.com/router-for-me/CLIProxyAPI
- Documentation: https://help.router-for.me/

## Essential Commands
```bash
# Required after any Go code changes
gofmt -w .

# Build
go build -o cli-proxy-api ./cmd/server

# Development
go run ./cmd/server

# Testing
go test ./...                              # All tests
go test -v -run TestName ./path/to/pkg    # Single test

# Verify compilation (REQUIRED after changes)
go build -o test-output ./cmd/server && rm test-output
```

Common runtime flags: `--config <path>`, `--tui`, `--standalone`, `--local-model`, `--no-browser`, `--oauth-callback-port <port>`

## Configuration
- Default config: `config.yaml` (template: `config.example.yaml`)
- `.env` auto-loaded from working directory
- Auth material defaults to `auths/` directory
- Storage backends: file-based (default) or Postgres/git/object store via `PGSTORE_*`, `GITSTORE_*`, `OBJECTSTORE_*` env vars

## Architecture Overview

### Core Request Flow
1. HTTP request arrives at `internal/api/` (Gin router)
2. `internal/thinking/` processes thinking/reasoning configuration (suffix parsing → canonical config → provider-specific output)
3. `internal/translator/` converts request to provider protocol
4. `internal/runtime/executor/` executes against provider (HTTP or WebSocket)
5. Response flows back through translator to client

### Key Directories
- `cmd/server/` — Server entrypoint
- `internal/api/` — HTTP API (routes, middleware, modules)
  - `internal/api/modules/amp/` — Amp integration with reverse proxy
- `internal/thinking/` — **Critical:** Thinking/reasoning pipeline
  - `apply.go`: Main `ApplyThinking()` entry point
  - `suffix.go`: Parses model suffix overrides (suffix overrides body config)
  - `types.go`: Canonical `ThinkingConfig` representation
  - `validate.go` / `convert.go`: Central normalization and validation
  - Provider-specific output via `ProviderApplier` interface
  - **Do not break the "canonical representation → per-provider translation" architecture**
- `internal/runtime/executor/` — Provider executors (HTTP, WebSocket streams)
- `internal/translator/` — Protocol translators + shared `common` utilities
- `internal/registry/` — Model registry with remote updater (`--local-model` disables updates)
- `internal/store/` — Storage backends and secret resolution
- `internal/cache/` — Request signature caching
- `internal/watcher/` — Config hot-reload
- `internal/wsrelay/` — WebSocket relay sessions
- `internal/usage/` — Token accounting
- `internal/tui/` — Bubbletea terminal UI
- `sdk/cliproxy/` — Embeddable SDK (service/builder/watchers/pipeline)
- `test/` — Cross-module integration tests

## Code Conventions

### General Rules
- Keep changes small and simple (KISS principle)
- Comments in English only
  - Translate existing non-English comments to English when editing that code
  - User-visible strings: preserve existing language in that file/area
- New Markdown docs in English (except language-specific files like `README_CN.md`)
- Follow `gofmt` formatting and `goimports`-style imports

### Go-Specific
- **Never use `log.Fatal` / `log.Fatalf`** (terminates process) — return errors and log via logrus
- Avoid panics in HTTP handlers — use logged errors with meaningful HTTP status codes
- Shadowed variables: suffix with context (`errStart := server.Start()`)
- Wrap defer errors: `defer func() { if err := f.Close(); err != nil { log.Errorf(...) } }()`
- Use logrus structured logging
- Never leak secrets/tokens in logs

### Module-Specific Rules
- **`internal/translator/`**: Do not make standalone changes to this directory
  - Only modify as part of broader changes elsewhere
  - If task requires changing ONLY `internal/translator/`: verify write access with `gh repo view --json viewerPermission -q .viewerPermission` (need `WRITE`, `MAINTAIN`, or `ADMIN`), otherwise file a GitHub issue with goal/rationale/code and stop
- **`internal/runtime/executor/`**: Executors and unit tests only
  - Place helper/supporting files under `internal/runtime/executor/helps/`

### Timeout Policy
- Timeouts allowed **only during credential acquisition**
- After upstream connection established: **no timeouts on subsequent network behavior**
- Intentional exceptions (do not modify):
  - Codex websocket liveness deadlines in `internal/runtime/executor/codex_websockets_executor.go`
  - wsrelay session deadlines in `internal/wsrelay/session.go`
  - Management APICall timeout in `internal/api/handlers/management/api_tools.go`
  - `cmd/fetch_antigravity_models` utility timeouts

## SDK Documentation
- Usage: `docs/sdk-usage.md`
- Advanced (executors & translators): `docs/sdk-advanced.md`
- Access control: `docs/sdk-access.md`
- Watcher API: `docs/sdk-watcher.md`
- Custom provider example: `examples/custom-provider/`
