---
feature: fallback-disabled-accounts
loop: outside-in
baseline_commit: 109a066d
baseline_state: red
---

# Cycle Log: Excluded credentials must be skipped during credential selection

## Baseline (planned_at 109a066d)

- **Suite commands**: `go test ./sdk/cliproxy/auth/...` (selection package),
  `go test ./internal/watcher/synthesizer/...` (writer package),
  `go test ./internal/api/handlers/management/...` (disable plumbing)
- **Result**: `red` — pre-existing failures, all verified on clean HEAD `109a066d` before
  any change was made:
  - `sdk/cliproxy/auth` (5): `TestManagerExecuteStream_CodexOnlyDoesNotEnterAntigravityCreditsFallback`,
    `TestManager_Execute_UnauthorizedRefreshFailureFallsBackToNextAuth`,
    `TestManager_Execute_UnauthorizedWithoutRefreshTokenDoesNotCallRefresh`,
    `TestManager_Execute_UnauthorizedRefreshThenRetryStillFailsFallsBackOnce`,
    `TestManagerExecuteHomeStopsWhenDispatchRepeatsTriedAuth`.
  - `internal/watcher/synthesizer` (2): `TestConfigSynthesizer_XAIKeys` ("auth count = 0, want 1"),
    `TestConfigSynthesizer_AllProviders` ("expected 6 auths, got 5").
  - `internal/api/handlers/management` (4): OAuth session store tests and
    `TestPatchPluginEnabledReloadSnapshotRawImmutability`.
- **Baseline build**: `go build -o test-output ./cmd/server` → OK.
- **Action required before trusting the loop's red/green signal**: none. Every failure above
  is unrelated to credential selection and predates the change; the loop's green signal is
  "the new tests pass and no failures beyond this recorded set appear".
- **False-green guard**: `go test {file} -run '^{name}$'` exits 0 when the name matches
  nothing; every single-test run below asserts `--- PASS: {name}` (or `--- FAIL: {name}`)
  appears in the `-v` output.

<!-- The loop appends a cycle entry per behavior (A*/U*) here. Do not write cycle entries manually. -->

## Cycle 1 — A1, A2, U1, U3: the `"*"` exclusion blocks the credential on every selection path

- **Tests** (all written and observed failing before any behaviour code):
  - `sdk/cliproxy/auth/selector_test.go::TestIsAuthBlockedForModel_ExcludedModels` (new; `"*"` rows for a named model and for the empty model, plus the no-op rows)
  - `sdk/cliproxy/auth/selector_test.go::TestFillFirstSelectorPick_SkipsExcludedAllCredential` (new)
  - `sdk/cliproxy/auth/selector_test.go::TestRoundRobinSelectorPick_SkipsExcludedAllCredential` (new)
  - `sdk/cliproxy/auth/scheduler_test.go::TestSchedulerPick_SkipsExcludedAllCredential` (new)
- **Compile step**: the tests reference `AttributeExcludedModels`, which did not exist.
  `go test ./sdk/cliproxy/auth/ -run '^TestIsAuthBlockedForModel_ExcludedModels$' -v -count=1`
  → `sdk/cliproxy/auth/selector_test.go:328:35: undefined: AttributeExcludedModels` (build failed).
  Per the playbook, the minimal declaration the test needs to run was added
  (`AttributeExcludedModels = "excluded_models"` in `classification.go`); no behaviour code.
- **Red** (after the declaration, before the fix):
  - `go test ./sdk/cliproxy/auth/ -run '^TestIsAuthBlockedForModel_ExcludedModels$' -v -count=1`
    → `selector_test.go:359: blocked = false, want true` for
    `excluded_all_blocks_named_model` and `excluded_all_blocks_empty_model` (the two no-op rows passed).
  - `go test ./sdk/cliproxy/auth/ -run '^TestFillFirstSelectorPick_SkipsExcludedAllCredential$' -v -count=1`
    → `selector_test.go:389: Pick() auth.ID = "a", want "b"`.
  - `go test ./sdk/cliproxy/auth/ -run '^TestRoundRobinSelectorPick_SkipsExcludedAllCredential$' -v -count=1`
    → `selector_test.go:409: Pick() #0 auth.ID = "a", want "b"`.
  - `go test ./sdk/cliproxy/auth/ -run '^TestSchedulerPick_SkipsExcludedAllCredential$' -v -count=1`
    → `scheduler_test.go:188: pickSingle() #0 auth.ID = "a-excluded", want "b-eligible"`.
- **Green**: added `authExcludedAllModels` (split on `,`, trimmed pattern `"*"`) and consulted it
  in `isAuthBlockedForModel` right after the `Disabled` check, returning
  `blockReasonDisabled` with a zero retry time. All four single tests → `--- PASS`;
  `go test ./sdk/cliproxy/auth/...` → exactly the 5 pre-existing baseline failures, no new ones.
- **Refactor**: none needed; the helper sits with the other attribute helpers (`authPriority`,
  `authWebsocketsEnabled`) and created no duplication.
