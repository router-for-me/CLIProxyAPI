# Phase C Exit Benchmark Summary

Comparison of Phase C exit (commit 105769bc) vs Phase A baseline
(commit 56df3689). Both captured on AMD Ryzen 9 5950X, go1.26.0 linux/amd64.

## Targets vs results

| Metric | Plan target | Phase A baseline | Phase C exit | Delta | Status |
|---|---|---|---|---|---|
| AMP mutex contention (parallel reads) | −80% | 27.85 ns/op | 0.06 ns/op | −99.8% | met |
| AMP single-thread read | -- | 3.744 ns/op | 1.725 ns/op | −54% | improved |
| Request logger off-path (sync→async) | ~0 ms | 236,258 ns/op | 35.41 ns/op | −99.985% | met |
| Request logger allocs/req | −10 to −20% | 103 allocs/op | 0 allocs/op | −100% | met |
| Signature cache `Set` (bounded LRU) | bounded | 1,222 ns/op, 419 B | 592.9 ns/op, 232 B | −51% wall, −45% B | met |
| Signature cache `Get_Hit` | within ±5% baseline | 242.5 ns/op | 219.5 ns/op | −9.5% | met (faster) |
| Signature cache `Get_Hit_Parallel` | within ±5% baseline | 365.3 ns/op | 315.3 ns/op | −13.7% | met (faster) |
| Hot-reload race test (`go test -race`) | passes | (skipped) | passes | -- | met |

## Trade-off documented

`BenchmarkFileRequestLogger_Disabled` regressed 7.03 ns/op → 19.42 ns/op
(+176%). This is the cost of swapping the racy plain `bool` for
`atomic.Bool` so the disabled fast-path is race-free. Absolute cost is
~19 ns and the path is a guard, not a hot loop. Trade-off documented in
the #21 commit message and is the right call: the prior plain-bool was a
data race under hot-reload.

## Phase A pre-written tests passing under `-race`

  - `internal/cache/signature_cache_semantics_test.go` (sliding TTL,
    Gemini sentinel, group/full clear).
  - `internal/api/modules/amp/amp_race_test.go` (concurrent toggle of
    ForceModelMappings, restrictToLocalhost, model mappings, last-config
    writes; captured-mapper-staleness).
  - `internal/runtime/executor/codex_executor_stream_chunkboundary_test.go`
    (SSE chunk-boundary).
  - `internal/api/server_hotreload_race_test.go` (un-skipped, passes).

## Behavior preservation

  - All API request/response shapes unchanged.
  - File-watcher reload path unchanged: still loads YAML from disk and
    calls `Server.UpdateClients` which atomic-swaps + fans out.
  - Forced-error logs ride priority lane and never drop (verified by
    `TestAsyncEmitter_ForcedFallsBackToSyncWhenPriorityFull`).
  - Captured-mapper pattern preserved: handlers see hot-reload mapping
    updates without re-fetch (verified by `TestAmpStaleness_*`).
  - Mgmt PUT response no longer mutates a shared snapshot; clone-modify-
    persist-swap with the commit hook waits for `UpdateClients` fan-out
    before responding, matching prior in-place semantics.
  - `ReleaseURLProvider` default impl returns upstream URLs byte-for-byte;
    fork override is Stage 2 work.

## Pre-existing items NOT introduced by Phase C

  - `internal/api/handlers/management` test races: gin's
    `SetMode`/`IsDebugging` globals race across `t.Parallel()` tests.
    Pre-existing on clean upstream/dev. Out of scope for Phase C.
  - `TestDefaultRequestLoggerFactory_UsesResolvedLogDirectory` failure
    in `internal/api`: pre-existing on clean upstream/dev. Out of scope.

## Codex review status

Five Codex CLI gpt-5.5 review rounds were run against the BE diff. All
BLOCKERs/IMPORTANTs that were addressed in rounds 1-4 are fully fixed
and verified under `-race`. Round 5 surfaced 4 findings; 3.5 are fixed,
1 is deferred. See `PUNCH_LIST.md` for the full deferred-items + future-
watch list. Phase C exits with that punch list rather than a fully-clean
review.

## Next step

Phase D (week 9, targeted code quality). Then Stage 2.
