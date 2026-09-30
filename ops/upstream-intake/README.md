# Upstream release intake

Track published stable releases from `router-for-me/CLIProxyAPI`. Use an explicit
baseline, wait at least **72 hours** after `publishedAt`, and selectively port
individual commits. This is our fork policy, not a universal release rule.

## Procedure

1. Read [absorbed.md](absorbed.md). Choose the explicit comparison baseline;
   assessed/fetched does not mean absorbed. On first intake, document the inherited
   upstream baseline and evidence before evaluating a candidate.
2. Assess the candidate:

   ```bash
   ops/upstream-intake/assess-release.sh v8.0.6 v8.0.4
   ```

   Replace both example tags with the chosen releases. The script requires `git`
   and `gh` read access. It rejects drafts, prereleases, malformed versions,
   candidates younger than 72 hours and non-ancestor baselines. It fetches with
   `--no-tags` into `refs/upstream/tags/*`, never normal `refs/tags/*`, and never
   writes to `origin`. The candidate/review grouping uses message prefixes only;
   read the diff to decide. `MIN_AGE_HOURS` may increase, but not lower, the gate.
3. Run `ops/upstream-intake/verify-absorb.sh` on the clean current baseline.
   Preserve unrelated files. If the baseline fails, diagnose and resolve the
   failure within authorized scope before absorption; do not waive it away.
4. Use a `codex/*` task branch. Inspect each candidate and our overlapping changes.
   Prefer protocol/client compatibility and security fixes; assess configuration,
   dependencies and large refactors separately. Port selected commits with:

   ```bash
   git cherry-pick -x <reviewed-upstream-sha>
   ```

   Never merge upstream branches or the whole release range.
5. Run the same regression gate on the candidate. For request/response changes,
   also replay representative fixed requests on the baseline and candidate with
   equivalent isolated configuration. Record actual client coverage; fixtures
   cannot establish that a real client turn succeeded.
6. Update [absorbed.md](absorbed.md) with upstream/local SHAs, skipped work and
   reasons, baseline/candidate gate evidence and replay limits. Mark absorption
   complete only after the selected changes reach our `main` with a passing gate.
7. When releasing, use the assessed candidate as the `Upstream release` and the
   prior accepted baseline as `Previous baseline` in
   `docs/releases/<tag>.md`. Ported and skipped work must agree with
   [absorbed.md](absorbed.md); do not claim alignment to a merely fetched tag.
8. Release through [docs/maintenance.md](../../docs/maintenance.md) when requested.
   Assessment or absorption alone does not authorize publishing or deployment.

## Regression gate

The gate requires Python 3 and checks the offline maintenance fixtures, tracked
Go formatting with `gofmt` from the active `go env GOROOT` toolchain,
`go vet ./...`, focused responses-tools
invariants, all unit/integration tests, race-sensitive packages and a server build
in a unique temporary directory. Formatter failures propagate; the gate neither
formats files nor reads unrelated untracked source. It records all failed stages
and returns nonzero if any stage fails.

Passing means no regression detected by those checks. For release-sensitive
behavior, keep the additional acceptance evidence and its limits in the ledger.
