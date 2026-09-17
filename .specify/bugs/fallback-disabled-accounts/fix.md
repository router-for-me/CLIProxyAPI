# Bug Fix: Excluded credentials are skipped during credential selection

- **Slug**: fallback-disabled-accounts
- **Fixed**: 2026-09-17
- **Assessment**: ./assessment.md
- **Status**: applied
- **TDD artifacts**: ./tdd/test-list.md, ./tdd/cycle-log.md, ./tdd/verification.md
  (`PASS_WITH_GAPS`, verified_at `109a066d`; folded into ./test.md)

## Summary

A credential whose `Attributes["excluded_models"]` matches the requested model (or contains
the `"*"` sentinel that the management API writes when a config-declared API key is disabled)
is now blocked exactly like a disabled credential: `isAuthBlockedForModel` returns
`blockReasonDisabled` with a zero retry time. Because that function is the single choke point
both selection paths already use — `collectAvailableByPriority` for the legacy
`FillFirstSelector` / `RoundRobinSelector`, and the model shards built by
`modelScheduler.upsertEntryLocked` / `promoteExpiredLocked` for the fast-path scheduler — one
check fixes both, and a "disabled" config API key is no longer an eligible routing candidate.

## Changes

| File | Change | Notes |
|------|--------|-------|
| `sdk/cliproxy/auth/classification.go` | modified | Added `AttributeExcludedModels = "excluded_models"` to the attribute constants |
| `sdk/cliproxy/auth/selector.go` | modified | Added `authExcludedForModel` and consulted it in `isAuthBlockedForModel` immediately after the `Disabled` check |
| `internal/watcher/synthesizer/helpers.go` | modified | The writer now stores the combined list under `coreauth.AttributeExcludedModels` instead of the raw literal |
| `sdk/cliproxy/auth/selector_test.go` | added tests | Unit table for the exclusion check + six selector tests |
| `sdk/cliproxy/auth/scheduler_test.go` | added tests | Scheduler parity and re-evaluation after an exclusion change |
| `internal/watcher/synthesizer/helpers_test.go` | added test | Writer → reader round trip for the disable sentinel |
| `.specify/bugs/fallback-disabled-accounts/spec.md` | added (TDD mode) | Bug spec: acceptance criteria AC-1..AC-6, requirements FR-1..FR-4 |
| `.specify/bugs/fallback-disabled-accounts/tasks.md` | added (TDD mode) | Behaviour-marked tasks; all 11 ticked by the loop |
| `.specify/bugs/fallback-disabled-accounts/tdd/test-list.md` | added (TDD mode) | 6 acceptance behaviours + 4 unit observations, all `DONE` |
| `.specify/bugs/fallback-disabled-accounts/tdd/cycle-log.md` | added (TDD mode) | Baseline + 6 cycle entries with recorded red/green evidence |
| `.specify/feature.json` | added (TDD mode) | Pins the bug directory as the TDD feature — **must be restored/removed afterwards** (see Follow-ups) |

## Diff Highlights

The core check (`sdk/cliproxy/auth/selector.go`):

```go
// authExcludedForModel reports whether the credential is excluded for the requested
// model. The management API writes the "*" sentinel when a config-declared API key is
// disabled; any other pattern matches the requested model exactly. Only the "*" sentinel
// blocks when the requested model is empty.
func authExcludedForModel(auth *Auth, model string) bool {
	if auth == nil || len(auth.Attributes) == 0 {
		return false
	}
	raw := strings.TrimSpace(auth.Attributes[AttributeExcludedModels])
	if raw == "" {
		return false
	}
	modelKey := canonicalModelKey(model)
	for _, pattern := range strings.Split(raw, ",") {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		if pattern == "*" {
			return true
		}
		if modelKey != "" && pattern == modelKey {
			return true
		}
	}
	return false
}
```

```go
	if auth.Disabled || auth.Status == StatusDisabled {
		return true, blockReasonDisabled, time.Time{}
	}
	if authExcludedForModel(auth, model) {
		return true, blockReasonDisabled, time.Time{}
	}
```

