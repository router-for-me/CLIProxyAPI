# Fork state

Last updated: 2026-11-21

## Verified

- Fork created from `router-for-me/CLIProxyAPI` at `main`, `v8.0.78` (upstream release of 2026-11-19).
- Issues enabled, Actions enabled, "allow Actions to create and approve PRs" on.
- `.github/workflows/sync-upstream.yml` installed (daily 09:23 UTC merge of `upstream/main`,
  green-only push to `main`, conflict issue / red PR fallback).
- `.github/workflows/fork-ci.yml` installed (build + `go test ./...` on PR and on push to `main`;
  `go vet` runs as an advisory step only).
- Upstream PR-policy and release workflows removed from the fork:
  `agents-md-guard`, `pr-path-guard`, `auto-retarget-main-pr-to-dev`, `release`, `docker-image`.
- Fork is level with upstream `main` (0 behind) as of `db7f177d`.
- Local source checkout: `G:\Tools\CLIProxyAPI\src` (this repo). Runtime stays
  `G:\Tools\CLIProxyAPI\core\cli-proxy-api.exe`, still upstream v8.0.78.

## Merged upstream fixes (2026-11-19)

| PR | What it fixes |
| --- | --- |
| router-for-me#7629 | Refresh token kept when the response omits it (was writing `""` → `invalid_grant` → account outage) |
| router-for-me#7663 | Stored models registered before listen (400 `unknown provider` burst after restart) |
| router-for-me#7636 | Rotation stops after an Antigravity `TOS_VIOLATION` instead of burning the pool |

Not taken yet: #7666 (`context_length_exceeded` before first byte), #7678 (management saved-vs-runtime
state, 14 files), #7643 (shorten overlong Codex call IDs — cheap follow-up).

## CI

`fork-ci` runs build + the full test suite. The 2026-11-21 upstream sync failed
because `TestModelsWithClientVersionReturnsCodexCatalog` hard-coded gpt-5.5
metadata that differs between the embedded and refreshed catalogs. The test
now checks the loaded template and still checks the custom model's fallback
version. Both workflows run the test. `go vet` stays advisory because upstream's
`internal/pluginhost/host_test.go` trips copylocks.

First run with the merged PRs failed once in
`TestServiceHeartbeatFailoverDoesNotDrainBlockingScope` and passed on rerun of the same
commit. That test asserts a 50 ms "nothing happened yet" window against a 100 ms heartbeat
timeout and 1 s loopback ceilings, so it is load-sensitive, not a regression from #7663
(which only touches `service_lifecycle.go` / `service_plugins.go`). Treat a failure there as a
rerun first; only investigate if it repeats.

## Touched upstream files

- `internal/api/server_test.go` — check the loaded Codex template's version and
  service tiers in the catalog integration test. Drop this patch once upstream
  accounts for refreshed catalog metadata.
- Fork-owned `.github/workflows/fork-ci.yml` runs all tests with advisory vet.
- Our additions: `FORK.md`, `FORK-STATE.md`, `.github/workflows/sync-upstream.yml`.

## 2026-11-21 sync verification

- Reproduced the CI failure locally with refreshed catalogs. The targeted
  test and the `internal/api` package passed with refreshed catalogs after
  the fix. The package also passed with embedded catalogs, and the server built.
- Full-suite CI and the merge are pending. The current `main` build-test job
  is queued for an `instabox-medium` runner.

## Next

- After CI is green: build a fork binary, exercise `update-core.ps1 -Rollback` against it, then
  replace the running core.
- Point `internal/managementasset/updater.go` at the UI fork's release so the core serves our
  management build instead of upstream's.
