# proxx CI

`proxx-ci.yml` is the fork's CI. It replaces upstream's `pr-test-build.yml` and
`translator-path-guard.yml`, which are disabled in the fork with `gh workflow disable` and left
unchanged so upstream merges stay conflict-free (fork rules in [README.md](README.md)).

## Shape

Every job starts at once. None waits on another.

| Job | What it runs |
|---|---|
| `checks` | `gofmt -l`, `go vet ./...`, `go mod tidy -diff`, Codex catalog validation, the test-input inventory, and (PRs only) the translator path guard. Every check runs, and the job lists every failure. |
| `build` | `go build ./cmd/server` |
| `test (1..8/8)` | `go test -race -shuffle=on -count=1`, in 8 shards planned by `.github/scripts/proxx_ci.py` |
| `warm-cache` | main only, not required: compiles everything CI compiles and saves the Go cache |
| `record-timings` | main only, not required: merges every shard's `go test -json` output into the timings file used to balance shards |

`proxx-e2e.yml` (self-update e2e on Linux and macOS) is still a separate workflow.

## Rules

- Nothing reaches `main` without the full suite. Selection only narrows PR feedback, and it is off
  until a merge queue runs the full suite before main (repo variable `PROXX_MERGE_QUEUE=true`).
- No retries, no wider timeouts, no skips. A flaky test is a bug in the test or the code.
- Checks only stay or grow.

## Shards

- Every shard computes the same plan from the committed `.github/proxx-ci/timings.json`, so the
  plan depends only on the commit, and re-runs reproduce it.
- `main` records fresh times on every full run (`proxx-timings` artifact of `record-timings`). To
  rebalance: `gh run download <run> -n proxx-timings -D .github/proxx-ci`, then commit it in a PR.
- Before running anything, each shard proves that the shards together cover every selected
  package exactly once. After running, it proves it ran exactly its own part.
- A package slower than 20s is split by top-level test name. The last part uses `-skip` with the
  names of the other parts, so a test added since the timings were recorded still runs.
- A single test slower than 20s is split by its subtests (`-run '^X$/^(subs)$'`). Its remainder runs
  `-run '^X$' -skip '^X$/^(subs)$'`, so a new subtest still runs. Each shard prints the plan digest;
  every shard of one run shows the same digest.

## Selection (PRs, with a merge queue only)

`proxx_ci.py select --base <sha>` maps each changed path to Go packages and selects every package
whose tests import any of them (`go list -deps -test`).

These rerun everything:
- `go.mod`, `go.sum`, `.github/**`, `hk.pkl`
- any changed path that is not inside a Go package directory and not in `SAFE_PATHS`

Safe paths (`SAFE_PATHS`): `*.md`, `LICENSE`, `assets/**`. No test reads them.

Two lists cover tests that read files outside their own package:
- `WHOLE_MODULE` lists packages whose tests build or scan the whole module. They run on any change
  to Go code or to an embedded file.
- `EXTRA_INPUTS` lists other cross-directory reads.

The `inventory` check fails CI when a test reads outside its package without being listed, so
neither list can rot. Dry-run selection with `python3 .github/scripts/proxx_ci.py select --files <paths>`.
