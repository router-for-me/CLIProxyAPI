# Absorbed upstream releases

One row per upstream release that was assessed. "Absorbed" means the listed
commits were ported onto our `main` **and** `verify-absorb.sh` exited 0.

| Upstream | Assessed | Ported | Skipped | Gate | Recorded |
| --- | --- | --- | --- | --- | --- |
| v8.0.7 | 2026-10-01 | `82f8e92b` `a8ffd5a8` `67cb32b6` `9e71c20d` `97f244b8` | none | PASS | 2026-10-01 |

## Comparison baseline

**Baseline: `v8.0.7`** (`97f244b8ddb9cbf564b6e6faab0159102cca8617`), advanced
from `v8.0.5` on 2026-10-01 when every commit in `v8.0.5..v8.0.7` was ported.

Earlier baseline: `v8.0.5` (`e5b5a1cfca354ec80d5c31242ccfb6c0f6b5fbcd`).

Established on 2026-10-01 from repository history rather than from tag
presence. Evidence:

- `v8.0.0` through `v8.0.5` are ancestors of our `main`; `v8.0.6` and
  `v8.0.7` are not. The fork shares upstream history, so ancestry — not tag
  existence — is what establishes that the code is already on our trunk.
- The tags were reached through the shared fork history, not by a reviewed
  port, so this baseline records inherited content only. It carries no
  per-commit assessment and no replay evidence.
- Verified against `main` at `f667aaf566ecabd5e419d11bd50a79a9b6d1637d`.

Do not advance this baseline on tag presence alone. An assessed release is not
automatically a new baseline; record that decision explicitly, along with any
work left skipped.

For each assessment, record the explicit baseline, candidate and our main SHA,
baseline/candidate gate logs, upstream-to-local commit mapping and replay coverage
(or "not performed"). An assessed release is not automatically a new baseline;
record that decision explicitly, including any remaining skipped work.

## Assessments

### v8.0.7 — absorbed, 2026-10-01, ahead of the age gate by owner decision

Baseline `v8.0.5`, candidate `v8.0.7` (`97f244b8ddb9cbf564b6e6faab0159102cca8617`),
our `main` `cbce10a9`.

**The 72-hour gate was waived for this release.** `v8.0.7` was published
`2026-09-30T19:14:25Z` and was roughly 7h old when it was ported; the policy
window would have expired at `2026-10-03T19:14:25Z`. The repository owner
authorized the early intake on 2026-10-01 after `codex`-client traffic to a
Meta Muse upstream exposed an unfixed tool-integer-normalization failure. The
gate script enforces `MIN_AGE_HOURS >= 72` in code and offers no waiver flag, so
the assessment was performed by hand and this entry is the record of the
deviation. Every other intake must still clear the window normally.

All five commits in `v8.0.5..v8.0.7` were ported with `git cherry-pick -x`, in
chronological order, with no conflicts:

| Upstream | Local | Subject | Upstream issue |
| --- | --- | --- | --- |
| `82f8e92b` | `4463cea7` | fix(executor): support negotiated ALPN protocols in uTLS client | #6223 |
| `a8ffd5a8` | `65aaae24` | fix(codex): skip tool parameter integer normalization for codex executor targets | #6244 |
| `67cb32b6` | `7c291254` | fix(codex): determine tool integer normalization by target executor identity | #6244 |
| `9e71c20d` | `27ddba81` | fix(codex): handle upstream stream disconnect before first payload as bad gateway | #6247 |
| `97f244b8` | `d15c8c32` | fix(codex): pass target executor to compatibility translation and token counting | #6244 |

Nothing was skipped. `67cb32b6` and `97f244b8` revise the approach taken by
`a8ffd5a8`, so all three were ported together.

Gate evidence, both run through `ops/upstream-intake/verify-absorb.sh`:

| Tree | Result |
| --- | --- |
| baseline (`cbce10a9`) | PASS |
| candidate (`d15c8c32`) | PASS |

The gate must run with Go 1.26.4, the version both workflows pin. The local
toolchain was Go 1.27.1, whose `gofmt` reformats three tracked files that CI
accepts; that first baseline run failed on `gofmt` for a toolchain-version
reason, not a source defect. Re-run with `GOTOOLCHAIN=go1.26.4`, both trees
pass. Do not read a Go 1.27 `gofmt` result as a source failure.

**Replay coverage: not performed.** No real Codex client turn was replayed
against either tree, so the gate result covers the automated checks only and
does not establish that a live Muse request behaves correctly. The reported
failure is expected to remain after this port: see the note below.

### Effect on the reported Meta Muse failure: none

The reported symptom is a tool argument arriving as the JSON number `2500.0`
where the Codex client's strict deserializer expects an integer. The ported
commits scope the existing schema rewrite so it is skipped for Codex executor
targets, where upstream validates reserved tool schemas strictly.

That scope change does not reach the Meta executor. `meta_executor_execute.go`
was not migrated to the executor-aware helper; it still calls
`ApplyPayloadConfigWithRequest`, which passes an empty target executor, and it
additionally calls `NormalizeCodexToolIntegerTypes` unconditionally at line 67.
`isCodexTargetExecutor` is false for that path both before and after the port,
so Meta requests are rewritten exactly as they were on the baseline. If a Muse
request still produces `2500.0` after this port, the cause is a tool or
parameter missing from `codexClientToolIntegerFields` in
`internal/client/codex/tool-schema/tool_schema.go`, which is a whitelist gap
rather than a release-alignment gap.

**Resolved locally rather than by widening the whitelist.** The response side is
now canonicalized for Responses clients by
`sdk/translator/codex_tool_arguments.go`, which rewrites integral float
literals in emitted tool-call arguments without touching schemas, so it covers
every tool and parameter and a whitelist entry is no longer the remedy. Tracked
upstream as `router-for-me/CLIProxyAPI#6255`.

## How to fill this in

After `assess-release.sh` and `verify-absorb.sh`:

```markdown
| v8.0.6 | 2026-10-01 | `abc1234` tool-schema fix, `def5678` reconnect backoff | `9999999` CI churn, `8888888` config rename | PASS | 2026-10-01 |
```

Keep the skipped column honest. A release where most commits are skipped is a
normal outcome and worth recording; a release with an empty ported column
should say why in a comment below the table.
