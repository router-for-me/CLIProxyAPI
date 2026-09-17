# Bug Assessment: Fallback routing selects disabled credentials instead of skipping them

- **Slug**: fallback-disabled-accounts
- **Created**: 2026-09-17
- **Source**: pasted text
- **Verdict**: likely valid, needs reproduction
- **Severity**: high

## Report (verbatim or summarized)

> The Fallback strategy is checking accounts sequentially and stopping at the first match,
> even when that account is disabled, instead of skipping disabled accounts to find an enabled one.
>
> When CLIProxyAPI's Fallback routing evaluates a model like
> `kilo/dots-studio/dots-3-note-preview:free`:
>
> 1. It finds all 8 accounts that have this model
> 2. It tries the first one (which happens to be disabled)
> 3. It fails and stops, instead of continuing to the next account
>
> Expected behavior: Skip disabled accounts and try the enabled one.
>
> The reporter states Quotio pushes disabled state through the management API
> `PATCH /auth-files/status` and that the defect is in CLIProxyAPI's routing logic, which
> "doesn't filter disabled accounts when building the fallback candidate list".
>
> Workarounds proposed by the reporter: switch to round-robin, delete the disabled accounts,
> or report upstream.

No URL was supplied; nothing was fetched.

## Symptom

With `routing.strategy: fill-first` ("Fallback" in the reporting client) and several
credentials that serve the same model, a request is dispatched to a credential that has been
marked disabled and fails, instead of being routed to an enabled credential. Expected: disabled
credentials are never candidates, so the request succeeds on an enabled credential.

## Reproduction

Reporter-supplied scenario (not yet reproduced locally):

