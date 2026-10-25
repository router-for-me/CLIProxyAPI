# Invite-only subscription sharing

The original CLI Proxy API Management Center at `/management.html` now includes
accounts, multiple personal API keys, owned channels, a model marketplace,
persistent usage records and an operation journal. Its existing login shell,
sidebar, themes, credential cards, details drawer, OAuth pages and shared UI
components are reused. `/hub.html` redirects to the same frontend.
Members manage their own credentials; administrators manage all credentials and
issue invitations. The usage page shows the signed-in member's own call records
by default, with a scope switch for calls made through their credentials (and
site-wide records for administrators). Site-wide totals live on the dashboard.
Audit records stay visible to every signed-in member, but members cannot
retrieve other members' keys or upstream tokens.

## Enable

Add this top-level section to your existing configuration and restart the server:

```yaml
multi-user:
  enabled: true
  data-dir: "./hub-data"
  session-ttl-hours: 168
```

The feature is disabled by default. `data-dir` is relative to the configuration
file directory; when omitted it defaults to `<oauth.auth-dir>-hub`. It must be
outside `oauth.auth-dir`: credential loaders recursively inspect JSON files.
Account settings require a restart, not hot reload. Account and journal files
remain local even when the credential store uses Postgres, Git, or object storage.
Use one server process per account data directory. This is not a Home worker feature.

1. Visit `/management.html`. The first registered account becomes administrator without
   an invitation. Complete this setup before sharing the site with friends.
2. Administrators generate invitation codes with an expiry and a use count.
3. A member registers, logs in, and creates one or more personal keys. Keys are
   named independently (for example, phone and laptop). The full secret is stored
   with the key, so its owner can copy it from the key list at any time; keys
   created before this behavior have no recoverable secret and keep only a display
   hint.
