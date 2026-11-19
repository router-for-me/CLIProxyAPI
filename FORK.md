# FORK.md — ArcticSlayy/CLIProxyAPI

This is a fork of [router-for-me/CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) (MIT).
Two reasons it exists:

1. **Stay current with upstream automatically.** `.github/workflows/sync-upstream.yml` merges
   `upstream/main` into this fork's `main` on GitHub's runners every day at 09:23 UTC, and only
   when vet + build + tests pass. Nothing runs on a local machine for this.
2. **Build a better management UI.** Upstream ships the Go core plus a management API
   (`docs/management-api-v8.md`) and no web frontend of its own. Our UI lives in new,
   fork-owned directories and talks to that API.

## Rules

- **Added files only.** Our work goes in new files (`ui/`, `FORK.md`, `docs/agents/STATE.md`).
  Upstream files stay upstream-shaped so the daily merge is a fast-forward in practice. If an
  upstream file must change, keep the diff small and list it in `docs/agents/STATE.md` under
  "Touched upstream files".
- **The fork owns `.github/workflows/`.** The sync restores our copy of that directory after
  every merge and ignores upstream's edits there, because `GITHUB_TOKEN` cannot push workflow
  changes. Consequence: upstream workflow improvements do not arrive on their own — port one by
  hand if it matters, then delete ours if it is identical.
- **Never merge with red checks.** A green sync run is the only thing allowed to move `main`.
- `AGENTS.md`, `CLAUDE.md` and `internal/translator/**` are upstream-owned. Upstream's guards
  for them were removed here (they would close our own PRs), but the rule they enforced still
  applies: do not put our notes in upstream's agent files.

## Build

```sh
go mod download
bash .github/scripts/refresh-model-catalogs.sh   # regenerates model catalogs; run before build
go build -o cli-proxy-api ./cmd/server
go run ./cmd/server --config config.yaml
go vet ./... && go test -timeout 20m ./...
```

Go version comes from `go.mod`. The release build command upstream uses, for reference:

```sh
CGO_ENABLED=1 go build -ldflags="-s -w -X main.Version=<v> -X main.Commit=<sha> -X main.BuildDate=<iso>" \
  -o cli-proxy-api ./cmd/server/
```

## Local checkout

`G:\Tools\CLIProxyAPI\src` (this repo). The running proxy on this machine is still the
upstream v8.0.78 binary at `G:\Tools\CLIProxyAPI\core\cli-proxy-api.exe`; it is replaced only
when a fork build has passed `update-core.ps1`-style verification, never straight from a
working tree.

## Upstream

- Repo: https://github.com/router-for-me/CLIProxyAPI (main branch; upstream also carries `dev`)
- License: MIT — fork and redistribution are fine, keep `LICENSE` intact.
- Sync workflow: `.github/workflows/sync-upstream.yml` (daily 09:23 UTC, `workflow_dispatch`
  with an optional branch input for a scratch test run).
