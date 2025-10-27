## 1. Implementation
- [x] Add executor-side stream chunker for Zhipu (Go)
- [x] Enforce first-packet max 2048 bytes
- [x] UTF‑8 safe segmentation; preserve [DONE]
- [x] Env knobs: ZHIPU_SSE_CHUNK_BYTES, ZHIPU_SSE_FIRST_CHUNK_MAX_BYTES
- [x] Structured logs: splitCount, maxSegmentLen

## 2. Validation
- [x] Unit test: long content split + [DONE] passthrough
- [x] E2E minimal: first chunk <= 2048 bytes
- [x] Benchmarks: REPEATS=3; collect TTFB, events, events/sec, total
- [x] Compare 64B vs 128B cut sizes; choose balanced default

## 3. Ops & Docs
- [ ] Add operator note for tuning chunk size vs total time
- [ ] Update runbook snippets for env vars

