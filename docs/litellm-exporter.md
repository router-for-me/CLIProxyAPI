# LiteLLM exporter

Enable `observability.accounting-outbox.litellm` with a persistent outbox, a base URL and `admin-key-env`. It works against stock LiteLLM and posts to its `/v1/rust_control_plane/logs` route. Inject the proxy-admin key through the environment variable named by `admin-key-env`, the YAML only holds the variable name. Use HTTPS outside a trusted local network. Redirects are refused.

Create a LiteLLM user, team and virtual key through `/user/new`, `/team/new` and `/key/generate`, then set `clients[ClientKeyID]` to that key's SHA-256 `key-hash`, `user-id` and `team-id`. `ClientKeyID` is the local accounting hash, not a LiteLLM key hash. Keep spend logs enabled and configure PostgreSQL persistence on LiteLLM.

Only successful requests with complete token usage and a fully priced USD estimate are exported, in batches of at most 64. `response_cost` is the local API-equivalent estimate. Failed and interrupted attempts are not exported because stock LiteLLM drops replayed failures, and they are marked rejected in the outbox. Events without a client mapping, complete usage or a price stay local and are retried hourly so configuration can be repaired.

Delivery is at-least-once. Spend-log rows deduplicate by event ID, but a retry after a lost response can add to LiteLLM's aggregate and budget counters a second time. Rate limits and service errors back off up to a minute, auth and route errors wait an hour, and a 422 rejects the batch permanently. Events claimed by a process that crashes mid-export stay claimed until they are pruned.

The worker sets no network timeouts. Cancellation interrupts requests during shutdown, and a stalled LiteLLM only blocks the exporter worker, not ingestion. `Service.AccountingExporterStatus` and the structured accounting logs report the exporter state.