4. Create a channel, such as `573`, then upload a CLIProxyAPI
   credential JSON into it, or use the channel's OAuth login action. Existing
   OAuth providers and refresh behavior are reused. JSON identifies the provider
   with `type`, as in existing CLIProxyAPI auth files; recognized
   [Kiro account-manager exports](kiro.md#login-and-import) can omit it.
   This is not a generic token-paste or provider-key configuration editor. Members can also mount AI providers: an
   OpenAI-compatible upstream defined by a base URL, an API key, an optional proxy
   URL, and a model list (fetched from the upstream `/models` or edited manually).
   Member-supplied upstream URLs are contacted by the server process; run this
   feature only on a trusted, invite-only site.
5. Find a model in the marketplace. The marketplace groups identical base model
   IDs into one card and lists every callable model ID inside it, with channel
   ownership, availability, context length and capability details per entry.
   Copy the complete pinned model ID and configure your client:

   ```text
   Base URL: https://your-site/v1
   API key:  sk-hub-...
   Model:    573-kimi#1/<model-listed-in-the-marketplace>
   ```

A username is the account's only name, used for login and display. It contains
3–32 lowercase letters, digits or underscores, starts with a letter, and cannot
change after registration. A channel identifier (`slug`) is the channel's only
name. It contains 1–40 lowercase letters, digits or hyphens, cannot start with a
hyphen, is unique across the whole site, and cannot change after creation because
model IDs depend on it. There are no separate account display names or channel
names. Existing usernames, identifiers and pinned model IDs remain unchanged.

## Kiro subscriptions

Kiro uses the existing channel OAuth, credential model editor, quota cards and
marketplace. Authentication, native credential import, model-discovery limits and
verification boundaries are documented in [Kiro subscription credentials](kiro.md).

## ZCode OAuth

Select **ZCode · GLM Coding Plan** on the OAuth page after choosing a channel.
Open the authorization link and sign in to BigModel within five minutes. The
server polls the official ZCode authorization flow; no local callback port or
pasted callback URL is required. Cancellation stops the pending login.

This integration uses a **personal GLM Coding Plan** only, not ZCode Start Plan,
team subscriptions, or international Z.ai accounts. It reuses or creates the
personal project's `zcode-api-key` and obtains a short-lived project access token.
The account login token is stored separately and is never sent to inference.
Project tokens are refreshed before expiry; an expired account login requires
another OAuth login. Credential ownership and channel prefixes follow the same
rules as other providers.

The model catalog contains `glm-5.3`, `glm-5.3-flash`, `glm-5.2`, and `glm-5-turbo`.
Requests use the OpenAI-compatible Coding Plan endpoint, with the existing
translation, streaming, usage accounting and payload configuration pipeline.
The quota page and credential cards query `bigmodel.cn/api/monitor/usage/quota/limit`
through the existing API-call proxy. Project tokens are refreshed before queries
and attached server-side without a `Bearer` prefix. The cards reuse the Codex
quota body, labels, manual-reset count and expiry display; subscription metadata
is not queried or displayed. The timeline projects fixed five-hour/weekly windows
and shows reset-card expiries, not calendar-month tool limits.

Manual reset cards use `zcode.z.ai/api/v1/coding-plan/reset/status` and `/use`.
The server attaches the separately stored ZCode JWT and BigModel business token.
The existing reset button and confirmation are shared with Codex; a selector
chooses five-hour or weekly cards, and confirmation consumes one card then reloads
quota. Requests use an idempotency key. Existing credentials without the ZCode JWT
must be authorized again before reset cards can be queried or consumed. Failed
reset-card queries do not erase valid quota data. No reset-opportunity requests or
history writes are triggered automatically.

CLI login is available with `--zcode-login` (and optional `--no-browser`), using the
same OAuth flow and token store. Protocol behavior is based on the official ZCode
3.14.5 client. Verification uses mock responses; real-account authorization,
inference, quota and reset-card consumption remain unverified.

References: [official setup guide](https://docs.bigmodel.cn/cn/coding-plan/tool/zcode)
and [official built-in model catalog](https://cdn-zcode.z.ai/zcode/config/zcode-builtin-23.json).

## Model naming

Model IDs contain no username; they are `channel-source/model`:

- Credentials: `{channel-slug}-{credential-type}#{index}/{model}`, for example
  `573-kimi#1/kimi-k3` for the first Kimi credential in channel `573` and
  `573-kimi#2/kimi-k3` for the second. The index is allocated per channel and
  credential type, increases monotonically, and is never reused.
- AI providers: `{channel-slug}-{provider-name}/{model}`, for example
  `573-daye/kimi-k3`. The provider name is chosen at creation (slug format, no
  `#`) and is immutable afterwards.

Upstream model names and public aliases may contain `/`, for example
`573-provider/vanchin/deepseek-v4.1-flash`. Only a registered credential/provider
prefix before the first `/` pins a request; the remaining model ID is preserved
verbatim. With even load balancing enabled, `vanchin/deepseek-v4.1-flash` is a bare
model name and participates in the same rotation, failover and key exclusions as
other bare names. A registered prefix takes precedence over an upstream namespace
with the same spelling, including when that credential is disabled.

This is a breaking rename: model IDs from before this scheme (including any
`username-slug` prefixed IDs) stop resolving after upgrade, so update client
configurations. On startup the server migrates stored channels and credentials
to pinned prefixes once, logging any channel slug collisions it resolves by
keeping the original composite prefix for the later channel.

## Frontend source and build

`frontend/` vendors the official [Management Center](https://github.com/router-for-me/Cli-Proxy-API-Management-Center)
source at revision `ee79a794526a30c03748a8864a9ac6589a31833b`, with its MIT license,
existing dependencies and Bun lockfile. There is no separate custom-designed hub UI.

```bash
bun install --cwd frontend --frozen-lockfile
bun run --cwd frontend verify
go build -o cli-proxy-api ./cmd/server
```

The frontend build produces its original single-file `dist/index.html` and copies
it to `internal/multiuser/panel/management.html` for Go embedding. Rebuild the
frontend before building Go after UI changes. Never edit the generated bundle.
Multi-user mode serves this bundled frontend rather than downloading an upstream
asset that does not have account integration. Single-user mode preserves the
original downloaded management panel. No existing local asset is overwritten.

Administrators retain the original provider, quota, config and diagnostic pages.
Members see account-sharing pages and the original owner-scoped credential/OAuth
workflows. The credential details drawer still edits proxy, headers, scheduling
weight and supported policy fields; ownership and channel prefixes cannot be
changed there. Download and refresh operations remain owner/admin-only. Batch
operations apply only to selectable, manageable credentials.

## Channels and routing

A member can create several channels, each with several credential files and AI
providers. A channel is owned by one member. Its published model IDs always have
the pinned source prefix described above. Only credentials in that channel can
serve those models; round-robin and existing cooldown behavior still operate
within the eligible credential pool.

A channel can be marked **public**: any signed-in member may then upload
credentials, mount providers, edit the announcement and moderate the message
board on it. The `public` flag itself remains owner/admin-only, so a public
channel cannot be locked back to private by a member. Channel ownership is never
transferred by content edits: the credential `owner` stays the channel owner for
permission checks, while `created_by` records the member who actually mounted
each credential or provider.

Each channel has a detail page with its mounted credentials and providers (with
the mounting member), a plain-text announcement editable by channel managers,
and a plain-text message board where every member can post to coordinate usage
and allocation. Announcements are capped at 5000 characters, board messages at
2000 characters, and each channel retains at most 200 messages with the oldest
pruned on insert. Messages can be deleted by their author or a channel manager;
they cannot be edited.

A key can opt into **even load balancing** (`auto_balance`, toggled per key in the
console). Such keys may call a bare base model name directly, for example
`kimi-k3` instead of `573-kimi#1/kimi-k3`. Each request is spread round-robin
across eligible published credentials serving that model across all channels.
Each base model has its own round-robin position; interleaved requests for other
models do not advance it, and thinking suffixes share their base model's rotation.
The local dispatcher tries the remaining credentials after any upstream failure
and returns the last upstream error only after exhausting the available routes.
Request-fault classifications, request-scoped `stop` actions and
`max-retry-credentials` do not truncate this traversal; credential health and
cooldown bookkeeping still apply. `request-retry` controls additional rounds.
For streams, empty startup events are buffered and discarded on failover, including
`response.created`, role-only Chat chunks and empty Messages starts. Actual output,
tool activity, terminal results or unrecognized events commit the stream, after
which failures are forwarded without replay. Cancellation stops immediately, and
prefixed model IDs or explicitly pinned credentials retain their existing routing.
Chat, Messages, Gemini and Responses requests plus the image endpoints
(`/v1/images/generations`, `/v1/images/edits`; see [Image API](image-api.md))
follow this routing. Model-list endpoints include deduplicated bare model names
alongside pinned IDs only for keys with `auto_balance` enabled. A bare name is
included only when at least one published, non-disabled route remains available
to that key. For keys without the flag, bare model IDs and automatic cross-channel
selection are not supported, independently of
`routing.force-model-prefix`. In multi-user mode the legacy `access.api-keys`
list does not authenticate proxy callers: use personal keys created in the
console. Web login sessions are not proxy API keys.
Plugin model routers do not reroute calls in this mode: a selected channel cannot
silently become another provider or an external plugin-owned execution route.

Keys expose a `blocked_models` string array in `GET /v8/hub/keys`. Update it with
`PATCH /v8/hub/keys/:id`; the supplied array replaces the entire blocklist, `[]`
clears it, and omitting the field leaves it unchanged. A complete pinned ID
blocks only that source/model route; a bare model name blocks that model across
all sources. Entries are trimmed and deduplicated; blank entries are rejected.
Unavailable or withdrawn model IDs may still be stored so exclusions survive
later publication. Thinking suffixes do not bypass exclusions.

Blocked routes are removed from proxy model lists and from credential selection,
including streaming, token counting, image calls, retries and even load
balancing. For example, excluding both `123/deepseek` and `564/deepseek` removes
those pinned IDs and the bare `deepseek` entry when no other source serves it.
Excluding only one preserves the bare entry and routes bare calls exclusively to
the remaining sources. Explicitly blocked calls return HTTP 403 (`model_blocked`).
The marketplace remains the unfiltered source catalog for the key editor.

Publishing or withdrawing a channel updates its credential metadata and model
registrations. Individual credentials can be disabled or removed. An optional
`enabled_models` array selects which base model names a credential publishes;
omitting it publishes all discovered models. An empty array publishes none.
The backend also provides per-credential model selection, aliases, capability
overrides, batch upsert/removal, source refresh, and reset through
`/v8/hub/credentials/:route_id/models`. The complete request, response, inheritance,
and discovery contract is documented in
[Credential model management](management-api-v8.md#credential-model-management).
These operations preserve the pinned source prefix and never export credential
tokens. Managers receive `model_definitions`, `model_aliases`,
`model_management_supported`, and, when configured, `model_config` in credential
summaries. The current **Models** dialog remains alias-only and saves through
`/credentials/:route_id/fields`; adopting the expanded API is a separate frontend
task. With an explicit model list, its entries take precedence over those aliases.

Existing credential files are not silently reassigned. They remain unclaimed and
are not shared until an administrator explicitly assigns them to a channel.
Config-backed upstream API keys remain managed in server configuration and are
not claimable through the console. The feature does not edit `/v0/management`.

Realtime voice/WebRTC and `/v1/live` are explicitly unavailable in multi-user mode
(HTTP 501): those existing paths bypass channel model selection and do not expose
complete token accounting. Chat, Messages, Gemini and Responses use the normal
channel-aware execution pipeline. Responses WebSocket uses the same routing checks.

## What the logs mean

The console has two related records:

- **Client calls** record the final HTTP status, endpoint, requested model, caller,
  key name, and elapsed time, including failures before any upstream execution.
- **Usage records** describe each upstream attempt, with the caller, key identity,
  credential owner, channel, credential identity, model, streaming flag, status,
  input/output/cache/reasoning tokens, the translated reasoning effort, elapsed
  time, and trace/request IDs. Retries
  remain separate, so usage is attributed to the credential actually attempted.

Status 499 is displayed as **Request interrupted**: the caller interrupted the
request, not an upstream service failure. Codex HTTP response streams that close
before a successful terminal event are classified as 502, not a timeout. Once
streaming has begun, the HTTP status cannot change; Messages reports the failure
through an SSE `error` event with `api_error` rather than marking partial output complete.

Attribution is captured before execution, not resolved later from a mutable key
or credential record. Revoking a key or deleting a credential does not remove its
history. Usage plugins run asynchronously; a completed call may appear shortly
before its usage record. The journal remains independent of the existing
`usage-statistics-enabled` Redis queue toggle.

Summary call counts deduplicate trace IDs among matching upstream records. They
are not the count of all client calls; early failures are in the client-call table.
Model-side usage (for example, image-tool usage) can produce additional usage
records. Summary Token totals use the existing non-overlapping canonical token
breakdown where it is complete. Missing token information is shown as unknown,
not converted to an estimated charge.

**Subscription quota is separate from Token usage.** The credential page exposes
provider-supported passive quota signals and their observation timestamps, plus
per-model signals. It does not invent a remaining balance or assume a fixed
Token-to-subscription-message ratio. Providers with no quota observation are shown
as having no upstream data.

Audit entries contain actor, action, object, time, result status and client IP.
They never contain passwords, raw API keys, OAuth codes or credential payloads.
Account management traffic is excluded from request-body logging. Account session
operations are attributable; writes made with the old shared management password
are necessarily attributed to `management-key`, not a named person.

## Persistence and backup

`accounts.db` is an atomically replaced JSON state file containing users, bcrypt
password hashes, hashed sessions, key records, invitations, channel definitions
(including slug prefix, `public` flag, announcement, and per-credential-type
index counters) and channel board messages.
`journal/YYYY-MM-DD.jsonl` is the append-and-sync event journal. Both are under
`multi-user.data-dir`; back up the account file and journal together with upstream
auth files. History has no automatic expiration, and each event is still synced
before its append completes. Account writes publish their new in-memory snapshot
only after the atomic file replacement succeeds; authentication reads do not wait
for that disk I/O. Run only one proxy instance against a data directory.

`journal-index.sqlite` is a rebuildable query index, not a second source of truth.
It uses the pure-Go SQLite driver and WAL on a local filesystem. The first startup
imports existing history in background batches; subsequent starts resume committed
file offsets, and queries import only newly appended records. Import checkpoints
and indexed rows commit together. Changed or removed daily files invalidate their
indexed records. Malformed or incomplete records fail the import rather than
silently producing incomplete totals.

While the initial index is being prepared, history and performance endpoints return
HTTP 503 with `code: journal_index_building` and `Retry-After: 2`. The console retries
that specific state automatically and cancels retries when leaving the page. Other
pages and event appends do not wait for history construction. Lists read only the
selected page from the journal; summary and performance queries use the index, with
bounded, data-versioned aggregate caches (at most 32 results and 32,768 groups or
buckets). No history is sampled and request/retry and Token accounting stay unchanged.

The index and its `-wal`/`-shm` sidecars are not required in backups. To rebuild the
index, stop the proxy, remove only those derived files, and restart; never remove
`accounts.db` or the `journal/` directory for this operation. Large histories need
additional disk space and a one-time import before their first query is available.

To compare cold-index construction and indexed queries on 100,000 and 1,000,000
synthetic events, run `go test ./internal/multiuser -run '^$' -bench
'^BenchmarkJournalQueries$' -benchtime=1x -benchmem -v` on one line. Keep construction
cost separate from query latency and compare on an otherwise idle machine.

## HTTP API

The account API base is `/v8/hub`. Protected endpoints accept
`Authorization: Bearer <login-session-token>`.

| Path | Methods | Access / purpose |
| --- | --- | --- |
| `/status` | GET | Public; reports whether first-account setup is needed. |
| `/register`, `/login` | POST | Public; register accepts `username`, `password` and `invite` only. Invitation required except first account. |
| `/me` | GET | Read account identity; usernames are immutable. |
| `/me/password`, `/logout` | POST | Change own password / end own session. |
| `/keys`, `/keys/:id` | GET, POST / PATCH, DELETE | Personal keys; delete revokes and retains history. PATCH accepts `name`, `disabled`, `auto_balance` and `blocked_models`; see [Channels and routing](#channels-and-routing) for key policy semantics. |
| `/channels`, `/channels/:id` | GET, POST / PATCH | Create with `slug` and optional `public`. PATCH accepts only `published`, `announcement` (channel managers) and `public` (owner/admin only); the identifier is immutable. |
| `/channels/:id` | DELETE | Owner/admin removes an empty channel (409 while credentials/providers are still mounted); board messages are removed with it. |
| `/channels/:id/messages` | GET, POST | Channel message board; all members read/post, cursor paging via `before_id`/`limit`. |
| `/channels/:id/messages/:mid` | DELETE | Author or channel manager removes a board message. |
| `/providers` | POST | Mount an AI provider into a channel (`channel_id` query, body `name`/`base_url`/`api_key`/optional `proxy_url`/`models`). |
| `/providers/discover` | POST | Fetch an upstream `/models` list with the given base URL and key (15s timeout). |
| `/providers/:route_id` | PATCH | Edit label/base URL/models/proxy; empty `api_key` keeps the stored key. Delete reuses `/credentials/:route_id`. |
| `/credentials` | GET, POST | Public-to-members summaries; upload to an owned channel. |
| `/credentials/:route_id` | PATCH, DELETE | Owner/admin operations. |
| `/credentials/:route_id/download` | GET | Owner/admin credential export; never public. |
| `/credentials/:route_id/fields` | PATCH | Original details-editor settings, excluding owner/prefix/token fields. |
| `/credentials/:route_id/refresh` | POST | Original credential refresh, without returning tokens. |
| `/credentials/:route_id/api-call` | POST | Member quota queries through provider-allowlisted endpoints; credential secrets are substituted server-side. See the quota lookup contract below. |
| `/credentials/:route_id/claim` | POST | Administrator assigns an unclaimed file to a channel. |
| `/marketplace` | GET | Published channel models with owner/provider information. |
| `/marketplace/performance` | GET | Trailing-24h per-model usage aggregates (requests, failures, latency/TTFT sums, output tokens, generation time) in hourly buckets, powering the marketplace performance dialog (TPS, avg latency, success rate, per-channel table, trends). |
| `/oauth/auth-url`, `/oauth/status` | GET | Start provider login for a channel / poll its state. |
| `/oauth/callback` | GET, POST | Submit an OAuth callback; validates pending state. |
| `/oauth/session` | DELETE | Cancel a pending OAuth flow. |
| `/users`, `/users/:username` | GET / PATCH | All-member directory / admin `role` or `disabled` changes only. |
| `/invites`, `/invites/:id` | GET, POST / DELETE | Administrator invitation issue/list/revoke. |
| `/usage`, `/usage/summary`, `/calls`, `/audit` | GET | All-member persistent usage and operation history. |

Quota lookup never requires credential export permission. The server resolves Kimi's
`.com`/`.ai` usage host from the live credential and substitutes Meta's persisted
DCA token for `$DCA_TOKEN$` only at `/muse-code/key`; ordinary `$TOKEN$` calls retain
their provider token behavior. Credential summaries include Antigravity project IDs
and parsed Codex account/subscription claims, not raw JWTs or credential secrets.
These rules also apply to management-mode quota queries; export permissions remain
unchanged. Antigravity quota and subscription queries use the server's automatically
updated Hub User-Agent; older client versions can omit Gemini or Gemini 4 Argon
quota groups. Each upstream group retains its own windows and remaining balance.

Credential `route_id` values are URL-safe base64 encodings of runtime credential
IDs, returned by `/credentials`; opaque IDs prevent path separators from becoming
route segments. Upload accepts multipart `file` or raw JSON with `name`, and a
required `channel_id` query parameter. Credential and marketplace summaries expose
the canonical channel identifier as `channel_slug`, not a separate `channel_name`.
Account and channel create/update requests reject removed name fields and
immutable identity fields. Model editor PATCH accepts `enabled_models` or
`all_models: true`. Clients select a channel through the model ID, not headers.

History filters: `consumer`, `owner`, `channel_id`, `credential_id`, `key_id`,
`model`, `from`, `to`, `page`, `page_size` (maximum 200). Dates are RFC3339; `from`
is inclusive and `to` is exclusive. Audit filters additionally include `actor`
and `action`. Owner/channel/credential filters apply to upstream usage, not the
client-call table, because early client errors have no serving credential.
The usage page matches partial input against dropdown candidates only; incomplete
model/Key input cannot be submitted. Key choices resolve to an exact `key_id`,
and `@username` choices resolve to `consumer` (all of that member's keys).
Model choices retain the existing requested-model matching; a trailing
`model(channel/model)` or `model（channel/model）` pins the serving channel for
bare-model calls. There is no fuzzy Key-name history query. Model candidates come
from a summary that ignores the applied `model` filter and pagination, so
narrowing the list never collapses the dropdown to the selected model.

Pagination uses one-based pages (maximum 1,000,000) and sizes from 1 to 200,
with a default size of 50. The usage page normalizes invalid URL parameters,
returns to the last valid page after a successful response reports fewer records,
and preserves the selected size when applying or clearing filters. Browser history
restores the applied filters. Page changes fetch entries without refetching summaries;
the header refresh reloads entries, statistics, and model candidates together.
Usage and audit pagination hide the previous page while loading and disable page
controls until the request settles. Local credential, quota, and marketplace lists
also synchronize their stored page after shrinkage, so later growth cannot restore
an obsolete page. Board history allows one earlier-message request at a time,
retains messages on failure, and ignores responses from a previous channel or connection.
