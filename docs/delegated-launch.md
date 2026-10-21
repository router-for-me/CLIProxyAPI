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
- Omit server-owned identity, ordinary allowance or all quota windows; supply malformed,
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

## Repair threat model and linearization (2026-10-20, before gate changes)

An unused admission expires in 30 seconds. First dispatch re-probes the exact
account and atomically establishes the pin under manager-read + pool locks after
revision validation. Later requests retain the same principal, logical session,
model and revision; they do not inherit the pending deadline. Each request still
probes fresh quota. One active request per binding prevents concurrent initial
execution. At most 256 records exist; active/preparing records are never evicted.
Inactive established pins expire after 30 minutes idle, refreshed at completion.
No absolute task lifetime or automatic credential renewal is claimed.

Release/cancel linearizes under the pool lock: mark revoked, cancel preparation
and any active request context, retain a short idempotency tombstone, deny future
requests. Dispatch that wins the lock first may have sent upstream work; cancellation
cannot undo it. Auth replacement/removal before dispatch linearization denies it;
after that point the immutable active request may finish, with future requests
denied. There is no switch to new credentials. Pruning is lazy on protocol calls,
with a fixed 256-record cap even if there are no subsequent calls.

Account identity is server-owned OAuth account/workspace metadata used with the
same immutable credential and explicit ChatGPT-Account-Id on probe and execution.
The authenticated backend validates that pairing; caller IDs/emails are not proof.
An optional response identity must match; the published payload omits it.
Unknown extra limits/spend-control schemas fail closed. Cache keys are affinity,
not identity: principal + opaque URL + logical session + exact model are the gate.

Final execution headers/target must also preserve that pairing after model header
overrides; bound HTTP refuses redirects. An override that replaces Authorization
or ChatGPT-Account-Id must refuse before outbound execution, rather than silently
probe one workspace and execute another.

Required abuse fixtures: first-use versus release; cancellation while probing or
streaming; idle versus active cleanup; auth replacement during probe; arbitrary
native/custom/omitted cache keys; identity mismatch; published no-identity and
nullable-window usage; structured exhaustion versus throttle/capacity; native
HTTP/SSE quota with request counts; useful tool output never authorizes replay.
Control lookup returns trusted refusal category/scope and executor-attempt counts;
consumer must use it, never exception prose or a blanket HTTP429 account block.

## F7/F8 repair threat rationale (before gate changes)

Scheduler Generation, transient Status and refresh timestamps are not credential
identity: healthy shared-account traffic must not revoke an established pin.
The private revision instead binds registration epoch, credential version, auth
ID/provider, credential tokens and account selection, credential mode, endpoint,
injected headers and proxy. Only security-relevant metadata/attributes enter the
hash; expiry, last_refresh, plan/email and scheduler availability do not. Manager
credential-version production remains unchanged. Removal/re-registration, real
token replacement and final account/model/header/target checks must still deny.
Fixtures must mutate those authority inputs both between turns and during probes.

Executor attempts cannot identify a refusal that happened before execution.
Every newly latched refusal therefore advances a separate refusal version;
re-reading an existing latch must not advance it. Resume requires the inspected
nonzero version and a current latch, preserving principal/session, inactive and
revocation checks. Missing, stale and already-consumed acknowledgements refuse.
Two quota-probe refusals at zero executor attempts must reject the first version.
Acknowledgement never authorizes dispatch: fresh auth/quota/model checks remain.

## F2 local contention threat rationale (before gate changes)

Local probe saturation supplies no evidence about upstream identity or quota.
An established binding must not retain a refusal merely because all four probe
slots are occupied. Reserve its single active owner before queueing; duplicates
must neither probe nor latch a refusal. Waiting has a three-second ceiling plus
the caller's earlier cancellation/deadline, at most one waiter per binding and
256 bindings total. Release cancels queued ownership. Preparation still uses
nonblocking shared slots and its existing five-second lifetime; no new queue or
retry goroutine is introduced. Acquisition never authorizes generation: fresh
authoritative quota and the final credential/model/revocation checks remain.

