# CLIProxyAPI (Cujuju private fork)

See AGENTS.md for upstream conventions. Desktop-facing API: docs/desktop-api.md.

## Tracker
- GitHub Issues: `Cujuju/CLIProxyAPI`
- Labels: `area/routing`, `area/server`, `area/desktop`, `kind/feature`; arc `arc/account-rotation`
- `upstream` remote push is disabled; never push to router-for-me.

## Fork checks
- The desktop app ships Windows and macOS builds. Cross-compile before committing: `CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build ./cmd/server` (and `GOOS=linux`).
- Go treats a `_<GOOS>.go` / `_<GOARCH>.go` file-name suffix as a build constraint (e.g. `quota_windows.go` builds only on Windows). Name files so they don't end in one unless that's intended.

## Upstream sync
- `scripts/sync-upstream.sh [--dry-run]`, from any checkout or worktree: merges `upstream/main` into a new `sync/upstream-<YYYYMMDD>` branch off `origin/main` in its own worktree, builds, cross-compiles the desktop targets, runs `go test ./...`, pushes the branch to `origin` and opens a PR. Merge that PR with "Create a merge commit", never squash or rebase, so upstream's history stays and the next sync merges cleanly. It never pushes to upstream. On conflicts, resolve and commit in the printed worktree, then rerun the script there.
- Known upstream failures on Windows (they fail at upstream/main too, so not merge regressions): `TestResponsesInterruptStopsHTTPUpstream` and `TestCodexClientModelsResponseMultiAgentV2FollowsConfig` (sdk/api/handlers/openai), `TestToolSearchDisabledMatchesDevGolden` (internal/translator/claude/openai/responses), `TestAdvertiserAndBrowser_Integration` (internal/discovery, needs mDNS), `TestModelsMultiAgentClientConfigAndReload` (internal/api), three `TestCodexClientModelsResponse*` (internal/client/codex/models), `TestCodexConfigurationUpdateCapability` (internal/registry), `TestDevinApplyPatchRejectionCarriesLogDiagnostic` (internal/runtime/executor).
- Fork-only changes worth re-checking after a merge: `sdk/cliproxy/auth` (soonest-reset, quota exhaustion, first-byte timeout, preferred auth, usage-lookup snapshot `QuotaState.LookupSignals`) and `internal/api/handlers/management/desktop_*.go`.