The writer (`internal/watcher/synthesizer/helpers.go:99`):

```go
	if len(combined) > 0 {
		auth.Attributes[coreauth.AttributeExcludedModels] = strings.Join(combined, ",")
	}
```

## Tests Added or Updated

- `sdk/cliproxy/auth/selector_test.go::TestIsAuthBlockedForModel_ExcludedModels` — table of 10
  rows pinning: `"*"` blocks a named model and the empty model with `blockReasonDisabled` +
  zero time; an exact per-model pattern blocks that model only (matched, different model,
  empty model, comma-separated list with a space, thinking-suffixed base model); absent / empty
  / whitespace-only lists are a no-op.
- `sdk/cliproxy/auth/selector_test.go::TestFillFirstSelectorPick_SkipsExcludedAllCredential` —
  `FillFirstSelector.Pick` returns the next eligible credential when the lowest-ID candidate is
  excluded for every model.
- `sdk/cliproxy/auth/selector_test.go::TestRoundRobinSelectorPick_SkipsExcludedAllCredential` —
  round-robin parity: the excluded credential is never picked across repeated picks.
- `sdk/cliproxy/auth/scheduler_test.go::TestSchedulerPick_SkipsExcludedAllCredential` — the
  fast-path scheduler (`pickSingle` via `newSchedulerForTest`) never returns the excluded auth.
- `sdk/cliproxy/auth/selector_test.go::TestFillFirstSelectorPick_PerModelExclusionBlocksOnlyThatModel` —
  a `gpt-5` exclusion blocks `gpt-5` but still routes `gpt-4o` to the credential.
- `sdk/cliproxy/auth/selector_test.go::TestFillFirstSelectorPick_ThinkingSuffixMatchesBaseModelExclusion` —
  a `gpt-5(high)` request is matched by the base-model exclusion.
- `sdk/cliproxy/auth/selector_test.go::TestFillFirstSelectorPick_ExcludedCredentialEligibleAfterExclusionRemoved` —
  clearing the attribute makes the credential eligible again (guard against a permanent block).
- `sdk/cliproxy/auth/scheduler_test.go::TestSchedulerPick_ExcludedCredentialReevaluatedAfterExclusionChange` —
  the scheduler drops the auth when the exclusion is added, restores it when removed, and
  drops it again when re-added (`upsertAuth` between picks).
- `sdk/cliproxy/auth/selector_test.go::TestFillFirstSelectorPick_CredentialWithoutExclusionsUnaffected` —
  a config-key-shaped credential (attributes map without the key) is still selected.
- `internal/watcher/synthesizer/helpers_test.go::TestApplyAuthExcludedModelsMeta_DisableSentinelBlocksSelection` —
  the disable written by the synthesizer is honoured by `FillFirstSelector.Pick` (writer →
  reader round trip; red evidence is the writer-key mutant in `cycle-log.md` cycle 6).

## Local Verification

- `gofmt -w .` then `gofmt -l .` → no output (clean, all touched files included).
- `go build -o test-output ./cmd/server && rm test-output` → `BUILD OK`, artifact removed.
- `go test ./sdk/cliproxy/auth/...` → `FAIL` with exactly the 5 pre-existing baseline failures
  (`TestManagerExecuteStream_CodexOnlyDoesNotEnterAntigravityCreditsFallback`,
  `TestManager_Execute_UnauthorizedRefreshFailureFallsBackToNextAuth`,
  `TestManager_Execute_UnauthorizedWithoutRefreshTokenDoesNotCallRefresh`,
  `TestManager_Execute_UnauthorizedRefreshThenRetryStillFailsFallsBackOnce`,
  `TestManagerExecuteHomeStopsWhenDispatchRepeatsTriedAuth`); the 9 new/changed tests report
  `--- PASS`.
- `go test ./internal/watcher/synthesizer/...` → `FAIL` with exactly the 2 pre-existing baseline
  failures (`TestConfigSynthesizer_XAIKeys`, `TestConfigSynthesizer_AllProviders`); all
  `TestApplyAuthExcludedModelsMeta*` tests report `--- PASS`.
