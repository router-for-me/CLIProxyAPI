# Bug Specification: Excluded credentials must be skipped during credential selection

**Bug**: `fallback-disabled-accounts`
**Created**: 2026-09-17
**Source**: `.specify/bugs/fallback-disabled-accounts/assessment.md` (TDD-mode spec for `/speckit.bug.fix`)
**Status**: Draft

## Summary

When a credential declared in `config.yaml` (including `openai-compatibility` provider
entries) is disabled through the management API `PATCH /v0/management/auth-files/status`,
the server does not set `auth.Disabled`. It writes `excluded-models: ["*"]` into
`config.yaml`, reloads, and the synthesizer stores the combined exclusion list on
`auth.Attributes["excluded_models"]`. Nothing reads that attribute when a credential is
evaluated for routing, so a "disabled" config API key remains a fully eligible candidate.
With `routing.strategy: fill-first` the lowest-ID credential is selected first and the
request fails, instead of being routed to an enabled credential.

The required behaviour: a credential whose `excluded_models` attribute matches the
requested model (or contains the `"*"` sentinel) is treated exactly like a disabled
credential — it is never selected as a routing candidate on any selection path.

## Reproduction (failing-test scenario)

1. Two credentials serve the same model; the first (lowest ID) has
   `Attributes["excluded_models"] = "*"` (what a management-API disable of a config API
   key produces); the second (higher ID) has no exclusions.
2. `FillFirstSelector.Pick` for that model currently returns the excluded credential
   (it sorts by ID and returns the first available). Expected: it returns the second.
3. The same scenario through `RoundRobinSelector.Pick` and through the fast-path
   scheduler (`authScheduler.pickSingle`) shows the same defect.

## Acceptance Criteria

- **AC-1**: A credential whose exclusion list contains `"*"` is never selected as a
  candidate; the selectors return the next eligible credential instead.
- **AC-2**: The exclusion is enforced on every selection path — both legacy strategies
  (`FillFirstSelector`, `RoundRobinSelector`) and the fast-path scheduler
  (`pickSingle`) — not just one of them.
- **AC-3**: A per-model exclusion (for example the pattern `gpt-5`) blocks only that
  model. A request for a different model still routes to the credential. A request
  carrying a thinking suffix (`gpt-5(high)`) is matched by its base model name.
- **AC-4**: Eligibility is a function of the current attributes only. Once the exclusion
  is removed (or the credential re-synthesized without it), the credential is eligible
  again — no permanent block, no mutation of the auth.
- **AC-5**: Credentials without an `excluded_models` attribute — file-backed OAuth
  credentials and most config keys — are unaffected. The check stays a no-op in that
  case and adds no observable behaviour change.
- **AC-6**: The attribute key is defined once and shared: the synthesizer writer and the
  selection-time reader both use the same named constant.

## Functional Requirements

- **FR-1**: The exclusion check lives at the single choke point both selection paths
  already share, `isAuthBlockedForModel` in `sdk/cliproxy/auth/selector.go`, and reports
  `blockReasonDisabled` with a zero retry time.
- **FR-2**: The attribute value is comma-separated and already normalized to
  lowercase + trimmed by the writer (`ApplyAuthExcludedModelsMeta`). Matching is exact;
  `"*"` is the only wildcard and no glob matching is implemented.
- **FR-3**: When the requested model is empty, only `"*"` blocks.
- **FR-4**: `AttributeExcludedModels = "excluded_models"` is exported from
  `sdk/cliproxy/auth` and used by both the reader and the writer
  (`internal/watcher/synthesizer/helpers.go`).

## Out of Scope

- Failover on non-retryable upstream statuses (e.g. `401`): the assessment's hypothesis
  (a). This spec covers only the demonstrated candidacy defect.
- Changing the management API's disable response or the on-disk `config.yaml` contract
  (`"via": "config:excluded-models"` stays as-is).
- Glob/prefix pattern support beyond the `"*"` sentinel.
- `internal/translator/` behaviour.

## Verification Commands

Copied verbatim from `.specify/memory/tdd-profile.md`:

- Single test: `go test {file} -run '^{name}$' -v -count=1`
- Package: `go test {file} -count=1`
- Coverage: `go test {file} -cover`

**False-green guard**: `go test {file} -run '^{name}$'` exits 0 when the regex matches no
test. Every single-test run must show `--- PASS: {name}` or `--- FAIL: {name}` in its
`-v` output.

**Baseline red (pre-existing, verified on HEAD `109a066d` before any change)**:
`go test ./sdk/cliproxy/auth/...` already fails 5 unrelated tests, and
`go test ./internal/watcher/synthesizer/...` fails 2 more. The loop scopes its green
signal to the tests it touches plus the unmodified set of pre-existing failures.
