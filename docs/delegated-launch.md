# Delegated launch boundary

## Threat model (before implementation)

The caller controls request IDs, sessions, models, blocked domains and binding IDs.
A binding grants no authentication authority. Existing API authentication must
establish a nonempty principal; bindings are scoped to that principal and session.
Credential files, model aliases and pool affinity do not establish independent
quota accounts. Only an authoritative backend account ID may do so.

Abuse-path fixtures required before accepting this boundary:

- Reuse another principal's binding, change its session/model, or replay an ID
  with a different prepare body: refuse before generation.
- Omit identity, ordinary allowance or either quota window; supply malformed,
  stale, partial, mismatched or timed-out usage: refuse before generation.
- Report 12% usage with ordinary allowance false: block every same-account alias.
- Replace/disable credentials between prepare and dispatch, or exhaust the exact
  account: refuse before generation; never reselect another credential/model.
- Redirect a usage probe: do not forward credentials or follow the redirect.
- Change the final model through payload rules/translation: refuse before sending.
- Request WebSocket/unsupported endpoints: refuse before generation.
- Flood duplicate/concurrent prepares: bounded memory, probes and request lifetime.
- Return a first-request quota error: structured account scope, no automatic replay.

No quota reservation is claimed: upstream usage can race after the final probe.
No live configuration, credential store, launcher or deployed image is changed.

## Source provenance

Implementation base: fetched router-for-me/CLIProxyAPI main
`20c9095cac089c6a8e40554debe4dd8a19d4c14f`.
The live ub2 image inspected on 2026-10-20 was
`sha256:36380637e7245bd01206dc3d35616fe08bf5e437c7247b447799e7163591fd75`.
Its build manifest names upstream `54946fa3dfa29c6ca7312ac141a92cdd5e413771`
plus dotfiles `095293d823a3b74f96d449a4439db69547adde06` patches. This PR changes
the owning upstream API/manager, not that older patched build. Integrating into
the tracked dotfiles image builder, wrapper and typed T3 consumer requires
separate changes, review and rollout authority. Existing T3 pool denial remains.

## Protocol v1

`POST /v1/delegated-launch/prepare` uses existing API authentication and takes
`clientRequestId`, `sessionId`, `provider: "codex"`, the exact `model`, and
`blockedQuotaDomains`. Session/model IDs contain 1–128 ASCII letters, numbers,
dots, underscores or hyphens. Custom endpoints, API keys, SIWC and executor
plugins without the execution-owned delegated method are unverified.

The JSON `status` discriminates `ready`, `waiting` and `unverified`. Ready
contains opaque `bindingId`, `basePath`, provider/model/session, `checkedAt`,
`expiresAt`, primary/secondary `windows`, `ordinaryUsageAllowed`, and account
`quotaDomains`. Set the launcher's Responses base URL to origin + `basePath`;
POST to its `/responses`. The session is carried in this URL because native
launcher custom-header support is not assumed. An optional `prompt_cache_key`
must match it. Other endpoints and WebSocket are unsupported. A missing
authenticated principal is refused even when ordinary API authentication is
configured as optional.

The quota domain is `codex:credential:<backend-account-id>`, matching direct
T3 Codex's `account/rateLimits/read` account ID convention. Different auth
files for that account never create independent capacity. Both required quota
windows and explicit ordinary allowance must be returned by a fresh no-cache
GET of `/backend-api/wham/usage`. The response must contain `account_id` matching
the selected credential and a fresh HTTP Date; absence means unverified. This
strict schema has only synthetic fixture evidence in this PR. Production
support must be established separately; no healthy state is inferred when an
upstream response omits identity or allowance.
Unknown nonempty additional model-specific limits also return unverified.

Refusals are `{error:{code,quotaScope,quotaDomains,bindingId?}}`. Bound refusal
scope is conservatively account-wide. HTTP 429 from the first generation
attempt maps to `usage_limit` with the binding/domain retained, without replay.
Streaming bootstrap errors use the same JSON refusal before response bytes;
errors after bytes use an SSE `error` event with that structure. Other upstream
errors retain their HTTP status and use `upstream_error`, never quota prose
matching. The consumer must only recover a launch before useful work began.

At most 256 prepare records and four usage probes exist at once. At most 32
distinct registered accounts are considered per prepare, deterministically
ordered by auth ID; probe saturation is unverified. Probe deadlines are three
seconds, prepare is five seconds. Probes run outside manager locks. A ready
binding expires within 30 seconds or at the earliest required window reset.
Each bound ingress revalidates quota and revision. The exact immutable auth
snapshot is passed directly to the HTTP Codex executor without manager retries,
model aliases, Home routing or credential fallback. Final payload validation
refuses model changes after user payload rules; ordinary APIs retain all their
existing selection behavior.

Idempotency is scoped to authenticated principal + `clientRequestId`, with
conflicting bodies refused. Duplicate callers share the original in-flight
probe/result, and cancelling a waiter does not cancel its owner. Results are
copied. Expired completed records are reclaimed; after expiry the same ID can
prepare anew. State is process-local and restart invalidates bindings. The
future consumer owns durable task identity, waiting and retry/re-prepare;
bindings are neither durable task records nor token reservations. Cancellation
of the owning prepare fails it closed. There is no long-lived admission lease
or guarantee against externally consumed quota after revalidation.
