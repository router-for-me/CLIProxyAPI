# Auto Router F2 — Runtime Performance Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Cut hot-path scorer overhead: compile each scoring profile once (not per request), cache score results for identical bodies, and window very large bodies so dimension scoring is O(window) while keyword rules keep scanning full text.

**Architecture:** Three additive pieces inside `internal/autorouter` + one in `internal/store`: (1) `CompiledProfile` — profile config pre-normalized at load/upsert, cached in `AutoRouterProfileStore` (invalidated on Upsert); (2) a mutex-guarded LRU (2048 entries) keyed by SHA-256(rawJSON+entryProtocol+routerID+profileHash) storing `ScoreResult` bodies, best-effort (any failure = compute directly); (3) head+tail windowing (24k+8k words) in the dimension scorer only.

**Tech Stack:** Go 1.26 stdlib (crypto/sha256, container/list or slice-based LRU), gjson.

**Working directory:** `/home/bilfid/projects/nixllm` (main branch, inline execution — same as F1).

**Baseline (verified F1):** 37 tests pass in `internal/autorouter`; benchmarks: LargeAgentBody 115.9ms/op 41.6MB 54 allocs; SmallBody 3.27µs 912B 12 allocs. Pre-existing failures: 3 claude_executor tests (unrelated).

---

### Task 1: CompiledProfile — compile once per profile version

**Files:**
- Modify: `internal/autorouter/profile.go` (add `CompiledProfile` + `CompileProfile`)
- Modify: `internal/store/pg_auto_router_profiles.go` (compile on Get/Upsert; store in cache)
- Test: `internal/autorouter/profile_compile_test.go` (new)

**Step 1: Write the failing test** — compile a raw config with unnormalized keywords/weights; assert the compiled form matches what `NormalizeProfile` would produce and that `ScoreWithProfileCompiled` yields identical results to `ScoreWithProfile` on the same inputs (parity).

**Step 2: Verify fail** — `go test ./internal/autorouter/ -run TestCompileProfile` fails (undefined).

**Step 3: Implement** — `CompiledProfile` struct: normalized `ProfileConfig` + pre-normalized keyword lists (per rule) + version + hash. `CompileProfile(config) (CompiledProfile, error)` = NormalizeProfile + hash + pre-normalize each rule's keywords (currently done per request inside `matchedKeywordRules`).

**Step 4: Parity test** — `ScoreWithProfile(raw, fmt, profile)` vs a new `ScoreWithProfileCompiled(raw, fmt, *CompiledProfile)`: same tier/cause/matched rules across the existing test corpus.

**Step 5: Store wiring** — in `pg_auto_router_profiles.go`: on cache miss, after successful `Get`, compile and store; on `Upsert`, invalidate + recompile. `AutoRoutersResolverImpl.AutoRouterProfile` returns the compiled form (add `AutoRouterProfileCompiled(ctx, routerID)`; keep the old method for compat).

**Step 6: Commit** — `feat(autorouter): compiled scoring profiles (precompute at load/upsert)`

---

### Task 2: Score cache LRU

**Files:**
- Create: `internal/autorouter/score_cache.go`
- Test: `internal/autorouter/score_cache_test.go`

**Step 1: Failing tests** — hit/miss; hash-key invalidation (same body + different profileHash = different entry); LRU eviction at capacity 2048 (fill 2049, oldest misses); concurrent access (go test -race, 100 goroutines mixed get/put); cache stores no raw bodies.

**Step 2: Implement** — `scoreCache`: fixed-capacity map[string]ScoreResult + FIFO slice ring (KISS: slice ring avoids container/list node churn); mutex-guarded; `key(rawJSON []byte, format, routerID, profileHash string) string` via sha256; constants `scoreCacheCapacity = 2048`.

**Step 3: Wire into handlers** — `sdk/api/handlers/handlers_auto_router.go` `resolveAutoRouterModel`: build key from rawJSON+entryProtocol+router.ID+profile.ProfileHash; on hit, skip scoring (still run Resolve — resolution is cheap and config may have changed); on miss, compute + put. Cache miss/error never blocks: plain function call fallback.

**Step 4: Commit** — `feat(autorouter): process-local LRU score cache for auto-router decisions`

---

### Task 3: Big-body windowing

**Files:**
- Modify: `internal/autorouter/scorer.go` (`scoreDimensionsWithFence`)
- Test: `internal/autorouter/scorer_test.go` + bench assertions stay green

**Step 1: Failing test** — window determinism: a body whose density signal lives in the head is scored the same with 100k filler words appended after it as with 100k filler before it (tail capture); `TestScorerParityOnCorpus` still green; `TestScoreLongUserTurnTokens` still green.

**Step 2: Implement** — in `scoreDimensionsWithFence`, if `wordCount > 32000`, build the scoring text as head 24k words + tail 8k words of the flat text (single pass via strings.Fields + slicing). Keyword rules (already full-text, bounded) and reasoning markers (countReasoningMarkers on full FlatText) stay untouched. Note: windowing applies ONLY inside scoreDimensionsWithFence's word loop; `strings.Contains(text, "?")` in the question dimension uses the windowed text.

**Step 3: Benchmark gate** — rerun `go test -bench BenchmarkScoreLargeAgentBody -benchmem`: allocs/op must drop meaningfully (the 40k-line body's Fields slice shrinks from ~400k entries to window size); record numbers in commit message.

**Step 4: Commit** — `perf(autorouter): window dimension scoring to head+tail for large bodies`

---

### Task 4: Verification

- `gofmt -w internal/autorouter/ internal/store/ sdk/api/handlers/`
- `go test -count=1 ./internal/autorouter/ ./internal/store/ -v` — all pass
- `go build -o test-output ./cmd/server && rm test-output`
- Sweep `./internal/... ./sdk/...` — only the 3 pre-existing executor failures
- Update design doc Progress line; commit.

## Out of scope

- Distributed cache, background eviction goroutines.
- Windowing keyword rules or reasoning markers (bounded/full-text by design).
- Any change to DecisionSnapshot shape.
