# Bug Verification: Excluded credentials are skipped during credential selection

- **Slug**: fallback-disabled-accounts
- **Tested**: 2026-09-17
- **Assessment**: ./assessment.md
- **Fix**: ./fix.md
- **Result**: verified
- **TDD verification**: ./tdd/verification.md (verdict `PASS_WITH_GAPS` → `verified`)

## Summary

The bug does not reproduce: credentials carrying `excluded_models` (including the `"*"`
sentinel the management API writes when a config API key is disabled) are skipped by
`FillFirstSelector`, `RoundRobinSelector` and the fast-path scheduler, and are eligible again
once the exclusion is removed. No regressions were introduced — the failure sets of all three
touched packages are byte-identical to their pre-existing baseline. The TDD auditor returned
`PASS_WITH_GAPS` (no `HIGH` smells, all 6 criteria covered; the gaps are evidence strength,
not behavior), which maps to `verified`.

## Checks Performed

| Check | Command / Action | Result | Notes |
|-------|------------------|--------|-------|
| Reproduction (post-fix) | `go test ./sdk/cliproxy/auth/ -run 'Excluded\|Exclusion' -v -count=1` | pass | 9 top-level tests + 10 subtests, all `--- PASS`, 0.7s |
| New / updated tests | same command, plus `go test ./internal/watcher/synthesizer/ -run 'TestApplyAuthExcludedModelsMeta' -v -count=1` | pass | round-trip test `--- PASS`; pre-existing writer tests still pin the wire key |
| Regression suite | `go test ./sdk/cliproxy/auth/ -count=1` | pass (baseline-identical) | 246 passed, 5 failed — all 5 pre-existing (see below) |
| Regression suite | `go test ./internal/watcher/synthesizer/ -count=1` | pass (baseline-identical) | 44 passed, 2 failed — both pre-existing (see below) |
| Regression suite | `go test ./internal/api/handlers/management/ -count=1` | pass (baseline-identical) | 4 pre-existing failures, unchanged |
| Build | `go build -o test-output ./cmd/server && rm test-output` | pass | `BUILD OK`; artifact removed |
| Test-strength audit | `/speckit-tdd-verify` (deliberate mutants, changed files only) | pass | 6 mutants: 5 caught, 1 equivalent survivor (`selection.go:159` redundant guard) — see `tdd/verification.md` |
| Independent regression check | stash/unstash comparison of the three packages by the calling agent | pass | failure sets identical with and without the fix (timings differ) |
| Lint / type-check | `gofmt -l` on the six touched files | pass | empty; `go build` covers type-checking |

### Pre-existing failure baseline (recorded so it is not mistaken for this fix)

Verified on clean HEAD `109a066d` before any edit and confirmed identical after the fix by an
independent stash/unstash run:

- `sdk/cliproxy/auth` (5): `TestManagerExecuteStream_CodexOnlyDoesNotEnterAntigravityCreditsFallback`,
  `TestManager_Execute_UnauthorizedRefreshFailureFallsBackToNextAuth`,
  `TestManager_Execute_UnauthorizedWithoutRefreshTokenDoesNotCallRefresh`,
  `TestManager_Execute_UnauthorizedRefreshThenRetryStillFailsFallsBackOnce`,
  `TestManagerExecuteHomeStopsWhenDispatchRepeatsTriedAuth`.
- `internal/watcher/synthesizer` (2): `TestConfigSynthesizer_XAIKeys`, `TestConfigSynthesizer_AllProviders`.
- `internal/api/handlers/management` (4): `TestGuardOAuthSessionPendingForSave`,
  `TestOAuthSessionStoreCompleteKeepsShortLivedSession`, `TestPatchPluginEnabledReloadSnapshotRawImmutability`,
  `TestRequestCodexTokenCompletionKeepsConcurrentSessionPending`.

None of the 11 touches credential selection, exclusions, or the synthesizer's excluded-model
metadata. The regression signal for this fix is therefore "no NEW failures" plus the new
exclusion tests passing — both hold.

## Output Excerpts

```
--- PASS: TestSchedulerPick_SkipsExcludedAllCredential (0.00s)
--- PASS: TestFillFirstSelectorPick_SkipsExcludedAllCredential (0.00s)
--- PASS: TestFillFirstSelectorPick_PerModelExclusionBlocksOnlyThatModel (0.00s)
--- PASS: TestFillFirstSelectorPick_ThinkingSuffixMatchesBaseModelExclusion (0.00s)
--- PASS: TestFillFirstSelectorPick_ExcludedCredentialEligibleAfterExclusionRemoved (0.00s)
--- PASS: TestSchedulerPick_ExcludedCredentialReevaluatedAfterExclusionChange (0.00s)
--- PASS: TestFillFirstSelectorPick_CredentialWithoutExclusionsUnaffected (0.00s)
--- PASS: TestRoundRobinSelectorPick_SkipsExcludedAllCredential (0.00s)
--- PASS: TestIsAuthBlockedForModel_ExcludedModels (0.00s)
ok  github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth  0.672s
```

Mutant that would have shipped without the tests (deliberate-mutant check, restored after):
`pattern == "*"` → `pattern == "#"` at `sdk/cliproxy/auth/selector.go:157` failed 6 tests
across all three selection entry points; the writer-key mutant failed the round trip
(`Pick() auth.ID = "config-key-a", want "config-key-b"`).

## Residual Risks

- **No process-level end-to-end test.** The fix is proven at the selection API
  (`FillFirstSelector` / `RoundRobinSelector` / `pickSingle`) and through the synthesizer →
  selector round trip, but the reporter's live scenario (management-API `PATCH`, config
  reload, a real request against upstream credentials) was not exercised; it needs a running
  server and provider credentials. Recorded as remediation task T012.
- **Test-first ordering is `LIKELY`, not `PROVEN`**: the work is uncommitted, so git cannot
  corroborate that tests preceded code. The cycle log records the red evidence; task T016
  asks for the artifacts to be committed with the fix.
- **Assessment hypothesis (a) remains open**: if the reporter's credentials were file-backed
  auth files rather than config keys, the user-visible failure would come from the
  retry/failover layer (e.g. a `401` outside the retryable set), which this fix does not
  address. The code-supported defect in hypothesis (b) is fixed.
- **Hand-written glob patterns** other than `"*"` (e.g. `grok-3-*`) remain unenforced, by
  documented contract in `spec.md`.
- **Audit independence**: `tdd.verify` ran in the session that wrote the fix; its
  `PASS_WITH_GAPS` verdict was reached by re-reading every artifact and re-running every
  mutant, but a fresh-context review at PR time is still advisable.

## Recommendation

Close the bug — the symptom no longer reproduces, the fix holds on every selection path, and
the failure baseline is unchanged. Remediation tasks T012–T016 in `tasks.md` cover the
non-blocking gaps (end-to-end coverage, clarity, multi-provider coverage, artifact commit);
they do not gate closure. Confirm with the reporter whether the affected credentials are
config-declared API keys (hypothesis (b), fixed here) or file-backed auth files (hypothesis
(a), still open).
