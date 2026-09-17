---
feature: fallback-disabled-accounts
verdict: PASS_WITH_GAPS
standard: .specify/extensions/tdd/templates/tdd-test-quality-rubric.md
verified_at: 109a066d
behaviors: 10
proven: 0
likely: 10
test_after: 0
no_test: 0
high_smells: 0
criteria_total: 6
criteria_covered: 6
mutation_score: not measured (no mutation tool in profile) # deliberate mutants: 6 run, 5 caught, 1 equivalent survivor
mutants_survived: 1 # triaged equivalent (unreachable guard), no behavioral effect
suite: auth 246 passed / 5 failed (pre-existing), 1.6s; synthesizer 44 passed / 2 failed (pre-existing), 0.6s
---

# TDD Verification: Excluded credentials must be skipped during credential selection

**Verdict: PASS_WITH_GAPS.** No `HIGH` smells, all 6 criteria covered at the spec's
named entry points, all behaviours have recorded reds and passing tests — but every
behaviour is `LIKELY` rather than `PROVEN` because the work is uncommitted (git cannot
corroborate test-before-code ordering), and the outer loop stops at the package's
selection API rather than a process-level request.

**Audit independence (disclosure, per Hard Rule 2):** this audit was run by the same
session that wrote the fix. Every artifact was re-read from disk for this report, and
all checks below were re-executed, but the audit is not independent. A fresh-context
review of the diff is recommended at PR time. The regression-signal comparison (stashed
vs applied change, identical pre-existing failure sets) was performed independently by
the calling agent and is corroborated here.

## Test-first evidence

Source 1 (cycle log) records a red command and output for every behaviour. Source 2 (git
history) cannot corroborate ordering: the branch is at base commit `109a066d` and the
entire fix lives in the working tree (`git diff --numstat` shows 6 files, +371/−7); there
are no feature commits to inspect. Per the rubric, a history that cannot show ordering
means `LIKELY`, not `PROVEN`, for every behaviour. Source 3 (files as they stand) agrees
with the tests the list names.

| Behavior | Class  | Evidence |
| -------- | ------ | -------- |
| A1 | LIKELY | cycle 1 red `Pick() auth.ID = "a", want "b"`; uncommitted tree, order not verifiable |
| A2 | LIKELY | cycle 1 reds for round-robin and `pickSingle`; uncommitted tree |
| A3 | LIKELY | cycle 2 red `Pick() for excluded model auth.ID = "a", want "b"`; cycle 3 red for the suffix half |
| A4 | LIKELY | cycle 4; first-run pass red supplied by deliberate mutant (sticky-disable), recorded |
| A5 | LIKELY | cycle 5; first mutant exposed a weak fixture, test strengthened, red recorded |
| A6 | LIKELY | cycle 6; first-run pass red supplied by writer-key mutant, recorded |
| U1 | LIKELY | cycle 1 red `blocked = false, want true` (two rows) |
| U2 | LIKELY | cycles 2–3 reds for per-model and suffix rows |
| U3 | LIKELY | rows green before and after (no-op boundary); covered by cycle 1/5 runs |
| U4 | LIKELY | cycle 6 round trip + pre-existing wire-key assertion (see M5) |

Existing tests (the highest-signal check): `git diff -U0` over
`sdk/cliproxy/auth/selector_test.go`, `sdk/cliproxy/auth/scheduler_test.go` and
`internal/watcher/synthesizer/helpers_test.go` shows **zero removed lines** — every change
to those files is an addition. No assertion was loosened, renamed out of a filter,
skipped, or excluded; no coverage threshold was touched. The only deletions in the whole
diff are the six realigned constant lines in `classification.go` (7 insertions / 6
deletions, pure gofmt alignment).

`tasks.md` consistency: 11 tasks ticked, every referenced behaviour (`A1`–`A6`, `U1`–`U4`)
is `DONE` on the test list; no task claims a behaviour that is not done, and no `DONE`
behaviour lacks a task.

## Findings

