# Desktop supervisor API

Routes for a desktop app that launches the proxy, shows per-account usage and
stops it. All sit under `/v8/management` and take the management key as
`Authorization: Bearer <key>` or `X-Management-Key: <key>`.

## Management key

- `MANAGEMENT_SUPERVISOR_PASSWORD` (recommended; random, ≥ 32 characters): read
  once at startup, then cleared from the proxy's environment, so plugins and
  child processes never see it. Accepted from loopback only. Exempt from the
  failed-key IP ban; other wrong keys are still counted and banned. Nothing is
  written to `config.yaml`.
- `MANAGEMENT_PASSWORD`: enables the routes without writing a key to disk.
- `config.yaml` (v8 layout; the legacy `remote-management:` block is still accepted):

```yaml
management:
  allow-remote: false   # localhost only
  secret-key: "<choose a key>"   # plaintext is hashed in memory at load
```

## Failed management keys

Counted per client IP. 5 failures lock the IP out of management for 5 minutes
(`403`). After a lockout, 3 more failures lock it out for 30 minutes, as does
every later lockout. A correct key, or 2 hours without a request, resets the IP
to the first step.

A client key (`access.api-keys`) sent as the management key gets `401` (`this
is a client API key; management calls need the management key`) and is not
counted.

- `GET /server/lockouts`: IPs with failures or an active lockout:
  `{"lockouts":[{"ip":"127.0.0.1","failures":2,"failures_allowed":3,"locked_until":"2026-10-26T19:30:00Z","lockouts":1}]}`.
  `failures_allowed` is the count that triggers the next lockout; `locked_until`
  is `null` when not locked out.
- `DELETE /server/lockouts`: lifts every lockout and clears all failures. A
  locked-out IP can't call it; the supervisor password can.

## Process lifecycle

- `-parent-pid <pid>`: the proxy shuts down gracefully when that process exits.
  Windows waits on the process handle; other platforms poll every
  `ParentPollInterval` (2s).
- `POST /server/shutdown`: replies `202 {"status":"shutting down"}`, then stops
  the proxy as SIGINT does. `403` when the TCP peer or resolved client IP is not
  loopback, even with `allow-remote: true`.

## Routing

- `routing.strategy: soonest-reset` (aliases `soonestreset`, `sr`): each new
  conversation goes to the available account whose weekly window resets first,
  then the 5-hour window, then credential ID. Unknown or past resets rank last.
  A cooling-down account is skipped until it recovers. With
  `routing.session-affinity: true`, a conversation stays on its account.
- `routing.prefer-auth-kind`: `oauth` uses subscriptions before API keys,
  `api-key` the reverse; unset mixes both. With none of the preferred kind
  available, requests use the other kind. A recovered preferred credential takes
  the next request, even in a conversation bound to a fallback. Explicit
  credential pins take precedence.
- `disabled: true` on an entry under `api-keys.<family>[].keys` stops routing to
  that key without deleting it. The key keeps its credential identity.

Read and write through the v8 config API (writes accept `If-Match: <ETag from the GET>`):

```http
GET /v8/management/config/routing/strategy          -> "soonest-reset"
PUT /v8/management/config/routing/strategy          body: "soonest-reset"
GET /v8/management/config/routing/session-affinity  -> true
PUT /v8/management/config/routing/session-affinity  body: true
```

## Claude usage lookups

Traffic headers carry only the 5-hour, weekly and weekly Fable windows, and are
not persisted. The proxy therefore looks up each enabled Claude OAuth
credential's usage (`GET https://api.anthropic.com/api/oauth/usage?cedar_ember=1`),
which also reports per-model and spend windows and the account's reset credits.
Lookups and reset claims send the credential's Claude Code device-profile
User-Agent: Claude reports reset credits only to Claude Code. Lookups are stored apart from header
readings: traffic updates the windows its headers carry and never removes
lookup-only windows. A window both report comes from the newer reading, except
a response header never lowers a lookup's usage for the same period (headers
can trail lookups by a point); a newer lookup always wins.

Every minute the proxy looks up credentials with no lookup, one older than 15
minutes, or a window that reset since the last lookup. Lookups run one at a
time, 2s apart. A failed credential is retried after 15 minutes. An expired
access token is not a failure: the lookup retries 10s later, after renewal.

