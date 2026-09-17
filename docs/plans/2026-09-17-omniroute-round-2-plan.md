# OmniRoute Round 2 Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Ship three OmniRoute-inspired workstreams: three new routing selectors + decision header, quota-share accounting endpoints + dashboard panel, and a live events feed via SSE.

**Architecture:** Read-only or additive changes only. New selectors attach to the existing scheduler pick path. `X-NixLLM-Decision` header is built in the conductor `Execute*` return path. Quota endpoints aggregate `usage_windows` (no schema change). Events live in an in-memory ring recorder at `internal/events/`. Dashboard mounts follow the developer-docs endpoint-catalog pattern.

**Tech Stack:** Go 1.26+, Gin, existing PG repository layer (`internal/store`), React + Vite SPA at `web/dashboard/`, Shadcn UI.

**Reference:** `docs/plans/2026-09-17-omniroute-round-2-design.md` (validated round-2 design); `docs/plans/2026-09-11-omniroute-incremental-routing-design.md` (round-1).

**Sequencing:** Four independent commits (WS1a, WS1b, WS2, WS3), each independently revertable. Tasks within a commit are sequential.

---

## Commit 1 — WS1a: New selectors (`fill-first`, `weighted`, `headroom`)

> **Note on `fill-first`:** the round-1 doc (`internal/config/strategy.go:24`) already ships `fill-first` as a *pool-level* canonical value, mapped to a deterministic "highest-priority-first" pick. The round-2 `fill-first` extends this with the "highest in-flight below cap" semantic described in the design doc. Both coexist: pool-level `fill-first` keeps its existing semantics; the new global selector reuses the name but applies the in-flight-highest-below-cap rule. Tasks 1-2 below reconcile the two namespaces.

### Task 1: Reconcile `fill-first` namespaces (canonical value + alias)

**Files:**
- Modify: `internal/config/strategy.go:21-62`

**Step 1: Re-read the existing constants and decide the canonical name for the new global selector**

Read `internal/config/strategy.go`. Note that `PoolStrategyFillFirst` already exists with deterministic-by-priority semantics. The new global selector must use a distinct canonical name to avoid collision.

**Step 2: Add `GlobalStrategyFillFirst = "fill-first"` as a separate constant**

The existing `PoolStrategyFillFirst` stays for pool rows. Add a new constant:

```go
const (
    GlobalStrategyRoundRobin = "round-robin"
    GlobalStrategyFillFirst  = "fill-first"  // new global selector (round-2)
    GlobalStrategyWeighted   = "weighted"
    GlobalStrategyHeadroom   = "headroom"
)
```

**Step 3: Extend `NormalizePoolRoutingStrategy` with the new aliases for the global namespace**

Add a new function `NormalizeGlobalRoutingStrategy` that knows about `weighted`/`w` and `headroom`/`hr`. Reuses existing aliases for `fill-first`. Keep `NormalizePoolRoutingStrategy` unchanged.

```go
func NormalizeGlobalRoutingStrategy(s string) string {
    switch strings.ToLower(strings.TrimSpace(s)) {
    case "round-robin", "roundrobin", "rr":
        return GlobalStrategyRoundRobin
    case "fill-first", "fillfirst", "ff":
        return GlobalStrategyFillFirst
    case "weighted", "w":
        return GlobalStrategyWeighted
    case "headroom", "hr":
        return GlobalStrategyHeadroom
    default:
        return ""
    }
}

func ValidateGlobalRoutingStrategy(s string) error {
    switch s {
    case "", GlobalStrategyRoundRobin, GlobalStrategyFillFirst, GlobalStrategyWeighted, GlobalStrategyHeadroom:
        return nil
    default:
        return fmt.Errorf("invalid global routing strategy %q: want one of round-robin, fill-first, weighted, headroom", s)
    }
}
```

**Step 4: Write the failing test**

Create `internal/config/strategy_test.go` (does not exist yet):

```go
package config

import "testing"

func TestNormalizeGlobalRoutingStrategy(t *testing.T) {
    cases := map[string]string{
        "round-robin":  GlobalStrategyRoundRobin,
        "rr":           GlobalStrategyRoundRobin,
        "fill-first":   GlobalStrategyFillFirst,
        "ff":           GlobalStrategyFillFirst,
        "weighted":     GlobalStrategyWeighted,
        "w":            GlobalStrategyWeighted,
        "headroom":     GlobalStrategyHeadroom,
        "hr":           GlobalStrategyHeadroom,
        "  weighted ":  GlobalStrategyWeighted,
        "unknown":      "",
    }
    for in, want := range cases {
        if got := NormalizeGlobalRoutingStrategy(in); got != want {
            t.Errorf("NormalizeGlobalRoutingStrategy(%q) = %q, want %q", in, got, want)
        }
    }
}

func TestValidateGlobalRoutingStrategy(t *testing.T) {
    if err := ValidateGlobalRoutingStrategy(GlobalStrategyHeadroom); err != nil {
        t.Errorf("headroom should validate: %v", err)
    }
    if err := ValidateGlobalRoutingStrategy("bogus"); err == nil {
        t.Errorf("bogus should fail validation")
    }
}
```

**Step 5: Run the test to verify it fails**

Run: `go test ./internal/config/ -run TestNormalizeGlobalRoutingStrategy -v`
Expected: FAIL with "undefined: GlobalStrategyRoundRobin" (or similar).

**Step 6: Apply Steps 2 and 3**

Edit `internal/config/strategy.go` to add the new constants and functions.

**Step 7: Run the test to verify it passes**

Run: `go test ./internal/config/ -run 'TestNormalizeGlobalRoutingStrategy|TestValidateGlobalRoutingStrategy' -v`
Expected: PASS.

**Step 8: Commit**