| # | Severity | Finding | Evidence |
| - | -------- | ------- | -------- |
| 1 | MED | The outer loop stops at the package's selection API (`FillFirstSelector.Pick`, `RoundRobinSelector.Pick`, `authScheduler.pickSingle`), which `spec.md` names as the real entry points. No test drives the full disable path through `Manager.pickNext`/HTTP, so a regression in the wiring between the management-API disable and credential selection would not be caught end to end. | `sdk/cliproxy/auth/selector_test.go:410`, `scheduler_test.go:170`; no test in the diff touches `Manager` |
| 2 | LOW | The sole-excluded-candidate case is unpinned: with every candidate excluded the selectors return `auth_unavailable` (verified by reading `collectAvailableByPriority` → `getAvailableAuths` and `modelScheduler.unavailableErrorLocked`), but no test asserts that the excluded credential is *not* used as a fallback. | `sdk/cliproxy/auth/selector.go:203,257`; `scheduler.go:823` |
| 3 | LOW | `TestIsAuthBlockedForModel_ExcludedModels` row `excluded_model_matches_within_comma_separated_list` carries the trim rule in an invisible detail: the space after the comma in `"gpt-5, gemini-2.5-pro"` (`selector_test.go:364`) is what pins `strings.TrimSpace(pattern)`. A reader could "clean up" the space and silently lose the trim coverage (mutant M4 proved this row is the only test that catches a dropped trim). | `sdk/cliproxy/auth/selector_test.go:363-367` |
| 4 | LOW | The multi-provider fast path (`pickMixed`) is untested for exclusions. It selects from the same pre-computed ready buckets via `pickReadyAtPriorityLocked`, so the fix covers it by construction, but no test proves it. | `sdk/cliproxy/auth/scheduler.go:254-408` |

### Graded but accepted (documented, no action)

- `if !tt.wantBlocked { return }` inside the unit table (`selector_test.go:397`) gates only
  the `reason`/`next` assertions. Every run asserts the primary `blocked` value; `reason` is
  consulted by callers only when blocked (`collectAvailableByPriority`, `upsertEntryLocked`),
  so the conditional does not create a run that asserts nothing. Not the catalogue's
  `Conditional logic` HIGH.
- `TestFillFirstSelectorPick_PerModelExclusionBlocksOnlyThatModel` asserts both halves of
  AC-3 ("blocks that model" / "still routes the other model"). They are two halves of one
  rule — splitting them would remove the "only" — and each phase has a labelled failure
  message.
- Fixture construction (`excluded := &Auth{ID: "a", ...}`, `eligible := &Auth{ID: "b"}`) is
  repeated across five tests. The stack profile records `helpers: []` — this repository has
  no fixture factory for the package, and the file's existing tests build fixtures inline
  the same way, so a factory would introduce a new convention.
- The unit table passes a real `time.Now()` into a pure function that ignores it on the
  exclusion path; this matches the file's existing tests.

## Mutation results

No mutation tool is present in the profile (`mutation: null`; `gremlins`/`go-mutesting` are
not in `go.mod`). Per the rubric, deliberate mutants were run against the high-risk
behaviours. Scope: the feature's changed files only
(`sdk/cliproxy/auth/selector.go`, `sdk/cliproxy/auth/classification.go`,
`internal/watcher/synthesizer/helpers.go`). Six mutants; every one was restored and the
restoration re-verified green before the next.

| # | Mutant | Location | Caught | Evidence |
| - | ------ | -------- | ------ | -------- |
| M1 | `pattern == "*"` → `pattern == "#"` (sentinel never matches) | `selector.go:157` | Yes — 6 tests | `TestFillFirstSelectorPick_SkipsExcludedAllCredential`, `TestRoundRobinSelectorPick_…`, `TestSchedulerPick_SkipsExcludedAllCredential`, `TestIsAuthBlockedForModel_ExcludedModels`, the two removal/restore tests |
| M2 | `canonicalModelKey(model)` → raw `strings.TrimSpace(model)` | `selector.go:156` | Yes — 2 tests | `TestFillFirstSelectorPick_ThinkingSuffixMatchesBaseModelExclusion`; table row `excluded_model_matches_thinking_suffix_base_model` |
| M3 | drop the `modelKey != ""` guard | `selector.go:159` | **Survived** | No test failed; judged **equivalent** (see below) |
| M4 | drop `pattern = strings.TrimSpace(pattern)` | `selector.go:154` | Yes — 1 test | table row `excluded_model_matches_within_comma_separated_list` (`blocked = false, want true`) |
| M5 | constant value `"excluded_models"` → `"excluded_models_audit"` | `classification.go:18` | Yes — pre-existing test | `TestApplyAuthExcludedModelsMeta_OAuthMergeWritesCombinedModels` (`expected excluded_models="global-a,per,shared", got ""`). The feature's own tests pass with this mutant because they use the constant on both sides — correct for an agreement test, and exactly why the wire value needs the pre-existing literal assertion. |
| M6 | writer joins `combined` → writes `combined[0]` only | `helpers.go:99` | Yes — pre-existing test | `TestApplyAuthExcludedModelsMeta_OAuthMergeWritesCombinedModels` (`got "global-a"`) |

