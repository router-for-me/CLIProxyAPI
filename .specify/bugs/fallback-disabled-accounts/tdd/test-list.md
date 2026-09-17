---
feature: fallback-disabled-accounts
loop: outside-in
profile: .specify/memory/tdd-profile.md
spec_criteria: 6
planned_at: 109a066d
updated_at: 109a066d
suite_baseline: red
---

# Test List: Excluded credentials must be skipped during credential selection

Planned by `/speckit.tdd-plan outer-only` on 2026-09-17 (HEAD `109a066d`).
`plan.md` does not exist in this bug directory, so the acceptance behaviors below are
derived from `spec.md` alone. The bug fix contract also requires unit-level observations
one level below the entry points; those are listed in the section after the outer table
and were appended when `tdd.run` opened the loop (the test-list template's rule for
behaviors discovered mid-loop).

## Outer loop: acceptance behaviors

The feature's real entry points are the selection API of `sdk/cliproxy/auth`: the
`Selector` implementations (`FillFirstSelector.Pick`, `RoundRobinSelector.Pick`) and the
fast-path scheduler (`authScheduler.pickSingle`). There is no HTTP route of its own — the
request path reaches these through `Manager.pickNext` — so the acceptance runner is the
Go package runner from the profile, and the list says so explicitly.

| id  | behavior | traces | kind | state | test |
| --- | -------- | ------ | ---- | ----- | ---- |
| A1 | A credential excluded for every model (`Attributes["excluded_models"] = "*"`) is never returned by `FillFirstSelector.Pick`; the next eligible credential is returned instead | AC-1, FR-1, FR-2 | example | DONE | `sdk/cliproxy/auth/selector_test.go::TestFillFirstSelectorPick_SkipsExcludedAllCredential` |
| A2 | The same exclusion is enforced on every selection path: `RoundRobinSelector.Pick` skips it and the fast-path scheduler (`authScheduler.pickSingle`) never returns it | AC-2, FR-1 | example | DONE | `sdk/cliproxy/auth/selector_test.go::TestRoundRobinSelectorPick_SkipsExcludedAllCredential`, `sdk/cliproxy/auth/scheduler_test.go::TestSchedulerPick_SkipsExcludedAllCredential` |
| A3 | A per-model exclusion (`gpt-5`) blocks only that model: a request for `gpt-5` routes to the other credential, a request for a different model still routes to the excluded one, and a request carrying a thinking suffix (`gpt-5(high)`) is matched by its base model name | AC-3, FR-2, FR-3 | example | DONE | `sdk/cliproxy/auth/selector_test.go::TestFillFirstSelectorPick_PerModelExclusionBlocksOnlyThatModel` (cycle 2), `sdk/cliproxy/auth/selector_test.go::TestFillFirstSelectorPick_ThinkingSuffixMatchesBaseModelExclusion` (cycle 3) |
| A4 | A credential excluded now is eligible again once the exclusion is removed (`Attributes["excluded_models"]` cleared, or the auth re-upserted without it) — on both the legacy selector and the scheduler | AC-4 | example | DONE | `sdk/cliproxy/auth/selector_test.go::TestFillFirstSelectorPick_ExcludedCredentialEligibleAfterExclusionRemoved`, `sdk/cliproxy/auth/scheduler_test.go::TestSchedulerPick_ExcludedCredentialReevaluatedAfterExclusionChange` (cycle 4; red via deliberate mutant, `cycle-log.md` cycle 4) |
| A5 | A credential with no `excluded_models` attribute (file-backed OAuth and most config keys) is unaffected and is still returned as the first eligible credential | AC-5 | example | DONE | `sdk/cliproxy/auth/selector_test.go::TestFillFirstSelectorPick_CredentialWithoutExclusionsUnaffected` (cycle 5; strengthened after the first mutant was not caught, `cycle-log.md` cycle 5) |
| A6 | The disable written by the config synthesizer (`ApplyAuthExcludedModelsMeta` with per-key `["*"]`) is readable by the selection path: the synthesized auth is skipped by `FillFirstSelector.Pick` | AC-6, FR-4 | example | DONE | `internal/watcher/synthesizer/helpers_test.go::TestApplyAuthExcludedModelsMeta_DisableSentinelBlocksSelection` (cycle 6; red via writer-key mismatch mutant, `cycle-log.md` cycle 6) |

## Unit-level observations (appended when the loop opened)

