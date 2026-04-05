# Qwen V2 Adapter Design

## Summary

CLIProxyAPI currently treats Qwen as an OpenAI-compatible upstream and sends requests to `https://portal.qwen.ai/v1/chat/completions`.
The captured Qwen analysis shows that this assumption no longer holds for the imported OAuth account flow:

- Real model discovery is exposed by `https://chat.qwen.ai/api/v2/models`
- Real chat execution is exposed by `https://chat.qwen.ai/api/v2/chat/completions`
- Chat execution requires a `chat_id`
- Cookie-backed session state is required for the V2 chat path

This design keeps the public CPA interface unchanged at `/v1/chat/completions` while adapting Qwen requests internally to Qwen V2.

## Goals

- Keep the existing CPA OpenAI-style public API for Qwen callers
- Support imported Qwen auth files that contain OAuth tokens and session cookies
- Support real Qwen model discovery from `chat.qwen.ai/api/v2/models`
- Support Qwen chat forwarding through `chat.qwen.ai/api/v2/chat/completions`
- Support stable `chat_id` reuse using client session headers when available
- Expose usable auth/account status for Qwen in management surfaces

## Non-Goals

- Do not expose a new public Qwen-native API surface
- Do not implement full Qwen Web feature parity
- Do not fabricate balance or quota values when the upstream does not expose stable fields
- Do not attempt full multimodal/tool-call fidelity in the first pass

## Current Problems

### Wrong upstream path

`internal/runtime/executor/qwen_executor.go` currently defaults to `https://portal.qwen.ai/v1` and builds OpenAI-compatible `chat/completions` requests.

### Wrong authentication mode

The current executor primarily uses `Authorization: Bearer <access_token>`.
The imported auth file also includes:

- `token_cookie`
- `session_cookies.refresh_token`

These are currently not used for Qwen V2 chat execution.

### Static model discovery

`sdk/cliproxy/service.go` uses `registry.GetQwenModels()` for Qwen model exposure.
That returns static registry data rather than the account's actual Qwen V2 model list.

### Session requirement mismatch

Qwen V2 chat requires `chat_id`, but current CPA request handling has no Qwen-specific chat session mapping.

## User-Approved Public Behavior

CPA keeps the OpenAI-compatible public surface.

For Qwen requests:

- `chat_id` is derived from `x-claude-code-session-id` when present
- otherwise from `x-client-request-id` when present
- otherwise from a generated UUID

This enables stable reuse for common CPA clients such as Claude Code and Codex.

## Proposed Architecture

### 1. Keep `QwenExecutor` as the public execution entrypoint

`QwenExecutor` remains the registered executor for provider `qwen`, but its internal transport logic changes from OpenAI-compatible Portal API requests to Qwen V2 requests against `chat.qwen.ai`.

### 2. Add Qwen V2 request helpers

Introduce focused helper logic for:

- auth metadata extraction
- cookie construction
- model list fetch
- user/account status fetch
- OpenAI request to Qwen V2 request adaptation
- Qwen V2 response to OpenAI response adaptation

This should avoid further growth of a single monolithic executor file.

### 3. Make dynamic Qwen model discovery auth-driven

The imported auth record becomes the source of truth for available models.
At startup and auth registration time, the system should query `GET /api/v2/models` for each Qwen auth and register those models into the global model registry.

### 4. Use cookies as the primary Qwen V2 authentication path

For V2 chat and V2 model discovery, request authentication should use session cookies derived from the auth file.

Bearer access tokens remain useful for refresh and possible fallback APIs, but they are no longer the primary chat execution mechanism.

## Auth Data Model

### Required fields from imported Qwen auth JSON

- `type`
- `access_token`
- `refresh_token`
- `expired`
- `last_refresh`
- `resource_url`
- `email`
- `token_cookie`
- `session_cookies`

### Storage changes

`internal/auth/qwen/qwen_token.go` should formally support persisted fields for:

- `token_cookie`
- `session_cookies`

These fields should be preserved when auth files are saved or re-saved.

### Runtime extraction

Qwen runtime helpers should expose a normalized credential shape:

- access token
- refresh token
- resource URL
- cookie jar map
- token cookie value

## Upstream API Mapping

### Model discovery

Request:

- `GET https://chat.qwen.ai/api/v2/models`

Authentication:

- Cookie-based

Behavior:

- Convert returned model records into `registry.ModelInfo`
- Register them against the corresponding auth/client ID
- Prefer dynamic models over static registry defaults

Mapped fields should include when available:

- `id`
- `name`
- `object`
- `owned_by`
- `description`
- `max_context_length`
- capability-derived metadata where safely representable

### Account / quota status

Preferred endpoints:

- `GET https://chat.qwen.ai/api/v2/users/status`
- `GET https://chat.qwen.ai/api/v2/users/me`

Behavior:

- Parse stable fields only
- Surface account/user status to management APIs
- If no stable balance field exists, return status-oriented information only

### Chat execution

Request:

- `POST https://chat.qwen.ai/api/v2/chat/completions`

Authentication:

- Cookie-based

Minimal request shape:

```json
{
  "chat_id": "<derived chat id>",
  "query": "<adapted text prompt>",
  "model": "<requested model>",
  "stream": true
}
```