```bash
git add internal/config/strategy.go internal/config/strategy_test.go
git commit -m "feat(strategy): add global fill-first, weighted, headroom canonical values

Round-2 (docs/plans/2026-09-17-omniroute-round-2-design.md) introduces
three new global routing selectors. Coexists with the existing pool-level
fill-first canonical value, which keeps its deterministic-by-priority
semantics on pool rows."
```

---

### Task 2: Add `weight` field to `UpstreamProviderEntry` (PG + renderer + canonical snapshot)

**Files:**
- Modify: `internal/store/runtimeconfig/upstream_providers.go` (or wherever `UpstreamProviderEntry` is defined — search: `grep -rn "type UpstreamProviderEntry"`)
- Modify: `internal/configsnapshot/` planner to thread `weight` (default 1)
- Modify: `internal/store/pg_normalized_import.go` to insert/update the new column
- Test: `internal/configsnapshot/normalized_test.go`

**Step 1: Locate the entry struct and migration file**

Run: `grep -rn "type UpstreamProviderEntry" internal/ && grep -rn "ALTER TABLE.*upstream_provider_entries" internal/store/`

Confirm the column list and migration path before editing.

**Step 2: Add the new column to the struct**

```go
type UpstreamProviderEntry struct {
    // ... existing fields
    Weight int `yaml:"weight,omitempty" json:"weight,omitempty"`
}
```

**Step 3: Add a migration**

Find the PG migration directory (look for `internal/store/migrations/` or similar). Append:

```sql
ALTER TABLE upstream_provider_entries
    ADD COLUMN IF NOT EXISTS weight INTEGER NOT NULL DEFAULT 1;
```

(If migrations are tracked by a tool like goose, follow its conventions — add a new file with the next sequential number.)

**Step 4: Write the failing test**

In `internal/configsnapshot/normalized_test.go` (create if missing):

```go
func TestPlanEntriesCarryWeight(t *testing.T) {
    snap := &NormalizedResourcePlan{
        Providers: []ProviderPlan{{
            ID: "openai-1",
            Entries: []EntryPlan{
                {ProviderKey: "openai-1/key-1", Weight: 0}, // zero should normalize to 1
                {ProviderKey: "openai-1/key-2", Weight: 3},
            },
        }},
    }
    for _, e := range snap.Providers[0].Entries {
        if e.Weight < 1 {
            t.Errorf("entry %s weight %d should normalize to >=1", e.ProviderKey, e.Weight)
        }
    }
}
```

**Step 5: Run the test to verify it fails**

Run: `go test ./internal/configsnapshot/ -run TestPlanEntriesCarryWeight -v`
Expected: FAIL (Weight field missing or normalization absent).

**Step 6: Apply normalization in the planner**

In the planner, when copying entries from input to the `NormalizedResourcePlan`, force `Weight` to be at least 1:

```go
if e.Weight < 1 {
    e.Weight = 1
}
```

**Step 7: Re-run the test**

Run: `go test ./internal/configsnapshot/ -run TestPlanEntriesCarryWeight -v`
Expected: PASS.

**Step 8: Verify compile**

Run: `go build -o test-output ./cmd/server && rm test-output`
Expected: success, no output.

**Step 9: Commit**

```bash
git add internal/store/runtimeconfig/upstream_providers.go internal/store/pg_normalized_import.go internal/configsnapshot/ internal/store/migrations/
git commit -m "feat(upstream): per-entry weight column for weighted selector

Default 1, normalizes 0/negative to 1 at the planner boundary. Used by
the round-2 weighted global selector (docs/plans/2026-09-17-...)."
```

---

### Task 3: Implement `fill-first` selector in the scheduler

**Files:**
- Modify: `sdk/cliproxy/auth/scheduler.go:494-527` (add a new branch above the P2C/LU branch for `fill-first`)
- Test: `sdk/cliproxy/auth/scheduler_fillfirst_test.go`

**Step 1: Read the existing P2C/LU branch**

Re-read `scheduler.go` around line 494. Note the `readyView` and `pickReadyLookup` helpers used by P2C/LU.

**Step 2: Write the failing test**

```go
package auth

import "testing"

func TestSchedulerFillFirstPicksHighestInFlight(t *testing.T) {
    // Build a scheduler with three ready auths at the same priority,
    // in-flight counts 0, 5, 2 respectively. Fill-first must pick the
    // one with in-flight=5 (highest below cap, deterministic tie-break).
    //
    // Use the same test fixture as scheduler_test.go (existing test
    // helpers in that file).
}

func TestSchedulerFillFirstRespectsMaxParallel(t *testing.T) {
    // One auth at in-flight=cap (e.g., cap=10, in-flight=10) must be
    // skipped; second-highest in-flight is picked.
}
```

Use the existing fixtures in `sdk/cliproxy/auth/scheduler_test.go` — copy the helper that builds a `*Manager` and ready entries.

**Step 3: Run the test to verify it fails**

Run: `go test ./sdk/cliproxy/auth/ -run TestSchedulerFillFirst -v`
Expected: FAIL (selector not wired in).

**Step 4: Wire the selector**

Add a new branch in the scheduler pick path:

```go
if strategy == schedulerStrategyFillFirst {
    entries := collectReadyEntries(candidateShards, bestPriority) // reuse P2C/LU's helper
    picked := pickFillFirst(entries, s.pickReadyLookup(s.inFlightSnapshot()))
    if picked != nil && picked.meta != nil {
        return picked.auth, picked.meta.providerKey, nil
    }
    return nil, "", s.mixedUnavailableErrorLocked(normalized, model, predicate)
}
```

Implement `pickFillFirst` in the same file (or a new `scheduler_fillfirst.go`):

```go
func pickFillFirst(entries []*scheduledAuth, lookup func(string) int) *scheduledAuth {
    var best *scheduledAuth
    var bestInFlight = -1
    for _, e := range entries {
        if e == nil || e.auth == nil {
            continue
        }
        cap := maxParallelFor(e.auth)
        inflight := lookup(e.auth.ID)
        if inflight >= cap {
            continue
        }
        if inflight > bestInFlight {
            best = e
            bestInFlight = inflight
        } else if inflight == bestInFlight && best != nil {
            // deterministic tie-break: smaller auth.ID wins
            if e.auth.ID < best.auth.ID {
                best = e
            }
        }
    }
    return best
}
```

