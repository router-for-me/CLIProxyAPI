# Absorbed upstream releases

One row per upstream release that was assessed. "Absorbed" means the listed
commits were ported onto our `main` **and** `verify-absorb.sh` exited 0.

| Upstream | Assessed | Ported | Skipped | Gate | Recorded |
| --- | --- | --- | --- | --- | --- |
| v8.0.7 | 2026-10-01 | `82f8e92b` `a8ffd5a8` `67cb32b6` `9e71c20d` `97f244b8` | none | PASS | 2026-10-01 |

| v8.0.13 | 2026-10-03 | 17 commits (mapping below) | 18 commits, including 3 merge commits (decisions below) | PASS; effective on main | 2026-10-03 |

## Comparison baseline

**Baseline: `v8.0.13`** (`d7914afdedca7af95ee974a42453dc49fc1388ce`), selectively advanced from
`v8.0.7` on 2026-10-03. This advancement takes effect only when the verified
intake branch and this ledger reach our `main`. The retained/deferred work below
is deliberately outside this alignment; reconsider it explicitly if a future
release depends on it.

Previous baseline: `v8.0.7` (`97f244b8ddb9cbf564b6e6faab0159102cca8617`), advanced
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

### v8.0.13 — selective absorption, effective on main, 2026-10-03

Baseline `v8.0.7`, candidate `v8.0.13` (`d7914afdedca7af95ee974a42453dc49fc1388ce`),
our clean baseline `main` `ceccde5b28c174b2e8d592c1c267c3c4f4e77986`. The published latest stable release was
verified with the upstream GitHub API on 2026-10-03. The range contains
35 commits: 32 non-merge commits and 3 merge commits.

**Owner-authorized age exception, this intake only.** The owner explicitly
waived the time condition on 2026-10-03. `v8.0.13` was published
`2026-10-03T09:02:46Z`; its normal window expires `2026-10-06T09:02:46Z`.
`assess-release.sh v8.0.13 v8.0.7` returned 3 because the release was 4h old.
Assessment then proceeded manually under that authorization, using only
`refs/upstream/tags/*` fetched with `--no-tags`. Neither the script nor the
72-hour policy was changed. No release, tap publication, installation or
service restart is part of this intake.

#### Ported commits

Each selected commit was cherry-picked individually with `-x`. The local SHAs
below identify reviewed task-branch commits before GitHub's rebase merge;
upstream provenance in the commit messages remains the durable lookup key.

| Upstream | Reviewed local | Decision and user-visible result |
| --- | --- | --- |
| `b467a83c` | `5f6a863e` | fix(xai): bump pinned grok client version to 1.0.44 for chat-proxy |
| `cf3102cc` | `5de25777` | refactor(executor): optimize translation logic and improve plugin invocation handling |
| `8fbf152b` | `d38d7b63` | fix(executor): preserve top-level responses token usage when service tier is present |
| `6d57ac90` | `3097e02f` | fix(claude): pin session date in cloaked reminder to prevent prompt cache invalidation |
| `c163bae4` | `208f0662` | fix(auth): preserve terminal unauthorized state and prevent unsafe refresh retry |
| `e6f6f26a` | `767827cc` | fix(claude): handle pause_turn stop reason in responses translation |
| `2044a01f` | `2a20220c` | fix(codex): preserve web search sources in responses request include |
| `5dbce4f3` | `44fd7da4` | perf(claude): prefilter JSON payloads before diagnostic walks |
| `e3abd9ae` | `b01b5735` | fix(antigravity): replace retired Claude 4.6 models with 5.5 |
| `c575291a` | `d5b5173e` | fix(antigravity): align Claude 5.5 token limits with upstream catalog |
| `6b037f61` | `2ae23a4a` | fix(antigravity): use actual Claude 5.5 high model IDs |
| `42d484f9` | `5156b2d3` | fix(claude): map request timeout status to timeout_error |
| `d4692663` | `083a6516` | fix(auth): snapshot auth before unlocking during registration and update |
| `ea8ffd5f` | `5ee551d3` | fix(devin): stream content early and defer thought stop for late signatures |
| `ed4d972e` | `d5ef963b` | fix(responses): complete reasoning summary stream lifecycle events |
| `2c1dcc7c` | `b4b711ce` | fix(claude): include web search sources in codex request when search tool is present |
| `d7914afd` | `5ee5ed04` | fix(codex): expand integer field normalization mappings for tools |

#### Skipped commits and reasons

