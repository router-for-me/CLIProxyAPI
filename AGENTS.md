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

## Lessons Learned

### OAuth Tool Name Remapping Must Track Whether Renaming Occurred
- **Symptom**: Responses returned corrupted tool names when the upstream client already sent TitleCase names (e.g. `Bash` not `bash`).
- **Root cause**: `remapOAuthToolNames` always ran `reverseRemapOAuthToolNames` on the response, even when no names were actually changed. If the request already used TitleCase names matching the rename targets, the reverse pass would incorrectly rewrite them to lowercase.
- **Fix**: `remapOAuthToolNames` returns `([]byte, bool)` — the bool tracks whether any rename actually occurred. Only call `reverseRemapOAuthToolNames` on responses when `renamed == true`.
- **Pattern**: When a function transforms data and a later stage needs to undo it, always track whether the transform was a no-op. Never assume the reverse path is safe to run unconditionally.
- **See**: `internal/runtime/executor/claude_executor.go` — `remapOAuthToolNames()`, `oauthToolNamesRemapped` flag.

### 429 Retry With Token Refresh Pattern (Qwen)
- **Symptom**: Qwen returns 429 (quota_exceeded) even when the user has available quota under a different session.
- **Root cause**: Qwen's rate limits are per-access-token; refreshing the token can yield a fresh quota window.
- **Fix**: Wrap the HTTP call in a `for` loop with a one-shot retry. On 429, call `Refresh()` to get a new token, update `auth`, and `continue`. Use a `qwenImmediateRetryAttempted` bool to prevent infinite loops. Apply to both `Execute` and `ExecuteStream`.
- **Pattern**: For providers where tokens are session-scoped and rate limits are per-token, a single retry with token refresh is a valid recovery strategy. Guard with a `bool` flag — never retry more than once.
- **See**: `internal/runtime/executor/qwen_executor.go` — `qwenShouldAttemptImmediateRefreshRetry()`, `refreshForImmediateRetry` field for test injection.

### Antigravity URL Fallback Order: Prefer Production
- **Symptom**: Antigravity requests were hitting daily/sandbox URLs first, causing unnecessary latency or failures.
- **Fix**: Move `antigravityBaseURLProd` to the first position in the fallback slice.
- **Pattern**: When defining URL fallback orders, always place the production endpoint first unless there is an explicit reason to prefer a non-production endpoint. Comment any intentional deviation.