(If the existing helpers for `maxParallelFor` differ, follow their naming.)

**Step 5: Re-run the test**

Run: `go test ./sdk/cliproxy/auth/ -run TestSchedulerFillFirst -v`
Expected: PASS.

**Step 6: Verify compile**

Run: `go build -o test-output ./cmd/server && rm test-output`
Expected: success.

**Step 7: Commit**

```bash
git add sdk/cliproxy/auth/scheduler.go sdk/cliproxy/auth/scheduler_fillfirst.go sdk/cliproxy/auth/scheduler_fillfirst_test.go
git commit -m "feat(scheduler): fill-first global selector (highest in-flight below cap)

Round-2 selector (docs/plans/2026-09-17-...). Deterministic tie-break by
auth.ID. Skips candidates at or above max_parallel_requests cap."
```

---

### Task 4: Implement `weighted` selector in the scheduler

**Files:**
- Modify: `sdk/cliproxy/auth/scheduler.go` (add a branch above `weighted-round-robin`'s branch for the new global `weighted`)
- Test: `sdk/cliproxy/auth/scheduler_weighted_test.go`

**Step 1: Distinguish from `weighted-round-robin`**

`schedulerStrategyWeightedRoundRobin` already exists. The new `schedulerStrategyWeighted` is distinct — it uses the per-entry `weight` column from Task 2, NOT the smoothed weighted state from round-1. The two must coexist: `weighted-round-robin` keeps its smooth-WRR semantics; `weighted` is the new explicit-weight selector.

**Step 2: Write the failing test**

```go
func TestSchedulerWeightedRespectsEntryWeight(t *testing.T) {
    // Two auths at priority 0 with weights 1 and 3. Over 10000 picks,
    // the weight-3 auth should be picked ~75% of the time (+/- 5%).
}

func TestSchedulerWeightedFallsBackOnZeroSum(t *testing.T) {
    // If all weights are 0 (impossible after normalization but defensive),
    // the selector must fall back to round-robin, not panic.
}
```

**Step 3: Run the test to verify it fails**

Run: `go test ./sdk/cliproxy/auth/ -run TestSchedulerWeighted -v`
Expected: FAIL.

**Step 4: Wire the selector**

```go
if strategy == schedulerStrategyWeighted {
    entries := collectReadyEntries(candidateShards, bestPriority)
    picked := pickWeighted(entries) // uses entry.Weight from Task 2's column
    if picked != nil && picked.meta != nil {
        return picked.auth, picked.meta.providerKey, nil
    }
    return nil, "", s.mixedUnavailableErrorLocked(normalized, model, predicate)
}
```

Implement `pickWeighted` with prefix-sum + linear scan (O(n); n is small per priority tier, matches fill-first's pattern — binary-search variant deferred unless profiling shows it matters):

```go
func pickWeighted(entries []*scheduledAuth) *scheduledAuth {
    var total int
    for _, e := range entries {
        if e == nil || e.auth == nil || e.meta == nil {
            continue
        }
        total += e.meta.Weight
    }
    if total <= 0 {
        return entries[0] // defensive: round-robin fallback
    }
    target := rand.IntN(total)
    cum := 0
    for _, e := range entries {
        if e == nil || e.auth == nil || e.meta == nil {
            continue
        }
        cum += e.meta.Weight
        if target < cum {
            return e
        }
    }
    return entries[len(entries)-1]
}
```

**Step 5: Re-run the test**

Run: `go test ./sdk/cliproxy/auth/ -run TestSchedulerWeighted -v`
Expected: PASS (chi-square in the test asserts 70-80% range).

**Step 6: Commit**

```bash
git add sdk/cliproxy/auth/scheduler.go sdk/cliproxy/auth/scheduler_weighted.go sdk/cliproxy/auth/scheduler_weighted_test.go
git commit -m "feat(scheduler): weighted global selector (per-entry weight sampling)

Round-2 selector. Distinct from weighted-round-robin (round-1): uses
the new per-entry weight column from Task 2, not the smoothed WRR state."
```

---

### Task 5: Implement `headroom` selector in the scheduler

**Files:**
- Modify: `sdk/cliproxy/auth/scheduler.go`
- Create: `sdk/cliproxy/auth/headroom_lookup.go` — small interface to query `usage_windows` for an auth's headroom
- Test: `sdk/cliproxy/auth/scheduler_headroom_test.go`

**Step 1: Define the headroom lookup**

```go
type HeadroomLookup interface {
    // Headroom returns the lowest remaining quota percent across active
    // windows for this auth, or 100.0 if no limit is set.
    Headroom(authID string) float64
}
```

Production: backed by a read-through cache of `usage_windows`. Tests: pass a fake that returns scripted values.

**Step 2: Write the failing test**

```go
func TestSchedulerHeadroomPicksHighestRemaining(t *testing.T) {
    // Three auths with headroom 10%, 80%, 50%. Pick must be the 80% one.
}

func TestSchedulerHeadroomFallsBackToLeastUsedOnExhaustion(t *testing.T) {
    // All three auths return 0% headroom. Fall back to least-used
    // (i.e. smallest in-flight).
}

func TestSchedulerHeadroomTieBreaksByPriority(t *testing.T) {
    // Two auths with equal headroom. Higher priority wins.
}
```

**Step 3: Run the test to verify it fails**

Run: `go test ./sdk/cliproxy/auth/ -run TestSchedulerHeadroom -v`
Expected: FAIL.

**Step 4: Wire the selector**

```go
if strategy == schedulerStrategyHeadroom {
    entries := collectReadyEntries(candidateShards, bestPriority)
    picked := pickHeadroom(entries, s.headroomLookup)
    if picked != nil && picked.meta != nil {
        if allExhausted(picked, entries, s.headroomLookup) {
            // emit a decision marker so X-NixLLM-Decision can carry it
            s.lastHeadroomExhausted = true
        }
        return picked.auth, picked.meta.providerKey, nil
    }
    return nil, "", s.mixedUnavailableErrorLocked(normalized, model, predicate)
}
```

Implement `pickHeadroom` with fallback to `least-used`:

```go
func pickHeadroom(entries []*scheduledAuth, lookup HeadroomLookup) *scheduledAuth {
    var best *scheduledAuth
    bestH := -1.0
    var anyPositive bool
    for _, e := range entries {
        if e == nil || e.auth == nil {
            continue
        }
        h := lookup.Headroom(e.auth.ID)
        if h > 0 {
            anyPositive = true
        }
        if h > bestH {
            best = e
            bestH = h
        } else if h == bestH && best != nil && e.auth.ID < best.auth.ID {
            best = e
        }
    }
    if !anyPositive {
        // fall back to least-used among the ready set
        return pickLeastUsed(entries, defaultInFlightLookup)
    }
    return best
}
```

**Step 5: Re-run the test**

Run: `go test ./sdk/cliproxy/auth/ -run TestSchedulerHeadroom -v`
Expected: PASS.

**Step 6: Commit**

```bash
git add sdk/cliproxy/auth/scheduler.go sdk/cliproxy/auth/headroom_lookup.go sdk/cliproxy/auth/scheduler_headroom_test.go
git commit -m "feat(scheduler): headroom global selector with least-used fallback

Round-2 selector. Reads HeadroomLookup (interface) so production can
back it with usage_windows and tests can drive it directly. Falls back
to least-used when all candidates have 0% headroom."
```

---

### Task 6: Add `X-NixLLM-Decision` header generation

**Files:**
- Create: `sdk/cliproxy/auth/decision.go`
- Modify: `sdk/cliproxy/auth/conductor_execution.go` (and the home/non-stream siblings) to call `decision.Build(...)` after a successful dispatch
- Modify: `sdk/cliproxy/auth/conductor.go` (stream variants: build and write header before first body byte)
- Test: `sdk/cliproxy/auth/decision_test.go`

**Step 1: Write the failing test**

```go
package auth

import (
    "net/http"
    "net/http/httptest"
    "testing"
)

func TestDecisionHeaderVersionedAndNAFilled(t *testing.T) {
    d := Decision{
        Model: "gpt-5", AuthID: "auth-1", Channel: "openai",
        Strategy: "fill-first", Breaker: "n/a",
        CooldownWaitMs: 0, Attempts: 1, PoolStrategy: "fallback",
    }
    h := d.Header()
    if !strings.HasPrefix(h, "v=1;") {
        t.Errorf("missing v=1 prefix: %q", h)
    }
    if !strings.Contains(h, "breaker=n/a") {
        t.Errorf("missing breaker=n/a: %q", h)
    }
}

func TestDecisionHeaderOversizedValueTruncates(t *testing.T) {
    d := Decision{Model: strings.Repeat("x", 2000), AuthID: "a"}
    h := d.Header()
    if len(h) > 1024 {
        t.Errorf("header should be truncated, got %d bytes", len(h))
    }
}
```

**Step 2: Run the test to verify it fails**

Run: `go test ./sdk/cliproxy/auth/ -run TestDecisionHeader -v`
Expected: FAIL.

**Step 3: Implement `decision.go`**

```go
package auth

import (
    "strings"
)

const DecisionHeader = "X-NixLLM-Decision"

type Decision struct {
    Model           string
    AuthID          string
    Channel         string
    Strategy        string
    Breaker         string // closed | open | half-open | n/a
    CooldownWaitMs  int
    Attempts        int
    PoolStrategy    string // attr | fallback | compound | n/a
    QuotaHeadroom   string // pct (e.g. "98.8") | n/a
}

func (d Decision) Header() string {
    fields := []struct{ k, v string }{
        {"v", "1"},
        {"model", d.Model},
        {"auth", d.AuthID},
        {"channel", d.Channel},
        {"strategy", d.Strategy},
        {"breaker", na(d.Breaker)},
        {"cooldown_wait_ms", itoa(d.CooldownWaitMs)},
        {"attempts", itoa(d.Attempts)},
        {"pool_strategy", na(d.PoolStrategy)},
        {"quota_headroom", na(d.QuotaHeadroom)},
    }
    var b strings.Builder
    for i, f := range fields {
        if i > 0 {
            b.WriteString("; ")
        }
        b.WriteString(f.k)
        b.WriteString("=")
        b.WriteString(f.v)
    }
    out := b.String()
    if len(out) > 1024 {
        // truncate at last complete field boundary
        out = out[:1024]
        if idx := strings.LastIndex(out, ";"); idx > 0 {
            out = out[:idx]
        }
    }
    return out
}

func na(s string) string {
    if s == "" {
        return "n/a"
    }
    return s
}

func itoa(i int) string {
    // avoid strconv import noise; or use strconv.Itoa
    return strconv.Itoa(i)
}
```

**Step 4: Re-run the test**

Run: `go test ./sdk/cliproxy/auth/ -run TestDecisionHeader -v`
Expected: PASS.

**Step 5: Wire into conductor**

In `conductor_execution.go`, locate the function that returns the successful dispatch path. After the chosen auth finishes its first successful read, build the `Decision` and write the header:

```go
dec := auth.Decision{
    Model:          model,
    AuthID:         picked.auth.ID,
    Channel:        picked.auth.Channel,
    Strategy:       currentStrategy(),
    Breaker:        breakerState(picked),
    CooldownWaitMs: waitedMs,
    Attempts:       attemptCount,
    PoolStrategy:   poolStrategyAttr(picked),
    QuotaHeadroom:  headroomFor(picked),
}
w.Header().Set(auth.DecisionHeader, dec.Header())
```

For the stream variants (`conductor.go` and `conductor_home_execution.go`), set the header *before* writing the first body chunk. Use `http.Flusher` if available.

**Step 6: Add a streaming-specific test**

In `decision_test.go`:

```go
func TestDecisionHeaderWrittenBeforeFirstStreamChunk(t *testing.T) {
    // Use httptest.ResponseRecorder; verify header is present
    // before any body write.
}
```

**Step 7: Verify compile + run all cliproxy tests**

Run: `go build -o test-output ./cmd/server && rm test-output && go test ./sdk/cliproxy/auth/ -v`
Expected: PASS for everything.

**Step 8: Commit**

```bash
git add sdk/cliproxy/auth/decision.go sdk/cliproxy/auth/decision_test.go sdk/cliproxy/auth/conductor_execution.go sdk/cliproxy/auth/conductor.go sdk/cliproxy/auth/conductor_home_execution.go
git commit -m "feat(auth): X-NixLLM-Decision response header on successful dispatch

Round-2 (docs/plans/2026-09-17-...). Always on. v=1 prefix; n/a for
inapplicable fields; 1KB truncation. Wired into both regular and
stream variants of conductor dispatch."
```

---

## Commit 2 — WS2: Quota-share accounting endpoints + dashboard panel

### Task 7: Implement `/v0/management/auths/:id/quota` and `/v0/management/pools/:key/quota`

**Files:**
- Create: `internal/api/handlers/management/quota_share.go`
- Modify: `internal/api/modules/management/routes.go` (or wherever management routes are registered)
- Test: `internal/api/handlers/management/quota_share_test.go`

**Step 1: Locate the management routes file**

Run: `grep -rn "/v0/management/runtime-config" internal/api/ | head -5`

Use the same registration pattern as `runtime_config.go`.

**Step 2: Write the failing test**

```go
func TestQuotaShareAuthsHeadroomMath(t *testing.T) {
    // Set up a PG fixture with usage_windows rows for one auth at
    // 1m/1h/1d sizes; mock the handler's repo; verify headroom_pct math
    // and over_limit flag.
}
```

Use the test patterns from `internal/api/handlers/management/quota_test.go` (already exists).

**Step 3: Run the test to verify it fails**

Run: `go test ./internal/api/handlers/management/ -run TestQuotaShareAuths -v`
Expected: FAIL.

**Step 4: Implement the handler**

```go
package management

import (
    "context"
    "time"

    "github.com/gin-gonic/gin"
)

type QuotaWindow struct {
    Size         string  `json:"size"`
    Used         int64   `json:"used"`
    Limit        int64   `json:"limit"`
    HeadroomPct  float64 `json:"headroom_pct"`
    OverLimit    bool    `json:"over_limit"`
}

type QuotaModel struct {
    Model   string `json:"model"`
    Used1h  int64  `json:"used_1h"`
    Limit1h int64  `json:"limit_1h"`
}

type QuotaAuthResponse struct {
    AuthID       string       `json:"auth_id"`
    Channel      string       `json:"channel"`
    PoolStrategy string       `json:"pool_strategy"`
    Windows      []QuotaWindow `json:"windows"`
    Models       []QuotaModel  `json:"models"`
    Partial      bool         `json:"partial,omitempty"`
}

func (h *Handlers) GetAuthQuota(c *gin.Context) {
    id := c.Param("id")
    if !h.pgEnabled() {
        c.JSON(503, gin.H{"error": "PG storage not enabled"})
        return
    }
    ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
    defer cancel()

    resp, partial, err := h.repo.GetAuthQuota(ctx, id)
    if err != nil {
        c.JSON(500, gin.H{"error": err.Error()})
        return
    }
    resp.Partial = partial
    c.JSON(200, resp)
}
```

Add `repo.GetAuthQuota(ctx, id)` in the existing PG repository layer (the repo already exposes `usage_windows` reads via alerts). Implementation: one query that aggregates `SUM(used) GROUP BY window_size, model`.

**Step 5: Apply headroom_pct math**

```go
func computeHeadroom(used, limit int64) (float64, bool) {
    if limit <= 0 {
        return 100.0, false
    }
    pct := float64(limit-used) / float64(limit) * 100.0
    if pct < 0 {
        return 0.0, true
    }
    return math.Round(pct*10) / 10, used > limit
}
```

**Step 6: Add the pool endpoint**

```go
func (h *Handlers) GetPoolQuota(c *gin.Context) {
    key := c.Param("key") // format "channel:rowID"
    // Same shape, but SUM(used) GROUP BY window_size across all auths
    // whose provider_key is in the pool identified by (channel, rowID).
}
```

**Step 7: Register routes**

```go
mgmt.GET("/auths/:id/quota", h.GetAuthQuota)
mgmt.GET("/pools/:key/quota", h.GetPoolQuota)
```

**Step 8: Re-run the test**

Run: `go test ./internal/api/handlers/management/ -run 'TestQuotaShare' -v`
Expected: PASS.

**Step 9: Verify compile**

Run: `go build -o test-output ./cmd/server && rm test-output`
Expected: success.

**Step 10: Commit**

```bash
git add internal/api/handlers/management/quota_share.go internal/api/handlers/management/quota_share_test.go internal/api/modules/management/routes.go internal/store/
git commit -m "feat(management): quota-share read endpoints for auths and pools

Round-2 (docs/plans/2026-09-17-...). Aggregates usage_windows; no schema
change. 503 without PGSTORE_DSN. 2s query timeout with partial=true on
slowness."
```

---

### Task 8: Mount `/dashboard/quota` panel in the SPA

**Files:**
- Modify: `web/dashboard/src/api/developerDocs.js` (register the two endpoints)
- Create: `web/dashboard/src/pages/Quota.jsx`
- Modify: `web/dashboard/src/App.jsx` (add the route)
- Test: `web/dashboard/src/pages/Quota.test.jsx`

**Step 1: Find the existing developer-docs entry pattern**

Run: `grep -n "endpoint" web/dashboard/src/api/developerDocs.js | head -10`

Follow the existing entry shape (key, method, path, description).

**Step 2: Add the two endpoints to the catalog**

```js
{
    key: 'auth-quota',
    method: 'GET',
    path: '/v0/management/auths/:id/quota',
    description: 'Live per-window quota for one auth (read-aggregated from usage_windows).'
},
{
    key: 'pool-quota',
    method: 'GET',
    path: '/v0/management/pools/:key/quota',
    description: 'Live per-window quota summed across one pool.'
}
```

**Step 3: Build the panel page**

```jsx
// web/dashboard/src/pages/Quota.jsx
import { useQuery } from '@tanstack/react-query';
import { api } from '../api';

export function Quota() {
    const { data, isLoading } = useQuery({
        queryKey: ['pool-quotas'],
        queryFn: () => api.get('/v0/management/pools/_catalog/quota'),
        refetchInterval: 10_000,
    });
    // Render headroom bars per pool, drill-down to per-auth + per-model.
}
```

Use the existing Shadcn `Card`, `Progress`, and `Tabs` primitives.

**Step 4: Write the test**

```jsx
test('Quota renders headroom bar from mocked API', async () => {
    // Mock api.get; render Quota; assert progress bar reflects headroom_pct.
});
```

Use the testing patterns from existing dashboard pages (`web/dashboard/src/pages/`).

**Step 5: Run the test**

Run: `cd web/dashboard && npm test -- --run Quota`
Expected: PASS.

**Step 6: Add a sidebar/nav entry**

Locate the sidebar nav component (likely `web/dashboard/src/components/Sidebar.jsx` or similar). Add a "Quota" link to the existing management section.

**Step 7: Verify build**

Run: `cd web/dashboard && npm run build`
Expected: success.

**Step 8: Embed via `make dash-embed`**

Run: `make dash-embed`
Expected: success; the dashboard SPA is re-bundled into `internal/dashboardasset/`.

**Step 9: Commit**

```bash
git add web/dashboard/src/api/developerDocs.js web/dashboard/src/pages/Quota.jsx web/dashboard/src/pages/Quota.test.jsx web/dashboard/src/App.jsx internal/dashboardasset/
git commit -m "feat(dashboard): /dashboard/quota panel with 10s refresh

Round-2 (docs/plans/2026-09-17-...). Reads from the new pool-quota
endpoint (Task 7). Drill-down to per-auth and per-model."
```

---

## Commit 3 — WS3: In-memory ring recorder + events endpoint + SSE

### Task 9: Implement `internal/events/ring.go`

**Files:**
- Create: `internal/events/ring.go`
- Create: `internal/events/types.go`
- Test: `internal/events/ring_test.go`

**Step 1: Write the failing test**

```go
func TestRingWrapsAtCapacity(t *testing.T) {
    r := NewRing(3)
    for i := 0; i < 5; i++ {
        r.Record(Event{Type: "test", Ts: time.Now(), Payload: i})
    }
    got := r.Snapshot()
    if len(got) != 3 {
        t.Fatalf("want 3 events, got %d", len(got))
    }
    // oldest remaining is i=2, then 3, then 4
    if got[0].Payload.(int) != 2 || got[2].Payload.(int) != 4 {
        t.Errorf("ring wraparound order wrong: %v", got)
    }
}

func TestRingDropsUnderContention(t *testing.T) {
    // Mock the mutex to always exceed the 1ms threshold; verify
    // Dropped() ticks up and the event is not recorded.
}
```

**Step 2: Run the test to verify it fails**

Run: `go test ./internal/events/ -v`
Expected: FAIL (package does not exist).

**Step 3: Implement `types.go`**

```go
package events

import (
    "encoding/json"
    "time"
)

type Event struct {
    Type      string          `json:"type"`
    Ts        time.Time       `json:"ts"`
    RequestID string          `json:"request_id,omitempty"`
    Model     string          `json:"model,omitempty"`
    AuthID    string          `json:"auth_id,omitempty"`
    Channel   string          `json:"channel,omitempty"`
    Payload   json.RawMessage `json:"payload,omitempty"`
}
```

**Step 4: Implement `ring.go`**

```go
package events

import (
    "sync"
    "sync/atomic"
    "time"
)

type Ring struct {
    mu      sync.Mutex
    buf     []Event
    cap     int
    cursor  int
    full    bool
    dropped atomic.Int64
    now     func() time.Time
}

func NewRing(capacity int) *Ring {
    if capacity < 100 {
        capacity = 100
    }
    if capacity > 50000 {
        capacity = 50000
    }
    return &Ring{
        buf: make([]Event, capacity),
        cap: capacity,
        now: time.Now,
    }
}

func (r *Ring) Record(e Event) {
    if e.Ts.IsZero() {
        e.Ts = r.now()
    }
    start := time.Now()
    if !r.tryLock() {
        r.dropped.Add(1)
        return
    }
    defer r.mu.Unlock()
    if time.Since(start) > time.Millisecond {
        r.dropped.Add(1)
        return
    }
    r.buf[r.cursor] = e
    r.cursor = (r.cursor + 1) % r.cap
    if r.cursor == 0 {
        r.full = true
    }
}

func (r *Ring) tryLock() bool {
    return r.mu.TryLock()
}

func (r *Ring) Snapshot() []Event {
    r.mu.Lock()
    defer r.mu.Unlock()
    var out []Event
    if r.full {
        out = make([]Event, r.cap)
        copy(out, r.buf[r.cursor:])
        copy(out[r.cap-r.cursor:], r.buf[:r.cursor])
    } else {
        out = make([]Event, r.cursor)
        copy(out, r.buf[:r.cursor])
    }
    // newest first
    for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
        out[i], out[j] = out[j], out[i]
    }
    return out
}

func (r *Ring) Dropped() int64 { return r.dropped.Load() }
```

**Step 5: Re-run the test**

Run: `go test ./internal/events/ -v`
Expected: PASS.

**Step 6: Commit**

```bash
git add internal/events/
git commit -m "feat(events): bounded in-memory ring buffer for structured events

Round-2 (docs/plans/2026-09-17-...). Capacity 100-50000, default 5000.
Non-blocking Record with dropped counter; Snapshot returns newest-first."
```

---

### Task 10: Implement `/v0/management/events` GET endpoint

**Files:**
- Create: `internal/api/handlers/management/events.go`
- Modify: `internal/api/modules/management/routes.go`
- Test: `internal/api/handlers/management/events_test.go`

**Step 1: Write the failing test**

```go
func TestEventsEndpointFiltersByType(t *testing.T) {
    // Seed the ring; GET /v0/management/events?type=routing.decision;
    // assert only matching events returned, newest first.
}
```

**Step 2: Run the test to verify it fails**

Run: `go test ./internal/api/handlers/management/ -run TestEventsEndpoint -v`
Expected: FAIL.

**Step 3: Implement the handler**

```go
func (h *Handlers) GetEvents(c *gin.Context) {
    if !h.pgEnabled() {
        c.JSON(503, gin.H{"error": "PG storage not enabled"})
        return
    }
    var (
        typ   = c.Query("type")
        auth  = c.Query("auth")
        since = parseSince(c.Query("since"))
        limit = parseLimit(c.Query("limit"), 100, 500)
    )
    snap := h.events.Snapshot()
    out := make([]events.Event, 0, limit)
    for _, e := range snap {
        if typ != "" && e.Type != typ { continue }
        if auth != "" && e.AuthID != auth { continue }
        if !since.IsZero() && e.Ts.Before(since) { continue }
        out = append(out, e)
        if len(out) >= limit { break }
    }
    c.JSON(200, gin.H{"events": out})
}

func (h *Handlers) GetEventsStats(c *gin.Context) {
    c.JSON(200, gin.H{"dropped": h.events.Dropped(), "capacity": h.events.Capacity()})
}
```

**Step 4: Register routes**

```go
mgmt.GET("/events", h.GetEvents)
mgmt.GET("/events/stats", h.GetEventsStats)
```

**Step 5: Re-run the test**

Run: `go test ./internal/api/handlers/management/ -run TestEventsEndpoint -v`
Expected: PASS.

**Step 6: Commit**

```bash
git add internal/api/handlers/management/events.go internal/api/handlers/management/events_test.go internal/api/modules/management/routes.go
git commit -m "feat(management): /v0/management/events GET + stats endpoints

Round-2 (docs/plans/2026-09-17-...). Newest-first, filterable by
type/auth/since, capped at 500/page. /events/stats exposes the dropped
counter so operators can detect backpressure."
```

---

### Task 11: Implement `/v0/management/events/stream` SSE endpoint

**Files:**
- Create: `internal/api/handlers/management/events_stream.go`
- Modify: `internal/api/modules/management/routes.go`
- Test: `internal/api/handlers/management/events_stream_test.go`

**Step 1: Write the failing test**

```go
func TestEventsStreamDeliversNewEvents(t *testing.T) {
    // Use httptest.NewServer; subscribe via http.Get and read SSE frames;
    // record an event mid-stream; assert the frame arrives within 1s.
}
```

**Step 2: Run the test to verify it fails**

Run: `go test ./internal/api/handlers/management/ -run TestEventsStream -v`
Expected: FAIL.

**Step 3: Implement the SSE handler**

```go
func (h *Handlers) StreamEvents(c *gin.Context) {
    if !h.pgEnabled() {
        c.JSON(503, gin.H{"error": "PG storage not enabled"})
        return
    }
    c.Writer.Header().Set("Content-Type", "text/event-stream")
    c.Writer.Header().Set("Cache-Control", "no-cache")
    c.Writer.Header().Set("Connection", "keep-alive")
    c.Writer.Flush()

    sub := h.events.Subscribe()
    defer h.events.Unsubscribe(sub)

    ctx := c.Request.Context()
    for {
        select {
        case <-ctx.Done():
            return
        case e, ok := <-sub.C:
            if !ok {
                return
            }
            payload, _ := json.Marshal(e)
            fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", e.Type, payload)
            c.Writer.Flush()
        }
    }
}
```

Add `Subscribe()` / `Unsubscribe()` to `internal/events/ring.go` — small fan-out map of channels, each capped at 32 (drop oldest if full).

**Step 4: Register the route**

```go
mgmt.GET("/events/stream", h.StreamEvents)
```

**Step 5: Re-run the test**

Run: `go test ./internal/api/handlers/management/ -run TestEventsStream -v`
Expected: PASS.

**Step 6: Verify compile**

Run: `go build -o test-output ./cmd/server && rm test-output`
Expected: success.

**Step 7: Commit**

```bash
git add internal/api/handlers/management/events_stream.go internal/api/handlers/management/events_stream_test.go internal/events/ring.go internal/api/modules/management/routes.go
git commit -m "feat(events): SSE stream endpoint with bounded per-subscriber channel

Round-2 (docs/plans/2026-09-17-...). One-way server-to-client stream;
client disconnect drops the goroutine within 5s. Subscribe channel
capped at 32 (drops oldest on overflow)."
```

---

### Task 12: Wire event emission sites

**Files:**
- Modify: `sdk/cliproxy/auth/conductor_cooldown.go` (cooldown wait + attempts exhausted)
- Modify: `sdk/cliproxy/auth/pool_breaker.go` (breaker trips + probe verdicts)
- Modify: `sdk/cliproxy/auth/classification.go` (G6 reclassification)
- Modify: `sdk/cliproxy/auth/conductor_execution.go` (routing decision)
- Modify: `internal/usage/` (quota threshold crossings)

**Step 1: Initialize the global recorder**

In `cmd/server/main.go`, find the spot where the conductor is built. Add:

```go
rec := events.NewRing(cfg.Routing.Events.RingCapacity)
events.SetGlobal(rec)
```

Pass `rec` into the conductor.

**Step 2: Add emission calls**

```go
// in conductor_execution.go, after a successful dispatch:
events.Global().Record(events.Event{
    Type:      "routing.decision",
    RequestID: reqID,
    Model:     model,
    AuthID:    picked.auth.ID,
    Channel:   picked.auth.Channel,
    Payload:   marshalDecision(d),
})

// in conductor_cooldown.go, after a wait actually elapsed:
events.Global().Record(events.Event{
    Type:      "routing.cooldown_wait",
    RequestID: reqID,
    Model:     model,
    AuthID:    picked.auth.ID,
    Payload:   json.RawMessage(fmt.Sprintf(`{"waited_ms":%d}`, ms)),
})

// in pool_breaker.go, on state CLOSED→OPEN:
events.Global().Record(events.Event{
    Type:   "breaker.tripped",
    AuthID: poolKey,
    Payload: json.RawMessage(fmt.Sprintf(`{"failures":%d}`, failures)),
})
```

**Step 3: Write a smoke test**

```go
func TestConductorEmitsRoutingDecisionEvent(t *testing.T) {
    // Use a fake recorder; run a successful dispatch; assert the
    // recorder received a routing.decision event with the expected fields.
}
```

**Step 4: Run the smoke test**

Run: `go test ./sdk/cliproxy/auth/ -run TestConductorEmits -v`
Expected: PASS.

**Step 5: Verify all tests still pass**

Run: `go test ./...`
Expected: PASS.

**Step 6: Commit**

```bash
git add sdk/cliproxy/auth/ cmd/server/main.go internal/usage/
git commit -m "feat(events): wire emission sites for routing, breaker, reclassifier, quota

Round-2 (docs/plans/2026-09-17-...). Seven event types now emitted:
routing.decision, routing.cooldown_wait, routing.attempts_exhausted,
breaker.tripped, breaker.probe_ok, breaker.probe_fail,
cooldown.reclassified, quota.threshold_cross."
```

---

## Commit 4 — WS3 dashboard: live events tab wired to SSE

### Task 13: Add "Live" tab to the dashboard logs page

**Files:**
- Reuse the existing logs page from commit `1a786734` — find it under `web/dashboard/src/pages/` or `web/dashboard/src/components/`
- Create: `web/dashboard/src/pages/EventsLive.jsx`
- Test: `web/dashboard/src/pages/EventsLive.test.jsx`

**Step 1: Locate the logs page**

Run: `grep -rln "logs\|Logs" web/dashboard/src/pages/ | head -5`

**Step 2: Add a new tab**

Inside the existing logs page component, add a Tabs primitive with two tabs: "Static" (existing) and "Live" (new).

**Step 3: Implement `EventsLive.jsx`**

```jsx
import { useEffect, useState } from 'react';

export function EventsLive() {
    const [events, setEvents] = useState([]);
    const [filter, setFilter] = useState({ type: '', auth: '' });

    useEffect(() => {
        const params = new URLSearchParams();
        if (filter.type) params.set('type', filter.type);
        if (filter.auth) params.set('auth', filter.auth);
        const url = `/v0/management/events/stream?${params}`;
        const es = new EventSource(url, { withCredentials: true });

        es.onmessage = (msg) => {
            const e = JSON.parse(msg.data);
            setEvents((prev) => [e, ...prev].slice(0, 500));
        };
        es.onerror = () => {
            es.close();
            // fall back to polling
            const interval = setInterval(async () => {
                const r = await fetch('/v0/management/events?limit=100');
                const j = await r.json();
                setEvents(j.events || []);
            }, 10_000);
            return () => clearInterval(interval);
        };
        return () => es.close();
    }, [filter]);

    return (
        <div>
            <input placeholder="type" value={filter.type} onChange={...} />
            <input placeholder="auth" value={filter.auth} onChange={...} />
            <List items={events} />
        </div>
    );
}
```

**Step 4: Write the test**

```jsx
test('EventsLive falls back to polling when SSE errors', async () => {
    // Mock EventSource to fire an error; verify fetch('/v0/management/events') is called.
});
```

**Step 5: Run the test**

Run: `cd web/dashboard && npm test -- --run EventsLive`
Expected: PASS.

**Step 6: Build + embed**

Run: `make dash-embed`
Expected: success.

**Step 7: Commit**

```bash
git add web/dashboard/src/pages/EventsLive.jsx web/dashboard/src/pages/EventsLive.test.jsx internal/dashboardasset/
git commit -m "feat(dashboard): Live events tab wired to SSE with polling fallback

Round-2 (docs/plans/2026-09-17-...). Filterable by type and auth.
Closes the existing logs page (commit 1a786734) onto the new event feed."
```

---

## Final checks

After all four commits, run the canonical checks from AGENTS.md:

```bash
gofmt -w .
go build -o test-output ./cmd/server && rm test-output
go test ./...
cd web/dashboard && npm run build && cd ../..
make dash-embed
```

Then commit any final formatting/build changes:

```bash
git add -A
git commit -m "chore: gofmt + dash-embed after round-2"
```

---

## References

- Round-2 design: `docs/plans/2026-09-17-omniroute-round-2-design.md`
- Round-1 design: `docs/plans/2026-09-11-omniroute-incremental-routing-design.md`
- Pool routing strategy: `docs/plans/2026-09-03-upstream-entry-routing-strategy-design.md`
- Logs page design: commit `1a786734`
- Endpoint catalog: `web/dashboard/src/api/developerDocs.js`
- AGENTS.md canonical-check recipe