| Upstream | Decision |
| --- | --- |
| `fd48ea68` | Merge-only wrapper for `b467a83c`; ported its reviewed non-merge commit. |
| `3ebee065` | Defer the 96-file `apply_patch` bridge (14,573 additions). It adds an executor/translator error contract and model capability API overlapping our generic custom/tool-search pipeline. It needs a separate joint protocol review; retain the existing fork implementation. |
| `63c04b4b` | Defer client configuration migration to `client.codex.*`; tied to the deferred bridge and changes config/SDK shape. |
| `028f6a19` | Skip promotional/provider README refresh; our neutral fork READMEs remain authoritative. |
| `f3fd2f23` | Defer routine signature log suppression; low-priority diagnostic churn. |
| `9f35c1dc` | Defer the companion signature logging/sanitizer refactor; replaces structured fields with formatted messages, contrary to this fork's logging convention. |
| `2783e10c` | Not applicable to the retained Antigravity executor: it publishes parsed terminal usage directly before `EnsurePublished`, and has no upstream bridge's `StreamUsageBuffer` to flush. Existing split-usage tests remain enabled. |
| `6fecc6e5` | Merge-only wrapper; assess individual commits instead. |
| `5ec31442` | Keep existing fork Gemini/Vertex/AIStudio terminal fixes (`cbcbe41c`, `47b61ca0`, `a589ffdb`, `6edda221`, `eba4cb0a`, `408f0da4`, `cec7bb55`) and tests. The upstream patch mixes these semantics with deferred bridge state/finalization; do not overwrite the fork helpers. |
| `30aa1f12` | Keep the already-shipped connection-pool test tolerance (`13e5ca57`). Both baseline and candidate normal/race suites pass; no new evidence requires weakening its bounds further. |
| `d7c3c0aa` | Defer optional connection debug tracing; wraps transports across multiple operations and is not needed for the selected compatibility fixes. |
| `52d5507d` | Defer shared provider config migration to `upstream.*`; changes YAML placement, alias precedence and WebSocket option scoping. |
| `3be5fa44` | Defer alias projection/save-layout changes coupled to the skipped config migrations; current layout is retained. |
| `e2bff010` | Merge-only wrapper; assess individual commits instead. |
| `8348923a` | Defer new `host.routing.reset_cooldown` plugin API/ABI callback; independent public capability with no selected-fix dependency. |
| `d306f2c5` | Depends on deferred model `apply_patch` capability/config implementation; existing fork custom capability policy remains in force. |
| `0fb50a18` | Defer v8 management API auth-index injection; broad config/management serialization change unrelated to selected fixes. |
| `a3b77566` | Already satisfied: the retained Codex executor publishes primary model usage before `publishCodexImageToolUsage`; the ordering was only reversed by the deferred bridge. |

#### Conflict and compatibility review

- `2044a01f`: kept its three new search-source response tests while excluding
  an unchanged `apply_patch` test used as upstream patch context. That test's
  bridge helpers do not exist in the retained fork architecture.
- `ed4d972e`: added reasoning summary lifecycle events while retaining the
  entire existing function/custom-call identity branch.
- `cf3102cc`: adapted only its new signature-log test to count the fork's
  structured diagnostic message. The first targeted run exposed this
  upstream-log-format dependency; no sanitizer/logging implementation changed.
- The integer schema mapping update remains executor-aware through existing
  request helpers. The fork's response-side integral-float canonicalization,
  opt-in context and custom-text preservation are unchanged and pass the gate.
- Antigravity's embedded catalog now uses `claude-opus-5-5-high` and
  `claude-sonnet-5-5-high` with upstream limits (1,000,000 context and 128,000
  completion tokens). This records the upstream catalog update, not a live
  provider availability check. Operator aliases to the removed 4.6 IDs should
  be reviewed before a later deployment; this task edits no runtime config.
- No Go dependencies, module paths, plugin APIs, YAML layout, maintenance
  workflows or deprecated management endpoints were changed.

#### Validation and replay evidence

`GOTOOLCHAIN=go1.26.4 bash ops/upstream-intake/verify-absorb.sh`:

| Tree | Result |
| --- | --- |
| baseline (`ceccde5b`) | PASS: maintenance fixtures, gofmt, vet, responses-tools invariants, full suite, all race-sensitive packages, server build |
| candidate source (`74404748`) | PASS: same complete gate |

The tracked Codex catalog is additionally checked offline using the PR workflow's
catalog validation command. The task retains the full logs outside tracked source
under `.git/task-evidence/upstream-v8.0.13-20261003/`.

**Isolated regression request replay:** exactly the same XAI client-version and
Claude server-tool-stop regression tests were run against the baseline (using
Go's source overlay, without editing the baseline) and candidate. Baseline
failed for pinned XAI version `0.2.120` and `pause_turn` emitting completed in
both stream and buffered responses; candidate passed both, including normalized
`PAUSE_TURN`, `max_tokens` and normal stop controls. Files:
`baseline-replay.log`, `candidate-replay.log`, `baseline-replay-overlay.json`.
These are credential-free HTTP/translator fixtures, not production client
traffic. **Real client turn replay: not performed.** The gate and fixture replay
do not prove live xAI, Claude, Devin or Antigravity provider availability.

## How to fill this in

After `assess-release.sh` and `verify-absorb.sh`:

```markdown
| v8.0.6 | 2026-10-01 | `abc1234` tool-schema fix, `def5678` reconnect backoff | `9999999` CI churn, `8888888` config rename | PASS | 2026-10-01 |
```

Keep the skipped column honest. A release where most commits are skipped is a
normal outcome and worth recording; a release with an empty ported column
should say why in a comment below the table.