Claude usage is also reconciled when an account has new response headers or a
response ends after its last lookup. `usage-lookups.settle-seconds` is the minimum
interval since that lookup (default 60; 0 or negative disables traffic-driven
lookups). Continuing traffic does not postpone the deadline. Response completion
after a mid-stream lookup schedules another lookup to capture the final usage.
Lookup timestamps use request start so activity during the lookup remains pending.
An account update during a lookup prevents that lookup from clearing its cooldown;
a later lookup revalidates it. With no further traffic, this uses the 15-minute schedule.
The legacy setting name is retained for existing configurations.

## GET /usage/windows

```json
{
  "strategy": "soonest-reset",
  "credentials": [
    {
      "id": "claude-work.json",
      "auth_index": "3f2a9c",
      "name": "claude-work.json",
      "provider": "claude",
      "label": "Work account",
      "email": "work@example.com",
      "note": "Work",
      "disabled": false,
      "status": "active",
      "status_message": "",
      "priority": 0,
      "cooldown_until": null,
      "weight": 1,
      "plan": {"tier": "default_claude_max_5x", "type": "claude_max", "label": "Max 5x", "multiplier": 5},
      "reset_credits": null,
      "observed_at": "2026-10-25T12:00:00Z",
      "next_pick": true,
      "windows": [
        {"key": "five_hour", "label": "5-hour", "used_percent": 12, "resets_at": "2026-10-25T18:00:00Z", "status": "allowed", "duration_seconds": 18000, "scoped": false, "reset_cadence": "on_use"},
        {"key": "seven_day", "label": "Weekly", "used_percent": 41, "resets_at": "2026-10-29T09:00:00Z", "status": "allowed_warning", "duration_seconds": 604800, "scoped": false, "reset_cadence": "fixed"},
        {"key": "seven_day_fable", "label": "Weekly · Fable", "used_percent": 100, "resets_at": "2026-10-30T09:00:00Z", "status": "rejected", "duration_seconds": 604800, "scoped": true, "reset_cadence": "fixed"}
      ]
    }
  ]
}
```

- `name`: the value for `DELETE /credentials?name=<name>` and the `name` field
  of `PATCH /credentials/status` and `/credentials/fields`.
- `label`: derived account label. `note`: user-set note. `email`, `note` and
  `priority` are omitted when unset.
- `status_message`: `""`, a proxy reason (e.g. `disabled via management API`), or
  the provider's last error response body verbatim, often JSON such as
  `{"type":"error","error":{"type":"rate_limit_error","message":"…"}}`. Raw API
  keys in it are masked.
- `cooldown_until`: when a benched credential becomes eligible again, else `null`.
- `weight`: the `weighted-round-robin` weight (default 1; 0 excludes the credential).
- `plan`: the subscription. Claude: from the OAuth profile, looked up every 6
  hours. Codex: the login token's `plan_type` (`{"tier": "plus", "type": "plus", "label": "Plus"}`).
  `null` before the first lookup and for other providers. `multiplier`: 5-hour
  usage relative to Claude Pro, from the tier (`..._5x` = 5; Pro = 1); `null`
  when unpublished (Team, Enterprise, every Codex plan).