1. Configure 8 credentials (accounts) that all serve `kilo/dots-studio/dots-3-note-preview:free`.
2. Mark 7 of them disabled (in the reporter's case via `PATCH /v0/management/auth-files/status`).
3. Set `routing.strategy: fill-first`.
4. Send a request for that model.
5. Observed: the request is dispatched to a disabled credential and fails.

Still required:

- [NEEDS CLARIFICATION: are the 8 credentials **file-backed auth files** (JSON under `auths/`,
   e.g. `sdk/auth/kilo.go`-style OAuth records) or **config-declared API keys**
   (`config.yaml` provider entries, including `openai-compatibility`)? The two are disabled by
   completely different mechanisms and only one of them is broken today — see below.]
- [NEEDS CLARIFICATION: the exact status/body returned when the request "fails". `401` is not in
   the retryable set, so a disabled upstream key returning `401` would abort the request with no
   failover attempt, which would look identical to "stops at the first match".]
- [NEEDS CLARIFICATION: whether round-robin was actually exercised or only predicted to work
   ("should work correctly" in the report).]

## Suspected Code Paths

Candidate selection (the "fallback candidate list" the report refers to):

- `sdk/cliproxy/auth/selector.go:294` — `FillFirstSelector.Pick`. This is the fill-first /
  "Fallback" strategy. It calls `getAvailableAuths` and returns `available[0]`.
- `sdk/cliproxy/auth/selector.go:219` — `getAvailableAuths`, shared by **both**
  `RoundRobinSelector.Pick` (`:257`) and `FillFirstSelector.Pick` (`:294`), via
  `collectAvailableByPriority` (`:199`).
- `sdk/cliproxy/auth/selector.go:305` — `isAuthBlockedForModel`. Returns
  `blocked = true, blockReasonDisabled` when `auth.Disabled || auth.Status == StatusDisabled`
  (`:309-311`), so disabled credentials are dropped before priority bucketing.
- `sdk/cliproxy/auth/scheduler.go:489` — `upsertAuthLocked`. The fast-path scheduler deletes any
  auth with `auth.Disabled` from its shards (`:495`).
- `sdk/cliproxy/auth/scheduler.go:671` — `upsertEntryLocked` maps blocked credentials to
  `scheduledStateDisabled`; `rebuildIndexesLocked` (`:891-898`) only admits
  `scheduledStateReady` into the ready buckets.
- `sdk/cliproxy/auth/conductor.go:4838`, `:4955` — legacy and fast-path candidate building both
  skip `candidate.Disabled` explicitly.
- `sdk/cliproxy/auth/conductor.go:2190` — `Manager.Update` re-upserts the auth into the scheduler
  (`:2224-2226`), so a disable toggle is propagated.

Disable plumbing from the management API:

- `internal/api/handlers/management/auth_files.go:1255` — `PatchAuthFileStatus` (`/auth-files/status`).
- `internal/api/handlers/management/auth_files.go:1425` — `applyAuthDisabledState` sets
  `auth.Disabled = true` **and** `auth.Status = StatusDisabled` for file-backed auths
  (line `:1350`).
- `internal/api/handlers/management/auth_files.go:1319` — `IsConfigAPIKeyAuth` branch. Config-
  declared API keys are **not** given `Disabled`; instead `toggleConfigAPIKeyExcludedAll`
  (`internal/api/handlers/management/config_apikey_disable.go:33`) writes
  `excluded-models: ["*"]` into `config.yaml`, saves, reloads, and returns
  `{"via": "config:excluded-models"}`.
- `internal/watcher/synthesizer/helpers.go:99` — the synthesizer stores the combined exclusion
  list as `auth.Attributes["excluded_models"]` with the comment "so that routing can read it at
  runtime".
- `internal/watcher/synthesizer/file.go:113-119`, `:160-176` — file-backed auths load
  `"disabled": true` into both `Disabled` and `Status`.

## Root Cause Hypothesis

Confidence: **medium**, split across two mutually exclusive explanations that the current report
does not disambiguate.

**(a) If the 8 accounts are file-backed auth files, the stated root cause is contradicted by the
code.** Disabled file auths carry `Disabled = true` and `Status = StatusDisabled`, and every
selection path excludes them: `isAuthBlockedForModel` (`sdk/cliproxy/auth/selector.go:309`) drops
them for both round-robin and fill-first, the scheduler removes them outright
(`sdk/cliproxy/auth/scheduler.go:495`), and both legacy candidate loops check `candidate.Disabled`
(`sdk/cliproxy/auth/conductor.go:4839`, `:4955`). In that case the observable "tries the disabled
account and stops" would have to come from the **failover/retry** layer — e.g. the upstream
returning a status outside the retryable set (`403/408/500/502/503/504`), so no second credential
is attempted — and not from candidate-list construction.

**(b) If the 8 accounts are config-declared API keys, there is a real, code-supported defect.**
Disabling them never sets `auth.Disabled`; it only rewrites config to
`excluded-models: ["*"]`. The synthesizer then parks that list in
`auth.Attributes["excluded_models"]`, but **nothing in the codebase ever reads it**: a repo-wide
search for `excluded_models` finds only the writer (`internal/watcher/synthesizer/helpers.go:95`,
`:99`), the file-metadata parser, and tests. The `ExcludedModels` config fields
(`internal/config/config.go:492`, `:562`, `:623`; `internal/config/vertex_compat.go:38`) are only
normalized and diffed, and `internal/registry`, `internal/runtime` and the SDK contain no
exclusion enforcement. Consequently a "disabled" config API key remains a fully eligible
candidate, and `fill-first` deterministically selects the lowest-ID such credential first — which
is precisely the reported behaviour. Round-robin would fail the same way, which is consistent with
the reporter only *predicting* (not testing) that round-robin works.

Hypothesis (b) is the one with a demonstrable defect in the routing path and is the basis for the
remediation below.

## Proposed Remediation

**Preferred**: enforce model exclusions when a credential is evaluated for candidacy, so a
credential excluded for the requested model is blocked exactly like a disabled one.

Add a check in `isAuthBlockedForModel` (`sdk/cliproxy/auth/selector.go:305`), immediately after the
existing `auth.Disabled || auth.Status == StatusDisabled` check:

- Parse `auth.Attributes["excluded_models"]` (comma-separated, as written by
  `internal/watcher/synthesizer/helpers.go:99`).
- Return `blocked = true, blockReasonDisabled` when the list contains the disable sentinel `"*"`,
  or when it contains the requested model (compared via `canonicalModelKey`, ignoring the thinking
  suffix, with the raw model as a fallback).
- When `model == ""`, only the `"*"` sentinel blocks.

`isAuthBlockedForModel` is the single choke point used by both the legacy selectors
(`collectAvailableByPriority`) and the scheduler's entry-state computation
(`sdk/cliproxy/auth/scheduler.go:671`, `:716`), so one change fixes both paths and keeps the
canonical "blocked" representation that `internal/thinking`-style shared logic relies on.

Introduce a named constant for the attribute key (e.g. `AttributeExcludedModels` in
`sdk/cliproxy/auth/`) and use it from both the writer
(`internal/watcher/synthesizer/helpers.go`) and the new reader, instead of two raw string literals.

**Alternatives**:

- Make the config-API-key disable path set a real `Disabled` flag on the synthesized auth instead
  of (or in addition to) `excluded-models: ["*"]`. Cleaner intent, but it changes the management
  API's response contract (`"via": "config:excluded-models"`) and would still leave the
  per-key `excluded-models` feature unimplemented for its documented purpose.
- Implement exclusion only in the config-API-key branch of `PatchAuthFileStatus`. Narrower, but
  leaves file-backed auths' per-account `excluded-models` (parsed at
  `internal/watcher/synthesizer/file.go:167`) equally unenforced.

