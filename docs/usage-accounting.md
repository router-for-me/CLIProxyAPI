# Usage accounting and replay contract

Usage events describe provider attempts, tool charges, and observed control calls. They are not a count of HTTP requests, WebSocket frames, or invoices. Enable usage statistics to collect them.

## Event identity and measurements

- `event_id` identifies one emitted accounting event. Consumers deduplicate only this ID, not `request_id`.
- `generation_id` groups retries for a logical generation; a downstream Responses WebSocket receives a new generation ID for every execution. `attempt_id` groups a provider attempt and its separately billed tools.
- `kind` distinguishes `attempt`, `tool`, `prewarm`, `management_call`, and `unmeasured`. Endpoint and transport are preserved. Missing executor coverage produces an unmeasured record for supported API route prefixes; model-list/configuration traffic is not a billable generation.
- `token_breakdown` remains the authoritative v2 normalized token contract. `raw_usage`, `usage_observed`, cache creation lifetimes, and provider cost preserve billing dimensions which cannot be reconstructed from token totals.
- `cost_usd` is an exact decimal string, currently sourced from xAI `cost_in_usd_ticks / 10000000000`. A video job's `billing_id` and `cost_scope=operation` identify a cumulative charge; repeated polling must not charge the same cumulative amount again.
- Claude cache creation retains both 5-minute and 1-hour tokens. Gemini thinking remains a separate legacy field and is included in the v2 billable output. OpenAI cached/reasoning tokens remain subsets rather than additional input/output.

Codex image tools emit their own model, event ID, and raw modality measurements on HTTP and all three WebSocket execution paths. Live/realtime observation accepts upstream terminal usage events, ignores client/delta frames, and deduplicates response IDs. Capture is bounded without truncating forwarded media. Opaque WebRTC sessions without provider usage remain unmeasured; duration does not establish their charge.

Alpha Search and model-bearing management POST probes publish available usage. Token-count endpoints do not turn predicted tokens into consumed tokens. Plugin executors must publish usage through the SDK to expose their own billing dimensions; arbitrary plugin routes are not automatically an exhaustive usage source.

## Durable local consumer

When a config file is supplied, the local journal is stored in `usage-journal/` beside it. The usage queue plugin persists events synchronously before asynchronous plugins run. Each file is atomically renamed after flushing; POSIX also flushes the directory. File permissions are private. API keys and credential-valued sources are fingerprinted; response headers are excluded from the durable copy. Legacy wire behavior remains available for old clients.

The existing authenticated management API exposes:

1. `GET /v0/management/usage-journal?count=500` (1–1000) reads without deletion.
2. Commit the events to a local inbox/database with a unique nonempty `event_id` constraint.
3. `POST /v0/management/usage-journal/ack` with `{"event_ids":["..."]}` removes committed events. Repeating an ACK is safe.

The journal supports one acknowledging accounting consumer. It is not a multi-consumer broker. Unacknowledged files have no expiry: an old client that never ACKs, or a stopped collector, requires disk monitoring. No model or request body is stored, but usage/source metadata can still be sensitive. A storage write failure is logged and latches an error returned by journal reads; investigate the storage failure and reconcile provider usage before restarting. This cannot guarantee recovery of an event that could not be written, or usage never reported by the upstream. Avoid treating legacy destructive queue delivery as equally durable.

## Validation

Regression tests cover cache lifetime preservation, xAI cost without tokens, explicit versus absent usage, queue overflow fallback, journal replay/ACK/restart and credential fingerprints, bounded live observation, and image-tool accounting through Execute, streaming, and downstream WebSocket paths. The WebSocket regression was verified red with the publication hooks removed, then green with the fix.

Full `go test ./...`, the server build, and race tests for usage, redisqueue, executor helpers, and live relay pass locally. A pre-existing text-only tool-result regression exposed by the full suite is also fixed: translated image relay messages now honor text-only tool mode while multimodal mode preserves them.

## Coordinated desktop consumer

EasyCLIProxyAPI needs the companion accounting update to consume the journal, preserve snapshots, distinguish unknown amounts, and apply modality/provider-specific tariffs. The desktop release must bundle a core release containing this change. Deploying only one side does not provide the full end-to-end guarantees.

## Independent review corrections

The stream accounting buffer now retains billing metadata independently of final token counters, including metadata observed before Gemini/Antigravity SSE filtering. Repeated cumulative tool counters merge without adding the same count twice. A failed provider attempt publishes `usage_complete=false`, preventing a partial token snapshot from being frozen as a complete estimate by the desktop consumer.

Live terminal usage is captured on the upstream read side, including a complete frame whose downstream write fails. Transcription events use the session's transcription model and item/content-index identity rather than the realtime model; absent transcription configuration remains an unknown model. This improves attribution without inventing usage after an upstream read failure.

## PR review corrections

The common HTTP handler context bridge now copies accounting scope and generation identity while preserving the execution context's existing cancellation behavior. Measured HTTP operations therefore suppress the unmeasured fallback correctly. The usage manager assigns a missing event ID once before either synchronous or asynchronous plugins run, preserving caller-supplied identities.

Coverage skips unmatched routes and locally rejected authorization, so unauthenticated requests cannot create permanent journal entries through that fallback. `usage_complete` uses the same resolved failure state as the emitted `failed` field. Model-bearing management calls select the provider/endpoint protocol parser and merge SSE usage; partial response bodies are retained when reading fails.

Management Gemini/Vertex generation calls also resolve models from `/models/{model}:generateContent` and `:streamGenerateContent` paths when the request body omits `model`; explicit body models remain authoritative. Opaque event IDs are preserved in payloads and mapped to SHA-256 filenames for durable storage and ACK. Previously written journal files remain readable and acknowledgeable.

Coverage fallback requires successful route admission, including legacy authentication-disabled and realtime client-secret paths. Requests blocked by Home heartbeat or other gates before admission do not create journal files; admitted handler failures remain visible. Alpha Search creates its reporter before the upstream attempt so transport/read failures and latency retain their source attribution. Gemini and Interactions streams preserve explicitly reported zero usage, including when a response tier is present.
