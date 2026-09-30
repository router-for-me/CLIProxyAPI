# Pinable upstream synchronization

## Baseline and invariant

The 2026-09-30 synchronization targets upstream
`a270e7b9e57aaecd8f82555f44c2108518ad2330` from fork main
`2eb057869ca11301edf1768e6ff68d8f95521e10`: 116 upstream commits since
`a5ab69521f7b4e0f244836d0419da8fcd89408ea`, retaining all 25 fork-only commits.
This upstream changes the Go module path from `/v7` to `/v8`; the toolchain
remains Go 1.26.0. Consumers compiling against the SDK must use the v8 imports.
The Desktop runtime host contract remains version `1`.

The 2026-09-21 synchronization integrates `router-for-me/CLIProxyAPI` at
`a5ab69521f7b4e0f244836d0419da8fcd89408ea` with the fork main previously at
`390fab7820295761d3e0991dfc1fdf8121dc9927`. Their merge base is the prior
upstream baseline `61fdfc341b96178a8dcb53f2efc46cbc341d267c`, so this run
integrates 15 new upstream commits while retaining the fork history.

The initial 2026-09-20 synchronization integrated upstream
`61fdfc341b96178a8dcb53f2efc46cbc341d267c` with the pre-sync fork revision
`491e5413d084f74614f10834d63b76d288fb3e56`; their shared ancestor was
`44e62bc8acc2f224bff9c62d222717d3f6723dea`.

Preserve both histories. Use a normal merge commit for synchronization PRs,
**not squash or rebase**, and do not reset the fork to upstream. Keeping the
upstream commit as an ancestor lets Git recognize already-integrated changes
on the next sync. Never resolve all conflicts with blanket `ours` or `theirs`.

## Host contract that must survive a sync

- `--version` / `-version` must work without loading configuration or starting
  services. Discovery JSON output must not contain the normal startup banner.
- `--parent-pid` retains parent-process monitoring and graceful cancellation.
- The authenticated `/v0/management/runtime-info` and
  `/v0/management/runtime-shutdown` endpoints retain contract version `1`.
  Shutdown additionally checks the actual loopback peer, not a forwarded header.
- `CLIPROXY_EPHEMERAL_API_KEY` / `Builder.WithEphemeralAPIKey` remain in-memory,
  service-scoped credentials. They must not enter persisted API-key configuration
  or appear in the authentication principal, and must survive plugin/config reload.
- Upstream startup paths and SDK initialization remain intact, including discovery,
  provider login modes, plugin hooks, and `discoveryManager` initialization.

## Conflict reduction

The 2026-09-30 merge has four import-block conflicts:
`internal/access/config_access/provider.go`, `internal/api/server_options.go`,
`internal/api/server_reload.go`, and `internal/cmd/run.go`. Keep all upstream
v8 imports plus the host-only imports; do not replace either implementation.
Migrate the seven fork-only Go files that Git cannot update because they are
absent upstream. The Interactions-to-Gemini import guard now recognizes any
numbered module version, with positive/negative cases, so a future major-module
rename cannot silently disable that architecture check.

The six-platform packaged-executable smoke test now also reads the v8 client-key
view without changing the file, rejects unauthenticated/ephemeral management
access, rotates the persisted client key through the v8 API, and checks that the
old key is rejected while the runtime-only key and v0 host control remain valid.
The original atomic-file-replacement, parent-exit, and loopback-shutdown probes
are retained, not replaced by the v8 checks.

The first synchronization conflicted in `cmd/server/main.go`,
`cmd/server/main_test.go`, and `sdk/cliproxy/builder.go`. Host version detection,
environment handling, and safe-mode adaptation therefore live in
`cmd/server/host_runtime.go`, with dedicated tests in `host_runtime_test.go`.
The upstream safe-mode function and `main_test.go` remain upstream-shaped rather
than carrying host-specific signatures.

