# OpenAI Compatibility API Key Entry Identity

- **Date:** 2026-08-24
- **Status:** Approved design
- **Scope:** Upstream Providers → OpenAI Compatibility API Keys

## 1. Goals and routing semantics

Add an optional identity/name to every API key entry belonging to an OpenAI Compatibility upstream provider. The identity serves both as a human-readable dashboard label and as a routeable key-level identity without removing the existing provider-level route.

The two routing levels are intentionally preserved:

- **Provider route:** `openai-compatible-<provider>` selects the provider's complete API-key pool and retains the current round-robin/fallback behavior.
- **Entry route:** `openai-compatible-<provider>:<entry-identity>` selects only the matching API-key entry.

Named entries use their normalized name as `<entry-identity>`. Entries without a name use the stable database child-row identity `key-<entry-id>`, rather than an array position. This avoids route changes caused by deleting or reordering a neighboring entry. Existing provider-level routes remain valid and continue to select the whole pool.

The API key value remains secret. It must not be used as a display label, route key, log field, or model-picker identity.

## 2. Data model and compatibility

Extend the OpenAI Compatibility API-key entry model in all layers:

- `config.OpenAICompatibilityAPIKey` gains an optional `Name` field serialized as `name`.
- `store.UpstreamProviderAPIKey` gains an optional `Name` field serialized as `name`.
- The management request/response types carry `id`, `name`, `api_key`, and `proxy_url` for each entry. The response exposes the persisted child-row ID so the dashboard can preserve it across edits.
- The PostgreSQL child table `upstream_provider_api_key_entries` gains a nullable `name TEXT` column and a case-insensitive uniqueness constraint for non-empty names within a provider.

Names are normalized for routing and uniqueness by trimming whitespace and lower-casing. The dashboard may display the normalized value returned by the server. An explicit name must use a stable slug-like form (`[a-z0-9][a-z0-9_-]*`, case-insensitive after normalization). Names using the reserved generated-identity form `key-<digits>` are rejected, so a generated fallback can never collide with an explicit name.

Names are optional for backward compatibility. Existing rows without a name remain valid and receive the generated identity `key-<id>`. Existing YAML configurations without names continue to load; their provider-level behavior is unchanged. Config-rendered entries without a persisted child-row ID cannot provide a stable key-level route and therefore retain the provider-level route behavior until they are persisted through the upstream-provider store.

## 3. Stable child-row persistence

The current provider update path replaces all child rows, which would change entry IDs on every save. Replace that behavior for API-key entries with an ID-aware synchronization algorithm:

1. The client sends the `id` for every existing entry and omits it for newly added entries.
2. The backend loads the current child rows for the provider and validates the incoming set.
3. An incoming entry with a known ID is updated in place, preserving its ID and its position/order; its API key, name, and proxy URL are replaced with the submitted values.
4. An incoming entry without an ID is inserted as a new child row.
5. Existing child rows whose IDs are absent from the request are deleted.
6. IDs belonging to another provider, duplicated IDs, or malformed IDs are rejected rather than reassigned.
7. The operation remains transactional with the parent provider update and the other child collections.

The existing child-row ID is therefore the stable fallback identity used by routes, even when the provider is edited, renamed, or its entries are reordered. Sort order remains an independent presentation/selection detail and must not determine route identity.

Migration/initialization must add the nullable name column and constraint without rewriting existing IDs. Existing rows will immediately resolve to `key-<id>` in responses and routing metadata.

## 4. Runtime and routing data flow

The upstream sync renderer copies entry `Name` and the stable child-row `ID` from the store into `config.OpenAICompatibilityAPIKey`. The reverse seeder copies the name when importing a config into the normalized store; it does not invent a persistent ID before insertion.

During OpenAI Compatibility auth synthesis, each generated auth retains the existing provider-level attributes (`provider_key`, `compat_name`, `config_index`, base URL, headers, and weighting). It additionally receives an entry-level routing attribute derived from the provider key and the entry identity:

