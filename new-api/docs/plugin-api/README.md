# Task plugin API v1

Task plugins are single-file synchronous ECMAScript modules. The plugin contract
is currently unreleased; [`v1.schema.json`](./v1.schema.json) and
[`v1.d.ts`](./v1.d.ts) are the authoritative v1 contract.

## Contract and lifecycle

Every plugin exports `meta`, `buildSubmitRequest`, `parseSubmitResponse`, and
`parseTaskResult`. A `per_task` plugin also exports `buildQueryRequest`; a
`batch` plugin exports `buildBatchQueryRequest` and `parseBatchResult`.
`meta.author.name` is required and `meta.author.url`, when present, must be an
absolute HTTP(S) URL. This is self-declared attribution; a future marketplace's
verified publisher identity is a separate host-owned record.
Plugins may declare authenticated vendor-native `meta.routes` and claim
host-owned names through `meta.protocols`. Submit and dynamic routes name a
`native` decoder and presenter; query routes name only a presenter. Protocol
bindings are registered once by the host registry, and protocol decoders receive
the host-parsed `body` union plus the pinned model. Shared protocol hooks are
synchronous transformations; Go owns connections and wire framing.

The host selects a channel, invokes the request-building hook, validates the
returned URL against the channel host, performs HTTP, and gives the decoded
response to the matching parse hook. It owns persistence, retries, polling,
billing, and settlement. Plugins only transform data and report usage facts.
See [v1.d.ts](./v1.d.ts) for signatures and
[v1.schema.json](./v1.schema.json) for machine-readable shapes.

Plugins that expose task outputs export `listArtifacts(task)` and
`buildContentRequest(ctx)` together. Artifacts are projected on explicit reads
from persisted `Task.Data`; they are never stored as a second source of truth.
The list contains only stable `key`, `type`, and optional `mimeType` fields.
The content hook receives the selected key, raw decoded task data, the explicit
private upstream task id, the producer plugin version, channel authentication,
and a safe client Range/conditional-header subset. Its URL and headers exist
only for that proxy request.

When a Responses observation reaches persisted `SUCCESS`, the host also runs
the pinned plugin's `listArtifacts` and injects a read-only
`ctx.artifacts[key] = {key, type, mimeType?, url}` map into `renderEvents` or
`renderFinal`. Each `url` is a long-lived host-signed capability URL, never the
provider URL from `Task.Data`. Nonterminal and failed tasks receive no artifact
map. Capability construction or rendering failure fails only that Responses
observation; it cannot change the task, billing settlement, or refunds.
The absolute URL uses `TaskPublicAddress`, falling back only to
`ServerAddress`; multi-node deployments must share the effective
`CRYPTO_SECRET`.
Dashboard artifact reads return each `content_url` (or the legacy
`legacy_content_url`) directly, without a temporary URL exchange. Capability
generation and verification are stateless and have no expiry; after
verification the host still loads the task, owner, and plugin needed to serve
the artifact. Rotating `CRYPTO_SECRET` invalidates issued URLs. The `access`
query is redacted before request logging.
Deployment boundaries and concurrency environment variables are documented in
[v1.md](./v1.md#generic-task-management-api).

The host requires `protocols.openai_video.render` to return a JSON object and
preserves provider extensions, including output URLs and metadata. Plugins may
return `task.data` to expose the latest persisted upstream snapshot. The host
still overwrites `id`, `object`, `model`, `status`, `progress`, `created_at`, and
`completed_at`, and removes legacy `task_id`. This is field-level projection,
not byte-for-byte HTTP forwarding: known private task IDs are replaced before
rendering, polling may redact inline video data, and retrieval reads the latest
saved snapshot rather than issuing a new upstream request. Artifact content
endpoints remain available for proxied downloads.

Provider-authenticated content URLs must use the channel base host or a
plugin-declared `meta.allowedHosts` entry. A public dynamic CDN URL may instead
set `credentialless: true`; the host then permits only GET/HEAD with no
plugin-supplied headers or body and applies SSRF checks to the initial URL and
every redirect.

Registry publication is generation-atomic. A request pins one plugin generation
for its full lifetime, while background polling may use a later active plugin
version. New versions must continue parsing responses for in-flight tasks.
Root administrators can inspect the local node with
`GET /api/plugin/task/runtime/status`. The response includes the node-local generation,
a deterministic revision of the active database overrides, the latest rebuild
outcome, and plugin-level compile or routing errors. Generation numbers are
local to a node; compare database revisions when diagnosing rollout lag between
nodes. If the database snapshot is temporarily unavailable, the endpoint keeps
serving node-local state and the last known revision with `database_error` set.

For live diagnosis, start the process with `DEBUG=true` and filter logs on
`task_plugin`. Plugin registry, routing, endpoint ownership, channel selection,
submit durability, polling adapters, and protocol observation emit safe
key/value lifecycle events. Request-context events carry the request id;
scheduled, background, and context-less work is labeled `SYSTEM`. Plugin
`console.log` output is also forwarded in DEBUG mode. Hook-time output is
prefixed with plugin key/version; module-initialization output may have an empty
identity during initial upload validation. Do not print credentials, headers,
request bodies, upstream payloads, or private URLs from plugin code; free-form
console output cannot be redacted by the host.

## Fixtures and dry runs

A fixture case is `{name?, hook, member?, args, expected?, expectedError?}`.
Keep deterministic cases for every exported hook, its main error branch, batch
behavior, renderers, usage, and content requests. Run a fixture locally with:

```sh
new-api plugin lint plugin.js
new-api plugin test plugin.js --fixture golden.json
```

Root administrators can open the plugin detail Sandbox tab, choose a hook, and
submit an `args` JSON array. `POST /api/plugin/task/:key/dryrun` compiles the
active database source or factory source in a temporary registry and invokes
only that synchronous function. Dry runs never execute a request descriptor and
therefore never contact an upstream service.

## Upload and release

Upload from the root-only task plugin page or `POST /api/plugin/task` with
`{"source":"...","remark":"..."}`. The server compiles the module, validates
v1 metadata and required exports, and rejects invalid source before saving it.
Use semantic plugin versions. Reusing a key/version with different source is
rejected; activate or roll back a stored version through the management page.

For a third-party platform, create a channel of type `Task Plugin`, select the
plugin key, provide an explicit base URL, and configure models. Clients may use
the plugin's declared native routes. The generic management surface remains
`POST /v1/tasks/:pluginKey`, `GET /v1/tasks/:taskId`, and
`GET /v1/tasks/:taskId/artifacts` plus
`GET|HEAD /v1/tasks/:taskId/artifacts/:key/content`.

## Security boundary

Plugins have no `fetch`, filesystem, `require`, imports, async functions, or
environment access. The host limits execution time, concurrency, input size,
allowed request hosts, and resolves OAuth credentials outside JavaScript.
Multipart files enter JavaScript only as opaque references.

This is not a hard memory-isolation boundary. A plugin sees data needed for the
current request and can influence an authenticated upstream request. Uploading a
plugin is an administrator-level trust decision equivalent to configuring a
channel credential. Review source and version diffs before activation. Never run
untrusted plugins merely because they compile.

Usage hooks may return facts such as seconds, resolution, or upstream units, but
must never calculate prices or attempt quota settlement. The host owns all
pricing and clamps billing conversions.
