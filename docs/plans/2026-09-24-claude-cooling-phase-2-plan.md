# Phase 2 — Claude Model-Level Cooling & Scoped Overage — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.
> Execution mode (user's standing choice): **subagent-driven**, fresh implementer per task → spec review → quality review → fix round → re-review, working directly on `main`.

**Goal:** Implement Phase 2 of `docs/plans/2026-09-22-nixllm-upstream-releases-design.md` (lines 76-84): overage-aware Retry-After parsing in the Claude executor, pool-wide per-model cooldown fail-fast, and scoped overage credential state.

**Architecture:** Three independent features that meet at `MarkResult`/`pickNextMixed`. (1) The Claude executor's error classifier grows header-awareness and stamps `statusErr.retryAfter` (the field+`RetryAfter()` method ALREADY exist on `statusErr`, openai_compat_executor.go:945-958 — `retryAfterFromError` in conductor_cooldown.go:1487 picks it up with zero MarkResult changes). (2) Overage-only responses suppress Retry-After and instead flow to `MarkResult` as `*auth.Error{Code:"overage"}` via a preserved-code path in `resultErrorFromError`. (3) A new manager-level aggregate maps (provider, canonical model) → pool deadline when ≥2 and ≥half of a provider's credentials are cooling for the same model; selection fail-fasts through the existing `newModelCooldownError` before any per-credential rotation.

**Tech Stack:** Go 1.26, logrus, gjson, existing gin-less conductor/auth packages.

**Upstream reference (study, don't port blindly):** upstream commit `44eaef0009f8464fd04db7ce4747a61244b7922e` "feat(claude): support model-level cooling and scope overage rate limits" (closes #5915, 12 files) and its `internal/runtime/executor/helps/claude_ratelimit.go`. NixLLM re-implements natively per the roadmap decision — smaller, KISS, no Fable/7d_oi/5h fuzz machinery unless cheap. Upstream's header vocabulary IS the API contract (verified from production traffic shape):
- `anthropic-ratelimit-unified-status`, `anthropic-ratelimit-unified-5h-status`, `anthropic-ratelimit-unified-7d-status`
- `anthropic-ratelimit-unified-overage-status`, `anthropic-ratelimit-unified-overage-disabled-reason`
- Values: `allowed` | `allowed_warning` | `rejected`; utilization headers carry 0.0..1.0 floats.

---

## Verified anchors (Phase-1 session recon; re-verify each before editing — past plans had stale line numbers)

- `internal/runtime/executor/claude_executor_request.go:274-280` — `classifyClaudeUpstreamError(statusCode int, body []byte) error` builds `statusErr{code,msg}`; special-case `claudeEntitlementError` for fast-mode credits at 429.
- Non-stream call site: `claude_executor_execute.go:236` — `return resp, classifyClaudeUpstreamError(httpResp.StatusCode, b)` (has `httpResp` in scope, headers available).
- Stream call sites: `claude_executor_stream.go:232` (headers in scope), `:215` (decode-failure statusErr — leave), error events `:438` `(malformed stream data` — leave, no HTTP response semantics).
- CountTokens: find the classify/classification call site in `claude_executor_tokens.go` yourself; treat identically if it has an `*http.Response` in scope.
- `statusErr` (openai_compat_executor.go:945-958): fields `code, msg, retryAfter *time.Duration`; methods `Error()`, `StatusCode() int`, `RetryAfter() *time.Duration`. **This type is shared across executors — never change its shape; only populate `retryAfter` from Claude sites.**
- `claudeFastRequestError` / `claudeFastDirectResponseError` (claude_executor_fast_error.go): fast path returns upstream bodies verbatim, deliberately unaffected. Do not touch.
- Reference parser precedent: `internal/auth/claude/anthropic_auth.go:98-116` `parseClaudeRetryAfter(resp *http.Response)` — parses `Retry-After` (duration-suffix + `http.ParseTime` fallback) then `Retry-After-Ms`. The executor package must NOT import internal/auth/claude; port the parsing shape into `helps`.
- Conductor consumption: `conductor_cooldown.go:1487-1504` `retryAfterFromError` — `errors.As` for `RetryAfter() *time.Duration`. `applyAuthFailureState` (`:1870+`) and the model-level 429 branch (`:866-890`, `if result.RetryAfter != nil { next = now.Add(*result.RetryAfter) } else { quota ladder }`) already honor it. **F1's only job is to make the executor produce errors implementing that interface.**
- `resultErrorFromError` (conductor_cooldown.go:1355-1382): clones `*auth.Error` when `errors.As` finds one, else wraps with `&Error{Message: err.Error()}`; then pins request-scoped/connection-lifecycle Codes, and backfills `HTTPStatus` via `statusCodeFromError` (`:1333-1345`, any error with `StatusCode() int`). **This is where the overage Code gets set (Task 4).**
- Per-model cooling writer: `conductor_cooldown.go` MarkResult block `:786+` — `modelKey := canonicalModelKey(result.Model)` (`:744`); 429 branch `:866`: honors `result.RetryAfter`, else `quotaCooldownAfterFailure(state.Quota, now)`; sets `state.Quota = QuotaState{Exceeded:true, Reason:"quota", NextRecoverAt:next, BackoffLevel}`; `shouldSuspendModel` / `setModelQuota` registry updates follow.
- `canonicalModelKey` (selector.go:350-363): TrimSpace + `thinking.ParseSuffix(model).ModelName` — THE canonical key for model identity. Use it everywhere.
- Selection entry: `conductor_selection.go:1874+` `pickNextMixed` — Home short-circuit first (`:1876`), then plugin/legacy or compound-route branch, then the eligibleProviders loop and the scheduler fast path (`m.scheduler.pickMixedWithStrategy`, `:1956`). The pool fail-fast goes right after the eligibleProviders loop, before the `disallowFreeAuthFromMetadata` line.
- `newModelCooldownError(model, provider, resetIn)` (selector.go:245-256) — the existing fail-fast error (becomes 429+Retry-After upstream; reuse, don't invent).
- Atomic-toggle pattern: `conductor_cooldown.go:23-70` — package-level `atomic.Bool` + `SetX(...)` exported setter + `service_auth.go:363-364` call from `applyRetryConfig` (this is also where our new toggle wires; note `applyRetryConfig` is called on config apply/hot-reload).
- `CooldownStateRecord` (cooldown_state.go:19-30) with `AuthID string`; `CooldownStateSnapshot()` (conductor_cooldown.go:615) feeds the dashboard + `alerts_runner.go` provider-cooldown detector. Pool-level record = this struct with empty `AuthID`.
- Fake-executor harness: reuse the `claudeCancellationTestExecutor` pattern (conductor_claude_cancellation_test.go:27-100) — `NewManager(nil,nil,NoopHook{})`, `SetRetryConfig(0,0,0)`, RegisterExecutor, `registry.GetGlobalRegistry().RegisterClient`, `manager.Register`. Conductor tests live in package `auth`; executor tests in package `executor` (no cross-import in tests: executor package imports cliproxyauth types fine).
- Config: `RoutingConfig` (internal/config/config_types.go:290+) already carries `CooldownWait` + `Retry` blocks; `*bool` pointer fields with nil-default behavior exist (e.g. `:30` `Enabled *bool`). `config.example.yaml` routing block ~:252-300 holds commented examples (cooldown-wait ~:285, our phase-1 retry block ~:292).

## Plan-level decisions (locked; reviewers enforce these)

1. **Retry-After parsing lives in `internal/runtime/executor/helps/`** (AGENTS.md helper rule). It must NOT read bodies — headers only. It must NOT import `internal/auth/claude`.
2. **Overage-only rejection ⇢ NO Retry-After.** When the unified headers declare an overage-only rejection, the parser returns nil so the per-credential ladder stays untouched; overage is an account cap handled by Task 4's scoped state, per the design ("an account cap, not a wait hint").
3. **Overage signal transport = `*auth.Error.Code == "overage"`.** `resultErrorFromError` gets one added branch: if the source error implements `interface{ OverageRejected() bool }` (or is wrapped in a type that does) → `resultErr.Code = "overage"` (keeping HTTPStatus backfill). No `Result` struct change, works for execute/count/stream paths uniformly. `Code=="overage"` must not be misclassified: verify `isRequestInvalidError`/lifecycle matchers don't touch it (they key on JSON bodies / specific codes).
4. **Overage horizon = 7 days** (`OverageCooldownHorizon = 7 * 24 * time.Hour`), model-scoped on `state.Quota.Reason = "overage"`. Existing per-model selection machinery (NextRetryAfter/Quota.Exceeded) then skips the credential for that model — no new selection predicate needed.
5. **Pool threshold: ≥2 credentials AND ≥half** of the non-disabled credentials of that provider are cooling on the same canonical model. Deadline = max contributing `NextRetryAfter`. Lazy expiry (no sweeper goroutine).
6. **Fail-fast placement = once, in `pickNextMixed`, after the eligibleProviders loop** — NOT inside scheduler internals, NOT in legacy adapters (legacy path is exercised only when plugin scheduler present; acceptable: check also at the top of `pickNextMixedLegacy` — actually NO: keep single site; legacy deployments keep old behavior; document the limitation in the toggle comment).
   Correction — design wants behavior parity for all non-Home selections: put the check in `pickNextMixed` right after the `eligibleProviders` loop AND at the top of `pickNextMixedLegacy` (after Home+plugin guards). Two small helpers, one behavior.
7. **Config toggle `routing.pool-model-cooldown`**, `*bool`, nil/enabled = on. Wire via `applyRetryConfig` → new `coreauth.SetPoolModelCooldownEnabled(bool)`. Pool cooldown recording stops entirely when off.
8. **Plan split for tests:** files `internal/runtime/executor/helps/claude_ratelimit_test.go`, `claude_executor_ratelimit_test.go`, `sdk/cliproxy/auth/overage_code_test.go`, `sdk/cliproxy/auth/model_pool_cooldown_test.go`, `sdk/cliproxy/auth/pool_fail_fast_test.go`. Everything deterministic (pass `now time.Time` explicitly; no goroutines in the aggregate).
9. **AGENTS.md constraints** apply as always: no new timeouts, no translator changes, English comments, gofmt (per-file, never tree-wide while another agent works), build verification before commit, `Co-Authored-By: Claude Code <noreply@anthropic.com>`.

---

### Task 1: Claude rate-limit header helpers (helps package)

**Files:**
- Create: `internal/runtime/executor/helps/claude_ratelimit.go`
- Test: `internal/runtime/executor/helps/claude_ratelimit_test.go`

**Step 1: Write failing tests** — table-driven, all via `http.Header{}` literals:
1. `ParseClaudeRetryAfterHeaders`: `Retry-After: 12` → 12s; `Retry-After: <http-date>` → ~until(t) (inject `now`); `Retry-After-Ms: 1500` → 1.5s; both present → `Retry-After` wins; garbage → nil; empty headers → nil. Signature `func ParseClaudeRetryAfterHeaders(h http.Header, now time.Time) *time.Duration`.
2. `IsClaudeOverageOnlyRejection`: overage-status `rejected` with 5h+7d `allowed` → true; overage-status `rejected` with 5h `rejected` → false (real shared rejection, Retry-After applies); overage `disabled-reason` set + missing statuses + healthy utilization (`...-5h-utilization: 0.0`, `...-7d-utilization: 0.0`) → true; missing utilization on an unmentioned window → false (upstream's conservatism); no overage headers at all → false; overage `allowed` → false.
3. `ClaudeSharedWindowRejected`: any of unified/5h/7d status == `rejected` → true, else false.

**Step 2: Run** `go test ./internal/runtime/executor/helps/ -run TestClaudeRatelimit -v` → FAIL (undefined).

**Step 3: Implement** the three exported functions. Header reads must be case-insensitive (`textproto.CanonicalMIMEHeaderKey` or iterate `h.Values`); trim spaces, lowercase enum compares. `ParseClaudeRetryAfterHeaders` returns nil if `IsClaudeOverageOnlyRejection` is true (documented in the doc comment). Reuse nothing from `internal/auth/claude` (copy the ~15-line parse shape).

**Step 4: Run tests** → PASS. `gofmt -l internal/runtime/executor/helps/` → empty.

**Step 5: Commit** `feat(helps): claude rate-limit overage header helpers` (+ Co-Authored-By trailer).

---

### Task 2: Thread headers through the Claude error classifier

**Files:**
- Modify: `internal/runtime/executor/claude_executor_request.go` (`classifyClaudeUpstreamError`)
- Modify: `internal/runtime/executor/claude_executor_execute.go` (call site ~:236), `claude_executor_stream.go` (call site ~:232), `claude_executor_tokens.go` (find its classify site)
- Test: `internal/runtime/executor/claude_executor_ratelimit_test.go`

**Step 1: Write failing tests** (package `executor`):
1. `classifyClaudeUpstreamError(429, headers with Retry-After: 30, body)` → returned error is `statusErr` with `RetryAfter()` == 30s pointer.
2. Same but headers carry overage-only rejection (no Retry-After header) → `RetryAfter()` nil.
3. 429 without rate-limit headers → nil (default ladder preserved).
4. 500 with Retry-After header → retryAfter still set when status ≥400 (header wins for any non-2xx where it exists; document this choice — upstream honors the header on 429 only for the ladder but we pass any non-2xx hint through; the conductor's branch for 429 is where it's consumed).
5. CountTokens classification (if reachable in a unit test) mirrors #1.

**Step 2: Run** → FAIL (signature mismatch).

**Step 3: Implement.** Change signature to `classifyClaudeUpstreamError(statusCode int, headers http.Header, body []byte) error`; inside, after building `err := statusErr{...}` and the fast-mode-credits special case, do:
```go
if ra := helps.ParseClaudeRetryAfterHeaders(headers, time.Now()); ra != nil {
    err.retryAfter = ra
}
```
Update ALL call sites (execute/stream/tokens + `fable_ratelimit_test.go`-era tests that call it compile-wise — fix signatures). Keep `wrapClaudeFastRequestError` paths untouched (fast = direct response).

**Step 4:** `go test ./internal/runtime/executor/ ./internal/runtime/executor/helps/ -count=1` — note the 3 known pre-existing environmental failures (X-Stainless-Os) are unrelated; everything else green. Build verify.

**Step 5: Commit** `feat(claude): parse retry-after hints into upstream errors`.

---

### Task 3: Overage error type in the executor

**Files:**
- Create: `internal/runtime/executor/claude_overage_error.go` (type + constructor, ~40 lines)
- Modify: `internal/runtime/executor/claude_executor_request.go` (classifier emits it)
- Test: `internal/runtime/executor/claude_executor_ratelimit_test.go` (extend)

**Step 1: Failing tests:** overage-only 429 → error implements `StatusCode() int` (delegates 429) AND `OverageRejected() bool` (true); unwraps to statusErr (`errors.As` finds `*statusErr`); non-overage 429 → NOT overage; spend-cap body shapes: 403 body error.message containing `spend cap` or 429 containing `usage limit` (word-set: `"usage limit"`, `"spend cap"`, `"credit balance"`) → overage true when there is NO usable `Retry-After` header (throttling answers keep the hint and never get the flag).

**Step 2: Run** → FAIL.

**Step 3: Implement:**
```go
type claudeOverageError struct{ statusErr }
func (e *claudeOverageError) OverageRejected() bool { return e != nil }
func (e *claudeOverageError) Unwrap() error         { return &e.statusErr }
```
Classifier: after Task 2's retryAfter assignment, `if ra == nil && overageIndicated(statusCode, headers, body) { return &claudeOverageError{err} }`. `overageIndicated` = `helps.IsClaudeOverageOnlyRejection(headers)` OR (status in {403,429} AND body keyword above AND `ParseClaudeRetryAfterHeaders(...)==nil`). Keep single-return discipline in `classifyClaudeUpstreamError`.

**Step 4:** executor tests green, build verify, gofmt.

**Step 5: Commit** `feat(claude): mark overage-only rejections on executor errors`.

---

### Task 4: Overage code through resultErrorFromError + scoped overage state in MarkResult

**Files:**
- Modify: `sdk/cliproxy/auth/conductor_cooldown.go` (`resultErrorFromError` ~:1355; MarkResult 429 model branch ~:866; add `overageRejectedFromError` helper near `statusCodeFromError` ~:1333)
- Test: `sdk/cliproxy/auth/overage_code_test.go`

**Step 1: Failing tests** (package `auth`, direct unit tests on the helpers + one MarkResult integration):
1. `resultErrorFromError(overageErr)` (local minimal type implementing `OverageRejected() bool` + `StatusCode() int`) → `Code == "overage"`, `HTTPStatus == 429`.
2. Plain 429 error → Code NOT "overage".
3. MarkResult integration (fake executor harness): upstream 429 carrying an overage-rejected error → `auth.ModelStates[model].Quota.Reason == "overage"`, `Quota.Exceeded == true`, `NextRecoverAt ≈ now+7d`, `NextRetryAfter == NextRecoverAt`, registry model suspended with reason "overage".
4. Throttling 429 (plain) → Reason "quota" (unchanged).

**Step 2: Run** → FAIL.

**Step 3: Implement.**
```go
const OverageCooldownHorizon = 7 * 24 * time.Hour

// overageRejectedFromError reports whether the executor marked this failure
// as an overage/spend-cap rejection rather than ordinary throttling.
func overageRejectedFromError(err error) bool {
    type overageMarker interface{ OverageRejected() bool }
    var m overageMarker
    return errors.As(err, &m) && m != nil && m.OverageRejected()
}
```
- In `resultErrorFromError`: after the Lifecycle block, `if overageRejectedFromError(err) { resultErr.Code = "overage" }` (request-scoped classification stays authoritative — put the overage branch AFTER, and skip overwriting if Code already set to request_scoped).
- In MarkResult's model 429 branch: before the ladder, `if result.Error != nil && result.Error.Code == "overage" { next = now.Add(OverageCooldownHorizon) ... Quota{Exceeded:true, Reason:"overage", NextRecoverAt: next} ... suspendReason="overage"; shouldSuspendModel=true; setModelQuota=true }`. Honor disableCooling exactly like the existing branch.
- Cross-check: `isRequestInvalidError` must be false for `Code=="overage"` errors (read it), `looksLikeQuotaForbidden` unaffected; `quotaCooldownAfterFailure` untouched.
- NOTE: `Result.Error` is built by `resultErrorFromError` at every conductor call site — verify via grep that overage errors reaching execute/count/stream MarkResult all pass through it (they do: same `resultErrorFromError(errExec)` shape). If any site builds Result differently, flag it in the report rather than improvising.

**Step 4:** `go test ./sdk/cliproxy/auth/ ./internal/runtime/executor/ -count=1` green; full-package build verify.

**Step 5: Commit** `feat(auth): scope overage rejections to model quotas`.

---

### Task 5: Pool-model cooldown aggregate

**Files:**
- Create: `sdk/cliproxy/auth/model_pool_cooldown.go`
- Test: `sdk/cliproxy/auth/model_pool_cooldown_test.go`

**Step 1: Failing tests** (pure unit, explicit `now`):
Construct the aggregate directly, key = (provider, canonical model):
```go
type modelPoolCooldowns struct {
    mu      sync.Mutex
    entries map[string]modelPoolCooldownEntry // key: provider + "\x00" + model
}
type modelPoolCooldownEntry struct {
    deadline  time.Time
    updatedAt time.Time
}
// recordPoolModelCooldown(providers []string, model, authID string, cooling time.Time, totalNonDisabled int, now time.Time)
//   - providers: the provider keys this credential contributes to
//   - cooling: that credential's NextRetryAfter for this model (zero => clears its contribution)
// poolModelCooldownBlock(providers []string, model string, now time.Time) (time.Time, bool)
```
Table cases with `now` fixed:
- 2 of 3 cooling → no block (below half).
- 2 of 4 → block (half ≥ half AND ≥2). 3 of 4 → block. 1 of 2 → no block (needs ≥2). 2 of 2 → block.
- Deadline = max(NextRetryAfter of contributors), NOT wall-clock from record time.
- Expired entry (now > deadline) → not blocked, entry deleted on read (lazy expiry).
- Clearing: when a success arrives for a contributor (cooling zero / success path), the credential's contribution resets; pool falls below threshold → entry dropped.

Implementation notes: the aggregate must NOT scan `m.auths` itself — MarkResult computes the count and passes it in (keeps this type pure/testable; count = non-disabled same-provider auths currently cooling for this model incl. the current one). Contribution tracking: keep `contributions map[authID]time.Time` per entry so clears are idempotent.

**Step 2: Run** → FAIL. **Step 3: implement.** **Step 4:** green + `go vet`.

**Step 5: Commit** `feat(auth): pool-model cooldown aggregate`.

---

### Task 6: Wire pool cooldown into MarkResult + selection fail-fast + snapshot + toggle

**Files:**
- Modify: `sdk/cliproxy/auth/conductor_cooldown.go` (field on Manager `modelPoolCooldowns` + record call in the 429 quota branch; also clear on success for that model; `CooldownStateSnapshot()` appends pool records with empty `AuthID`)
- Modify: `sdk/cliproxy/auth/conductor.go` (Manager field init in NewManager)
- Modify: `sdk/cliproxy/auth/conductor_selection.go` (fail-fast in `pickNextMixed` after eligibleProviders loop + top of `pickNextMixedLegacy` after its Home/plugin guards)
- Modify: `sdk/cliproxy/auth/conductor_cooldown.go` (package `atomic.Bool poolModelCooldownEnabled` + `SetPoolModelCooldownEnabled`)
- Modify: `internal/config/config_types.go` (`RoutingConfig.PoolModelCooldown *bool yaml:"pool-model-cooldown,omitempty"` — nil or true = enabled; comment states legacy plugin-scheduler deployments use the legacy path only)
- Modify: `sdk/cliproxy/service_auth.go` (`applyRetryConfig`: `coreauth.SetPoolModelCooldownEnabled(cfg.Routing.PoolModelCooldown == nil || *cfg.Routing.PoolModelCooldown)`)
- Modify: `config.example.yaml` (commented toggle beside the retry block ~:292)
- Test: `sdk/cliproxy/auth/pool_fail_fast_test.go`

**Step 1: Failing tests** (fake-executor end-to-end, harness per verified anchors):
1. 3 Claude credentials; two of them 429'd with Retry-After 5m on model M → third request for M fails fast with `*modelCooldownError` WITHOUT the third credential's executor being hit (assert its call count 0 and resetIn ≈ 5m).
2. Same setup with `SetPoolModelCooldownEnabled(false)` (test-order caution: this global atomic must be reset via t.Cleanup to true) → third credential IS picked (old rotation behavior).
3. One credential cooling → third picked (threshold not met).
4. After deadline passes (Retry-After 1ms + poll loop with timeout — or, better, deadline computed from `Retry-After` so a 10ms test works) → credential eligible again.
5. `CooldownStateSnapshot()` includes pool records: `AuthID == ""`, Model set, Reason "quota"/"overage".
6. `resultErrorFromError`-level: alert runner compatibility — provider_cooldown detector finds pool records (call the existing detector helper on the snapshot; if that's heavy, assert record shape only and note it).

**Step 2: Run** → FAIL.

**Step 3: Implement** in dependency order: Manager field & init → record/clear calls in the 429/success model branches → fail-fast helpers → selection insertion → toggle+config+service wiring → snapshot extension. Fail-fast helper shape:
```go
// poolModelCooldownBlock consults the pool-wide per-model aggregate; enabled
// gating lives here so selection never needs the atomic.
func (m *Manager) poolModelCooldownBlock(providers []string, model string, now time.Time) (time.Time, bool)
```
Selection insertion (both sites): `if !poolModelCooldownEnabled.Load() { skip }` … naah — put the atomic check inside the helper. On block: `newModelCooldownError(canonicalModelKey(model), firstProvider, resetIn)`.

**Step 4:** `go test ./sdk/cliproxy/auth/ ./internal/runtime/executor/ ./test/ -count=1` green (the auth package has heavy selection coverage — watch for new failures, they're yours: e.g. tests that assumed rotation-through-cooling-credentials with 2+ creds on one model might now fail fast unexpectedly. If a pre-existing test breaks BECAUSE the pool aggregate changed legit behavior, that is a real semantic question — bring it back in your report, do not "fix" by weakening the feature).

**Step 5:** build verify, gofmt, full `go test ./sdk/cliproxy/... ./test/...`.

**Step 6: Commit** `feat(conductor): pool-wide per-model cooldown fail-fast (opt-out)`.

---

### Task 7: Documentation + DoD sweep

**Files:**
- Modify: `config.example.yaml` (verify Task 6's block reads well; add overage mention: "silent account caps (overage/spend-cap) cool only the affected model for the credential")
- Modify: `docs/plans/2026-09-22-nixllm-upstream-releases-design.md` — NO. Design docs are immutable history; note deviations in the commit message instead.

**Step 1:** whole-DoD: `gofmt -l .` empty; `go build -o test-output ./cmd/server && rm test-output`; `go test ./... 2>&1 | tail -30` — only the 4 known pre-existing failures (3× X-Stainless-Os environmental, 1× internal/util worktree-walk artifact); + `internal/home` flake only under load.
**Step 2:** final review by fresh subagent (whole Phase-2 diff), then tag `v7.2.138-0.1.29` (per the pairing rule: latest is `v7.2.138-0.1.28` = Phase 1; bump the 0.x half).
**Step 3:** memory update; commits stay LOCAL (never pushed).

---

## Explicit non-goals (out of scope, Phase-2 reviews reject imitations)
- Any `internal/translator/` change.
- Streaming retry / `FirstByte` wiring (reserved from Phase 1 ledger).
- Plan-provision upsert or Claude plan type detection (`claude_oauth_profile_fetcher`) — the design mentions it as available context; we do NOT wire plan-type-specific horizons in this phase (KISS; `Reason:"overage"` + 7d is uniform). Recorded as a Phase-2+ follow-up.
- Per-entry DB retry overrides (dormant from Phase 1).
- Fable / 7d_oi / fuzzy-grace machinery from upstream's full parser (headers-unknown ⇒ false conservatively).