- **Notes**: this cycle bundles one behaviour observed at four levels (unit, `FillFirstSelector`,
  `RoundRobinSelector`, scheduler) instead of opening a later mutant-check cycle for the
  scheduler — the four reds are genuine pre-implementation failures, which is stronger evidence.
  U3's no-op rows passed before and after (boundary observation of the same check). U2
  (per-model rows) is deferred to cycle 2, where the `"*"`-only implementation fails them.
- **Commit**: none — this session runs `--no-commit`; the parent agent owns git.

## Cycle 2 — A3 (per-model half), U2: an exclusion blocks only the model it names

- **Tests** (written and observed failing before the implementation change):
  - `sdk/cliproxy/auth/selector_test.go::TestIsAuthBlockedForModel_ExcludedModels` — new rows
    `excluded_model_blocks_only_that_model`, `excluded_model_does_not_block_another_model`,
    `excluded_model_does_not_block_empty_model`, `excluded_model_matches_within_comma_separated_list`
  - `sdk/cliproxy/auth/selector_test.go::TestFillFirstSelectorPick_PerModelExclusionBlocksOnlyThatModel` (new)
- **Red**:
  - `go test ./sdk/cliproxy/auth/ -run '^TestIsAuthBlockedForModel_ExcludedModels$' -v -count=1`
    → `selector_test.go:383: blocked = false, want true` for
    `excluded_model_blocks_only_that_model` and `excluded_model_matches_within_comma_separated_list`
    (the two `"*"` rows and the no-op rows kept passing).
  - `go test ./sdk/cliproxy/auth/ -run '^TestFillFirstSelectorPick_PerModelExclusionBlocksOnlyThatModel$' -v -count=1`
    → `selector_test.go:432: Pick() for excluded model auth.ID = "a", want "b"`.
- **Green**: `authExcludedAllModels` became `authExcludedForModel(auth, model)` — the same split
  and trim, plus an exact match of the pattern against the requested model, skipped when the
  model is empty (only `"*"` blocks then). `isAuthBlockedForModel` passes the model through.
  All single tests → `--- PASS`; `go test ./sdk/cliproxy/auth/...` → same 5 pre-existing failures, no new ones.
- **Refactor**: the rename folded the cycle-1 helper into the final shape instead of adding a
  second parallel check; no duplication, no further refactor needed.
- **Notes**: the requested model is still compared raw (`pattern == model`); the base-model
  match for thinking-suffixed requests is cycle 3's red.
- **Commit**: none (`--no-commit`).

## Cycle 3 — A3 (suffix half), U2: a thinking-suffixed request matches the base-model exclusion

- **Tests** (written and observed failing before the implementation change):
  - `sdk/cliproxy/auth/selector_test.go::TestFillFirstSelectorPick_ThinkingSuffixMatchesBaseModelExclusion` (new)
  - `sdk/cliproxy/auth/selector_test.go::TestIsAuthBlockedForModel_ExcludedModels` — new row
    `excluded_model_matches_thinking_suffix_base_model`
- **Red**:
  - `go test ./sdk/cliproxy/auth/ -run '^TestFillFirstSelectorPick_ThinkingSuffixMatchesBaseModelExclusion$' -v -count=1`
    → `selector_test.go:468: Pick() auth.ID = "a", want "b"`.
  - `go test ./sdk/cliproxy/auth/ -run '^TestIsAuthBlockedForModel_ExcludedModels$' -v -count=1`
    → `selector_test.go:389: blocked = false, want true` for `excluded_model_matches_thinking_suffix_base_model`
    (all other rows still green).
- **Green**: the pattern is compared against `canonicalModelKey(model)` instead of the raw
  model, which strips the thinking suffix (`gpt-5(high)` → `gpt-5`). Single tests → `--- PASS`;
  `go test ./sdk/cliproxy/auth/...` → same 5 pre-existing failures, no new ones.
- **Refactor**: none needed; the comparison moved onto the existing `canonicalModelKey` helper
  the neighbouring `ModelStates` lookup already uses.
- **Notes**: patterns themselves are not suffix-stripped — the writer never produces suffixed
  patterns (config `excluded-models` entries are plain model ids and the disable path writes
  `"*"`), and the contract in `spec.md` is exact match on the canonical requested model plus
  the `"*"` sentinel.
- **Commit**: none (`--no-commit`).

## Cycle 4 — A4: removing the exclusion restores eligibility (no sticky block)

- **Tests** (new):
  - `sdk/cliproxy/auth/selector_test.go::TestFillFirstSelectorPick_ExcludedCredentialEligibleAfterExclusionRemoved`
  - `sdk/cliproxy/auth/scheduler_test.go::TestSchedulerPick_ExcludedCredentialReevaluatedAfterExclusionChange`
    (excluded → eligible after removal → excluded again, with `upsertAuth` between picks)
