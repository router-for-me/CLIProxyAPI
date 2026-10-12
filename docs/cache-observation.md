# Provider Cache Observation

The optional `cache-observation` setting observes existing Claude Messages and
Codex Responses HTTP executions after payload configuration, normalization and
signing. It generates no requests and changes no payload, header, routing,
signing, cache marker or pruning policy. It is disabled when absent.

Set these 3 values together in the private runtime configuration:

- `session-id`: the exact resolved internal session identity of an existing
  conversation. Unmatched sessions are ignored.
- `expires-at`: an RFC3339 timestamp in the future, at most 1h away.
- `max-attempts`: an execution budget from 1 to 100, including failed attempts.

The budget is process-local. Reloading identical values does not reset it, and
a different configuration cannot replace an unexpired capture. A process restart
starts a new capture with a different hash key. Capture stops admitting executions
at the expiry or budget. Already admitted executions may finish after expiry.
Remove the setting after the finite observation. No timer, daemon or extra service
is created.

## Evidence

Each admitted execution emits `cache_observation` JSON in the existing application
logger, with `request` and, when canonical usage publishes, `completion` phases.
Both use the existing execution `request_id`, not the parent inbound trace.
An unmatched request phase remains incomplete evidence. No completion is invented.

The record contains scoped HMAC digests for the complete final body, ordered
tool/system/history components and cumulative prefixes, selected settings,
session, credential grouping and route. Its random key stays in memory and is
never emitted. Hashes compare only within the same capture. A full-body change is
expected as history grows, so compare retained component and prefix hashes.
Changing cache markers or OAuth attribution also changes those exact-byte hashes.
Hashes do not reproduce provider tokenization or establish the provider namespace.

Cache marker paths and known TTLs, timestamps, request size, failed/status outcome
and canonical token buckets are retained. Raw Claude 5m/1h creation counters are
retained when present, including split stream updates. Missing usage stays
`absent`, without token totals. Reported zero counts are distinct from missing
counts. `usage_fields` identifies which top-level numeric counters were present.
The observer adds no fields to translated client response schemas.

Raw request/response text, tool arguments, system content, URLs, session/account
identifiers, credentials, headers and error bodies are never emitted. Model and
tier names are also scoped digests. Request/body logging remains independently
configured and is not enabled by observation. Existing unrelated diagnostic
logging is outside this observer's contract.

## Limits

Requests larger than 8 MiB, invalid JSON and shapes exceeding 512 components or
markers are explicitly incomplete. They must not be called stable or cache-safe.
WebSocket, token-count, image and other provider paths are not instrumented by
this observer. The execution ID groups a model execution, not each HTTP redirect
or lower-level transport retry.

Returned counters establish observed reuse only for the route and sequence
actually observed. Missing TTL, served tier or usage remains unknown. Total cost
comparisons require matched complete sequences and a current pricing manifest
through the existing cost owner. Synthetic transport tests establish the observer
and byte preservation, not provider cache hits, OAuth attribution normalization
or billed savings.

Sources: [Claude Prompt Caching](https://platform.claude.com/docs/en/build-with-claude/prompt-caching)
and [OpenAI Prompt Caching](https://developers.openai.com/api/docs/guides/prompt-caching),
read 2026-10-12. Upstream [#6273](https://github.com/router-for-me/CLIProxyAPI/issues/6273)
keeps TTL-specific billing out of translated OpenAI response schemas.
