# Upstream release intake

Track published stable releases from `router-for-me/CLIProxyAPI`. Use an explicit
baseline, wait at least **72 hours** after `publishedAt`, and selectively port
individual commits. This is our fork policy, not a universal release rule.

The three-day window exists to let regressions and hurried follow-up fixes
surface upstream before we port from a release. Upstream publishes frequently, so
a fixed wait costs little. Do not shorten it, and do not tier it by commit prefix:
the wait is the only thing standing between a bad upstream cut and our `main`.

## Procedure

1. Read [absorbed.md](absorbed.md). Choose the explicit comparison baseline;
   assessed/fetched does not mean absorbed. On first intake, derive the inherited
   baseline and record the evidence before evaluating a candidate:

   ```bash
   ops/upstream-intake/establish-baseline.sh main
   ```

   Ancestry against the trunk is the evidence, not tag presence: this fork shares
   upstream history, so upstream tags reach our commits whether or not anyone
   assessed them. A baseline derived this way records inherited content only, with
   no per-commit assessment and no replay evidence.
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
   When an upstream commit and our fork solve the same problem, prefer the
   upstream implementation. Keep fork-only deltas only where they cover behavior
   upstream does not address, and record the choice in absorbed.md.
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

## Tag namespace hygiene

Upstream tags belong in the local-only `refs/upstream/tags/*` namespace. A plain
`git fetch upstream --tags` writes them into `refs/tags/*` instead, and a later
`git push --tags` would then publish every one of them from this fork. Upstream's
whole release history is present locally this way today.

Stop it at the source once per clone:

```bash
git config remote.upstream.tagOpt --no-tags
```

With that set, a plain `git fetch upstream` no longer imports tags at all, and
only the explicit namespace-targeted fetches below bring any in.

```bash
ops/upstream-intake/check-tag-namespace.sh --remote
```

The check compares tag names, but never proposes deleting a tag that `origin`
also publishes: this fork numbers its releases independently and our `v1.0.0`
already shares a name with an upstream tag pointing at a different commit.
It also warns when `remote.upstream.tagOpt` is unset, since that is what makes
the leak recur.

At release time `ops/upstream-intake/guard-release-tag.sh <tag>` rejects any tag
that resolves to the same commit as the upstream tag of that name. The release
workflow runs it before building anything.

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