No `plan.md` exists, so `tdd.plan` ran `outer-only` and did not derive an inner-loop
section from components. These observations sit one level below the A behaviors, inside
the same choke point named by FR-1, and are exercised in the A1–A5 cycles' evidence.

| id  | behavior | traces | kind | state | test |
| --- | -------- | ------ | ---- | ----- | ---- |
| U1 | `isAuthBlockedForModel` reports `blocked, blockReasonDisabled, zero` for a `"*"` exclusion — for a named model and for the empty model (`model == ""`) | AC-1, FR-1, FR-3 | example | DONE | `sdk/cliproxy/auth/selector_test.go::TestIsAuthBlockedForModel_ExcludedModels` (rows `excluded_all_blocks_named_model`, `excluded_all_blocks_empty_model`) |
| U2 | Exact matching uses the canonical (suffix-stripped) model key and blocks only the matched model; with `model == ""` only `"*"` blocks | AC-3, FR-2, FR-3 | example | DONE | `sdk/cliproxy/auth/selector_test.go::TestIsAuthBlockedForModel_ExcludedModels` (rows `excluded_model_blocks_only_that_model`, `excluded_model_does_not_block_another_model`, `excluded_model_does_not_block_empty_model`, `excluded_model_matches_within_comma_separated_list`, `excluded_model_matches_thinking_suffix_base_model`) |
| U3 | An absent, empty, or whitespace-only exclusion list is a no-op: the credential is not blocked | AC-5, FR-2 | example | DONE | `sdk/cliproxy/auth/selector_test.go::TestIsAuthBlockedForModel_ExcludedModels` (rows `no_exclusion_attribute_is_not_blocked`, `empty_exclusion_list_is_not_blocked`, `whitespace_only_exclusion_list_is_not_blocked`) |
| U4 | The writer and the reader share `AttributeExcludedModels`; the runtime attribute key stays `excluded_models` (matching `extractExcludedModelsFromMetadata`'s on-disk key) | AC-6, FR-4 | example | DONE | `internal/watcher/synthesizer/helpers_test.go::TestApplyAuthExcludedModelsMeta` (existing, pins the wire key) and `::TestApplyAuthExcludedModelsMeta_DisableSentinelBlocksSelection` (cycle 6 round trip) |

## Invariants and edge cases still to place

- Concurrent selection while an exclusion is edited is not covered: the check reads the
  attribute map without locking, the same way `authPriority` and `authWebsocketsEnabled`
  already do. No requirement, no test.
- A hand-written glob pattern other than `"*"` (e.g. `grok-3-*`) is not honoured; the
  contract is exact match plus the `"*"` sentinel. Recorded as out of scope in `spec.md`.

## Out of scope

- Failover on non-retryable upstream statuses (`401`): assessment hypothesis (a), no
  requirement in this bug spec, no test.
- The management API disable response and the `config.yaml` contract
  (`"via": "config:excluded-models"`): unchanged, no test.
- `internal/translator/`: untouched by contract.

## Verification commands

Copied verbatim from `.specify/memory/tdd-profile.md` at planning time:

- Single test: `go test {file} -run '^{name}$' -v -count=1`
- Package: `go test {file} -count=1`
- Full suite: `go test ./...`
- Coverage: `go test {file} -cover`

**False-green guard (from the profile)**: `go test {file} -run '^{name}$'` exits 0 when the
regex matches nothing. Every single-test run must show `--- PASS: {name}` or
`--- FAIL: {name}` in its `-v` output.

**Suite baseline is `red`** and was verified on clean HEAD `109a066d` before any change:

- `go test ./sdk/cliproxy/auth/...` → 5 pre-existing failures, unrelated:
  `TestManagerExecuteStream_CodexOnlyDoesNotEnterAntigravityCreditsFallback`,
  `TestManager_Execute_UnauthorizedRefreshFailureFallsBackToNextAuth`,
  `TestManager_Execute_UnauthorizedWithoutRefreshTokenDoesNotCallRefresh`,
  `TestManager_Execute_UnauthorizedRefreshThenRetryStillFailsFallsBackOnce`,
  `TestManagerExecuteHomeStopsWhenDispatchRepeatsTriedAuth`.
- `go test ./internal/watcher/synthesizer/...` → 2 pre-existing failures, unrelated:
  `TestConfigSynthesizer_XAIKeys`, `TestConfigSynthesizer_AllProviders`.
- `go test ./internal/api/handlers/management/...` → 4 pre-existing failures, unrelated
  (OAuth session store and plugin snapshot tests).

The loop's green signal is the new tests plus "no new failures beyond this recorded set".