## Request Adaptation

### Chat ID selection

Priority order:

1. `x-claude-code-session-id`
2. `x-client-request-id`
3. generated UUID

The chosen value should be:

- used as `chat_id` for the Qwen V2 request
- emitted in logs
- returned as `X-Qwen-Chat-ID` response header for observability

### Message flattening

Qwen V2 currently expects a single `query` string rather than OpenAI `messages`.

First-pass adaptation:

- the last `user` message becomes the main prompt body
- preceding `system`, `assistant`, and `user` messages are serialized into a text context block
- the final `query` is constructed as:
  - serialized prior context
  - separator
  - latest user prompt

This keeps the adapter deterministic and avoids inventing unsupported upstream semantics.

### Unsupported features

For the first implementation pass:

- advanced tool-call fidelity is not guaranteed
- multimodal request fidelity is not guaranteed
- unsupported payload shapes should return explicit OpenAI-style errors instead of silent degradation

## Response Adaptation

### Non-streaming

Qwen V2 non-stream responses should be translated into OpenAI-compatible chat completion responses expected by current CPA clients.

### Streaming

Qwen V2 streaming responses should be converted into OpenAI-compatible SSE chunks.

If Qwen V2 emits a format that does not map cleanly:

- preserve clear logs with upstream body summaries
- return a structured error rather than fake success

## Error Mapping

### Authentication failures

Map Qwen auth/session failures to OpenAI-style authentication errors.

### Model not supported

Map model availability failures to OpenAI-style invalid request errors with clear model identifiers.

### Quota / rate limit

Continue mapping quota and rate-limit conditions to HTTP `429`.
If the upstream provides retry hints, preserve them where possible.

### Unknown upstream shape

Return an explicit adapter error and log the upstream shape summary.

## Management Surface Changes

### `/v1/models`

Should reflect dynamically registered Qwen models for authenticated Qwen accounts, not only static `models.json` definitions.

### `/mgmt/auth-files/models?name=...`

Should return the dynamically registered models for the selected Qwen auth file after a successful model sync.

### Qwen account status endpoint behavior

Existing management surfaces should be extended to expose stable status information for Qwen auth files when available, including:

- auth file identity
- user/account identity fields
- current availability
- model count
- recent quota or retry state

If no stable upstream balance field exists, the response should avoid claiming one.

## Startup and Refresh Behavior

### Model sync timing

Dynamic Qwen model sync should run:

- when a Qwen auth file is loaded
- after successful Qwen login/import
- after successful token refresh when needed

### Token refresh

Existing refresh behavior should be retained, but the refreshed auth metadata must preserve:

- `access_token`
- `refresh_token`
- `expired`
- `last_refresh`
- existing cookie metadata when still valid

If a refresh response also yields cookie-relevant information in the future, the code should be structured to accept it without redesign.

## Testing Strategy

Implementation follows TDD.

### Unit tests

Add tests for:

- chat ID selection priority
- UUID fallback when no session headers exist
- auth metadata parsing of `token_cookie` and `session_cookies`
- Qwen V2 model response to `ModelInfo` conversion
- OpenAI `messages` flattening to Qwen `query`
- Qwen error response to OpenAI-style error mapping

### Executor tests

Add tests for:

- non-streaming Qwen V2 execution path
- streaming Qwen V2 execution path
- cookie-authenticated requests
- quota/rate-limit translation
- model discovery registration flow

## Acceptance Criteria

- Importing a Qwen auth file in the provided JSON shape preserves cookie metadata
- A loaded Qwen auth can fetch real models from `chat.qwen.ai/api/v2/models`
- `/v1/models` exposes those dynamically discovered Qwen models
- `/mgmt/auth-files/models?name=...` returns those models for the auth file
- `/v1/chat/completions` requests targeting Qwen use derived `chat_id` values
- `x-claude-code-session-id` is preferred over `x-client-request-id`
- UUID fallback works when neither header exists
- Qwen V2 chat execution succeeds through the CPA OpenAI-compatible surface
- Relevant Go tests pass

## Risks

### Upstream response variability

Qwen V2 endpoints may evolve without compatibility guarantees.
The adapter should keep parsing narrow and defensive.

### Hidden session requirements

Some chat operations may require additional cookies or chat bootstrap behavior not yet captured.
This is the main implementation risk and should be isolated behind helper functions.

### Message compression quality

Flattening OpenAI message history into a single `query` is intentionally minimal.
It may reduce conversational fidelity compared with a native upstream session model.

## Recommended Implementation Order

1. Add failing tests for auth parsing and chat ID selection
2. Add failing tests for model conversion and request adaptation
3. Update Qwen auth storage to preserve cookie fields
4. Add Qwen V2 helper/client logic for cookies, models, and status
5. Switch `QwenExecutor` from Portal/OpenAI-compatible transport to Qwen V2 transport
6. Register dynamic Qwen models into the global registry
7. Update management behavior for dynamic Qwen model/status exposure
8. Run focused tests, then broader Go validation

## Notes

This design intentionally keeps the external CPA API stable for existing clients.
The main tradeoff is that Qwen-specific semantics are absorbed into the adapter layer rather than exposed directly.
