## ADDED Requirements

### Requirement: Zhipu streaming normalization via executor chunking
The system MUST provide smoother streaming for provider="zhipu" by emitting smaller, more frequent OpenAI-compatible SSE frames when upstream delivers large or low-frequency events.

#### Scenario: First packet size limit
- WHEN the first streaming content is emitted for provider="zhipu"
- THEN the first SSE data payload MUST NOT exceed 2048 bytes
- AND implementation MUST be UTF‑8 safe and OpenAI-compatible

#### Scenario: Chunk size tuning
- GIVEN environment knobs `ZHIPU_SSE_CHUNK_BYTES` and `ZHIPU_SSE_FIRST_CHUNK_MAX_BYTES` (fallback to `SSE_CHUNK_BYTES`, `SSE_FIRST_CHUNK_MAX_BYTES`)
- WHEN upstream emits a single large content frame
- THEN the executor splits it into multiple SSE `data: { ... }` events with each segment capped by the configured chunk size, while respecting the first-packet cap

#### Scenario: Completion marker preserved
- WHEN upstream signals stream completion
- THEN the `[DONE]` marker MUST be preserved and delivered after all split segments
