# ADR 0008: Decision model passthrough (TypeSafe Jev, OpenAI Decisions)

Status: accepted (2026-11-20)

## Context

"Decision" (System One) models answer typed questions about supplied state with probabilities instead of
generating text:

- TypeSafe Jev: `POST https://api.typesafe.ai/v1/systemone`, body `{model, state, questions: {id: {type:
  noul|choice|score|box, instructions, criteria}}, assets?}` (JSON or `multipart/form-data` with the JSON
  in a `request` part), response `{model, answers, usage: {input_tokens, output_tokens}}`. Models
  `jev-latest`, `jev-preview`, `jev-1.14.0`, `jev-1.13.0`. $0.042 per 1M input tokens, output free.
- OpenAI Decisions: `POST https://api.openai.com/v1/decisions`, body `{model, input, questions: [{type:
  predicate|choice|score, name?, instructions, choices|levels}]}`, response `{model, answers, usage:
  {input_tokens, output_tokens, total_tokens, input_tokens_details.cached_tokens, ...}}`. Model
  `gpt-6-luna` only. $0.10 per 1M uncached input tokens, output free.

Neither has streaming. Neither fits the chat/Responses/Messages formats the upstream executors speak, and
the two schemas differ in every field name.

## Decision

proxx serves both as **native passthrough routes** on the client API (same `/v1` client-key auth as the
other routes), so the vendors' own SDKs work by pointing their base URL at proxx:

| Client route | Upstream | Usage provider |
|---|---|---|
| `POST /v1/systemone` | `<typesafe base-url>/v1/systemone` (default `https://api.typesafe.ai`) | `typesafe` |
| `POST /v1/decisions` | `<openai base-url>/decisions` (default `https://api.openai.com/v1`) | `openai` |

- The request body and `Content-Type` are forwarded byte for byte (JSON or multipart). There is no
  translation, so user payload rules (`payload.*`) are deliberately not applied: the AGENTS.md "payload
  rules are the final barrier" rule covers executor request building, and these routes build no payload.
  A test pins this. Bodies over 64 MB are rejected with 413 (never truncated). The client's
  `Authorization` / `X-Api-Key` is dropped and replaced with an upstream key. Vendor feature headers
  (`TypeSafe-*`, `OpenAI-*` except `OpenAI-Organization`/`OpenAI-Project`) are forwarded.
- The upstream status, body and headers are returned unchanged, minus hop-by-hop headers,
  `Content-Length`, `Content-Encoding` and `Set-Cookie`.
- Keys: `proxx.decisions.<vendor>.api-keys`, used round-robin per vendor.
  - 429: the key cools down for `Retry-After` (default 60s, capped at 1h) and the next key is tried.
  - 401/403: the key cools down for 10 minutes and the next key is tried.
  - 500/502/503/504 or a transport error: the next key is tried, no cooldown.
  - Anything else (e.g. 400) is returned as is. Requests are not streamed, so retrying is safe.
  - Each key is tried at most once per request. When every key is cooling down, proxx answers 429 with
    `Retry-After` set to the earliest key's remaining cooldown. With no keys configured it answers 503.
- Every upstream attempt publishes a usage record (provider above, `ExecutorType` `proxx-decisions`,
  model = the response `model` (versioned ID) else the request `model`, alias = the request `model`),
  so cost, the spend aggregator and the request log viewer work unchanged. Failed attempts are
  published as failed records.
- Prices: OpenAI `gpt-6-luna` comes from the catalog (input $0.10, matching Decisions). The catalog's
  chat output price would overstate cost if Decisions ever reported output tokens; it currently reports
  0, so this is accepted and noted here. Jev is not in models.dev, so the pricing package
  carries a built-in fallback (`typesafe` / `jev*`: input $0.042, output $0) used only when neither
  config nor catalog prices the model. `proxx.pricing.models` still overrides it.
- Upstream attempts are written to request logs as `API REQUEST n` / `API RESPONSE n` sections through the
  executor logging helpers (keys are masked there).
- Config is read per request, so key changes hot-reload. Like every other provider key, the keys are
  returned in plain text by the authenticated management config API (upstream masks only TURN secrets). Cooldown state lives in memory and is keyed by a
  hash of the key.
- Upstream hook: one line in `internal/api/server_routes.go` (`s.registerProxxDecisionRoutes(v1)`). Code
  lives in `internal/proxx/decisions`, `internal/api/server_routes_decisions_proxx.go`, and a built-in
  price fallback in the fork-owned `internal/proxx/pricing`.
- The upstream HTTP client has no overall timeout (AGENTS.md) and honors `proxy-url` (per vendor, else the
  global one). `Accept-Encoding` is not forwarded, so Go decompresses responses before usage parsing.

## Alternatives considered

- **One proxx-defined decision format translated to both vendors.** It would allow failover between Jev
  and OpenAI and model groups across them, but the schemas differ in every field and in semantics
  (map vs. ordered list of questions, `noul` vs `predicate`, `criteria` vs `choices`/`levels`, `box` has
  no OpenAI equivalent). A lossy translation layer without a concrete consumer is guesswork. It can be
  added later on top of these routes.
- **Reusing upstream auth entries and executors.** Executors are built around chat-style translators and
  the scheduler's model registry; decision models are not chat models and must not appear in `/v1/models`
  for chat clients. A small dedicated key pool is simpler and keeps upstream untouched.

## Non-goals

- Listing decision models in `/v1/models` (chat clients would try to chat with them).
- Model groups / failover across vendors.
- Panel UI for the keys (they are edited in the config file or the config API).

## Tests

- Unit: `internal/proxx/decisions` (key rotation, cooldowns, failover, header filtering, usage parsing,
  multipart model extraction) and the pricing fallback.
- E2E: `PROXX_E2E=1 go test ./test/proxx/decisions_e2e/ -run E2E -v -count=1` builds the binary and runs
  it against mock TypeSafe and OpenAI upstreams (no real keys needed).
