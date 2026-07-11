# Hybrid Claude Code / Codex fork

This repository is a narrow fork of
[`router-for-me/CLIProxyAPI`](https://github.com/router-for-me/CLIProxyAPI).
It keeps upstream as the canonical base and adds two isolated capabilities:

1. Codex-native Responses compaction for Claude Code requests routed to Codex.
2. Provider-neutral request, cache, cost, and compaction observability in the
   server log and TUI.

The latest tagged release in the current fork base is upstream `v7.2.67` at
`2075f77c8ebe9ec872759965661936fb1ac2931f`. The feature commits are rebased
through upstream `main` at `9418054a3b2184cc6fa618f1bbef51ffca17c32d` on
`feature/hybrid-compaction-observability`.

## Remotes and upstream pulls

The official repository must remain named `upstream`. Add a personal GitHub
fork as `origin` only after it exists:

```powershell
git remote -v
git remote add origin https://github.com/<account>/CLIProxyAPI.git
git push -u origin feature/hybrid-compaction-observability
```

Refresh and rebase onto a new upstream release with:

```powershell
git fetch upstream --tags
git rebase upstream/main
go test ./...
go build -o test-output ./cmd/server
```

Keep fork-specific work in focused commits. Do not merge generated binaries,
OAuth files, local configuration, logs, or secrets into Git.

## Fork boundaries

- Native compaction is opt-in under `codex.native-compaction`; all upstream
  defaults remain unchanged when it is disabled.
- The lane is entered only by Claude-format requests that have a Claude Code
  session ID and are already executing through the Codex executor. Native
  Claude/Fable requests remain in the Claude executor.
- Codex models in the Anthropic picker advertise a configurable virtual client
  context window while native Claude/Fable models retain their real metadata.
  This delays Claude Code compaction so native Responses compaction can own the
  Codex lane; the proxy's real trigger and hard boundary are unchanged.
- Compaction state is memory-bounded and restart-safe. It is isolated by Claude
  Code session, exact requested model (including effort suffix), and Codex auth
  ID, then persisted atomically with a version and integrity checksum beneath
  the configured auth directory.
- A stable deterministic `prompt_cache_key` replaces the upstream one-hour
  random cache-key rotation for Claude Code sessions.
- Request logs contain metadata and token counts only. They never include
  prompts, tool bodies, headers, API keys, or OAuth data.
- The TUI cost is an estimate based on the built-in catalog, not an invoice or
  subscription charge.

See [docs/windows-claude-code-hybrid.md](docs/windows-claude-code-hybrid.md)
for the machine setup and operating model.
