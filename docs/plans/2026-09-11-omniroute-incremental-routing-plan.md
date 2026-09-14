# Incremental Routing Improvements (G2–G6) Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Implement the five validated gaps from `docs/plans/2026-09-11-omniroute-incremental-routing-design.md`: success-decay (G2), pool-level circuit breaker (G3), p2c/least-used strategies (G4), bounded cooldown wait (G5), and 403→429 reclassification (G6).

**Architecture:** All changes attach to existing paths in `sdk/cliproxy/auth` (Manager/MarkResult/scheduler/selector) and the canonical config chain (PG row → render → synthesizer → auth attribute). No new layers. Work happens directly on `main` (operator's choice — no worktree).

**Tech Stack:** Go 1.26+, stdlib `testing` (no assertion libs), math/rand/v2, logrus. Config: YAML `routing.cooldown_wait`. PG migration for one new column.

**Conventions (apply to every task):**
- Tests live in the same package (internal tests), stdlib `testing`, table-driven, `t.Cleanup` to restore package-level atomics (see `withQuotaCooldownEnabled` in `cooldown_backoff_test.go:13`), `t.Parallel()` on pure tests.
- After every code change: `gofmt -w <files>` and compile-verify `go build -o test-output ./cmd/server && rm test-output`.
- Every commit message ends with `Co-Authored-By: Claude Code <noreply@anthropic.com>`.
- Run only the targeted test first (`go test -v -run TestX ./sdk/cliproxy/auth`), full `go test ./...` at the end.

---

### Task 1: Config plumbing — `routing.cooldown_wait`

**Files:**
- Modify: `internal/config/config_types.go:290-306` (RoutingConfig)
- Modify: `sdk/cliproxy/auth/conductor_cooldown.go:23-36` (package atomics)
- Modify: `sdk/cliproxy/service_auth.go:357-364` (applyRetryConfig)
- Modify: `internal/api/server.go:212-213`, `internal/api/server_reload.go:104-106`
- Modify: `config.example.yaml:252-281` (routing block docs)
- Test: `internal/config/routing_cooldown_wait_test.go`

**Step 1: Write the failing test** (`internal/config/routing_cooldown_wait_test.go`, package `config`): parse a YAML snippet with `routing: {cooldown-wait: {max-wait-ms: 5000, max-attempts: 2, reclassify-403: true}}` via the existing config-load path (mirror how other tests in `internal/config` load YAML) and assert the three fields land on `cfg.Routing.CooldownWait`. Also assert zero-value defaults round-trip.

**Step 2: Run it** — `go test -v -run TestRoutingCooldownWait ./internal/config` → FAIL (fields don't exist).

**Step 3: Implement config type** — in `config_types.go` add:

```go
// CooldownWaitConfig bounds server-side cooldown waits and controls
// OmniRoute-style error reclassification.
type CooldownWaitConfig struct {
	// MaxWaitMS caps the total time a request may wait server-side on
	// cooldowns before surfacing 429 + Retry-After. 0 means the default
	// (15000).
	MaxWaitMS int `yaml:"max-wait-ms,omitempty" json:"max-wait-ms,omitempty"`
	// MaxAttempts caps re-dispatches inside one cooldown-wait cycle.
	// 0 means the default (3).
	MaxAttempts int `yaml:"max-attempts,omitempty" json:"max-attempts,omitempty"`
	// Reclassify403 rewrites quota-shaped 403 bodies to the 429 quota
	// ladder instead of the 30-minute unauthorized cooldown. Default false.
	Reclassify403 bool `yaml:"reclassify-403,omitempty" json:"reclassify-403,omitempty"`
}
```
and add to `RoutingConfig`:
```go
	// CooldownWait bounds server-side cooldown waiting and 403
	// reclassification. See CooldownWaitConfig for defaults.
	CooldownWait CooldownWaitConfig `yaml:"cooldown-wait,omitempty" json:"cooldown-wait,omitempty"`
```

**Step 4: Auth-package knob** — in `conductor_cooldown.go` next to the existing atomics (lines 23-36):

```go
const (
	defaultCooldownWaitMaxWaitMS   = 15000
	defaultCooldownWaitMaxAttempts = 3
)

var (
	cooldownWaitBudgetMS    atomic.Int64
	cooldownWaitMaxAttempts atomic.Int64
	reclassifyQuota403      atomic.Bool
)

// SetCooldownWaitConfig configures the bounded cooldown wait (G5) and the
// 403→429 reclassification gate (G6). Non-positive values take defaults.
func SetCooldownWaitConfig(maxWaitMS, maxAttempts int, reclassify403 bool) {
	if maxWaitMS <= 0 {
		maxWaitMS = defaultCooldownWaitMaxWaitMS
	}
	if maxAttempts <= 0 {
		maxAttempts = defaultCooldownWaitMaxAttempts
	}
	cooldownWaitBudgetMS.Store(int64(maxWaitMS))
	cooldownWaitMaxAttempts.Store(int64(maxAttempts))
	reclassifyQuota403.Store(reclassify403)
}
```

**Step 5: Wire it** — in `applyRetryConfig` (`service_auth.go`) after the existing `coreauth.SetTransientErrorCooldownSeconds(...)` add `coreauth.SetCooldownWaitConfig(cfg.Routing.CooldownWait.MaxWaitMS, cfg.Routing.CooldownWait.MaxAttempts, cfg.Routing.CooldownWait.Reclassify403)`. In `internal/api/server.go` next to `auth.SetTransientErrorCooldownSeconds(...)` add the same call. In `server_reload.go` extend the change-detection condition to also compare `Routing.CooldownWait` (struct compare with `==` works — all fields comparable).

**Step 6: Document** — append a commented `cooldown-wait:` example inside the `routing:` block of `config.example.yaml` (English comments only).

**Step 7: Run** — `go test ./internal/config ./sdk/cliproxy/auth` → PASS; compile-verify.

**Step 8: Commit** — `feat(routing): add routing.cooldown_wait config plumbing`

---

### Task 2: G6 — 403→429 reclassification

**Files:**
- Modify: `sdk/cliproxy/auth/conductor_cooldown.go` (MarkResult, ~line 856 where `statusCode := statusCodeFromResult(result.Error)`)
- Test: `sdk/cliproxy/auth/mark_result_quota403_reclass_test.go`

**Step 1: Write the failing test.** Mirror the Manager setup in `mark_result_disable_cooling_test.go` (register one active auth, call `MarkResult`, inspect `ModelStates` / `isAuthBlockedForModel`). Table cases:
- 403 body containing `"quota exceeded"` with gate ON → `state.Quota.Exceeded == true` and `NextRetryAfter` within ~5s of now (quota ladder base 1s, NOT 30 min).
- Same body with gate OFF → `NextRetryAfter` ≈ now+30min, `Quota.Exceeded == false`.
- 403 body `"billing required"` gate ON → stays 30-min class (billing excluded).
- 403 body `"insufficient quota for this model"` gate ON → quota ladder (pattern `quota` matches).
- 403 body unrelated (`"forbidden"`) gate ON → 30-min class.
Save/restore via `t.Cleanup`: `prev := reclassifyQuota403.Load(); reclassifyQuota403.Store(true/false)`.

**Step 2: Run** → FAIL (function undefined / behavior absent).

**Step 3: Implement** — add helper near `statusCodeFromResult` (conductor_cooldown.go:1394):

```go
// quota403BodyPatterns is the narrow whitelist for reclassifying a 403 as a
// quota error (OmniRoute-style status restatement). "billing" is explicitly
// excluded: billing-shaped 403s stay in the payment-required class.
var quota403BodyPatterns = []string{"quota", "rate limit", "exceeded", "resource_exhausted"}

func looksLikeQuotaForbidden(err *Error) bool {
	if err == nil || statusCodeFromResult(err) != http.StatusForbidden {
		return false
	}
	msg := strings.ToLower(err.Code + " " + err.Message)
	if strings.Contains(msg, "billing") {
		return false
	}
	for _, pattern := range quota403BodyPatterns {
		if strings.Contains(msg, pattern) {
			return true
		}
	}
	return false
}
```

Then in `MarkResult`, rewrite the assignment `statusCode := statusCodeFromResult(result.Error)` to:

```go
				statusCode := statusCodeFromResult(result.Error)
				if statusCode == http.StatusForbidden && reclassifyQuota403.Load() && looksLikeQuotaForbidden(result.Error) {
					// Quota-shaped 403s take the retry-eligible 429 quota
					// ladder instead of the 30-minute unauthorized cooldown.
					statusCode = http.StatusTooManyRequests
					result.RetryAfter = nil
				}
```
No other switch changes — the existing `case 429:` branch handles the rest.

**Step 4: Run** → PASS. **Step 5: gofmt + compile-verify.**

**Step 6: Commit** — `feat(auth): reclassify quota-shaped 403s behind opt-in gate`

---

### Task 3: G5 — bounded cooldown wait

**Files:**
- Modify: `sdk/cliproxy/auth/conductor_selection.go:985-1021` (`shouldRetryAfterError`)
- Test: `sdk/cliproxy/auth/cooldown_wait_budget_test.go`

**Step 1: Write the failing test.** Build a Manager (mirror `conductor_selection_cooldown_test.go`) with one auth put into model cooldown with `NextRetryAfter = now + 60s`. Table:
- Budget 15s, wait 60s → `shouldRetryAfterError` returns `(0, false)` (too long to wait).
- Budget 90s, wait 60s → `(60s, true)` at attempt 0.
- Budget 90s, wait 60s, attempt == MaxAttempts (3) → `(0, false)` (attempt cap).
- Budget 0/unset → defaults apply (15s): wait 60s → false.
Restore atomics with `t.Cleanup`.

**Step 2: Run** → FAIL.

**Step 3: Implement** — inside `shouldRetryAfterError`, in the `if found { ... }` branch (closestCooldownWait path), apply the budget and attempt cap **before** the existing `wait > maxWait` check:

```go
	if found {
		// Bounded cooldown wait (G5): the routing.cooldown_wait budget is a
		// total ceiling across re-dispatches; it only ever tightens the
		// pre-existing max-retry-interval ceiling.
		if budget := cooldownWaitBudgetMS.Load(); budget > 0 {
			budgetWait := time.Duration(budget) * time.Millisecond
			if maxWait <= 0 || budgetWait < maxWait {
				maxWait = budgetWait
			}
		}
		if wait > maxWait {
			return 0, false
		}
		if cap := int(cooldownWaitMaxAttempts.Load()); cap > 0 && attempt >= cap {
			return 0, false
		}
		return wait, true
	}
```
(Rename `cap` → `attemptCap` — `cap` is a builtin.) The 429-Retry-After branch below keeps using the (already budget-tightened) `maxWait` — add the same budget tightening above the `if wait > maxWait` style check there by hoisting the budget block before both branches.

Note: `attempt` semantics — the outer `Execute` loop (conductor_execution.go:48-68) passes its loop counter; `max_attempts` therefore caps cooldown-wait-driven re-dispatches per request.

**Step 4: Run** → PASS. **Step 5: gofmt + compile + `go test ./sdk/cliproxy/auth`.**

**Step 6: Commit** — `feat(auth): bounded server-side cooldown wait (max_wait_ms/max_attempts)`

---

### Task 4: G2 — success-decay on per-model failure state

**Files:**
- Modify: `sdk/cliproxy/auth/types.go:219-235` (ModelState) and `types.go:444-458` (Clone)
- Modify: `sdk/cliproxy/auth/conductor_cooldown.go` — `MarkResult` success path (~line 740), `resetModelState` (1057), `mergeModelState` (999), `modelStateIsClean` (1070), `updateAggregatedAvailability` (1086)
- Test: `sdk/cliproxy/auth/model_state_decay_test.go`

**Step 1: Write the failing test.** Register auth; loop: `MarkResult` failure with 503 (transient, 1-min cooldown) ×4 → assert `state.FailureCount == 4` and blocked. `MarkResult` success → assert `FailureCount == 2`, state still carries a **short** residual cooldown (blocked at now, unblocked shortly after deadline). Second success → `resetModelState` semantics (FailureCount 0, clean, `modelStateIsClean == true`). Also table-test pure `decayModelStateOnSuccess(nil)` safety and 1→reset path.

**Step 2: Run** → FAIL (field absent).

**Step 3: Implement field** — in `ModelState`:
```go
	// FailureCount tracks recent failures for staged recovery (G2): success
	// halves it instead of instantly restoring full health. Not persisted in
	// cooldown-state records.
	FailureCount int `json:"failure_count,omitempty"`
```
Copy it in `Clone`. Zero it in `resetModelState`. In `mergeModelState` take the max of both. In `modelStateIsClean` treat `state.FailureCount != 0` as not-clean. In `updateAggregatedAvailability` treat a nonzero FailureCount like the other non-blocking residue (it does not itself block; deadlines do).

**Step 4: Implement decay** — helper in conductor_cooldown.go:

```go
// decayModelStateOnSuccess halves the model's failure count on a successful
// result (G2). A count that survives the halving keeps a short residual
// cooldown — half the remaining window, floor 1s — so a flapping model
// recovers in stages instead of returning at full health and failing again
// immediately. Counts of 0 or 1 take the historical full reset.
func decayModelStateOnSuccess(state *ModelState, now time.Time) {
	if state == nil {
		return
	}
	if state.FailureCount <= 1 {
		resetModelState(state, now)
		return
	}
	state.FailureCount /= 2
	pullIn := func(deadline time.Time) time.Time {
		if deadline.IsZero() || !deadline.After(now) {
			return now.Add(time.Second)
		}
		remaining := deadline.Sub(now) / 2
		if remaining < time.Second {
			remaining = time.Second
		}
		return now.Add(remaining)
	}
	state.NextRetryAfter = pullIn(state.NextRetryAfter)
	state.Quota.NextRecoverAt = pullIn(state.Quota.NextRecoverAt)
	state.Unavailable = true
	state.UpdatedAt = now
}
```
In `MarkResult` success path (the `if result.Success` + `modelKey != ""` branch), replace the unconditional `resetModelState(state, now)` with `decayModelStateOnSuccess(state, now)`. In the failure path (inside `if !shouldSkipCredentialCooldown(...)`, after the status switch), add `state.FailureCount++`.

**Step 5: Run** → PASS. **Step 6: gofmt + compile + package tests.**

**Step 7: Commit** — `feat(auth): staged model recovery via success-decay of failure count`

---

### Task 5: G3 core — `PoolBreaker` type

**Files:**
- Create: `sdk/cliproxy/auth/pool_breaker.go`
- Test: `sdk/cliproxy/auth/pool_breaker_test.go`

**Step 1: Write the failing test.** Table + state-machine cases on the pure type (no Manager): threshold trip at 8 failures in window; OPEN blocks with deadline = openedAt+resetPeriod; lazy HALF_OPEN after reset timeout (one probe allowed, second caller blocked); probe failure → OPEN with doubled reset period (cap 5 min, verify with a state that already went through several cycles — shorten by testing the doubling math via a small window override or by driving failures in a loop); success in HALF_OPEN → CLOSED + zeroed; success in CLOSED resets the failure window; entry for unknown key never blocks. Use `t.Parallel()` — the type is self-contained.

**Step 2: Run** → FAIL (undefined).

**Step 3: Implement:**

```go
package auth

// pool_breaker.go implements the opt-in pool-level circuit breaker (G3,
// OmniRoute reference): only 408/5xx failures contribute; 401/402/403/429
// belong to the per-auth+model cooldown. State is in-memory by design — a
// restart starts every pool clean.

type breakerState int

const (
	breakerClosed breakerState = iota
	breakerOpen
	breakerHalfOpen
)

const (
	poolBreakerFailureThreshold = 8
	poolBreakerWindow           = 60 * time.Second
	poolBreakerResetBase        = 30 * time.Second
	poolBreakerResetMax         = 5 * time.Minute
)

type poolBreakerEntry struct {
	failures    int
	windowStart time.Time
	state       breakerState
	openedAt    time.Time
	resetPeriod time.Duration
	probeUsed   bool
}

type poolBreaker struct {
	mu    sync.Mutex
	pools map[string]*poolBreakerEntry
}

var globalPoolBreaker = &poolBreaker{pools: make(map[string]*poolBreakerEntry)}

func (b *poolBreaker) entry(key string, now time.Time) *poolBreakerEntry {
	entry, ok := b.pools[key]
	if !ok {
		entry = &poolBreakerEntry{state: breakerClosed, resetPeriod: poolBreakerResetBase}
		b.pools[key] = entry
	}
	return entry
}

// recordFailure feeds one 408/5xx result into the breaker. Fixed-window
// counting (KISS): failures inside one 60s window accumulate; hitting the
// threshold opens the breaker.
func (b *poolBreaker) recordFailure(key string, now time.Time) {
	if key == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	entry := b.entry(key, now)
	if entry.state == breakerHalfOpen {
		entry.state = breakerOpen
		entry.openedAt = now
		entry.resetPeriod *= 2
		if entry.resetPeriod > poolBreakerResetMax {
			entry.resetPeriod = poolBreakerResetMax
		}
		return
	}
	if entry.state == breakerOpen {
		return
	}
	if entry.windowStart.IsZero() || now.Sub(entry.windowStart) > poolBreakerWindow {
		entry.windowStart = now
		entry.failures = 0
	}
	entry.failures++
	if entry.failures >= poolBreakerFailureThreshold {
		entry.state = breakerOpen
		entry.openedAt = now
	}
}

// recordSuccess closes an open/half-open breaker and clears the window.
func (b *poolBreaker) recordSuccess(key string, now time.Time) {
	if key == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if entry, ok := b.pools[key]; ok {
		*entry = poolBreakerEntry{state: breakerClosed, resetPeriod: poolBreakerResetBase}
		delete(b.pools, key)
	}
}

// blockDeadline reports whether the pool currently blocks selection and when
// it lifts. OPEN blocks until openedAt+resetPeriod; the lazy transition to
// HALF_OPEN admits exactly one probe (the first caller after expiry); other
// callers stay blocked until the probe resolves or a fresh reset period
// elapses.
func (b *poolBreaker) blockDeadline(key string, now time.Time) (time.Time, bool) {
	if key == "" {
		return time.Time{}, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	entry, ok := b.pools[key]
	if !ok {
		return time.Time{}, false
	}
	switch entry.state {
	case breakerOpen:
		deadline := entry.openedAt.Add(entry.resetPeriod)
		if now.Before(deadline) {
			return deadline, true
		}
		entry.state = breakerHalfOpen
		entry.probeUsed = false
		fallthrough
	case breakerHalfOpen:
		if !entry.probeUsed {
			entry.probeUsed = true
			return time.Time{}, false // probe admitted
		}
		return now.Add(entry.resetPeriod), true
	default:
		return time.Time{}, false
	}
}
```

**Step 4: Run** → PASS. **Step 5: gofmt + compile.**

**Step 6: Commit** — `feat(auth): add opt-in pool-level circuit breaker type`

---

### Task 6: G3 wiring — attribute, MarkResult, selection, snapshot

**Files:**
- Modify: `sdk/cliproxy/auth/classification.go:16-26` (attribute const)
- Modify: `sdk/cliproxy/auth/conductor_cooldown.go` (MarkResult failure/success paths; snapshot)
- Modify: `sdk/cliproxy/auth/selector.go:559-603` (`isAuthBlockedForModel`)
- Modify: `internal/api/handlers/management/cooldown_providers.go`
- Test: `sdk/cliproxy/auth/pool_breaker_wiring_test.go`

**Step 1: Write the failing test.** Use the `registerPoolStrategyAuth` helper pattern from `pool_strategy_default_test.go:10` — register two auths with `provider_key: "claude:7"`, `AttributePoolStrategy: "round-robin"` plus `attrs[AttributePoolCircuitBreaker] = "true"`. Drive `MarkResult` failures (503 × threshold) → assert `isAuthBlockedForModel` blocks with a nonzero deadline (cooldown reason). An auth in the same pool WITHOUT the opt-in attribute must still be blocked (breaker is pool-keyed), while an auth with `provider_key: "claude:8"` (no breaker attr → key "" → not tracked) stays selectable. A success via MarkResult reopens selection.

**Step 2: Run** → FAIL.

**Step 3: Implement attribute + helpers** — in `classification.go` add `AttributePoolCircuitBreaker = "pool_circuit_breaker"`. In conductor_cooldown.go (or pool_breaker.go):

```go
// poolBreakerKeyForAuth returns the breaker key for an auth that opted in
// via the pool_circuit_breaker attribute and carries a compound pool key.
func poolBreakerKeyForAuth(auth *Auth) string {
	if auth == nil || auth.Attributes == nil {
		return ""
	}
	if auth.Attributes[AttributePoolCircuitBreaker] != "true" {
		return ""
	}
	return strings.TrimSpace(auth.Attributes["provider_key"])
}
```

**Step 4: Wire MarkResult** — failure path: in the `case 408, 500, 502, 503, 504:` branch add
```go
							if key := poolBreakerKeyForAuth(auth); key != "" {
								globalPoolBreaker.recordFailure(key, now)
							}
```
Success path (modelKey branch): add `if key := poolBreakerKeyForAuth(auth); key != "" { globalPoolBreaker.recordSuccess(key, now) }` right after `decayModelStateOnSuccess`. Note: the reclassified-403 (Task 2) flows through `case 429` — correctly NOT feeding the breaker.

**Step 5: Wire selection** — in `isAuthBlockedForModel`, immediately after the Disabled checks and before the model-state handling:

```go
	if key := poolBreakerKeyForAuth(auth); key != "" {
		if deadline, blocked := globalPoolBreaker.blockDeadline(key, now); blocked {
			return true, blockReasonCooldown, deadline
		}
	}
```
A nonzero deadline surfaces as the existing 429 + `Retry-After` cooldown path (`modelCooldownError`) — no new error type.

**Step 6: Snapshot** — add to pool_breaker.go:

```go
// PoolBreakerRecord is one pool's live breaker state for observability.
type PoolBreakerRecord struct {
	PoolKey   string    `json:"pool_key"`
	State     string    `json:"state"` // closed|open|half_open
	Failures  int       `json:"failures"`
	OpenedAt  time.Time `json:"opened_at,omitempty"`
	OpenUntil time.Time `json:"open_until,omitempty"`
}

// PoolBreakerSnapshot returns the live breaker states (in-memory; not persisted).
func PoolBreakerSnapshot() []PoolBreakerRecord { ... }
```
Map `breakerState` to the strings above. In `internal/api/handlers/management/cooldown_providers.go`, add a `pool_breakers` field to the response built from `coreauth.PoolBreakerSnapshot()`. Do NOT add breaker records to `CooldownStateRecord` persistence — they are in-memory by design.

**Step 7: Run** → PASS. **Step 8: gofmt + compile + package tests.**

**Step 9: Commit** — `feat(auth): wire pool breaker into MarkResult and selection`

---

### Task 7: G3 config surface — store → render → synthesizer

**Files (find exact anchors by mirroring the `UpstreamProviderStrategy` chain):**
- `grep -rn "UpstreamProviderStrategy" internal/store/ internal/api/handlers/management/upstream_providers.go internal/upstreamsync/ internal/config/ internal/watcher/synthesizer/` — every hit site gets the parallel bool.

**Steps:**
1. Add `CircuitBreaker bool` (JSON `circuit_breaker`) to the store `UpstreamProvider` struct + PG column `circuit_breaker` (follow the existing migration convention in `internal/store/` for `upstream_providers` — find it via `grep -rn "ALTER TABLE upstream_providers\|ADD COLUMN" internal/store/` and add one boolean NOT NULL DEFAULT false migration).
2. Management DTO: accept/return `circuit_breaker` in upstream_providers.go request/response structs (validate nothing — bool).
3. Render: `internal/upstreamsync/render.go` carries it onto the config structs next to `UpstreamProviderStrategy` (add `UpstreamProviderCircuitBreaker bool` to `config.ClaudeKey`, the OpenAI-compatibility provider struct, and the OpenCodeGo equivalent — wherever `UpstreamProviderStrategy` appears). Seed round-trip in `internal/upstreamsync/seed.go` mirrors it.
4. Synthesizer: in `internal/watcher/synthesizer/config.go` next to the existing `attrs[coreauth.AttributePoolStrategy] = s` stamps, add:
```go
if ck.UpstreamProviderCircuitBreaker {
	attrs[coreauth.AttributePoolCircuitBreaker] = "true"
}
```
(and the openai-compat / opencode-go analogues at their stamp sites, lines ~421/471/622).
5. `config.example.yaml` / management docs: one-line mention where pool strategies are documented.
6. Test: extend the store/DTO test that covers `routing_strategy` round-trip (grep `UpstreamProviderStrategy` in `internal/store/*_test.go` / management tests) with `circuit_breaker: true` asserting the full chain: DTO → store row → rendered config field.
7. Run: `go test ./internal/store/... ./internal/upstreamsync/... ./internal/watcher/... ./internal/api/...` → PASS; compile-verify.

**Commit** — `feat(upstream): circuit_breaker opt-in on upstream provider rows`

---

### Task 8: G4 — canonical strategies `power-of-two-choices` / `least-used`

**Files:**
- Modify: `internal/config/strategy.go`
- Modify: `sdk/cliproxy/service_config.go:33-55` (`normalizedRoutingRuntimeState`) + `newRoutingSelector` (57-74)
- Modify: `internal/api/handlers/management/config_basic.go:335-373` (its own `normalizeRoutingStrategy`)
- Test: `internal/config/strategy_test.go` (extend tables)

**Step 1: Failing tests** — extend `TestNormalizePoolRoutingStrategy`: `{"p2c", "power-of-two-choices"}`, `{"two-random-choices", "power-of-two-choices"}`, `{"power-of-two-choices", "power-of-two-choices"}`, `{"least-used", "least-used"}`, `{"least-busy", "least-used"}`; extend `TestValidatePoolRoutingStrategy` to accept both canonical values and reject garbage. Add a `normalizedRoutingRuntimeState` test in `sdk/cliproxy` (mirror existing strategy tests there — grep `normalizedRoutingRuntimeState` in `*_test.go`; if none, add a small one) asserting the new strategy strings survive normalization.

**Step 2: Run** → FAIL. **Step 3: Implement:**

strategy.go:
```go
const (
	...
	PoolStrategyPowerOfTwoChoices = "power-of-two-choices"
	PoolStrategyLeastUsed         = "least-used"
)
```
Normalize cases: `"power-of-two-choices", "poweroftwochoices", "p2c", "two-random-choices"` → `PoolStrategyPowerOfTwoChoices`; `"least-used", "leastused", "least-busy"` → `PoolStrategyLeastUsed`. Update the `ValidatePoolRoutingStrategy` switch and its error message. `normalizedRoutingRuntimeState` adds the two cases; `newRoutingSelector` maps them (placeholder selectors — real behavior lands in Task 10; for now map to `&coreauth.RoundRobinSelector{}` with a TODO-free comment noting the scheduler path implements the semantics, then flip in Task 10). Also add the two values to `normalizeRoutingStrategy` in config_basic.go.

**Step 4: Run** → PASS. **Step 5: gofmt + compile.**

**Step 6: Commit** — `feat(config): canonicalize power-of-two-choices and least-used strategies`

---

### Task 9: G4 — per-credential in-flight counter

**Files:**
- Modify: `sdk/cliproxy/auth/scheduler.go` (`scheduledAuth` struct + methods)
- Modify: `sdk/cliproxy/auth/conductor_execution.go` (pick/release sites)
- Test: `sdk/cliproxy/auth/scheduler_inflight_test.go`

**Step 1: Failing test.** Build scheduler via `newSchedulerForTest`, register two auths for one model. Assert `adjustAuthInFlight(authID, +2)` then a `leastUsedProbe`-style helper (expose `inFlightForAuth(authID) int` for tests) reports 2; `-1` reports 1; unknown auth is a no-op; concurrent adjust (spawn 50 goroutines) leaves the count exact.

**Step 2: Run** → FAIL.

**Step 3: Implement.** In `scheduledAuth` add `inFlight int` (guarded by the scheduler mutex — no atomics; pick and adjust both hold `s.mu`). Methods on `authScheduler`:

```go
// adjustInFlight applies a delta to an auth's in-flight counter across all
// its model shards' entries. No-op for unknown auths.
func (s *authScheduler) adjustInFlight(authID string, delta int) {
	if s == nil || authID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, provider := range s.providers {
		if meta, ok := provider.auths[authID]; ok && meta != nil && meta.auth != nil {
			if entry := meta; true {
				_ = entry // entries live per model shard; walk shards
			}
		}
	}
}
```
(Walk `provider.modelShards[*].entries[authID].inFlight += delta` — every shard entry for the auth, since picks come from a shard. Simpler and sufficient: store the counter once per auth in `scheduledAuthMeta` instead of `scheduledAuth` — pick reads `entry.meta.inFlight`. Prefer the meta location; only one copy to maintain.)

Manager wrappers in conductor_execution.go:
```go
func (m *Manager) adjustAuthInFlight(auth *Auth, delta int) {
	if m == nil || m.scheduler == nil || auth == nil {
		return
	}
	m.scheduler.adjustInFlight(auth.ID, delta)
}
```
Call sites: increment right after a successful pick in the execution loops (where `auth` is selected in `executeMixedOnce` — find the common pick site; if the three loops pick separately, add at each), decrement in `MarkResult` after `m.mu.Unlock()` alongside the scheduler upsert (`m.adjustAuthInFlight(authSnapshot, -1)`). This pairs every pick with exactly one result.

**Step 4: Run** → PASS. **Step 5: gofmt + compile + package tests** (watch for races: `go test -race -run TestSchedulerInFlight ./sdk/cliproxy/auth`).

**Step 6: Commit** — `feat(auth): track per-credential in-flight count in scheduler`

---

### Task 10: G4 — scheduler picks for p2c / least-used

**Files:**
- Modify: `sdk/cliproxy/auth/scheduler.go:17-21` (strategy enum), `pickReadyAtPriorityLocked` (858-883), `readyView` pick helpers (1036-1074)
- Modify: the mapping site where `AttributePoolStrategy` / selector type becomes `schedulerStrategy` (grep `schedulerStrategyWeightedRoundRobin` for all assignment sites and add parallel cases)
- Modify: `sdk/cliproxy/service_config.go` `newRoutingSelector` — finalize the Task 8 placeholder mapping
- Test: `sdk/cliproxy/auth/scheduler_strategy_p2c_test.go`

**Step 1: Failing test.** Register 5 auths (priority 0) with controlled in-flight counts via Task 9 helpers:
- p2c: seed rand or run many picks and assert the picked auth is *always* one of the two lowest-in-flight candidates over enough iterations AND that picks spread (not fill-first).
- least-used: deterministic — auth with min inFlight always picked; on tie, round-robin cursor advances (two equal auths alternate).
- both strategies still respect priority buckets and the `tried` predicate.

**Step 2: Run** → FAIL.

**Step 3: Implement.** Enum:
```go
	schedulerStrategyP2C       schedulerStrategy = 4
	schedulerStrategyLeastUsed schedulerStrategy = 5
```
In `pickReadyAtPriorityLocked` extend the switch:
```go
	case schedulerStrategyP2C:
		picked = view.pickPowerOfTwo(predicate)
	case schedulerStrategyLeastUsed:
		picked = view.pickLeastUsed(predicate)
```
In scheduler.go rotation primitives:
```go
// pickPowerOfTwo samples two distinct matching entries and returns the one
// with fewer in-flight requests (O(1) choices over the ready view).
func (v *readyView) pickPowerOfTwo(predicate func(*scheduledAuth) bool) *scheduledAuth {
	if v == nil || len(v.flat) == 0 {
		return nil
	}
	var candidates []*scheduledAuth
	for _, entry := range v.flat {
		if predicate == nil || predicate(entry) {
			candidates = append(candidates, entry)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	if len(candidates) == 1 {
		return candidates[0]
	}
	first := rand.IntN(len(candidates))
	second := rand.IntN(len(candidates) - 1)
	if second >= first {
		second++
	}
	a, b := candidates[first], candidates[second]
	if a.inFlightOrMeta() <= b.inFlightOrMeta() {
		return a
	}
	return b
}
```
(Read the counter from `meta` per Task 9's final location; drop the placeholder helper name.) `pickLeastUsed`: linear scan for min in-flight; tie-break by advancing a cursor over the tied subset (round-robin among equals). Note in a comment: weights are a WRR-only concept; p2c/least-used ignore weight.

Where the scheduler strategy is derived (grep sites), map the new canonical pool-strategy strings to the new enum values; finish `newRoutingSelector` so the new strategies stop falling back to RR.

**Step 4: Run** → PASS (`go test -race ./sdk/cliproxy/auth`). **Step 5: gofmt + compile.**

**Step 6: Commit** — `feat(auth): implement power-of-two-choices and least-used scheduler picks`

---

### Task 11: Full verification

1. `gofmt -w .` (or gofmt on changed files) — no diffs.
2. `go build -o test-output ./cmd/server && rm test-output`.
3. `go test ./...` — all green; if `test/` integration tests need PG and are skipped without `PGSTORE_DSN`, note the skip rather than forcing them.
4. `go vet ./sdk/cliproxy/... ./internal/config/...`.
5. Cross-check the three strategy canonicalization boundaries still round-trip (management DTO → render → seed) via the existing tests touched in Tasks 7-8.
6. Commit any stragglers; summarize which gaps shipped (G2, G3, G4, G5, G6) and the one deliberate deviation note: `cooldown_wait` budget only ever *tightens* the existing max-retry-interval ceiling, never loosens it.

---

## Known deviations from the design doc (accepted during extraction)

- **G2:** `ModelState` had no failure counter — `FailureCount` is net-new; the full mutation surface (`resetModelState`, `mergeModelState`, `modelStateIsClean`, `updateAggregatedAvailability`, `Clone`) is enumerated in Task 4. Success with surviving count keeps a *short residual* cooldown (half remaining, floor 1s) exactly as designed.
- **G4:** no per-credential in-flight counter existed (the policy `parallelLimiter` is per-downstream-principal, unrelated) — Task 9 adds one on the `authScheduler` itself (`inFlight map[string]int` keyed by auth ID, guarded by `s.mu`) rather than on `scheduledAuthMeta`. The counter is lifecycle-paired: incremented at pick time and decremented when the result is recorded. p2c/least-used ignore entry weights (weights are a WRR concept).
- **G3:** breaker persistence stays in-memory (per design); observability is a dedicated `PoolBreakerSnapshot` surfaced through the existing cooldown management endpoint instead of overloading `CooldownStateRecord` (whose restore path would misinterpret pool-keyed rows as auth rows).
- **G5:** a cooldown-wait budget already partially exists (`maxRetryInterval` caps `closestCooldownWait`); the new budget composes by tightening, defaults 15s/3s-3-attempts per the design.
