# Fork state

Last updated: 2026-11-19

## Verified

- Fork created from `router-for-me/CLIProxyAPI` at `main`, `v8.0.78` (upstream release of 2026-11-19).
- Issues enabled, Actions enabled, "allow Actions to create and approve PRs" on.
- `.github/workflows/sync-upstream.yml` installed (daily 09:23 UTC merge of `upstream/main`,
  green-only push to `main`, conflict issue / red PR fallback).
- `.github/workflows/fork-ci.yml` installed (build + `go test ./...` on PR and on push to `main`;
  `go vet` runs as an advisory step only).
- Upstream PR-policy and release workflows removed from the fork:
  `agents-md-guard`, `pr-path-guard`, `auto-retarget-main-pr-to-dev`, `release`, `docker-image`.

## Touched upstream files

None. Our additions: `FORK.md`, `docs/agents/STATE.md`, `.github/workflows/fork-ci.yml`,
`.github/workflows/sync-upstream.yml`.

## Not verified yet

- First green run of `fork-ci` on GitHub (no local Go toolchain on this machine; CI is the only
  build evidence). Confirm with `gh run list -R ArcticSlayy/CLIProxyAPI`.
- First `sync-upstream` run (expected "Nothing new upstream" while the fork sits on v8.0.78).
- Scratch-run path of the sync workflow (branch input) — untested.

## Next

- Build the management UI against `docs/management-api-v8.md` in a fork-owned `ui/` directory.
  Upstream ships no frontend; the API is the contract.
- Only replace `G:\Tools\CLIProxyAPI\core\cli-proxy-api.exe` with a fork build after the fork's
  CI is green and the local `update-core.ps1` verify/revert path has been exercised.