- **First run**: both passed — the decision is a pure read of the current attribute, which
  cycles 1–3 already implement. No natural red exists for this behaviour after those cycles,
  so the playbook's deliberate-mutant check supplied the red:
  - **Mutant**: `isAuthBlockedForModel`'s exclusion branch also set `auth.Disabled = true`
    (the tempting "disable it once" shortcut).
  - **Red with the mutant**:
    `go test ./sdk/cliproxy/auth/ -run '^(TestFillFirstSelectorPick_ExcludedCredentialEligibleAfterExclusionRemoved|TestSchedulerPick_ExcludedCredentialReevaluatedAfterExclusionChange)$' -v -count=1`
    → `selector_test.go:481: Pick() after removal auth.ID = "b", want "a"` and
    `scheduler_test.go:226: pickSingle() after removal auth.ID = "b-eligible", want "a-excluded"`.
  - The mutant was then removed exactly (the function is back to a pure read).
- **Green**: both tests → `--- PASS`; `go test ./sdk/cliproxy/auth/...` → same 5 pre-existing
  failures, no new ones.
- **Refactor**: none needed.
- **Notes**: no test-after admission — the mutant check is the playbook's prescribed response to
  a first-run pass, and the failure output above is real recorded output.
- **Commit**: none (`--no-commit`).

## Cycle 5 — A5, U3 (whitespace row): a credential without an exclusion is unaffected

- **Tests** (new / extended):
  - `sdk/cliproxy/auth/selector_test.go::TestFillFirstSelectorPick_CredentialWithoutExclusionsUnaffected` (new)
  - `sdk/cliproxy/auth/selector_test.go::TestIsAuthBlockedForModel_ExcludedModels` — new row
    `whitespace_only_exclusion_list_is_not_blocked`
- **First run**: passed — a missing/empty attribute is already a no-op in the cycles 1–3
  implementation. Deliberate-mutant check supplied the red:
  - **Mutant 1**: `authExcludedForModel` defaulted an empty/missing list to `"*"`
    (`if raw == "" { raw = "*" }`).
  - **Finding**: the unit row for the empty list failed
    (`selector_test.go:395: blocked = true, want false`), but the selector test still passed —
    its lowest-ID credential had a nil attributes map, which the early `len(...) == 0` guard
    short-circuits before the mutant is reached. Per the playbook the weak test was rewritten:
    the credential now carries a realistic config-key map (`auth_kind: apikey`) with no
    `excluded_models` key, matching what `ApplyAuthExcludedModelsMeta` produces for config keys.
  - **Red with the mutant after the rewrite**:
    `go test ./sdk/cliproxy/auth/ -run '^(TestFillFirstSelectorPick_CredentialWithoutExclusionsUnaffected|TestIsAuthBlockedForModel_ExcludedModels)$' -v -count=1`
    → `selector_test.go:477: Pick() auth.ID = "b", want "a"` and
    `selector_test.go:395: blocked = true, want false`.
  - The mutant was then removed exactly.
- **Green**: both tests → `--- PASS`; `go test ./sdk/cliproxy/auth/...` → same 5 pre-existing
  failures, no new ones.
- **Refactor**: none needed; the tests now reuse the same `AttributeExcludedModels` constant and
  the table's existing shape.
- **Commit**: none (`--no-commit`).

## Cycle 6 — A6, U4: the synthesizer's disable write is the routing reader's key

- **Test** (new): `internal/watcher/synthesizer/helpers_test.go::TestApplyAuthExcludedModelsMeta_DisableSentinelBlocksSelection`
  — config key A synthesized with per-key `["*"]` (the write `PATCH /auth-files/status` performs),
  config key B plain; `FillFirstSelector.Pick` must return B.
- **First run**: passed — the writer's literal and the reader's constant already had the same
  value (`excluded_models`). Deliberate-mutant check supplied the red:
  - **Mutant**: `ApplyAuthExcludedModelsMeta` wrote the list under a different key
    (`"excluded_models_mutant"`), simulating a writer/reader key mismatch.
  - **Red with the mutant**:
    `go test ./internal/watcher/synthesizer/ -run '^TestApplyAuthExcludedModelsMeta_DisableSentinelBlocksSelection$' -v -count=1`
    → `helpers_test.go:245: Pick() auth.ID = "config-key-a", want "config-key-b"`.
  - The mutant was then removed exactly.
- **Green / refactor (structural, same cycle)**: `internal/watcher/synthesizer/helpers.go` now
  writes the list under `coreauth.AttributeExcludedModels` instead of the raw literal, so the
  writer and the reader share one key. Existing synthesizer tests still pin the wire value
  (they assert the `"excluded_models"` key): `TestApplyAuthExcludedModelsMeta`,
  `TestApplyAuthExcludedModelsMeta_OAuthMergeWritesCombinedModels` → `--- PASS`.
  `go test ./internal/watcher/synthesizer/...` → same 2 pre-existing failures
  (`TestConfigSynthesizer_XAIKeys`, `TestConfigSynthesizer_AllProviders`), no new ones.
- **Notes**: U4 is covered by the existing `TestApplyAuthExcludedModelsMeta` (wire key) plus this
  cycle's round-trip test. The `excluded_models_hash` sibling key is intentionally left as a
  literal — the contract covers the key the reader consults.
- **Commit**: none (`--no-commit`).