The owner latches genuine probe/auth refusals and drops failed ownership together
under the pool lock, so resume cannot acknowledge a refusal before its producer
finishes. Local wait expiry or caller cancellation drops ownership without a latch
or refusal-version change. No executor request is replayed. Required fixtures:
mixed prepare/authorize saturation, duplicate ownership, queued cancellation,
deadline/release, fresh exhaustion/identity denial after waiting, and stale resume.

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

## Protocol v2

`POST /v1/delegated-launch/prepare` uses existing API authentication and takes
`clientRequestId`, `sessionId`, `provider: "codex"`, exact `model`, and
`blockedQuotaDomains`. Session/model IDs contain 1–128 ASCII letters, numbers,
dots, underscores or hyphens. IDs are logical consumer task/session IDs, allocated
before native `thread/start`; they are not native cache keys. Ready returns
`protocolVersion:2`, `bindingId`, `basePath`, provider/model/session, `checkedAt`, `expiresAt`, windows,
ordinary allowance and quota domains. Waiting/unverified never authorizes generation.

Set the native provider's `base_url` to origin + `basePath`, using the same
existing client authentication for prepare, execution and control. Its path is
`/v1/delegated-launch/bindings/<bindingId>/sessions/<sessionId>`. Native
`prompt_cache_key` may be generated, custom or omitted; it controls affinity only.
Principal, opaque URL, logical session, model and immutable auth revision enforce
isolation. The URL alone is not bearer authority.

The account domain is `codex:credential:<backend-account-id>`, matching direct
Codex `account/rateLimits/read`. The ID comes from the proxy-owned OAuth account/
workspace metadata, never a prepare body, cache key, email or credential filename.
Prepare and execution use the same immutable access token and explicit
`ChatGPT-Account-Id`. The authenticated backend validates the pairing. Official
backend client `Client::headers` uses precisely this account-selection boundary;
the generated usage payload does not carry identity. Optional response `account_id`
therefore must match if present, but absence is supported. No local unsigned JWT
parse is treated as cryptographic validation. Custom endpoints, API keys, SIWC,
injected auth headers and executor plugins without this boundary remain unsupported.

Read-only usage GET is fixed to `https://chatgpt.com/backend-api/wham/usage`, with
no redirects, no-cache headers, a fresh HTTP Date, bounded body and existing auth
transport. Both `rate_limit.allowed` and `limit_reached` are required. Each present
window must have a valid percentage, positive duration and future reset. Nullable
or absent secondary/primary windows are supported if at least one valid window
exists for healthy admission. False allowance/reached evidence can refuse even
without windows; it never fabricates health. `spend_control.reached=true` and known
`rate_limit_reached_type.type` workspace/account depletion values refuse account
admission. Unknown reached types, missing required booleans, and any nonempty
`additional_rate_limits` fail closed. Additional model-specific limit-name mappings
are deliberately unsupported, including apparently unrelated names; none are ignored.
Credit balances do not authorize paid usage or override ordinary limits.

### Lifecycle and control

Pending `expiresAt` is at most 30 seconds or the earliest window reset. Initial
dispatch re-probes immediately before establishing the pin. Established bindings
keep their principal/session/model/revision across subsequent turns, including
long tool delays, and re-probe quota before each request. No blind child relaunch
or replacement binding is required. Idle expiry is 30 minutes after completion;
an active HTTP/SSE request has no imposed lifetime and is not evicted. At most
256 records, one active executor per binding, four concurrent usage probes and
32 distinct candidate accounts per preparation are allowed. Prepare saturation
fails closed. Bound authorization waits at most three seconds for a shared probe
slot, subject to earlier caller cancellation/deadline. Wait expiry returns
capacity_unverified without latching; caller cancellation also never latches.
Neither case starts an executor or changes the binding. Consumers own waiting;
the proxy performs no automatic generation retry.
No locks span network probes. Probe deadlines are three seconds; prepare is five.

Control endpoints relative to `basePath`, all using the same authentication:

- `GET /state`: `{state: pending|established|released|invalid, active,
  executorAttempts, refusalVersion, refusal?}`. Refusal is `{code,quotaScope,quotaDomains,bindingId}`.
  The counter measures executor invocations, including compact and invocations
  rejected by final payload validation, not billed tokens or guaranteed upstream
  requests. Offline transport fixtures separately count actual provider requests.
  `refusalVersion` is a separate monotonically increasing binding-local counter:
  it advances on every newly latched refusal, including pre-executor probe/auth
  denials, and stays stable when an existing latch is returned. It starts at zero;
  it remains at its last value after acknowledgement, with no refusal present.
