# Bug Fix PR: skip credentials excluded for the requested model

## Summary

Credential selection now honours the `excluded_models` auth attribute, so a config-declared API
key that was disabled through the management API is no longer a routing candidate.

## Problem

`PATCH /v0/management/auth-files/status` does not set `auth.Disabled` for config-declared API keys
(provider key entries, `openai-compatibility` entries). It rewrites `config.yaml` with
`excluded-models: ["*"]`, saves and reloads
(`internal/api/handlers/management/auth_files.go:1319-1347`,
`internal/api/handlers/management/config_apikey_disable.go:12-33`).

The synthesizer stored that list on the auth as the `excluded_models` attribute
(`internal/watcher/synthesizer/helpers.go:99`) — annotated *"so that routing can read it at
runtime"* — but nothing in the codebase ever read it. Eligibility is decided by
`isAuthBlockedForModel`, which only checked `auth.Disabled` / `auth.Status == StatusDisabled`.
A config-key "disable" sets neither, so the credential stayed eligible and `fill-first`
deterministically picked the lowest-ID one first, failing the request instead of falling through to
an enabled credential.

File-backed OAuth auths were never affected: they set both `Disabled` and `Status`
(`internal/watcher/synthesizer/file.go:113-119`, `:160-176`), which the selectors already filter.

## Changes

| File | Change | Notes |
|------|--------|-------|
| `sdk/cliproxy/auth/classification.go` | modified | added `AttributeExcludedModels = "excluded_models"` |
| `sdk/cliproxy/auth/selector.go` | modified | new `authExcludedForModel`; `isAuthBlockedForModel` returns `blockReasonDisabled` for excluded credentials |
| `internal/watcher/synthesizer/helpers.go` | modified | writer uses the new constant instead of a raw literal |
| `sdk/cliproxy/auth/selector_test.go` | tests | 8 new tests incl. a 10-row table |
| `sdk/cliproxy/auth/scheduler_test.go` | tests | 2 new fast-path scheduler tests |
| `internal/watcher/synthesizer/helpers_test.go` | test | writer/reader round-trip for the disable sentinel |

`isAuthBlockedForModel` is the single choke point used by both the legacy selectors
(`collectAvailableByPriority`, shared by round-robin and fill-first) and the fast-path scheduler
shards, so one check covers every selection path.

Semantics: the `"*"` sentinel blocks for every model, including when no model is supplied; any
other pattern matches the requested model's canonical key (also matching the base model of a
thinking suffix such as `gpt-5(high)` against an exclusion of `gpt-5`); a credential with no
`excluded_models` attribute is untouched. No glob matching beyond `"*"`.

## Verification

- `go build -o test-output ./cmd/server && rm test-output` → build OK
- `go test ./sdk/cliproxy/auth/ -run 'Excluded|Exclusion' -v -count=1` → 9 tests / 10 subtests, all PASS
- `go test ./sdk/cliproxy/auth/` → 246 pass, 5 fail; `./internal/watcher/synthesizer/` → 44 pass, 2 fail; `./internal/api/handlers/management/` → 4 fail
- Those 11 failures are **pre-existing on the base commit `109a066d`**: stashing the six tracked
  changes and re-running produces a byte-identical failure set (only timings differ), so this
  change introduces **no new failures**.
- TDD audit (`tdd/verification.md`): verdict `PASS_WITH_GAPS` → `verified`. 6 criteria covered via
  the spec's named entry points, 0 high-severity test smells, 5 of 6 deliberate mutants caught, the
  one survivor judged behaviourally equivalent.

## Not covered

- Hypothesis (a) from the assessment is untouched: if a credential legitimately returns a
  non-retryable upstream status (`401` is not in the retryable set), no failover occurs. Unrelated
  to this fix and still needing reporter confirmation.
- No process-level end-to-end test drives the management disable endpoint through to a real
  `Manager.pickNext`; the mediator is covered by unit tests at the selection API instead
  (tracked as T012 in `tasks.md`).
- Glob patterns other than `"*"` are not honoured.

Assessment: `.specify/bugs/fallback-disabled-accounts/assessment.md`
No `Closes #n` line: GitHub Issues are disabled on this fork, so the tracked report could not be
filed. See `.specify/bugs/fallback-disabled-accounts/issue-draft.md`.

## Notes for reviewers

- The branch's single commit is titled `chore(bugs): record ... artifacts` but also carries the
  source fix. A `git add` targeting the gitignored `sdk/cliproxy` directory exited non-zero, so the
  intended `fix(auth): ...` commit never ran and the six already-staged source files were swept into
  the following artifacts commit. History was deliberately not rewritten/force-pushed; the code
  change is the six files under `sdk/cliproxy/auth/` and `internal/watcher/synthesizer/`.
- Base is `snapshot` (the branch this work forked from, at `109a066d`), not the fork's default
  `sync`, which carries two unrelated commits that would otherwise appear in this PR.
