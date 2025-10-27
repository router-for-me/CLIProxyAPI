## Why
Zhipu GLM streaming currently yields very few large SSE frames, producing a coarse user experience compared to GPT‑5. We recently implemented executor-side chunk normalization in Go (`internal/runtime/executor/zhipu_executor.go`) to split oversized or low-frequency upstream frames into smaller OpenAI-compatible SSE chunks with configurable limits and a hard cap for the first packet.

Goals:
- Improve perceived streaming smoothness for GLM without requiring upstream provider changes.
- Keep behavior OpenAI-compatible and respect a 2048-byte maximum for the first packet.
- Make chunk size tunable via environment variables for fast iteration during benchmarking.

## What Changes
- Add executor-side streaming chunk normalization for provider="zhipu":
  - Split content into small UTF‑8–safe segments with `ZHIPU_SSE_CHUNK_BYTES` (default 128–256) and enforce `ZHIPU_SSE_FIRST_CHUNK_MAX_BYTES` (<= 2048).
  - Preserve SSE semantics and `[DONE]` marker.
  - Maintain structured debug logs with `splitCount` and `maxSegmentLen`.
- Expose env knobs aligned with existing bridge knobs:
  - `ZHIPU_SSE_CHUNK_BYTES`, `ZHIPU_SSE_FIRST_CHUNK_MAX_BYTES` (fallback to `SSE_CHUNK_BYTES`, `SSE_FIRST_CHUNK_MAX_BYTES`).

## Impact
- User experience: More frequent, smaller streaming events; better parity with GPT‑5 streaming feel.
- Performance: More events may increase total duration on some networks; operators can tune chunk size to balance smoothness vs overall time.
- Compatibility: OpenAI-compatible SSE remains intact; first packet remains <= 2048 bytes.

## Related
- Prior changes related to Zhipu routing and legacy cleanup are no longer relevant to this update.