- named: `openai-compatible-<provider>:<normalized-name>`
- unnamed persisted row: `openai-compatible-<provider>:key-<id>`
- legacy/config-only entry without a persisted ID: no stable entry suffix; provider-level routing remains available

The provider-level `provider_key` is not replaced by the entry key. Executor lookup continues to resolve the shared OpenAI Compatibility executor from the provider key, while conductor matching and registry/live-provider reporting can use the entry-level key for exact selection. A route without a suffix matches the provider's complete pool. A route with a suffix matches only the auth whose entry-level key equals that suffix.

If an entry is renamed, the new route identity is returned after save; the old named route is no longer valid. If an unnamed entry is edited, its `key-<id>` route remains stable. Disabled entries are excluded from both pool and exact-entry selection, as with current provider behavior.

## 5. Dashboard UX

In `UpstreamProvidersPage`, the OpenAI Compatibility API key editor gains an optional **Identity** field for each row alongside the masked API key and proxy URL. The editor must:

- preserve and submit the existing entry `id`;
- show the server-normalized identity after reload;
- validate the slug format before submit;
- reject duplicate identities case-insensitively within the provider;
- reject reserved `key-<digits>` identities;
- clearly indicate that blank identity generates `key-<id>` after persistence;
- keep the masked API key input and never expose the secret in labels or error text.

The provider list and editor summary continue to identify the provider itself by its provider name. Where entry summaries are shown, display identities (named identities or generated `key-<id>`) and counts, not API key values. The model-routing picker must show provider-level and entry-level choices distinctly, with entry identities readable to operators.

Live-status detection must recognize exact entry keys and continue recognizing the existing provider key. A provider-level live result must not incorrectly mark every unrelated entry as live unless the runtime explicitly reports the provider-level pool key.

## 6. Validation and errors

Backend validation is authoritative and runs before mutating the database. It must reject:

- duplicate entry IDs in one request;
- entry IDs that do not belong to the target provider;
- duplicate non-empty names after trim/lower-case normalization;
- invalid name syntax;
- reserved `key-<digits>` names;
- missing API keys for entries that are submitted;
- malformed proxy URLs according to existing provider validation rules.

Conflict errors should use the existing management API error shape and identify the identity conflict without including any secret. Database uniqueness remains a final concurrency safeguard; a concurrent conflict is returned as HTTP 409. Updates and deletes must be all-or-nothing for the provider transaction.

Legacy requests that omit entry IDs are accepted only for newly created entries. An update payload that omits IDs for existing rows is treated as removal plus insertion, so clients must round-trip IDs to preserve route stability. The dashboard does so automatically.

## 7. Testing strategy

Add focused tests across the data flow:

- config serialization/deserialization preserves `Name`;
- upstream sync render and seed round-trip names;
- PostgreSQL migration creates the nullable name column and uniqueness constraint;
- PostgreSQL create/update preserves child IDs, updates in place, inserts new rows, deletes omitted rows, and rejects cross-provider/duplicate IDs;
- backend validation rejects duplicate, invalid, and reserved names;
- entry route identity generation returns named identities and `key-<id>` fallbacks;
- OpenAI Compatibility synthesis emits both provider-level metadata and exact entry-level routing metadata;
- conductor selection keeps provider-level pool routing and exact entry routing separate;
- model-provider/live-status reporting recognizes exact entry keys without false positives;
- dashboard editor build/hydration preserves IDs and validates identities.

Run the relevant Go and dashboard tests, then the required repository checks (`gofmt`, `go test ./...`, and `go build -o test-output ./cmd/server && rm test-output`) before implementation is considered complete.

## 8. Non-goals

This change does not introduce per-entry model lists, per-entry headers, per-entry weights beyond existing provider configuration, or a new top-level provider type. It does not remove provider-level routing, change the secret storage policy, or expose raw API keys through management responses beyond the existing masked/credential-handling behavior.
