# Upstream release intake

We track `router-for-me/CLIProxyAPI` **releases only**. `upstream/dev` and
`upstream/main` are never rebased onto. This directory holds the procedure and
the tooling that enforces it.

## Rule

An upstream release is only absorbed after it has been ported **and** the
regression gate is clean. A port that fails the gate is not absorbed; it is
either fixed on our side or dropped with a written reason.

"It compiles" and "the tests still pass" are not the gate. The gate is
`verify-absorb.sh`.

## Procedure

1. **Fetch the release tag locally only.** Never push upstream tags to `origin`:
   `release.yaml` publishes a full multi-platform release for every
   `v[0-9]+.[0-9]+.[0-9]+` tag pushed here.

   ```bash
   ops/upstream-intake/assess-release.sh v8.0.6
   ```

   The script fetches into `refs/upstream/tags/`, prints the commit list
   between the previous absorbed tag and the new one, and classifies each
   commit as candidate / skip by path and by conventional prefix.

2. **Decide what to port.** Port protocol and client-compatibility fixes
   first. Skip behaviour changes, configuration-semantics changes, and
   upstream CI/build churn by default. Port by cherry-picking individual
   commits, not by merging a range.

3. **Record the decision.** Append a row to `absorbed.md` saying which
   upstream version was assessed, which commits were ported, and which were
   skipped with reasons.

4. **Run the regression gate.** This is the blocking step.

   ```bash
   ops/upstream-intake/verify-absorb.sh
   ```

   It must exit 0. If it does not, the port is not absorbed.

5. **Absorb and release.** Commit on a task branch, merge to `main`, tag our
   own version, let `release.yaml` publish.

## What the gate covers

| Check | Catches |
| --- | --- |
| `gofmt -l` | formatting drift |
| `go vet ./...` | suspicious constructs |
| `go test ./... -count=1` | unit and integration breakage across all 128 packages |
| `go test -race` on concurrency-sensitive packages | data races in the executor and auth paths |
| server build | link-time breakage that tests do not exercise |
| responses-tools / item-ID invariant tests | regressions in the namespaces this fork exists to maintain |

Passing the gate means "no regression we can detect", not "no regression at
all". For a port that touches request or response paths, additionally
replay a real client turn against the deployed service before releasing; see
the acceptance runbook in this repository's docs.