- `go test ./internal/api/handlers/management/...` → `FAIL` with exactly the 4 pre-existing
  baseline failures (`TestRequestCodexTokenCompletionKeepsConcurrentSessionPending`,
  `TestOAuthSessionStoreCompleteKeepsShortLivedSession`, `TestGuardOAuthSessionPendingForSave`,
  `TestPatchPluginEnabledReloadSnapshotRawImmutability`); the disable-path tests in
  `config_apikey_disable_test.go` pass.
- Baseline comparison method: every failing set above was captured on clean HEAD `109a066d`
  before any edit and re-captured after the change; the sets are identical, so the change
  introduced no new failures. The TDD loop's red/green evidence per cycle is in
  `./tdd/cycle-log.md`.

## Deviations from Assessment

The assessment's remediation was implemented as specified, with these notes:

1. **Matching granularity**: the assessment suggested "compared via `canonicalModelKey`,
   ignoring the thinking suffix, with the raw model as a fallback". The implementation compares
   the pattern against `canonicalModelKey(model)` only. `canonicalModelKey` already returns the
   trimmed raw model when no suffix parses, so the fallback is implicit; the one case not
   honoured is a suffixed *pattern* matching a suffixed *request* (e.g. pattern `gpt-5(high)`),
   which the writer never produces — config `excluded-models` entries are plain model ids and
   the disable path writes `"*"`. Recorded in `spec.md` as out of scope.
2. **Scope of tests**: one addition beyond the assessment's test list — the synthesizer
   round-trip test (`helpers_test.go`), which makes the writer/reader constant requirement
   (FR-4/AC-6) observable. No test was removed.
3. **TDD preflight**: the TDD loop's preflight requires a green suite before the first cycle.
   The suite is red at HEAD for unrelated, pre-existing reasons (11 failures across three
   packages, listed above and in `tdd/cycle-log.md`); the loop therefore ran with the failure
   set recorded as the baseline and the green signal scoped to it, mirroring how
   `specs/002-dedicated-model-providers` handled the same repo condition.
4. **Cycle discipline**: cycle 1 bundled one behaviour observed at four levels (unit,
   FillFirst, round-robin, scheduler) instead of splitting the shared choke-point fix across
   mutant-check cycles; cycles 4, 5 and 6 obtained their red evidence through deliberate
   mutants (recorded verbatim in the cycle log), and cycle 5's first mutant exposed a weak
   test that was then strengthened before the code was restored. All recorded in
   `./tdd/cycle-log.md`.

## Follow-ups

- **Restore `.specify/feature.json`**: this run created it to pin
  `.specify/bugs/fallback-disabled-accounts` as the TDD feature; a later step must restore or
  remove it so the spec-kit resolver points at the intended feature again.
- **`tdd.verify` / `speckit.bug.test`**: not run in this session; it is the next step to audit
  the loop's discipline and test strength from cold context and to write `tdd/verification.md`.
- **Multi-provider mixed path**: `pickMixed` with more than one provider selects from the same
  pre-computed ready buckets via `pickReadyAtPriorityLocked`, so the exclusion is enforced
  there too; no dedicated test was added because the bug contract named `pickSingle`.
- **Observability** (assessment risk): a candidate skipped *for exclusion* is indistinguishable
  from one toggled disabled in the `auth_unavailable` / `model_cooldown` error shapes. A
  debug-level log on skip would help operators; deliberately not added to keep the change
  minimal.
- **Hypothesis (a) of the assessment** (non-retryable upstream status such as `401` aborting
  instead of failing over for file-backed auths) is untouched by this fix and still needs
  reporter confirmation before the original report can be closed.
- **Glob patterns**: hand-written wildcards other than `"*"` (e.g. `grok-3-*`) remain
  unenforced; documented in `spec.md` rather than silently approximated.
- **Git**: no commit/push/PR was made in this session; all changes are in the working tree on
  `fix/fallback-disabled-accounts`.