- `reset_credits`: a Codex or Claude account's rate-limit reset credits, from
  its usage lookup (Codex: every 15 minutes, also recording windows; Claude: the
  usage lookup above):
  `{"available": 1, "applicable": 0, "checked_at": "2026-10-26T12:00:00Z", "expiries": ["2026-11-25T12:00:00Z"]}`.
  `available`: credits held. `applicable`: credits usable now (Codex: against a
  reached limit; Claude: unpaused grants Claude marks usable, none during a
  claim cooldown). `expiries`: when held credits expire, soonest first (Codex:
  from ChatGPT's reset-credit list, read only while credits are held; Claude:
  each grant's end, once per reset left). It can be shorter than `available`: a
  credit without an expiry, or a failed list lookup, adds none. Claude's
  `checked_at` is rewritten only when the credits change or every 15 minutes.
  `null` before the first lookup, when the account reports none (Claude: not
  eligible), and for other providers.
- `observed_at`: time of the latest usage snapshot, else `null`.
- `next_pick`: the credential the strategy would choose for a new conversation
  within its provider. Rotating strategies (`round-robin`) mark none.
  Config-file credentials are ranked but not listed, so a provider may have no
  listed `next_pick`.
- `windows`: account-wide first, shorter before longer. `used_percent`,
  `resets_at`, `status` and `duration_seconds` (window length) are `null` when
  unknown. `scoped: true` marks model-, feature- or spend-specific windows; only
  unscoped windows describe the account and drive `soonest-reset`.
  `reset_cadence`: `"fixed"` (next period starts at the reset), `"on_use"` (starts
  with the first request after the reset) or `null` (unknown: Codex, Devin).
  Claude reports `on_use` for 5-hour and `fixed` for weekly windows; Codex
  reports `on_use` for its account-wide 5-hour window. An `on_use` window that
  has not started has `resets_at: null` (Codex: unused, with a reset a full
  window away; one started under a minute ago reads the same). Only Claude,
  Codex and Devin report windows; other providers return `[]`.

Config-backed API keys are listed, disabled ones included, with
`"api_key": {"masked": "sk-a…1234", "success": 7, "failed": 2}` (counts since
the proxy started). Raw keys are never returned. Keys without observed quota
have `"windows": []`. Usage lookups and login renewal don't apply to API keys.

## POST /usage/refresh

Body `{"auth_index": "3f2a9c"}` refreshes one Claude or Codex credential; an
empty body refreshes every enabled Claude and Codex OAuth credential, 2s apart.
A Codex lookup reads ChatGPT's usage document, so its windows and reset credits
appear without Codex traffic. Returns the `GET /usage/windows` shape, plus
`"errors": {"<id>": "<message>"}` for failed lookups.

- `404`: unknown `auth_index`.
- `400`: not an enabled Claude or Codex OAuth credential, or invalid body.

## POST /usage/reset-credit

Spends one of a Codex or Claude account's rate-limit reset credits. Body
`{"auth_index": "<auth_index>", "grant_id": "<Claude grant id>"}`; enabled Codex and Claude OAuth logins only.
`grant_id` is optional for legacy clients and is rejected for Codex. A supplied ID must be a non-empty string.

- Codex: ChatGPT's `POST /backend-api/wham/rate-limit-reset-credits/consume`, as
  Codex's `/usage` panel does.
- Claude: a fresh usage lookup picks Claude's `next_grant_id`, then
  `POST https://api.anthropic.com/api/organizations/<organization_uuid>/reset_rate_limits`
  claims it (`{"program": "cedar_ember", "grant_id": …, "request_id": …}`), as
  Claude Code's `/usage` does. Needs the login's `organization_uuid`.

When `grant_id` is supplied, the fresh lookup validates that exact grant instead of picking the default.
Missing, spent, expired, paused, blocked or unknown-scope selections return `no_credit` without claiming another grant.
The app always supplies an explicit Claude grant ID.

Claude `reset_credits.grants` lists held, unexpired grants with `id`, `label`, `scope`, `available`, `applicable`,
nullable `expires_at`, `clears`, nullable `gives_back_weekly` and nullable `unavailable_reason`.
Scope is `fable`, `general` or `unknown`, determined from restoration semantics, never campaign IDs or upstream `type`.
Both known scopes currently report upstream `type: "full"`. Codex and older saved lookups omit `grants`.
The existing total counts and expiry list remain available. Grant changes are persisted even when totals stay the same.

Each call sends a fresh request id, so it spends at most one credit.

```json
{"code": "reset", "windows_reset": 2, "usage": { "...": "GET /usage/windows payload" }}
```

`code`: `reset` (spent), `nothing_to_reset`
(no limit to clear, nothing spent; Claude's `not_limited`), `no_credit` (Claude:
no usable grant, or `ineligible`) or `already_redeemed` (Claude's
`already_used`). `windows_reset` is Codex's count; Claude reports `0`. Usage and
credits are then looked up again; a failed lookup adds `refresh_error`. Which
limits a reset clears depends on the grant (a Claude grant lists them in
`clears`), so the proxy clears nothing itself: that lookup lifts a cooldown only
when its windows show room, as any lookup does. After a failed lookup the
cooldown stays until a later lookup or its own expiry.

- `400`: not an enabled Codex or Claude login. `404`: unknown index.
- `502`: the provider failed, or Claude answered `cooldown`, `unavailable`
  (unconfirmed; check the limits before retrying), 429, 401 or 403.

## POST /usage/start-window

Starts a login's `on_use` windows now by sending one minimal request through
that credential (Claude: `max_tokens: 1`; Codex: a one-word prompt, since its
upstream rejects output caps). Body `{"auth_index": "<auth_index>", "model":
"<model id>"}`; enabled Claude and Codex logins only. The request runs through
the normal executor, so login renewal and response-header windows apply.
Returns `{"usage": <GET /usage/windows payload>}`.
Codex uses the lowest supported reasoning effort when the model declares levels; otherwise the effort is omitted.

- `400`: missing field, or not an enabled Claude or Codex login. `404`: unknown
  index. `409`: an account-wide `on_use` window is already running.
- `502`: the request failed; `{"error": "…", "status_code": 429}` carries the
  upstream status when known.

## GET / PUT /routing/preferred-auth

`PUT {"provider":"claude","auth_index":"3f2a9c"}` sends that provider's new
requests to the credential while it is available, ahead of the strategy and
session affinity. When it is cooling down, disabled or already tried in the
request, routing is automatic. `auth_index: ""` clears it. Both methods return
`{"preferred":{"claude":"3f2a9c"}}`. Held in memory only; re-apply after a
restart. `GET /usage/windows` marks it `"preferred": true` and moves
`next_pick` to it while usable. Sessions bound during the override keep their
binding until session affinity expires.

- `400`: missing provider, or the credential belongs to another provider.
- `404`: unknown `auth_index`.

## POST /routing/move-session

`{"session_id":"claude:<session uuid>","auth_index":"3f2a9c"}` moves a
conversation to another credential of the same provider: its live
session-affinity bindings (every model, and its subagents) now point at that
credential, keeping their expiry. `session_id` is a request event's
`session_id`. The next request uses the new credential while it is available;
otherwise routing picks one as usual. Returns `{"bound":2}`, the number of
bindings the conversation has for that provider (including any already there).

- `400`: missing `session_id` or `auth_index`.
- `404`: unknown `auth_index`, or no live binding for that provider.
- `409`: session affinity is off.

## GET /usage/events

Server-sent events: `event: ready` on connect, then one `event: request` per
upstream attempt, successful or failed:

```json
{"at":"…","request_id":"…","selection_id":"…","provider":"claude","model":"…",
 "auth_id":"…","auth_index":"3f2a9c","stream":true,"failed":false,"status_code":0,
 "latency_ms":5400,"ttft_ms":900,"input_tokens":1200,"output_tokens":300,"cached_tokens":0,
 "cache_creation_tokens":0,"session_id":"claude:ac980658-…"}
```

- `upstream_id`: optional provider response ID: Claude `message.id`,
  OpenAI/Codex `response.id` or chat completion `id`, Gemini `responseId`.
  Join it to Claude Code `message.id` or Codex `response_id` in local logs.
  Omitted when unavailable, including failures before a response. The proxy's
  `request_id` remains a separate execution identifier.

- `cached_tokens`: cache reads. `cache_creation_tokens`: cache writes. Claude
  counts both apart from `input_tokens`; OpenAI includes reads and writes in
  `input_tokens`. OpenAI/Codex writes come from `input_tokens_details.cache_write_tokens`
  (or Chat Completions `prompt_tokens_details.cache_write_tokens`).
- `cache_creation_1h_tokens`: optional integer, the 1-hour subset of Claude's
  total `cache_creation_tokens`, from `usage.cache_creation.ephemeral_1h_input_tokens`.
- `cache_creation_long_tokens`: optional integer, the 12-hour subset of OpenAI/Codex's
  total `cache_creation_tokens`, from `input_tokens_details.cache_write_tokens_details.ttl_12h_tokens`
  (or the corresponding `prompt_tokens_details` field). The other write lifetime is
  `ttl_30m_tokens`. Both subset fields include zero when reported and are omitted
  when upstream did not report the split; neither is added to the total.
- `tier`: optional billed response tier, never inferred from the request.
  Claude `usage.speed` supplies `standard` or `fast`. OpenAI/Codex response
  `service_tier` maps `default` to `standard`, `priority`/`fast` to `fast`, and
  `flex` to `flex`. Unknown or absent values are omitted. OpenAI renamed Priority
  processing to Fast mode; `priority` and `fast` select the same service.
  Sources: [OpenAI cache usage](https://developers.openai.com/api/docs/guides/prompt-caching#usage),
  [OpenAI Fast mode](https://developers.openai.com/api/docs/guides/fast-mode),
  [Claude fast mode](https://platform.claude.com/docs/en/build-with-claude/fast-mode).
- `alias`: optional client-requested model/alias; `model` is the effective upstream
  request model. Keep them separate when displaying requested versus effective routing.
- `reasoning_effort`: optional effective effort extracted from the final translated
  and configured upstream payload (including a numeric thinking budget where applicable).
  Omitted means unknown, not a default effort. Do not backfill it from current settings.
- `reasoning_tokens`: upstream reasoning/thinking token count, included only when reported,
  including zero. Claude uses `output_tokens_details.thinking_tokens`, OpenAI/Codex
  use reasoning token details, and Gemini uses `thoughtsTokenCount`.
- `request_settings`: optional object observed from the final translated and configured
  upstream request. Fields are omitted when unknown: `thinking` (`adaptive`, `enabled`,
  or `disabled`), `thinking_budget` (integer token budget), `max_tokens` (integer output
  limit from `max_tokens`, `max_output_tokens`, or `max_completion_tokens`), and `betas`
  (sorted, de-duplicated strings from the actual upstream `anthropic-beta` header).
  OpenAI/Codex report `thinking: "disabled"` only for reasoning effort `none`.
  The object is omitted when nothing is known; effort remains in `reasoning_effort`.
- `session_kind`: optional `root`, `subagent`, `fork`, or `compaction`, with precedence compaction > fork > subagent > root. Root requires a known session and no parent; missing kind means unknown.
- `parent_session_id`: optional parent conversation for a subagent execution.
- `trace_id`: optional inbound request trace identifier, shared by its execution attempts.
- `downstream_transport` and `upstream_transport`: optional observed transports per
  execution attempt, `http`, `websocket`, or `websocket+http` if both were actually
  observed within that attempt. Empty/omitted means unknown; `stream` does not imply WebSocket.
- `transport_fallback_reason`: optional stable metadata-only code for an observed
  fallback or failed upgrade (for example `websocket_upgrade_426`); never raw error text.
- `purpose: "window-start"` marks requests sent by `POST /usage/start-window`;
  `purpose: "canary"` marks client requests carrying `X-CPA-Purpose: canary`.
  Other header values are ignored. The header is stripped before forwarding upstream.
  Purpose is otherwise absent for client traffic.
- `status_code` is the upstream HTTP status observed for an attempt, successful or failed; omitted when unknown. Failed attempts add a sanitized `error` summary. Attempts of
  one credential selection share `selection_id`.
- `session_id`: the client conversation, as session affinity keys it (`claude:<session uuid>`,
  `codex:<thread id>`, with `:agent:<id>` for subagents); omitted when the client sends none.
- `user_agent`: the client request's User-Agent header, naming the program that sent it
  (e.g. `claude-cli/…`, `codex_cli_rs/…`); omitted when the client sends none.
- `event: resync`: the reader fell behind; reload `GET /usage/windows`.
- `event: usage` (data `{}`): a lookup or live upstream response recorded windows; reload
  `GET /usage/windows`. Updates before the reader catches up share one event.
- `: keep-alive` comment lines every 15s.

## Adding a Claude account (OAuth)

1. `GET /oauth/auth-url?provider=claude&is_webui=true` returns
   `{"status":"ok","url":"<authorize URL>","state":"<state>"}`. Open `url` in a
   browser. With `is_webui=true` the proxy takes the redirect on `localhost:54545`.
2. Without `is_webui`, post the final redirect URL:
   `POST /oauth/callback` `{"provider":"claude","redirect_url":"<url>"}`.
3. Poll `GET /oauth/status?state=<state>`: `{"status":"wait"}` while pending,
   `{"status":"ok"}` when saved, `{"status":"error","error":"<message>"}` on
   failure. Sessions time out after 5 minutes;
   `DELETE /oauth/session?state=<state>` cancels one.

## Pool usage limit (429)

When every credential serving a model is spent, the request fails with `429`
and error type `usage_limit_reached`, including the request that spends the
last credential. `X-Should-Retry: false` stops client retries. The soonest reset
is repeated in each client's limit headers:

- Claude Code: `Anthropic-Ratelimit-Unified-Status: rejected`, `-Reset`,
  `-Representative-Claim` (`five_hour` or `seven_day`), and that window's
  `-5h-`/`-7d-` `Status`, `Reset` and `Utilization: 1`. Read only when Claude
  Code is signed in to a subscription, not with `ANTHROPIC_AUTH_TOKEN`.
- Codex: `X-Codex-Primary-Used-Percent: 100`, `X-Codex-Primary-Reset-At`, and
  `X-Codex-Primary-Window-Minutes` when the window is known.

## Codex login

Codex keeps its own ChatGPT login and sends its account calls (`/wham/usage`,
plan, plugins) to chatgpt.com itself; the proxy doesn't forward them. While
`access.api-keys` is set, the proxy also accepts Codex's access token as a
client credential (`codex-login` provider): an RS256 token from
`https://auth.openai.com`, checked against that issuer's JWKS, unexpired, for
the API audience, whose ChatGPT account and user match a Codex credential the
proxy holds. Usage records name it `codex-login:<auth id>`.

### Codex model, image and search calls (`/backend-api/codex/*`)

With `openai_base_url = "http://<proxy>/backend-api/codex"`, Codex keeps its
built-in OpenAI provider and sends model calls here with its `auth.json`
token. A custom `model_provider` turns off tools Codex offers only on that
provider, such as image generation.

| Route | Served by |
|---|---|
| `GET /responses` (websocket), `POST /responses`, `POST /responses/compact` | the Responses API, as `/v1/responses` |
| `POST /alpha/search` | standalone web search |
| `POST /images/generations`, `POST /images/edits` | the Images API, as `/v1/images/*` (`gpt-image-2` routes to Codex accounts) |

`GET /models` (Codex's model list) is fetched from chatgpt.com as a pooled
Codex account: the one new requests route to, or the one that recovers first
when all are spent. The client key and cookies are stripped. A `401` from
chatgpt.com is returned as `502`, and a client that disconnects first gets
`499`. Other `/backend-api/*` paths answer `404`.

## Claude Code connectivity check

`GET`/`HEAD /api/hello` answers `200 {"message":"hello"}` without a key, as
api.anthropic.com does. Claude Code probes it at startup.

Each `/usage/events` request includes `turn_key` (string or null). Codex uses `codex:<turn_id>` from X-Codex-Turn-Metadata or its JSON-string client_metadata counterpart. Claude messages use `claude:<prompt count>:<12 hex SHA256>` of the last user content without tool_result. Tool loops retain that key; unsupported requests report null.

`GET /credentials/models` preserves each model's upstream Unix-seconds `created` timestamp when positive; absent timestamps have no inferred ordering.


## Complete capture transport v2

Opt-in downstream capture requires `SPILLWAY_CAPTURE_DIR` and a persistent 32-byte hex `SPILLWAY_CAPTURE_KEY`; the supervisor supplies both. The proxy removes the key from its environment after reading it. The journal encrypts every event with AES-GCM and commits bounded batches synchronously to bbolt (already a dependency). Small HTTP writes coalesce; event batches flush at the memory threshold, every three seconds and before finalization/disable. Abrupt interruption can lose only the uncommitted suffix and is reported partial. It applies disk backpressure instead of dropping chunks under memory/concurrency pressure. No body/message-count cap applies. Headers, query strings, provider-hidden payloads and WebSocket control frames are excluded.

- `PUT /observability/captures/lease` with `{enabled,version:2}` returns `version: 2`, `leaseSeconds: 0`, `chunkBytes: 65536`. Recording continues until explicit disable; disabling preserves backlog and finalizes active streams partial. Storage configuration/recovery failure returns 503. Enabling without protocol 2 returns 409, so old clients cannot enable an unreadable journal.
- `GET /observability/captures?after=N` returns `{version:2,epoch,dropped:0,records,error?}` with at most four events. Epoch and sequence persist across restart. Each capture appends `event: start`, `data`, then `end`. Data chunks contain base64 `data`, direction, zero-based direction byte offset, and message frame/type/time/end metadata. Terminal counts and SHA-256 hashes cover concatenated raw bytes per direction. HTTP bodies remain wire encoded; WebSocket payloads are application bytes after negotiated frame decompression.
- `POST /observability/captures/ack` with `{epoch,sequence}` releases only events through a consumer's durable commit. Invalid epochs/future cursors return 400. GET never deletes content. Consumers must retry an acknowledgement after restart even if their next page is empty.
- `DELETE /observability/captures` is explicit user Clear: stops active recording, removes backlog and rotates epoch. It is separate from disable and never runs automatically.

Journal polling and recording renewal retry failed terminal commits without requiring restart. Restart appends partial terminal events for unfinished streams and retains their committed bytes. Storage failures are explicit and inference continues; failed capture must not be reported complete. Missing keys or corrupt journals fail recovery without replacing them. Reusable journal pages remain allocated after acknowledgement. Clients must store original bytes and metadata before acknowledgement and verify direction counts/hashes and message boundaries before calling a finalized capture complete.