The ephemeral-key builder option and initialization live in
`sdk/cliproxy/builder_runtime.go`. The upstream service constructor literal is
retained, followed by one host-specific initialization call. This prevents a
long fork-only field name from reformatting the entire literal and colliding
with newly added upstream fields. Runtime fields in the management handler are
kept in their own formatting group for the same reason.

The 2026-09-21 synchronization had exactly one textual conflict:
`internal/runtime/executor/codex_stream_bootstrap_buffering_test.go`. Upstream had
independently added the same captured-start mock-clock synchronization that the
fork introduced on 2026-09-20. The reviewed resolution adopts the complete
upstream version and removes the equivalent fork-only helper/edits. The file is
therefore no longer a fork divergence, reducing the chance of repeated conflicts
in later upstream merges. No production executor behavior was changed by this
resolution.

Regression tests cover clean discovery output, upstream safe-mode decisions,
runtime-key isolation, unchanged configured keys, repeated reloads without
provider duplication, preservation of upstream discovery initialization, and
the Codex bootstrap timeout ordering cases.

Upstream-only contribution policies (AGENTS changes, translator-path restrictions,
and automatic main-to-dev retargeting) are scoped to `router-for-me/CLIProxyAPI`.
They must not redirect or close normal fork synchronization PRs. The fork's own
validation workflow has read-only repository permissions.

## Bootstrap test stability

The 2026-09-20 integration exposed a scheduler-dependent mock-clock ordering
failure in Codex bootstrap timeout tests. A fork-only captured-start handshake
was initially added to make the fixtures deterministic. Upstream subsequently
implemented the same ordering guarantee before the 2026-09-21 sync, so the fork
now follows upstream's test implementation directly instead of maintaining a
parallel patch. The persistent CI still repeats the affected timeout tests 20
times in addition to the complete Go suite.

## Next synchronization

Use Bash, Git, Python 3 for the script tests, and the Go version declared in
`go.mod`. Start on the desired current fork branch with a clean working tree.

```bash
git switch main
git pull --ff-only origin main
bash scripts/pinable/sync-upstream.sh
```

The script adds the official `upstream` remote only when it is missing; an
existing remote is displayed and used without silently rewriting its URL.
A branch, tag, or full commit SHA can replace the default `main` argument.
The target is fetched once and pinned to its resolved commit for this run.

It creates a dedicated `sync/upstream-<date>-<sha>` branch, starts a real merge,
builds the server, and runs `go test ./...` before creating a merge commit.
It never pushes a branch, force-updates a ref, discards changes, or overwrites
an existing sync branch. A repeated sync to an already-integrated commit is a no-op.

When conflicts occur, inspect both implementations, retain the host contract,
and explicitly stage only reviewed resolutions:

```bash
git status
git diff
# Edit the conflicted files and run gofmt on changed Go files.
git add <reviewed-files>
bash scripts/pinable/sync-upstream.sh --continue
```

If validation fails, fix and stage the change, then use `--continue` again.
No merge commit is created after a failed build or test. To abandon the pending
merge started by the script, run:

```bash
bash scripts/pinable/sync-upstream.sh --abort
```

The script enables `rerere.enabled=true` and `rerere.autoupdate=false` locally.
Git can suggest a recorded resolution for a recurring conflict, but it is never
auto-staged: inspect it and run the regression suite again. The rerere cache is
local to this clone, is not distributed merely by committing this script, and
cannot guarantee that future semantic conflicts will disappear.

After success, review the branch diff, push the named sync branch, and open a PR
to the fork's `main`. Merge using **Create a merge commit**. The read-only
`Pinable upstream check` workflow validates the actual PR merge result, builds
the server, runs the Go suite, checks the sync script, repeats timeout ordering
regressions, and probes both discovery CLI forms.

## Script regression tests

```bash
python3 scripts/pinable/test_sync_upstream.py -v
```

These tests use isolated temporary Git repositories and a fake Go command to
exercise merge ancestry, no-op synchronization, dirty/untracked-file refusal,
manual conflict continuation, abort, and failed-validation behavior. They test
the shell workflow, not the Go application; the CI Go suite remains required.
