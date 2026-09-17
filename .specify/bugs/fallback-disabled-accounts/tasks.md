# Tasks: Excluded credentials must be skipped during credential selection

**Input**: Design documents from `.specify/bugs/fallback-disabled-accounts/`
(`spec.md`, `assessment.md`, `tdd/test-list.md`)

**Prerequisites**: `spec.md` (required). `plan.md` does not exist for this bug — the TDD
plan ran `outer-only`, so `tdd/test-list.md` is the design source for the behaviors.

**Tests**: Included and MANDATORY (constitution Principle III). Test tasks precede the
implementation task for the same behavior and carry the behavior marker `[A#]` / `[U#]`
in brackets; the TDD loop ticks a task only when it can read that marker. There is no
unticked non-behavioral work in this bug: the loop runs `gofmt` and the build itself.

**Organization**: Tasks are grouped by the acceptance criterion they serve.

## Format: `[ID] [P?] [Criterion] [Behavior] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Criterion]**: The acceptance criterion from `spec.md` the task serves
- **[Behavior]**: The test-list id the task implements; the TDD loop reads this marker
- Include exact file paths in descriptions

## Phase 1: Reproduction and core fix (AC-1, AC-2)

### Tests

- [X] T001 [P] [AC-1] [U1] Unit table test for `isAuthBlockedForModel` with `excluded_models`: `"*"` blocks a named model and blocks when `model == ""`, with `blockReasonDisabled` and a zero retry time; absent/empty attribute is a no-op — `sdk/cliproxy/auth/selector_test.go::TestIsAuthBlockedForModel_ExcludedModels`
- [X] T002 [P] [AC-1] [A1] `FillFirstSelector.Pick` returns the next eligible credential when the lowest-ID candidate is excluded for every model — `sdk/cliproxy/auth/selector_test.go::TestFillFirstSelectorPick_SkipsExcludedAllCredential`
- [X] T003 [P] [AC-2] [A2] `RoundRobinSelector.Pick` and `authScheduler.pickSingle` apply the same exclusion as `FillFirstSelector.Pick` — `sdk/cliproxy/auth/selector_test.go::TestRoundRobinSelectorPick_SkipsExcludedAllCredential`, `sdk/cliproxy/auth/scheduler_test.go::TestSchedulerPick_SkipsExcludedAllCredential`

### Implementation

- [X] T004 [AC-1] [AC-2] [A1] [A2] [U1] Block with `blockReasonDisabled` in `isAuthBlockedForModel` (`sdk/cliproxy/auth/selector.go`) when the `AttributeExcludedModels` attribute contains the `"*"` sentinel. Tests T001–T003 must be RED first. (The `AttributeExcludedModels = "excluded_models"` declaration in `classification.go` is the minimal symbol the tests need to compile; no other code was written before the red.)

**Checkpoint**: the disable sentinel produced by `PATCH /v0/management/auth-files/status` is honoured on both selection paths.

## Phase 2: Per-model semantics (AC-3, AC-5)

### Tests

- [X] T005 [P] [AC-3] [A3] [U2] Per-model exclusion blocks only the matched model; a different model still routes to the credential — `sdk/cliproxy/auth/selector_test.go::TestFillFirstSelectorPick_PerModelExclusionBlocksOnlyThatModel`; extend T001's table with the matched/unmatched/empty-model rows
- [X] T006 [P] [AC-5] [A5] [U3] A credential with no `excluded_models` attribute is unaffected — `sdk/cliproxy/auth/selector_test.go::TestFillFirstSelectorPick_CredentialWithoutExclusionsUnaffected`; extend T001's table with absent/whitespace rows

### Implementation

- [X] T007 [AC-3] [A3] [U2] Match exclusion patterns against `canonicalModelKey(model)` so a thinking-suffixed request matches its base model; skip empty patterns; no-op when the attribute is absent.

**Checkpoint**: a per-model exclusion is model-aware and never widens into a full credential block.