- `POST /release`: idempotent revoke and cancel registered active contexts. A
  wrong principal/session has no effect and discloses no binding state.
- `POST /touch`: explicit same-principal/session heartbeat for an established,
  unexpired pin during long tool work. It refreshes only idle storage activity,
  never admission quota freshness or the pinned auth revision. Pending/released/
  expired bindings cannot be revived. No automatic heartbeat client is supplied.
- `POST /resume` with `{expectedRefusalVersion:<inspected nonzero version>}`
  acknowledges the currently latched refusal without changing the pin. It is
  refused while active/revoked, expired, wrong-principal/session, at a different
  refusal version or with no current latch. Missing/zero versions and the old
  executor-attempt request shape refuse; no legacy consumer exists. Fresh quota
  and auth validation still precede the next request. This is an explicit consumer
  retry control, never automatic launch replay after useful work.
- `POST /v1/delegated-launch/cancel` with `{clientRequestId}` cancels pending
  preparation or revokes its binding, with the same authenticated idempotency scope.

Release and final dispatch linearize as specified above. Auth removal/disable/
replacement denies new requests; already-linearized immutable work may finish.
Release attempts cancellation but cannot undo external work. Revoked records are
short tombstones; inactive expired records are reclaimed lazily. Duplicate prepare
returns the original binding/result while retained, including its original pending
snapshot timestamps; it is not fresh admission or authority to duplicate a task.
After expiry/restart the consumer owns durable task identity and safe recovery.

### Native error and transport contract

Bound generation and `/responses/compact` use one exact-auth/model HTTP executor
invocation, bypassing manager selection, aliases, Home and retry/failover. Final
payload model validation runs after payload configuration on both endpoints.
WebSocket is explicitly disabled: consumer sets `supports_websockets=false`.
Installed `codex-cli 0.163.0` loopback fixtures prove actual HTTP use, independent
native cache keys, a successful turn, tool dispatch and manual compaction. This
custom provider uses local compaction through bound `/responses` (zero remote
compact requests in that native test). Bound remote `/responses/compact` is also
implemented and independently tested through the real HTTP executor; no unbound
fallback is permitted.

Before SSE bytes, structured quota/throttle/capacity refusals use HTTP429 JSON.
After bytes they use framed `response.failed` JSON with native codes
`insufficient_quota`, `rate_limit_exceeded` or `server_is_overloaded`. Other denials
retain non-quota status/types. The native error projection compresses metadata:
HTTP429 becomes `responseTooManyFailedAttempts.httpStatusCode=429`; streamed quota
becomes `usageLimitExceeded`; throttle becomes `rateLimitExceeded`; capacity becomes
`serverOverloaded`. Authenticated `/state` is the supported source of binding/scope,
not error prose or HTTP429 alone. Classified refusals latch to prevent accidental
native retries from duplicating execution; explicit `/resume` can acknowledge them.

Only upstream structured `usage_limit_reached`/`insufficient_quota`, or authoritative
ordinary account usage evidence, produces `usage_limit`, account scope and exhausted
account domains. Structured rate limiting produces `throttled/transient`; structured
capacity produces `model_capacity/model`, both without exhausted account domains.
The executor's configurable model-level cooling policy is not proof of upstream
quota scope: quota under that setting is `quota_unverified/unknown`, without account
domains. Unclassified/prose-only errors never become subscription exhaustion.
Unknown/model-specific quota evidence is deliberately unverified; no fabricated
model window or independent account is returned.

Consumer requirements before enabling T3's custom-provider branch:

1. Prepare before launching the child, retain trusted binding/domain data, set the
   returned base URL and explicitly disable native request/stream retries (`0`) and
   WebSocket. Reject protocol/capability mismatch; do not use cache keys as identity.
2. On native launch refusal, inspect authenticated `/state`. Only `usage_limit` with
   `quotaScope=account` and verified domains may enter the exhausted-account block
   set. Throttle, capacity, unknown or absent metadata must not masquerade as account
   exhaustion. Preserve one logical task/clientRequestId in fallback/waiting.