**M3 judgment.** Patterns are trimmed and empty ones are skipped before the comparison, so
`pattern` is always non-empty; `modelKey` is `""` only when the requested model is empty,
and a non-empty pattern can never equal `""`. The guard is therefore unreachable-proof
redundant: removing it changes no observable behaviour, it is an equivalent mutant rather
than a test weakness. It is kept as documentation of FR-3 ("with `model == ""` only `*`
blocks"). No remediation task: behaving differently would require the guard to be live, and
the report records it so the survivor is not mistaken for an unmeasured hole.

Sampled behaviours: A1, A2, A3, U1, U2, U3, A6's writer (7 of 10); A4 and A5 were already
covered by mutants during the loop (sticky-disable, fail-closed), and U4 is covered by M5's
pre-existing assertion. Coverage corroboration: `authExcludedForModel` 100%,
`collectAvailableByPriority` 100%, `ApplyAuthExcludedModelsMeta` 100%,
`isAuthBlockedForModel` 70% (its other branches belong to pre-existing tests);
package totals 68.2% (`sdk/cliproxy/auth`) and 86.6% (`internal/watcher/synthesizer`).

## Traceability

| Criterion | Tests | Real entry point |
| --------- | ----- | ---------------- |
| AC-1 | `A1`, `A2`, `U1` | Yes — `FillFirstSelector.Pick`, `RoundRobinSelector.Pick`, `pickSingle` |
| AC-2 | `A2` | Yes — round-robin and scheduler parity |
| AC-3 | `A3`, `U2` | Yes — selector with a per-model exclusion and a suffixed request |
| AC-4 | `A4` | Yes — selector and scheduler after an exclusion change |
| AC-5 | `A5`, `U3` | Yes — config-key-shaped and nil-attribute credentials |
| AC-6 | `A6`, `U4` | Yes — synthesizer → `FillFirstSelector.Pick` round trip (two real components, no doubles) |

Untested criteria: none. Tests tracing to nothing: none. Every test named in the list
exists and was executed in this audit (`go test ./sdk/cliproxy/auth/ -run 'Excluded|Exclusion'`
→ 9 top-level tests, 10 subtests, all `PASS`; `TestApplyAuthExcludedModelsMeta*` → `PASS`).

## Suite state

- `go test ./sdk/cliproxy/auth/ -count=1` → 246 passed, 5 failed, 1.6s. The 5 failures are
  the recorded pre-existing baseline (`TestManagerExecuteStream_CodexOnlyDoesNotEnterAntigravityCreditsFallback`,
  `TestManager_Execute_UnauthorizedRefreshFailureFallsBackToNextAuth`,
  `TestManager_Execute_UnauthorizedWithoutRefreshTokenDoesNotCallRefresh`,
  `TestManager_Execute_UnauthorizedRefreshThenRetryStillFailsFallsBackOnce`,
  `TestManagerExecuteHomeStopsWhenDispatchRepeatsTriedAuth`).
- `go test ./internal/watcher/synthesizer/ -count=1` → 44 passed, 2 failed, 0.6s; failures
  are the recorded pre-existing `TestConfigSynthesizer_XAIKeys` and
  `TestConfigSynthesizer_AllProviders`.
- 11 pre-existing failures across the three packages (the full list is in the cycle log's
  baseline entry); an independent stash/unstash comparison by the calling agent confirmed
  the sets are identical with and without the fix. None of them touch credential selection.

## What was not audited

- **Test-first ordering in git**: impossible to audit — the change is uncommitted; the
  branch is at base `109a066d`. All ten behaviours are capped at `LIKELY` for this reason.
- **Independence**: the audit ran in the session that wrote the tests. A fresh-context
  review of the diff was not performed; it is recommended at PR time.
- **Process-level end-to-end path**: no server boot, no HTTP request, no live
  management-API `PATCH` → config reload → routing round trip (finding 1). The reporter's
  live scenario also needs upstream provider credentials, which were not available.
- **Mutation**: deliberate mutants only, scoped to the changed files; no tool-based score.
  A4, A5 and U4 were not re-sampled here (their mutants are in the cycle log).
- **`pickMixed` multi-provider path** (finding 4) and the sole-excluded-candidate error
  behaviour (finding 2): read and reasoned about, not tested.
- **Performance/load**: no criterion, no test, not assessed.
- **`excluded_models_hash`**: the sibling attribute is written but never read for routing;
  consolidation under the new constant was deliberately not done (out of the fix's scope).
