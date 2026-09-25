# Upstream Entries — Max Concurrent & Auto-Disable — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.
> Execution mode (user's standing choice): **subagent-driven**, fresh implementer per task → spec review → quality review → fix round → re-review, working directly on `main`.

**Goal:** Implement `docs/plans/2026-09-25-auto-disable-max-concurrent-design.md` — (1) a per-`api_key_entry` concurrency cap (`max_concurrent` + `max_wait_ms`) enforced as an in-flight-aware scheduler eligibility constraint with wait-then-failover in the execution retry loop, and (2) rule-based permanent auto-disable of an entry on configured upstream error codes, persisted back to PG via a server-side sink and re-rendered, with manual or swept auto re-enable.

**Architecture:** Two features that meet at config stamping (synthesizer). (1) `max_concurrent` per entry is stamped onto `auth.Attributes["max_parallel"]` — the attribute the round-2 scheduler ALREADY reads via `maxParallelForAuth` (`scheduler_fillfirst.go:18`) — then the scheduler's ready/busy state becomes in-flight-aware so EVERY strategy treats a full entry as non-eligible; the execution loop's picked-nothing path sleeps bounded by `max_wait_ms` then failovers. (2) Per-provider `auto_disable_error_codes` + `auto_disable_cooldown_seconds` are stamped onto auth attributes; the conductor's error-classification path fires a server-side `AutoDisableSink` (pattern: `SetRefreshSink` in `conductor.go:228`) on code match; the sink writes `auto_disabled=true` + reason + timestamp to the PG entry and re-renders config, so the renderer's existing `e.Disabled` skip drops the entry. Re-enable is manual (dashboard) or swept server-side after the cooldown.

**Tech Stack:** Go 1.26, logrus, Gin (management API), `sdk/cliproxy/auth` (conductor/scheduler), `internal/upstreamsync`, `internal/watcher/synthesizer`, `internal/store` (PG), React/Vite dashboard.

---

## Verified anchors (re-verify each before editing; prior plans had stale line numbers)

### Config/store
- `internal/store/pg_upstream_providers.go:107-159` — `UpstreamProviderAPIKey` struct (fields `Weight`/`Priority`/`Disabled`/`Retry*` show the pointer-field convention for "unset vs 0").
- `internal/store/pg_upstream_providers.go:554-568` — `loadChildren` entries SELECT (add new columns here).
- `internal/store/pg_upstream_providers.go:670-770` — `syncAPIKeyEntriesTx` INSERT/UPDATE for entries (add new columns in both statements).
- `internal/store/pg_upstream_providers.go:341-440` — `Update` (provider row UPDATE statement; add auto-disable cols to `SET`/`RETURNING`).
- `internal/store/postgresstore.go:834-967` — `Migrate()`: idempotent `ALTER TABLE ... ADD COLUMN IF NOT EXISTS` list; entries table = `s.fullTableName(s.cfg.UpstreamProviderEntriesTable)`, provider table = `s.fullTableName(s.cfg.UpstreamProvidersTable)`. The weight/priority/disabled/retry migrations above show the exact shape to copy.
- `internal/upstreamsync/render.go:225-315` — `claudeKeyFromProviderWithPools` + `buildClaudeKeyWithPools` (skip `e.Disabled`; copy `Weight`/`Priority`; add `MaxConcurrent`/`MaxWaitMs`). `:317-359` — `openAICompatFromProviderWithPools` (same skip + copy into `config.OpenAICompatibilityAPIKey`).
- Provider-level row fields for auto-disable live on `store.UpstreamProvider` (`pg_upstream_providers.go:18-82`) and render onto `config.ClaudeKey` / `config.OpenAICompatibility`.

### GitHub issues happen if the task touches ONLY `internal/translator/` — this plan does not.

### Runtime (sdk/cliproxy/auth)
- `sdk/cliproxy/auth/scheduler_fillfirst.go:11-30` — `maxParallelForAuth` reads `auth.Attributes["max_parallel"]`; `pickFillFirst` (`:35-80`) already respects it.
- `sdk/cliproxy/auth/scheduler.go:461-488` — `maxParallelLookup()` (builds `caps map[authID]int` from `maxParallelForAuth`); `:566,:631,:699,:907` call sites pass it to pickers.
- `sdk/cliproxy/auth/scheduler.go:1274-1284` — `pickReadyLocked`; `:1320-1345` — `pickReadyAtPriorityLocked` (switch on strategy, passes `inFlightCount`/`maxParallel`); `:1286-1316` — `highestReadyPriorityLocked` uses only `predicate` (NOT in-flight-aware — the gap).
- `sdk/cliproxy/auth/scheduler.go:952-985` — `scheduledAuthPredicate` (no in-flight/maxParallel access — needs extension or a wrapper).
- `sdk/cliproxy/auth/scheduler.go:222-268` — `adjustInFlight`/`inFlightSnapshot`/`inFlightForAuth`.
- `sdk/cliproxy/auth/conductor_execution.go:309-326` — `acquireAuthInFlight`/`releaseAuthInFlight` (counter drives cap). `:369-593` — `executeMixedOnce` loop (picked-nothing → wait-then-failover insertion point after `errPick != nil` branch; `:422-428` pick error path).
- `sdk/cliproxy/auth/selector.go:675-728` — `isAuthBlockedForModel` (per-model ready/blocked decision; pool breaker + cooldown states).
- `sdk/cliproxy/auth/conductor.go:228-250` — `SetRefreshSink` (atomic-pointer sink pattern to mirror for `SetAutoDisableSink`); `:253-298` — `recordRefreshOutcome` (fire-and-forget + panic-recovered pattern).
- `sdk/cliproxy/auth/conductor_cooldown.go:638-660` — `CooldownStateSnapshot` (management reads cooldowns); `:1615-1625` — `shouldSkipCredentialCooldown` (errors that must NOT trigger the new sink).
- `sdk/cliproxy/auth/classification.go:20` — `AttributeEntryProviderKey = "entry_provider_key"` (entry → auth identity; used to attribute auto-disable to a PG entry).

### Synthesizer
- `internal/watcher/synthesizer/config.go:55-115` — `addOpenAICompatEntryProviderKey` / `addClaudeEntryProviderKey` (stamp `entry_provider_key`; sibling spot to stamp `max_parallel` + auto-disable attrs).
- `internal/watcher/synthesizer/config.go:565-575` — second `addClaudeEntryProviderKey` call site (Claude). Locate where `Attributes` map is built for each auth and add stamping there.

### Server side (management)
- `internal/api/handlers/management/handler.go:440-470` — `SyncLogSink()` (the RefreshSink adapter pattern; `AutoDisableSink` adapter mirrors this).
- `internal/api/handlers/management/handler.go:1063-1110` — `applyUpstreamProviders` (re-render config from PG + reload; the sink calls this after writing `auto_disabled`).
- `internal/api/handlers/management/alerts_runner.go` — background sweep pattern for the auto-re-enable sweeper.
- `internal/api/handlers/management/upstream_providers.go` — provider CRUD handlers (dashboard round-trips new fields via configsnapshot automatically).
- `internal/api/server_management.go` — mgmt route registration (`mgmt` group); add re-enable convenience route if needed.

### Config snapshot + validation
- `internal/configsnapshot/` — `NormalizedResourcePlan`, `MarshalYAML`/`UnmarshalYAML`, `Checksum` round-trip; new yaml-tagged fields flow through with zero code if tags are correct.
- `internal/configvalidation/` — `Validate(snapshot)` re-parses config; new fields flow through.

### Dashboard
- `web/dashboard/src/pages/upstream-provider-editor/` — entries tab (add `max_concurrent`/`max_wait_ms` inputs), provider form (add auto-disable code list + cooldown).
- `web/dashboard/src/pages/UpstreamProvidersPage.jsx` — list status chip for auto-disabled count.
- Per-memory: `make dash-embed` must copy dist into `internal/dashboardasset` before rebuilding; configsnapshot/validation are shared by dashboard saves + CLI imports.

---

## Plan-level decisions (locked; reviewers enforce these)

1. **`max_concurrent` maps to the EXISTING `auth.Attributes["max_parallel"]` attribute**, not a new attribute. The scheduler's fill-first/p2c/least-used pickers already consume it via `maxParallelLookup()`; only `highestReadyPriorityLocked`/`readyView.first` need to become in-flight-aware so a full entry is NON-eligible in every strategy (decision: in-flight-aware ready/busy).
2. **In-flight-aware ready/busy** is implemented by (a) extending the pickers that receive `inFlightCount`+`maxParallel` to skip entries at/over cap in the READY scan (not just fill-first), and (b) making `highestReadyPriorityLocked` treat a priority bucket whose every candidate is over cap as empty (using the same in-flight/maxParallel lookups). `isAuthBlockedForModel` is left untouched — it has no counter access and the scheduler already carries the counter.
3. **Wait-then-failover lives in `executeMixedOnce`** (and `executeCountMixedOnce`): when `pickNextMixed` returns `auth_not_found`/nil because all entries were over cap, the loop sleeps in small increments (e.g. 50ms) up to the entry's `max_wait_ms` (shared per-request cap resolver), then `continue`s to re-pick. No blocking in the scheduler; the sleep honors ctx cancellation + the request's own deadline. When the wait budget expires, the error returns to the higher routing layer unchanged (design D6).
   - Clarification: the sleep must live OUTSIDE `m.mu`/scheduler lock — it sits in the execution loop which holds no scheduler lock while waiting.
   - To avoid sleeping when a cap-unrelated `auth_not_found` occurred, the pick path must distinguish "all-entries-over-cap" from "no auth at all". Introduce a sentinel: pickers return a marker (new `blockReasonOverCap` surfaced as a typed error `*Error{Code:"entry_capacity", Retryable:true}` or a bool on the pick result). The design doc's D6 (no new user-facing error) is preserved: `entry_capacity` is internal, translated to a short sleep; only after budget expiry does the outer error surface.
4. **Auto-disable config is provider-level only** (no global): `auto_disable_error_codes` (string list) + `auto_disable_cooldown_seconds` (*int; nil = manual re-enable). Empty list = feature off. Rendered onto every auth of that provider.
5. **Sink contract:** `AutoDisableSink func(ctx context.Context, e AutoDisableEvent)` where `AutoDisableEvent{ Provider, EntryID, Code, Message string }`. Fired from the conductor's classification path on code match, exactly like `RefreshSink` (atomic pointer, fire-and-forget goroutine, panic-recovered). Core `sdk/cliproxy/auth` NEVER imports the PG store.
6. **Sink adapter (server side)** — in one PG tx per event: `UPDATE entries SET disabled=true, auto_disabled=true, auto_disabled_at=NOW(), auto_disabled_reason=left($code,256) WHERE id=$entryID AND provider_id=$providerID AND NOT (disabled AND NOT auto_disabled)` (never clobber an operator's manual `disabled`), then call `applyUpstreamProviders` to re-render + reload. Idempotent: re-firing on an already-auto-disabled entry is a no-op.
7. **`auto_disabled` renders as `disabled` to config**: the renderer already skips `e.Disabled`; persisting `disabled=true` + `auto_disabled=true` means rendering needs ZERO change (design D1). Re-enable clears `auto_disabled` (and `disabled` only if it was auto-set) → re-render.
8. **Re-enable sweeper (server-side)** — background goroutine (alerts_runner style), interval ~60s: `UPDATE entries SET auto_disabled=false, disabled=false ... WHERE auto_disabled=true AND auto_disabled_at + (provider's cooldown seconds) <= NOW()` only for providers that configured `auto_disable_cooldown_seconds > 0`; NEVER touches entries where `disabled=true AND auto_disabled=false` (manual). Then re-render when anything changed.
9. **Attribute stamping:** synthesizer stamps `max_parallel` (entry `MaxConcurrent`) + `auto_disable_codes` (provider) + `auto_disable_cooldown_seconds` + `entry_auto_id` (providerID:entryID for sink attribution via `AttributeEntryProviderKey` — already present) onto each auth. Attribute keys: `"max_parallel"` (existing), new `"auto_disable_codes"` (JSON array string), `"auto_disable_cooldown_seconds"`.
10. **Error-code matching:** conductor matches `err.Code` (exact) OR numeric `HTTPStatus` string (e.g. config lists `"401"` and error status is 401) against the auth's auto-disable code list. `shouldSkipCredentialCooldown(err)` errors NEVER trigger the sink (request-scoped / connection-lifecycle stay outside; design keeps transient cooling intact).
11. **Config snapshot + validation round-trip all new fields** — new yaml tags on config types; `Checksum` must change when they change.
12. **AGENTS.md constraints:** no standalone `internal/translator/` change (none planned); no new upstream-connection timeouts (the wait sleep is pre-connection admission, bounded, ctx-cancelable — allowed); English comments; gofmt per-file (never tree-wide while another agent works); build verification (`go build -o /tmp/... `) before every commit; commit message ends with `Co-Authored-By: Claude Code <noreply@anthropic.com>`.

---

### Task 1: Store struct fields + PG migration + entry SELECT/INSERT/UPDATE

**Files:**
- Modify: `internal/store/pg_upstream_providers.go` (`UpstreamProviderAPIKey`, `UpstreamProvider`, `loadChildren` SELECT, `syncAPIKeyEntriesTx` INSERT/UPDATE)
- Modify: `internal/store/postgresstore.go` (`Migrate()`)
- Test: `internal/store/pg_upstream_providers_test.go`

**Step 1: Write failing tests** — table/round-trip against the store:
1. Create provider with API-key entries carrying `MaxConcurrent: 3`, `MaxWaitMs: 1500` → `Get` returns them.
2. Create provider with `AutoDisableErrorCodes: ["401","account_suspended"]`, `AutoDisableCooldownSeconds: 3600` → `Get` returns them.
3. Update round-trips the new fields (entry with ID retained).
4. Migration idempotency: run `Migrate` twice (existing test helper `ensureMigrated`), new columns exist.

**Step 2: Run** `go test ./internal/store/ -run 'TestUpstreamProvider' -v` → FAIL (unknown field / columns).

**Step 3: Implement.**

Struct additions:
```go
// in UpstreamProviderAPIKey
MaxConcurrent *int `json:"max_concurrent,omitempty"`       // per-entry in-flight hard cap; nil/0 = unlimited
MaxWaitMs     *int `json:"max_wait_ms,omitempty"`           // wait budget before failover; nil/0 = default

// auto-disabled (runtime-written, not operator input)
AutoDisabled        bool           `json:"auto_disabled,omitempty"`
AutoDisabledAt      *time.Time     `json:"auto_disabled_at,omitempty"`
AutoDisabledReason  string         `json:"auto_disabled_reason,omitempty"`
```
```go
// in UpstreamProvider
AutoDisableErrorCodes      []string `json:"auto_disable_error_codes,omitempty"`
AutoDisableCooldownSeconds *int     `json:"auto_disable_cooldown_seconds,omitempty"` // nil = manual re-enable
```

Migration (append to `Migrate()`, copy the weight/disabled column shapes):
```go
// max_concurrent / max_wait_ms per entry (Max Concurrent feature).
if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
    `ALTER TABLE %s ADD COLUMN IF NOT EXISTS max_concurrent INTEGER`, upstreamEntriesTable,
)); err != nil { return fmt.Errorf("postgres store: migrate upstream entries max_concurrent column: %w", err) }
if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
    `ALTER TABLE %s ADD COLUMN IF NOT EXISTS max_wait_ms INTEGER`, upstreamEntriesTable,
)); err != nil { return fmt.Errorf("postgres store: migrate upstream entries max_wait_ms column: %w", err) }
// auto_disabled runtime flag + attribution.
if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
    `ALTER TABLE %s ADD COLUMN IF NOT EXISTS auto_disabled BOOLEAN NOT NULL DEFAULT FALSE`, upstreamEntriesTable,
)); err != nil { return fmt.Errorf("postgres store: migrate upstream entries auto_disabled column: %w", err) }
if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
    `ALTER TABLE %s ADD COLUMN IF NOT EXISTS auto_disabled_at TIMESTAMPTZ`, upstreamEntriesTable,
)); err != nil { return fmt.Errorf("postgres store: migrate upstream entries auto_disabled_at column: %w", err) }
if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
    `ALTER TABLE %s ADD COLUMN IF NOT EXISTS auto_disabled_reason TEXT`, upstreamEntriesTable,
)); err != nil { return fmt.Errorf("postgres store: migrate upstream entries auto_disabled_reason column: %w", err) }
// provider-level auto-disable config (Auto-Disable feature).
if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
    `ALTER TABLE %s ADD COLUMN IF NOT EXISTS auto_disable_error_codes TEXT[]`, upstreamProvidersTable,
)); err != nil { return fmt.Errorf("postgres store: migrate upstream providers auto_disable_error_codes column: %w", err) }
if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
    `ALTER TABLE %s ADD COLUMN IF NOT EXISTS auto_disable_cooldown_seconds INTEGER`, upstreamProvidersTable,
)); err != nil { return fmt.Errorf("postgres store: migrate upstream providers auto_disable_cooldown_seconds column: %w", err) }
```

`loadChildren` SELECT — add `max_concurrent, max_wait_ms, auto_disabled, auto_disabled_at, auto_disabled_reason` (as `sql.NullInt64` x2, `bool`, `sql.NullTime`, `sql.NullString`) and scan them.

`syncAPIKeyEntriesTx` — add columns to INSERT and UPDATE statements (nullable via existing `nullableInt`, `nullableTime`; `auto_disabled`/`auto_disabled_reason`/`auto_disabled_at` are NULL-ed ONLY on insert; on update, preserve existing auto flags when the incoming entry has them zero — i.e. only set `auto_disabled` fields when the incoming struct has them set, to avoid an operator PUT wiping the runtime flag; simplest correct approach: `COALESCE`-skip by using `entry.AutoDisabled` for insert, and for update preserve via `CASE WHEN $n::boolean THEN ... END` or by only writing them when nonzero — see the retry-col `nullable*` precedent; keep the operator-editable fields (`max_concurrent`, `max_wait_ms`) always-written).

Provider row `Update` — add `auto_disable_error_codes` (marshal via existing `marshalStringSlice`, PG `TEXT[]`/nullable) + `auto_disable_cooldown_seconds` to the `SET` and `RETURNING` (mirror `cloak_sensitive_words`'s text[] handling).

`loadChildren` for provider row — add a SELECT of the two new provider columns; `scanUpstreamProvider` gains the columns.

**Step 4: Run** `go test ./internal/store/ -run 'TestUpstreamProvider' -count=1` → PASS. Also run the store migration test `go test ./internal/store/ -run 'TestMigrate' -count=1`.

**Step 5: Verify build** `go build -o /tmp/nixllm-build ./cmd/server`. **Step 6: Commit** `feat(store): per-entry max concurrent + auto-disable columns`.

---

### Task 2: Config types + renderer + configsnapshot round-trip

**Files:**
- Modify: `internal/config/config_types.go` (`ClaudeKey`, `OpenAICompatibility`, `OpenAICompatibilityAPIKey`, `OpenCodeGoAPIKey` family)
- Modify: `internal/upstreamsync/render.go` (`buildClaudeKeyWithPools`, OpenAI compat + opencode-go paths)
- Test: `internal/upstreamsync/claude_multi_entry_test.go` (extend) + an existing render test

**Step 1: Failing tests:**
1. Provider row (with entries carrying `MaxConcurrent`/`MaxWaitMs`) renders entries with those values.
2. Provider row with `AutoDisableErrorCodes`/`AutoDisableCooldownSeconds` renders them onto each entry.
3. Auto-disabled (or disabled) entries still skipped at render.

**Step 2: Run** `go test ./internal/upstreamsync/ -run TestRender -v` → FAIL.

**Step 3: Implement.** Add to config types:
```go
// config.ClaudeKey
MaxConcurrent *int `yaml:"max-concurrent,omitempty" json:"max-concurrent,omitempty"`   // per-entry in-flight cap; nil/0 unlimited (JSON non-empty? § note)
MaxWaitMs     *int `yaml:"max-wait-ms,omitempty" json:"max-wait-ms,omitempty"`
```
(`config.OpenAICompatibilityAPIKey` same; `config.OpenAICompatibility` + Claude provider-level carry `AutoDisableErrorCodes []string` + `AutoDisableCooldownSeconds *int`.)

Renderer copy loops (`buildClaudeKeyWithPools` for Claude; OpenAI compat + opencode-go paths): copy `MaxConcurrent`/`MaxWaitMs` from entry, copy provider auto-disable fields onto each item. Preserve the existing `e.Disabled` skip.

**Step 4:** Run upstreamsync tests → PASS. **Step 5:** `go build -o /tmp/nixllm-build ./cmd/server`. **Step 6: Commit** `feat(config): render per-entry max concurrent + provider auto-disable fields`.

---

### Task 3: Synthesizer attribute stamping

**Files:**
- Modify: `internal/watcher/synthesizer/config.go`
- Test: extend `internal/watcher/synthesizer/*_test.go` (locate existing synthesizer test for Claude/OpenAI entries)

**Step 1: Failing tests** — a provider entry with `MaxConcurrent` produces an auth with `Attributes["max_parallel"] == "3"`; provider with auto-disable codes produces auth with `Attributes["auto_disable_codes"]` JSON array; cooldown seconds stamped; no stamp when unset.

**Step 2: Run** synthesizer tests → FAIL.

**Step 3: Implement.** At each auth-Attributes build site (the same loops where `addOpenAICompatEntryProviderKey` / `addClaudeEntryProviderKey` are called):
```go
if entry.MaxConcurrent != nil && *entry.MaxConcurrent > 0 {
    attrs["max_parallel"] = strconv.Itoa(*entry.MaxConcurrent)
}
if entry.MaxWaitMs != nil && *entry.MaxWaitMs > 0 {
    attrs["max_wait_ms"] = strconv.Itoa(*entry.MaxWaitMs)
}
if len(provider.AutoDisableErrorCodes) > 0 {
    b, _ := json.Marshal(provider.AutoDisableErrorCodes)
    attrs["auto_disable_codes"] = string(b)
}
if provider.AutoDisableCooldownSeconds != nil && *provider.AutoDisableCooldownSeconds > 0 {
    attrs["auto_disable_cooldown_seconds"] = strconv.Itoa(*provider.AutoDisableCooldownSeconds)
}
```

**Step 4:** synthesizer tests → PASS. **Step 5:** build verify. **Step 6: Commit** `feat(synthesizer): stamp per-entry concurrency + auto-disable attrs`.

---

### Task 4: Scheduler in-flight-aware eligibility (hard cap across all strategies)

**Files:**
- Modify: `sdk/cliproxy/auth/scheduler.go` (`pickReadyAtPriorityLocked`, `highestReadyPriorityLocked`, `scheduledAuthPredicate`, readyView)
- Modify: `sdk/cliproxy/auth/scheduler_fillfirst.go` (`pickFillFirst` — already cap-aware; keep)
- Test: `sdk/cliproxy/auth/scheduler_inflight_test.go` (extend) or new `scheduler_capacity_test.go`

**Step 1: Failing tests** (in `sdk/cliproxy/auth`):
1. Two entries, both with `max_parallel=1`, first in-flight → pick must return the SECOND (round-robin strategy: first is non-eligible; currently the bucket `pickFirst` returns the first because predicate ignores in-flight).
2. Both entries at cap → pick returns nil / `entry_capacity` marker.
3. Fill-first: one entry below cap, one at cap → fill-first keeps the below-cap one.
4. `max_parallel` unset → unlimited (all eligible).
5. After a release (in-flight dec), the entry becomes eligible again.

**Step 2: Run** → FAIL (round-robin picks the busy entry).

**Step 3: Implement.** The core change: the ready scan must treat an entry at/over its `maxParallel` cap as non-ready. Concretely:
- Add a helper `entryOverCap(entry *scheduledAuth, inFlightCount func(string) int, maxParallel func(string) int) bool` (nil entry → false).
- `highestReadyPriorityLocked` becomes capacity-aware: pass `inFlightCount`/`maxParallel`, and when scanning a bucket's `flat`, skip candidates for which `entryOverCap` is true; if after the scan a bucket has no eligible candidate, it is treated as NOT ready (so the next priority bucket serves).
- `pickReadyAtPriorityLocked`'s per-strategy pickers already receive the lookups; ensure `pickWeighted`/`pickRoundRobin`/`pickFirst` wrappers also skip over-cap entries (extend predicate or the view iteration). Safest: build the capacity check INTO `scheduledAuthPredicate` by changing its signature to accept the two lookups (update ALL call sites — `pickSingleWithStrategy`, `pickMixedWithStrategy`, headroom branch) OR add a composite helper `capacityAwarePredicate(predicate, inFlightCount, maxParallel)` that wraps the existing predicate. Prefer the composite wrapper to keep call-site churn low and explicit.
- When every eligible candidate is over cap, the pick returns nil; `pickReadyAtPriorityLocked`/`pickReadyLocked` must surface the distinction so `pickMixedWithStrategy` / `pickSingleWithStrategy` can return a typed `*Error{Code:"entry_capacity", Retryable:true}` instead of the generic `auth_not_found`. (A `shard.unavailableErrorLocked` variant with `blockReasonOverCap`.)
   - Add `blockReasonOverCap` to the `blockReason` enum (selector.go:230-236) and thread it through `unavailableErrorLocked` / `isAuthBlockedForModel`? — NO: `isAuthBlockedForModel` has no counter access. Instead the over-cap signal is computed in the pickers and returned directly by the scheduler methods (a new `(auth, overCap bool)` or a sentinel error). Keep it local to the scheduler; `pickMixedWithStrategy`/`pickSingleWithStrategy` translate `overCap=true` → `entry_capacity` error.

**Step 4:** run scheduler tests → PASS. **Step 5:** build verify the auth package (already part of server). **Step 6: Commit** `feat(auth): in-flight-aware scheduler eligibility for per-entry concurrency caps`.

---

### Task 5: Execute-loop wait-then-failover (max_wait_ms)

**Files:**
- Modify: `sdk/cliproxy/auth/conductor_execution.go` (`executeMixedOnce`, `executeCountMixedOnce`)
- Modify: `sdk/cliproxy/auth/retry_loop.go` (optional helper for bounded sleep honoring ctx)
- Test: `sdk/cliproxy/auth/conductor_execution_test.go` (or new `conductor_capacity_wait_test.go`)

**Step 1: Failing tests:**
1. `pickNextMixed` returns `entry_capacity` → executeMixedOnce sleeps (instrumented/fake clock) then re-picks; when a later pick succeeds before budget, returns that auth's response.
2. All entries stay over cap until `max_wait_ms` expires → returns the `entry_capacity`/outer error.
3. ctx canceled during the wait → returns promptly (ctx cancellation honored, no goroutine leak).
4. `max_wait_ms` unset → a short default (e.g. 0 or 200ms) applies.

**Step 2: Run** → FAIL (no wait behavior).

**Step 3: Implement.** In `executeMixedOnce` (and `executeCountMixedOnce`), the `errPick != nil` branch (`:423`):
```go
if isEntryCapacityError(errPick) {
    if !m.waitForCapacity(execCtx, maxWaitForAuth(authCandidates)) { // sleeps ≤ max_wait_ms in ≤50ms increments, ctx-aware
        // budget exhausted — fall through to the normal error return
    }
    continue // re-pick immediately (selection is now non-blocking)
}
```
where `maxWaitForAuth` resolves the request's wait budget from the candidate first-seen entry's `max_wait_ms` attr (or default `DefaultCapacityWaitMS`). `waitForCapacity` uses `select { case <-ctx.Done(): return false; case <-timer.C: return true }` per increment — never holds the scheduler lock (it runs in the execution loop, between picks). Guard so a NON-capacity `auth_not_found` (genuinely no auth) skips the wait entirely (design: no spurious delay).

**Step 4:** run tests → PASS. **Step 5:** build verify. **Step 6: Commit** `feat(auth): bounded wait-then-failover when all entries are at capacity`.

---

### Task 6: Conductor error classification → AutoDisableSink

**Files:**
- Modify: `sdk/cliproxy/auth/conductor.go` (add `SetAutoDisableSink` + `AutoDisableSink`/`AutoDisableEvent` types + `recordAutoDisable` fire-and-forget)
- Modify: `sdk/cliproxy/auth/conductor_cooldown.go` (classification path: match codes → fire sink)
- Test: `sdk/cliproxy/auth/ auto_disable_sink_test.go` (new)

**Step 1: Failing tests:**
1. Auth with `auto_disable_codes=["401","account_suspended"]`; error `{Code:"account_suspended", HTTPStatus:400}` → sink fired with `Provider`, `EntryID` (from `AttributeEntryProviderKey`), `Code`.
2. Error `{Code:"other", HTTPStatus:400}` → sink NOT fired.
3. Numeric code list `["401"]` matches an error with `HTTPStatus:401` (even when `err.Code` is unrelated).
4. `shouldSkipCredentialCooldown` error → NOT fired.
5. Sink nil → no-op; sink panic → recovered (never blocks classification).
6. `AutoDisableEvent.EntryID` is correctly parsed from `entry_provider_key` (`providerKey:key-<id>`), including the fallback `:name` form producing `EntryID=0` (sink adapter treats 0 as "cannot attribute → no-op logged").

**Step 2: Run** → FAIL (no sink).

**Step 3: Implement.**
```go
// conductor.go
type AutoDisableEvent struct {
    Provider string `json:"provider"`
    EntryID  int64  `json:"entry_id"`
    Code     string `json:"code"`
    Message  string `json:"message,omitempty"`
}
type AutoDisableSink func(ctx context.Context, e AutoDisableEvent)
// SetAutoDisableSink mirrors SetRefreshSink (atomic.Pointer[AutoDisableSink], nil detaches).
// recordAutoDisable mirrors recordRefreshOutcome (goroutine + panic recover).
```
In the classification path (where `resultErrorFromError` / code pinning happens, before `applyAuthFailureState`), after computing the final `resultErr` code:
```go
if resultErr != nil && !shouldSkipCredentialCooldown(resultErr) {
    if code := authAutoDisableCodeMatch(auth, resultErr); code != "" {
        entryID := entryIDFromProviderKey(auth)  // parse AttributeEntryProviderKey's "key-<id>" suffix
        if entryID > 0 {
            m.recordAutoDisable(context.Background(), AutoDisableEvent{Provider: auth.Provider, EntryID: entryID, Code: code, Message: trimMessage(resultErr.Message)})
        }
    }
}
```
`authAutoDisableCodeMatch` parses `auth.Attributes["auto_disable_codes"]` (JSON array), matches exact `err.Code` OR the numeric `HTTPStatus` (config lists `"401"`, `strconv.Itoa(resultErr.StatusCode())` matches). Returns the matched code ("" = no match).

**Step 4:** run tests → PASS. **Step 5:** build verify. **Step 6: Commit** `feat(auth): auto-disable sink fired on configured upstream error codes`.

---

### Task 7: Server-side sink adapter + re-render

**Files:**
- Create: `internal/store/pg_upstream_entries_flag.go` (or method on `pgUpstreamProviderStore`): `SetEntryAutoDisabled(ctx, providerID, entryID, code) (bool, error)` — tx: `UPDATE entries SET disabled=true, auto_disabled=true, auto_disabled_at=NOW(), auto_disabled_reason=left($3,256) WHERE id=$2 AND provider_id=$1 AND NOT (disabled AND NOT auto_disabled) RETURNING id`; returns `false, nil` when no row matched (idempotent no-op).
- Create: `internal/store/pg_upstream_entries_flag_test.go`
- Modify: `internal/api/handlers/management/auto_disable_sink.go` (adapter + wiring caller)
- Modify: `internal/api/handlers/management/handler.go` (add `SetAutoDisableSink` wiring on the manager + `AutoDisableSink()` accessor)
- Test: `internal/api/handlers/management/auto_disable_sink_test.go`

**Step 1: Failing tests:**
1. Store: `SetEntryAutoDisabled(providerID, entryID, "401")` sets `disabled=true`,`auto_disabled=true`; second call is idempotent (returns false).
2. Store: entry manually `disabled=true, auto_disabled=false` → `SetEntryAutoDisabled` returns false (NOT clobbered; rows unchanged).
3. Adapter: receives event → store write happens + `applyUpstreamProviders` re-render triggered (assert via injected fake re-render func).

**Step 2: Run** → FAIL (no methods).

**Step 3: Implement.** Add the store method (guarded by `if s == nil || s.db == nil`); the adapter:
```go
func (h *Handler) AutoDisableSink() coreauth.AutoDisableSink {
    if h == nil { return nil }
    // (read pgUpstreamProviders; nil → nil)
    return func(ctx context.Context, e coreauth.AutoDisableEvent) {
        if e.EntryID <= 0 { log.Warn("auto-disable: unattributable entry (no PG id)"); return }
        go func() {
            defer func(){ _ = recover() }()
            h.mu.Lock(); store := h.pgUpstreamProviders; h.mu.Unlock()
            if store == nil { return }
            wctx, cancel := context.WithTimeout(context.Background(), 5*time.Second); defer cancel()
            if _, err := store.SetEntryAutoDisabled(wctx, providerID, e.EntryID, e.Code); err != nil {
                log.WithError(err).WithField("entry_id", e.EntryID).Warn("auto-disable: write failed")
                return
            }
            h.applyUpstreamProviders(context.Background()) // re-renders config + reloads
        }()
    }
}
```
Problem: the event carries `provider` (string) but the store method wants `providerID`. Resolve: either the manager maps provider-key → provider row id (superset: the sink fires only when the field was stamped from a PG row; include the providerID in the stamped attribute `entry_auto_id = <providerID>:<entryID>`), OR the store resolves by entryID alone (entries are globally unique). Simplest: `SetEntryAutoDisabledByEntryID(ctx, entryID, code)` — `UPDATE ... WHERE id=$1 AND NOT (disabled AND NOT auto_disabled)`; entry IDs are unique across providers. Use that (drop providerID from the store signature; keep it in the event for logging/UI).

Also, remove any earlier plan of a convenience re-enable route: dashboard re-enable = PUT provider with the entry's `auto_disabled=false` — already flows through existing CRUD + configsnapshot round-trip (no new route).

**Step 4:** run tests → PASS. **Step 5:** build verify. **Step 6: Commit** `feat(management): auto-disable sink persists flag + re-renders`.

---

### Task 8: Auto-re-enable background sweeper

**Files:**
- Create: `internal/store/pg_upstream_entries_reenable.go`: `ReenableExpiredAutoDisabled(ctx) (int64, error)` — tx:
  ```sql
  UPDATE %s e SET auto_disabled=false, disabled=false, auto_disabled_reason=NULL, auto_disabled_at=NULL
  FROM %s p
  WHERE e.auto_disabled=true
    AND e.provider_id=p.id
    AND p.auto_disable_cooldown_seconds IS NOT NULL
    AND p.auto_disable_cooldown_seconds > 0
    AND e.auto_disabled_at <= NOW() - (p.auto_disable_cooldown_seconds * INTERVAL '1 second')
    AND NOT (e.disabled AND NOT e.auto_disabled)
  ```
  returns rows affected. (Never touches manual `disabled=true, auto_disabled=false` rows.)
- Create: `internal/store/pg_upstream_entries_reenable_test.go`
- Modify: `internal/api/handlers/management/auto_disable_sweeper.go` (background goroutine, interval 60s, calls the store, re-renders when rows>0; nil-safe when PG absent; stop channel/handle)
- Modify: server wiring to start/stop the sweeper (mirror `StartSyncLogSweep` / `alerts_runner` lifecycle)
- Test: sweeper unit test (fake store + fake re-render)

**Step 1: Failing tests** — store: manual-disabled not touched; expired auto-disabled re-enabled; not-yet-expired stays; cooldown nil/0 rows skipped.

**Step 2: Run** → FAIL. **Step 3: Implement** per above. **Step 4:** PASS. **Step 5:** build verify. **Step 6: Commit** `feat(management): auto-re-enable sweeper for expired auto-disabled entries`.

---

### Task 9: Config example + configsnapshot/validation round-trip checks

**Files:**
- Modify: `config.example.yaml` (documented examples for per-entry `max_concurrent`/`max_wait_ms` under the provider section + provider-level `auto_disable_error_codes`/`auto_disable_cooldown_seconds`)
- Test: extend `internal/configsnapshot/yaml_test.go` (round-trip a provider with the new fields; confirm `Checksum` changes when `max_concurrent` changes)

**Step 1: Failing tests** — snapshot round-trip + checksum sensitivity. **Step 2:** run → FAIL (fields dropped). **Step 3:** fix yaml tags on config types (verify `yaml:"-"-`-free; ensure `auto_disable_*` render on the right config types) so round-trip works. **Step 4:** PASS. **Step 5:** build verify. **Step 6: Commit** `feat(configsnapshot): round-trip per-entry concurrency + auto-disable fields`.

---

### Task 10: Dashboard editor + list + auto-disabled badges

**Files:**
- Modify: `web/dashboard/src/pages/upstream-provider-editor/` (entries section: `max_concurrent`/`max_wait_ms` inputs; provider form: auto-disable code chip editor + cooldown input; auto-disabled badge + Re-enable action)
- Modify: `web/dashboard/src/pages/UpstreamProvidersPage.jsx` (list status chip / count for auto-disabled)
- Test: manual `npm run build` (existing convention; dashboard tests are sparse)

**Step 1: Implement UI.** **Step 2:** `cd web/dashboard && npm run build` → succeeds. **Step 3:** re-run unit tests for the editor if any exist (`npm test -- --watch=false`); else skip. **Step 4: Commit** `feat(dashboard): per-entry concurrency + auto-disable editor UI`.

---

### Task 11: `make dash-embed` + final integration build + full test pass

**Files:**
- Modify: (none new; run embed)
- Run: `make dash-embed` (rebuilds SPA + embeds dist into `internal/dashboardasset`, then rebuilds Go binary per memory)

**Steps:**
1. `make dash-embed` → binary builds with embedded dashboard.
2. `go test ./... -count=1` → PASS (note known pre-existing environmental/ flaky failures: 3 env + internal/util + flaky home timer — confirm they are only those).
3. `gofmt -l` on `internal/` and `sdk/` → empty for touched files.
4. **Commit** if `make dash-embed` changed embedded assets: `chore(dashboard): embed updated dashboard`.

---

## Execution Handoff

Plan complete and saved to `docs/plans/2026-09-25-auto-disable-max-concurrent-plan.md`.

**Two execution options:**

1. **Subagent-Driven (this session)** — I dispatch a fresh subagent per task (Task 1→11), spec review → quality review → fix round → re-review, working on `main`.
2. **Parallel Session (separate)** — a new session with `superpowers:executing-plans` runs the plan task-by-task.

Given the user's standing choice is **subagent-driven, working directly on `main`**, option 1 is the default when ready.