**Files likely to change**:

- `sdk/cliproxy/auth/selector.go` (blocking check in `isAuthBlockedForModel`)
- `sdk/cliproxy/auth/auth.go` or the existing attribute-constant home (new `AttributeExcludedModels`)
- `internal/watcher/synthesizer/helpers.go` (use the constant)

**Tests to add or update**:

- `sdk/cliproxy/auth/selector_test.go` — `FillFirstSelector.Pick` skips an auth whose
  `Attributes["excluded_models"]` is `"*"` and returns the next enabled credential.
- `sdk/cliproxy/auth/selector_test.go` — the same auth is returned once the exclusion is removed
  (regression guard against a permanent block).
- `sdk/cliproxy/auth/selector_test.go` — a per-model exclusion (`"gpt-5"`) blocks only that model;
  another model still routes to the credential.
- `sdk/cliproxy/auth/scheduler_test.go` — the fast-path scheduler (`pickSingle` / `pickMixed`)
  never returns an auth excluded by `"*"`.
- `sdk/cliproxy/auth/selector_test.go` — round-robin parity: both strategies skip the excluded
  credential.

## Risks & Considerations

- **Semantic widening**: treating a per-model exclusion as a full credential block would be wrong;
  the check must be model-aware (only `"*"`, or an exact match on the requested/base model).
- **Attribute absence**: file-backed OAuth auths and many config keys will have no
  `excluded_models` attribute at all. The check must be a no-op in that case — the vast majority
  of auths — so it must not add per-request cost beyond a map lookup plus a short string split.
- **`"?*"` / pattern semantics**: `NormalizeExcludedModels` lowercases and trims, and `"*"` is the
  only wildcard the management API writes today. If users hand-write glob patterns, an exact-match
  implementation will not honour them; that behaviour must be documented rather than silently
  approximated.
- **Retry interaction**: if the true user-visible failure is a non-retryable upstream status
  (hypothesis (a)), this fix removes the disabled credential from consideration but does not add
  failover for `401`. Worth confirming with the reporter before closing.
- **No API/format/migration impact**: the change is internal to credential selection; no config
  schema, HTTP contract, or on-disk format changes.
- **Observability**: blocked-by-exclusion credentials currently surface as `auth_unavailable` or,
  when all candidates are cooldown-blocked, `model_cooldown`. Reusing `blockReasonDisabled` keeps
  the existing error shapes, but the operator loses the distinction between "toggle disabled" and
  "excluded for this model". Consider a debug-level log when a candidate is skipped for exclusion.

## Open Questions

- [NEEDS CLARIFICATION: are the 8 kilo accounts file-backed auth files or config-declared API
  keys?] This decides whether hypothesis (a) or (b) applies.
- [NEEDS CLARIFICATION: what HTTP status/body does the failing request return, and is
  `request-retry` / `max-retry-credentials` at its default (3 / 0)?]
- [NEEDS CLARIFICATION: does `kilo` here resolve to the first-class `kilo` executor, or to an
  `openai-compatibility` entry or plugin?]
- [NEEDS CLARIFICATION: was round-robin actually tested against the same 7-disabled/1-enabled
  set, or only assumed to work?]