## Phase 3: Durability and writer/reader agreement (AC-4, AC-6)

### Tests

- [X] T008 [P] [AC-3] [A3] A thinking-suffixed request (`gpt-5(high)`) is matched by the base-model exclusion — `sdk/cliproxy/auth/selector_test.go::TestFillFirstSelectorPick_ThinkingSuffixMatchesBaseModelExclusion`
- [X] T009 [P] [AC-4] [A4] Eligibility returns when the exclusion is removed — `sdk/cliproxy/auth/selector_test.go::TestFillFirstSelectorPick_ExcludedCredentialEligibleAfterExclusionRemoved` and `sdk/cliproxy/auth/scheduler_test.go::TestSchedulerPick_ExcludedCredentialReevaluatedAfterExclusionChange`
- [X] T010 [P] [AC-6] [A6] An auth synthesized with per-key `["*"]` via `ApplyAuthExcludedModelsMeta` is skipped by `FillFirstSelector.Pick` — `internal/watcher/synthesizer/helpers_test.go::TestApplyAuthExcludedModelsMeta_DisableSentinelBlocksSelection`

### Implementation

- [X] T011 [AC-6] [A6] Use `coreauth.AttributeExcludedModels` in `internal/watcher/synthesizer/helpers.go` instead of the raw literal, so writer and reader share one key.

**Checkpoint**: the runtime decision is a pure function of the current attribute, and the writer's key is the reader's key.

---

## Phase 4: TDD remediation

From `tdd/verification.md` (verdict `PASS_WITH_GAPS`, `verified_at` 109a066d). The core fix is
complete and every behavior is `DONE`; these tasks close the gaps the audit recorded. No task
here blocks the fix itself.

- [ ] T012 [AC-2] [A2] Add a process-level integration test for the disable path: route a request through `Manager.pickNext` (fast path and legacy) with a config-synthesized auth whose attribute is `"*"` and assert the eligible credential is selected. Closes verification finding 1 (MED). Proof: `go test ./sdk/cliproxy/auth/ -run '^TestManagerPickNext_SkipsExcludedCredential$' -v -count=1` shows `--- PASS`.
- [ ] T013 [AC-1] [A1] Pin the sole-excluded-candidate case: with every candidate excluded, selection returns `auth_unavailable` (it never falls back to an excluded credential). Closes verification finding 2 (LOW). Proof: `go test ./sdk/cliproxy/auth/ -run '^TestFillFirstSelectorPick_AllExcludedReturnsUnavailable$' -v -count=1` shows `--- PASS`.
- [ ] T014 [AC-3] [U2] Make the trim rule visible in the unit table: rename the row `excluded_model_matches_within_comma_separated_list` (or comment `selector_test.go:364`) so the space after the comma is not removed as cosmetic — mutant M4 showed this row is the only test that catches a dropped trim. Closes verification finding 3 (LOW). Proof: `go test ./sdk/cliproxy/auth/ -run '^TestIsAuthBlockedForModel_ExcludedModels$' -v -count=1` shows `--- PASS` with the row still present.
- [ ] T015 [AC-2] [A2] Cover the multi-provider fast path: `pickMixed` with two providers where one provider's only credential is excluded still returns the eligible provider's credential. Closes verification finding 4 (LOW). Proof: `go test ./sdk/cliproxy/auth/ -run '^TestSchedulerPickMixed_SkipsExcludedCredential$' -v -count=1` shows `--- PASS`.
- [ ] T016 — Commit the TDD artifacts (`.specify/bugs/fallback-disabled-accounts/`: `spec.md`, `tasks.md`, `tdd/test-list.md`, `tdd/cycle-log.md`, `tdd/verification.md`) together with the fix, so test-before-code ordering is corroborable in history and future audits can grade `PROVEN` instead of `LIKELY` (verification "Test-first evidence"). Owner: the calling agent (this session ran `--no-commit` by instruction). Proof: `git log --stat` on the fix commit lists the `tdd/` artifacts.