3. Recover only before useful work/tool dispatch. `response.created` is not useful
   work. A tool event/control fixture demonstrates useful work before the next quota
   error; it must remain an ordinary task failure, never startup replay. The proxy
   never relaunches a child, switches accounts/models or implements this T3 decision.
4. Send explicit `/touch` during idle tool work longer than 30 minutes. Release on cancellation/end, and acknowledge only explicit same-pin continuation
   via `/resume`. A restart invalidates process-local pins. Durable waiting, restart
   recovery and safe acknowledgement belong to the consumer, not token reservation.

The existing T3 adapter recognizes the typed native quota signals, but its current
429 projection lacks this control lookup and category-sensitive admission recovery.
That consumer work and actual T3 task-state integration are not proven by proxy
native compatibility tests. Keep existing custom-provider denial until implemented.

### Public contract provenance and offline proof

Public OpenAI Codex source pin: `87f3c3284e1669b90ad9f4925039e9e3bc769b61`.
This is a schema/parser reference, not a claim about the installed binary revision.
Relevant sources are generated `RateLimitStatusPayload`, `RateLimitStatusDetails`,
`SpendControlStatusDetails`, `AdditionalRateLimitDetails`, backend `Client::headers`,
Responses `parse_failed_response`, and documented client cache-key policy.
[Generated usage payload](https://github.com/openai/codex/blob/87f3c3284e1669b90ad9f4925039e9e3bc769b61/codex-rs/codex-backend-openapi-models/src/models/rate_limit_status_payload.rs),
[account-scoped backend client](https://github.com/openai/codex/blob/87f3c3284e1669b90ad9f4925039e9e3bc769b61/codex-rs/backend-client/src/client.rs),
and [structured SSE classifier](https://github.com/openai/codex/blob/87f3c3284e1669b90ad9f4925039e9e3bc769b61/codex-rs/codex-api/src/sse/responses_error.rs)
justify the fixture shapes and transport choices. The committed
`testdata/delegated-usage-pro.json` is a sanitized, synthetic generated-contract
fixture (single window, nullable fields, no response identity), not a live capture.

`TestDelegatedLaunchNativeAppServer` is opt-in via `QUOTA_NATIVE_CODEX`, uses an
isolated temporary HOME/CODEX_HOME, only synthetic credentials, loopback auth/usage/
Responses, and a synthetic dynamic tool answered by the harness. Eight independent
cases run with `-parallel 3`; evidence can be written to `QUOTA_NATIVE_EVIDENCE_PATH`.
It counts actual requests, proves typed events and trusted scope, checks accidental
retry suppression, and records useful tool dispatch. The real native two-turn case advances the injected pool clock by 31 seconds
between turns on the same binding. Deterministic Go tests also cover
>30-second second turns, pending expiry, auth changes, cancellation, active retention,
idle cleanup, source-backed decoder shapes, control isolation and compact pinning.
No live endpoint/schema support, deployment, wrapper/T3 enablement or paid probe is
claimed. Packaging the current patched image and downstream consumer remain separate.


### Attempt identity contract (protocol 2)

Refusals may carry `attemptedAccountDomain`, taken from the immutable authenticated
executor credential. This identifies the failed attempt, not depleted quota.
A `throttled/transient` refusal keeps empty `quotaDomains`; consumers may exclude
that attempted account while trying other eligible accounts on the same model.
`model_capacity/model` remains a candidate-wide capacity signal even with attempt
identity present. Account depletion retains its authoritative `usage_limit/account`
scope. No scope is inferred from error prose. Consumers must compare attempt identity
with the admitted binding domains and refuse malformed/foreign identities.

All result/state `protocolVersion` values are now 2. Version 1 decoders must fail
closed; deploy all three matching layers only after independent review and approval.
The authenticated HTTP route namespace remains `/v1/delegated-launch`.
Offline replay: `QUOTA_CONTRACT_EXPORT=<private-dir> go test ./internal/api
-run '^TestDelegatedLaunchAttemptIdentityContract$' -count=1` exports actual
HTTP-executor throttle/capacity states for the wrapper and T3 fixture checks.
