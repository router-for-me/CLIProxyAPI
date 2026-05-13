# Runhua Merge Playbook

This fork keeps a small local maintenance layer on top of upstream
`router-for-me/CLIProxyAPI`. Use this playbook whenever updating
`RunhuaHuang/CLIProxyAPI:main` from upstream.

## Remotes

- `origin`: upstream, `https://github.com/router-for-me/CLIProxyAPI.git`
- `runhua`: local fork, `https://github.com/RunhuaHuang/CLIProxyAPI.git`

Do not push local-only maintenance commits to `origin`. Push them to `runhua`.

## Preserved Local Changes

Keep these changes unless they have been accepted upstream with equivalent
behavior:

- Vertex prompt caching for Claude-compatible Agent requests:
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
- Local source-checkout update and restart guidance:
  - `README.md`
  - `README_CN.md`
- Local runtime ergonomics:
  - `cmd/server/main.go` falls back to `config.yaml` next to the binary when
    the current working directory has no config.
  - `internal/cmd/run.go` opens the management dashboard after service start.

Do not commit generated binaries:

- `cliproxy`
- `cliproxyapi`

## Update Workflow

Start from the fork main branch:

```bash
git switch main
git fetch origin main
git fetch runhua main
git reset --hard runhua/main
```

Merge upstream:

```bash
git merge origin/main
```

Resolve conflicts by preserving the local changes listed above unless upstream
has the same behavior. If upstream has accepted the Vertex cache PR, prefer the
upstream implementation and remove duplicate local code.

After resolving conflicts:

```bash
gofmt -w \
  cmd/server/main.go \
  internal/cmd/run.go \
  internal/runtime/executor/gemini_vertex_executor.go \
  internal/runtime/executor/helps/usage_helpers.go \
  internal/translator/gemini/claude/gemini_claude_response.go \
  sdk/api/handlers/gemini/gemini_handlers.go

go test ./internal/translator/gemini/claude ./internal/runtime/executor/helps ./internal/runtime/executor ./internal/api ./internal/api/modules/amp ./sdk/api/handlers/gemini
```

Build and restart the local service when testing the live local instance:

```bash
go build -ldflags "-X main.Version=$(git describe --tags --always) -X main.Commit=$(git rev-parse --short HEAD) -X main.BuildDate=$(date -u +%Y-%m-%dT%H:%M:%SZ)" -o cliproxy ./cmd/server
launchctl kickstart -k gui/$(id -u)/com.runhua.cliproxyapi.local
curl -sS http://127.0.0.1:8317/healthz
```

Push the updated fork main:

```bash
git push runhua main
```

## Vertex Cache Smoke Test

For cache-sensitive merges, verify the behavior with a repeated Agent request:

1. Query `/v1beta/cachedContents` before the test.
2. Send the same `/v1/messages?beta=true` request at least three times through
   the local proxy using a Vertex-backed Gemini model.
3. Query `/v1beta/cachedContents` again.
4. Confirm only one new Vertex cache is created for the stable prompt and the
   Agent SSE usage contains `cache_read_input_tokens`.

Expected shape:

```text
round1 usage: input_tokens=<small>, cache_read_input_tokens=<large>
round2 usage: input_tokens=<small>, cache_read_input_tokens=<same large>
round3 usage: input_tokens=<small>, cache_read_input_tokens=<same large>
```

If `cache_read_input_tokens` disappears, inspect:

- Vertex stream usage parsing in `ParseVertexGeminiStreamUsage`
- Vertex translator context marker in `GeminiVertexExecutor`
- Claude usage mapping in `gemini_claude_response.go`
