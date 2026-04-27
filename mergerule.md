# Merge Rule for Upstream Updates (CLIProxyAPI)

This project keeps `main` as the custom fork baseline:
- base: upstream `router-for-me/CLIProxyAPI` (`origin/main`)
  - upstream URL: `https://github.com/router-for-me/CLIProxyAPI.git`
- plus local required changes (currently Vertex prompt cache support + local run guidance)

Use this file as the merge checklist whenever Codex updates from upstream.

## 1) Branch policy
- Always operate on `main`.
- `origin`: `https://github.com/router-for-me/CLIProxyAPI.git` (upstream only)
- `runhua`: `https://github.com/RunhuaHuang/CLIProxyAPI.git` (fork; push local updates here)
- Do not make `origin/main` include local-only commits.

## 2) Merge flow
Run in order:

```bash
cd '/Users/runhua/Runhua MBP/Python/CLIProxyAPI/CLIProxyAPI'
git switch main
git fetch --all --prune
git rebase origin/main
```

When conflicts happen:
- resolve normally
- keep custom Vertex-related files listed below unless upstream already has equivalent behavior

After rebase succeeds, update fork main:

```bash
git push runhua main:main --force-with-lease
```

## 3) Local-keep list (must be preserved after conflicts)
- `internal/runtime/executor/gemini_vertex_executor.go`
- `internal/runtime/executor/gemini_vertex_cache_test.go`
- `internal/runtime/executor/helps/usage_helpers.go`
- `internal/runtime/executor/helps/usage_helpers_test.go`
- `internal/translator/gemini/claude/gemini_claude_response.go`
- `internal/translator/gemini/claude/gemini_claude_response_test.go`
- `sdk/api/handlers/gemini/gemini_handlers.go`
- `internal/api/server.go`
- `internal/api/server_test.go`
- `internal/api/modules/amp/routes.go`
- `internal/api/modules/amp/routes_test.go`
- `cmd/server/main.go`
- `internal/cmd/run.go`
- `README.md`
- `README_CN.md`

If upstream ever lands the same behavior, remove duplicates cleanly and keep only one source of truth.

## 4) Quality gate before finishing
```bash
gofmt -w cmd/server/main.go internal/cmd/run.go internal/runtime/executor/gemini_vertex_executor.go \
  internal/runtime/executor/helps/usage_helpers.go internal/translator/gemini/claude/gemini_claude_response.go \
  sdk/api/handlers/gemini/gemini_handlers.go
go test ./internal/translator/gemini/claude ./internal/runtime/executor/helps ./internal/runtime/executor ./internal/api ./internal/api/modules/amp ./sdk/api/handlers/gemini
go build -o cliproxy ./cmd/server
```

## 5) Restart and verify
- Restart using your normal launch command.
- Confirm runtime version with `./cliproxy -version`.
- Keep this branch as the one that runs in local service:
  - expected shape: `vX.Y.Z-<n>-g<commit>` where `<commit>` is local `HEAD` and includes your custom commits.
- For quick cache sanity:
  - send repeated Vertex-backed Agent requests and check `cache_read_input_tokens` usage stability.
