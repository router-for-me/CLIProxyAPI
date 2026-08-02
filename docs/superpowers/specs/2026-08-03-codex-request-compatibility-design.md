# Codex Request Compatibility Design

## Goal

Prevent local reasoning-level rejection and upstream parameter errors while preserving the capability differences advertised by the current Codex model catalog.

## Scope

The change covers requests routed through the Codex executor over HTTP or WebSocket:

- Remove top-level `metadata` before a Codex upstream request is sent.
- Preserve `client_metadata` only for ordinary requests to the official ChatGPT Codex backend.
- Remove `client_metadata` for compact requests and for API-key or custom upstream targets.
- Preserve a requested high reasoning effort when the final selected model supports it.
- Downgrade unsupported high reasoning effort to the closest lower supported level instead of rejecting the request locally.
- Cap `max` and `ultra` at `xhigh` for `/responses/compact` compatibility.
- Align built-in GPT-5.6 capabilities with the embedded Codex client catalog: Sol and Terra include `ultra`; Luna stops at `max`.

Ordinary unsupported low or medium values remain validation errors. Non-Codex providers retain their existing validation and conversion behavior.

## Considered Approaches

### 1. Capability-aware high-effort compatibility

Add `ultra` to the canonical thinking representation, align the built-in GPT-5.6 model definitions, and downgrade only the ordered high-effort family according to the final selected `ModelInfo`. Add a compact endpoint cap and target-aware metadata filtering at the Codex executor boundary.

This is the selected approach because it preserves exact model and credential capabilities while covering older or narrower upstreams without weakening unrelated validation.

### 2. Expand every Codex model to `max` and `ultra`

This removes the local error for known models but misrepresents Luna and custom API-key upstreams. Unsupported values would reach the provider and fail later.

### 3. Clamp every unsupported same-family level

This maximizes permissiveness but hides configuration mistakes such as unsupported low or medium values and changes validation semantics for unrelated models.

## Architecture

### Canonical thinking levels

Add `LevelUltra` above `LevelMax` in the canonical level order. Suffix parsing, request-body extraction, and OpenAI/Codex appliers continue to transport level strings through the existing canonical pipeline.

The final selected `ModelInfo` remains the source of truth. For OpenAI/Codex same-family requests, only these high-effort values use compatibility fallback:

- `ultra`: `ultra`, then `max`, then `xhigh`, then `high`.
- `max`: `max`, then `xhigh`, then `high`.
- `xhigh`: `xhigh`, then `high`.

If no candidate is supported, existing validation reports the mismatch. Cross-family conversion keeps its current mapping behavior.

### Compact request compatibility

After the normal thinking pipeline has produced the final Codex payload, the executor request normalizer checks the target path. `/responses/compact` rewrites `reasoning.effort=max` and `reasoning.effort=ultra` to `xhigh`. Ordinary Responses and WebSocket turns retain the model-supported level.

Keeping this rule at the endpoint boundary avoids teaching the provider-neutral thinking package about HTTP paths.

### Metadata compatibility

The existing Codex request metadata normalizer remains the single outbound boundary:

- `metadata` is removed from every Codex executor payload.
- `client_metadata` is retained only when the target is the official `chatgpt.com/backend-api/codex/responses` endpoint.
- `client_metadata` is removed for `/responses/compact`, official public API-key endpoints, and custom base URLs.

Official application identity assembly runs before normalization. This permits ordinary ChatGPT requests to receive the generated identity while guaranteeing that unsupported targets do not receive it.

### Model catalog alignment

Update every embedded GPT-5.6 definition consistently across Codex subscription tiers:

- GPT-5.6 Sol: `low`, `medium`, `high`, `xhigh`, `max`, `ultra`.
- GPT-5.6 Terra: `low`, `medium`, `high`, `xhigh`, `max`, `ultra`.
- GPT-5.6 Luna: `low`, `medium`, `high`, `xhigh`, `max`.

The embedded Codex client manifest already advertises this split and remains unchanged.

## Data Flow

1. Routing selects the credential and final upstream model.
2. The manager attaches the exact configured `ModelInfo` for API-key attempts when available.
3. The thinking pipeline extracts the requested effort from the suffix or original request.
4. High-effort compatibility selects the strongest supported value at or below the requested intent.
5. The Codex applier writes `reasoning.effort`.
6. Official identity assembly adds ordinary ChatGPT `client_metadata` when in scope.
7. The outbound normalizer removes unsupported metadata and applies the compact effort cap.
8. HTTP or WebSocket transport sends the normalized payload.

## Error Handling

- Malformed JSON retains existing behavior; normalizers return the original body when a JSON mutation fails.
- Unsupported ordinary levels continue through the existing typed thinking error.
- A high-effort value with no supported fallback also retains the typed validation error.
- Metadata filtering does not log payload contents or credentials.

## Testing

- Red/green tests for same-family `ultra`, `max`, and `xhigh` fallback using exact configured model capabilities.
- Registry tests asserting the Sol/Terra/Luna capability split in every embedded Codex tier.
- Executor tests proving ordinary official requests preserve `client_metadata`, while compact, API-key, and custom targets remove it.
- Executor tests proving ordinary model-supported `max`/`ultra` survive and compact requests emit `xhigh`.
- Existing thinking, registry, executor, and integration tests.
- Required `gofmt`, full `go test ./...`, and server compile verification.

## Delivery

Implementation is performed in the current workspace because `main` is checked out by another worktree. After verification, the completed commit is pushed explicitly as `HEAD:main`.
