# v8 Management API

The base path is `/v8/management`. Configuration endpoints follow the structure in
[config.example.yaml](../config.example.yaml); operational endpoints are grouped
by function. The existing `/v0/management` API remains supported. Its reference is
available in the [management API guide](https://help.router-for.me/management/api).

## Access

The v8 endpoints use the same management key and remote-access policy as v0.
When [multi-user mode](multi-user.md) is enabled, an administrator's account session
also authenticates v8 management requests. Members use `/v8/hub`, not management
endpoints. Shared management-key operations are audited as `management-key`.
Send `Authorization: Bearer <management-key>` or `X-Management-Key: <management-key>`.
OAuth callbacks validate a pending login state and do not require a management-key
header. Home mode disables local management endpoints for both versions.

## Configuration endpoints

All paths below are relative to `/v8/management`.

| Path | Methods | Description |
| --- | --- | --- |
| `/config` | GET, PUT, PATCH | Read, replace, or merge the complete configuration as JSON. |
| `/config.yaml` | GET, PUT | Read or replace the complete configuration as YAML. |
| `/config/<section>/<field>` | GET, PUT, PATCH, DELETE | Read or modify a section or field using its path in the v8 tree. |

GET renders the persisted configuration in the v8 layout without migrating the
file or adding omitted runtime defaults. Successful v8 configuration writes save
the latest layout; reads and rejected writes leave the file unchanged.
Historical v8 paths and request bodies remain accepted as aliases.

JSON writes accept the value directly, without a `{ "value": ... }` envelope.
PUT replaces its target. PATCH merges objects and replaces lists and scalars.
Scalar field updates retain existing comments; complete replacements use the submitted document.
DELETE removes a field. Paths identify mapping keys, not array indexes; replace
an entire list to change its entries. Optional per-key `null` overrides inherit
the corresponding group value. Legacy field names are rejected by v8 writes.

| Example path | Value or purpose |
| --- | --- |
| `/config/access/api-keys` | Client authentication keys, for example `["client-key"]`. |
| `/config/api-keys` | All upstream provider groups. |
| `/config/api-keys/codex` | Codex upstream groups. |
| `/config/client/codex/optimize-multi-agent-v2` | Boolean, default `false`; applies to Codex clients across OAuth and API-key routes. |
| `/config/observability/logs/debug` | A boolean, for example `true`. |
| `/config/routing/retry/request-retry` | A number, for example `0`. |
| `/config/plugins/configs/<id>` | A plugin configuration object. |

For example, `PATCH /v8/management/config` with this body disables additional
request retries while preserving other settings:

```json
{
  "routing": {
    "retry": {
      "request-retry": 0
    }
  }
}
```

JSON reads omit TURN usernames and credentials under
`oauth.providers.codex.live-media-relay.ice-servers`; YAML reads include them.
The Home-owned revision fields
`credentials.concurrency.lifecycle-config-revision`,
`credentials.concurrency.observation-barrier-revision`, and `plugins.auth-revision`
cannot be changed through these endpoints.

### Codex multi-agent configuration migration

`client.codex.optimize-multi-agent-v2` is the sole runtime setting. Loading older
YAML accepts `providers.codex.optimize-multi-agent-v2`,
`oauth.providers.codex.optimize-multi-agent-v2`, and the flat
`codex.optimize-multi-agent-v2` path. The client path always wins conflicts,
including explicit `false` or `null`. If it is absent, the historical OAuth path
wins over `providers`, which wins over the flat path.

These aliases now have client-wide semantics, not OAuth-only scope. Loading does
not rewrite legacy-only settings; normalization removes aliases conflicting with
the client path. A successful v8 write migrates the aliases while preserving their
comments. Historical API paths remain usable. Home configuration publishers
need matching client schema support before emitting the new path; older YAML
payloads remain readable.

## Operational endpoints

All paths below are relative to `/v8/management`. Request and response bodies
retain the corresponding business operation's fields.

| Path | Methods | Description |
| --- | --- | --- |
| `/config/upstream/<provider>` | GET, PUT, PATCH, DELETE | Manage shared provider settings. |
| `/server/latest-version` | GET | Get latest release information. |
| `/requests/api-call` | POST | Make an authenticated upstream call. |
| `/routing/cooldown/reset` | POST | Clear credential cooldown. |
| `/routing/model-definitions/<channel>` | GET | Get model definitions. |
| `/observability/logs` | GET, DELETE | Read or clear application logs. |
| `/observability/logs/errors` | GET | List error-log files. |
| `/observability/logs/errors/<name>` | GET | Download an error-log file. |
| `/observability/logs/requests/<id>` | GET | Get a request log. |
| `/observability/usage/api-keys` | GET | Get API-key usage. |
| `/observability/usage/queue` | GET | Get queued usage events. |
| `/credentials` | GET, POST, DELETE | List, upload, or delete credential files. |
| `/credentials/models` | GET | Get currently registered credential models. |
| `/credentials/model-management?name=<file-or-id>` | GET, PUT, PATCH, DELETE | Read, replace, merge, or reset a credential's model configuration. |
| `/credentials/models/discover?name=<file-or-id>` | POST | Refresh the credential's model source without replacing saved configuration. |
| `/credentials/download` | GET | Download a credential file. |
| `/credentials/status` | PATCH | Change credential status. |
| `/credentials/fields` | PATCH | Change credential fields. |
| `/credentials/refresh` | POST | Refresh credentials. |
| `/oauth/import?provider=vertex` | POST | Import a Vertex service account using a multipart `file` upload. |
| `/oauth/auth-url?provider=<provider>` | GET | Start built-in or plugin OAuth. |
| `/oauth/status?state=<state>` | GET | Get login status. |
| `/oauth/session?state=<state>` | DELETE | Cancel a login session. |
| `/oauth/callback` | GET, POST | Submit an OAuth callback. |
| `/plugins` | GET | List installed plugins. |
| `/plugins/<id>` | DELETE | Delete a plugin. |
| `/plugins/store` | GET | List the plugin store. |
| `/plugins/store/<id>/install` | POST | Install or update a plugin. |
| `/plugins/<id>/quota` | GET, POST, DELETE | Read, fetch, or reset plugin quota. |
| `/branding` | PUT | Replace site branding shown in the management panel. |

## Credential model management

The management endpoints above select a file-backed credential by `name`; optional
`auth_index` disambiguates the lookup. In multi-user mode, the same operations are
available at `/v8/hub/credentials/:route_id/models` (GET, PUT, PATCH, DELETE) and
`/v8/hub/credentials/:route_id/models/discover` (POST). Use the credential summary's
`route_id`, not its filename. Hub operations require credential owner, channel
manager, or administrator access. Configuration-backed API keys, AI provider
records, virtual plugin credentials, and Home-managed credentials are not supported
by these endpoints.

GET and successful writes return:

- `models`: editable `{name, alias?, ...capabilities}` entries. `name` is the
  upstream ID; `alias`, when set, is the callable base ID. Neither contains the
  credential routing prefix. Without a saved override, entries derive from the
  current catalog and existing per-credential/global aliases.
- `configured`: whether the credential has an explicit `models` configuration.
- `available_models`: source definitions with capability metadata, independent of
  the saved selection and publication allowlist. Codex uses the credential's plan
  catalog. Antigravity uses the verified account/catalog intersection.
- `catalog_available`: whether a source catalog is known. `false` differs from an
  authoritative empty account catalog; both have `available_models: []`.
- `registered_models`: currently registered `{id, name}` entries. `id` is the
  callable routing ID and `name` the upstream ID; publication, exclusions, and
  disabled state still apply.
- `source`: `{kind, auto_refresh, refresh_interval_seconds, supports_refresh,
  upstream_discovery}`. Configured source URLs and credential tokens are omitted.
- `enabled_models`, `excluded_models`: existing publication/exclusion policy,
  returned for context but not edited by this endpoint.
- `capabilities`: booleans for `manual_models`, `aliases`, `thinking`,
  `max_context_length`, `modalities`, `is_compat`,
  `support_configuration_update`, `merge`, and `reset`.

PUT replaces the entire model selection. For example:

```json
{
  "models": [
    {
      "name": "gpt-5",
      "alias": "my-model",
      "max-context-length": 200000,
      "thinking": {"levels": ["low", "medium", "high"]},
      "is-compat": false,
      "support-configuration-update": false,
      "input-modalities": ["text", "image"],
      "output-modalities": ["text"]
    },
    {"name": "new-upstream-model"}
  ]
}
```

Optional fields inherit source capabilities when omitted or `null`; explicit
`false`, empty modality arrays, and an empty `thinking` object are overrides.
`max-context-length` must be positive. `thinking` uses the registry JSON shape:
`min`, `max`, `levels`, `zero_allowed`, and `dynamic_allowed`. Modalities accept
`text`, `image`, `audio`, `video`, and `document`. Levels and modalities are
normalized and deduplicated. `support-configuration-update` is Codex-only. Manual
entries are not proof of upstream availability. Antigravity advertises
`manual_models: false`: entries absent from its verified source catalog cannot be
registered, even if saved for later availability.

An explicit `{"models":[]}` publishes no models. DELETE removes the explicit list
and restores catalog-derived models and existing aliases. It does not clear
`enabled_models`, `excluded_models`, or other credential settings. An explicit
list takes precedence over legacy `model_aliases` and global aliases; those alias
settings become effective again after DELETE. Configuration is persisted in the
credential's `models` metadata and reapplied on model registration and file reload.
Aliases retain the existing upstream-routing and response-model rewriting behavior.

PATCH supports batch upsert and removal:

```json
{
  "models": [{"name": "gpt-5", "alias": "second-name"}],
  "remove": ["my-model"]
}
```

Entries are keyed by the case-insensitive public name (`alias`, otherwise `name`).
Removal runs before upsert. An upsert replaces the entire matching entry, so omitted
capabilities inherit again; it is not a field-level merge. Other entries and their
order are preserved. To rename an alias, remove its old public name and upsert the
new entry. A removal-only PATCH is supported. Without an explicit list, PATCH
starts from the current default entries; an unknown account catalog returns 503
instead of silently treating the defaults as empty. PUT requires `models` and does
not accept removals. Unknown fields, duplicate public names, `models: null`, and
invalid capabilities return 400 without changing configuration.

### Model source refresh

POST discovery refreshes the active source and returns the same payload. It never
adds, removes, or replaces saved model entries:

| `source.kind` | Meaning |
| --- | --- |
| `remote_catalog` | Project-maintained remote directory, not an account-specific upstream `/models` call. |
| `file_catalog` | Configured local directory file, reread on refresh. |
| `builtin` | Embedded or code-defined models, including ZCode; no remote refresh. |
| `disabled` | Shared directory refresh disabled by the active configuration. |
| `account_upstream` | Antigravity account discovery, including account-specific capabilities. |

Shared remote/file catalogs refresh every three hours and notify affected
credentials when definitions change. A manual refresh uses the same publication
path and configured source. The default `--local-model` policy remains effective;
an explicitly configured source may override it. Refreshing a shared catalog can
update other credentials using that catalog, not only the selected credential.
Antigravity reuses its existing three-hour cache, in-flight deduplication, retry
backoff, and stale-result guards; manual discovery expires the success cache but
does not bypass failure backoff. A successful authoritative empty account catalog
withdraws its registered models. Failed refreshes retain the last valid catalog.

Unsupported operations or non-refreshable sources return 409. Refresh failures
return 502, and unavailable catalogs required by the operation return 503. Existing
`GET /credentials/models` and `/v0/management` contracts are unchanged. Existing
alias-only dialogs keep their current endpoint; the expanded operations remain
backend-only until a client adopts these new endpoints.

## Branding

`PUT /v8/management/branding` replaces the `branding` configuration section in
one validated write. The body carries all three fields; empty values restore
the default product branding.

```json
{
  "site-name": "My Site",
  "site-subtitle": "Gateway for my team",
  "site-logo": "data:image/png;base64,..."
}
```

`site-name` is limited to 100 characters, `site-subtitle` to 200, and
`site-logo` must be a `data:image/` or `http(s)` URL under 4MB. The public,
unauthenticated `GET /v8/branding` endpoint returns the same fields so the
login page can render custom branding before signing in.

## OAuth

The login URL is shared by all providers. Set the required `provider` query
parameter to `claude`, `codex`, `antigravity`, `kimi`, `kimi-ai`, `qoder`,
`qoder-cn`, `xai`, `devin`, `meta`, or a registered plugin provider ID. For example:

```http
GET /v8/management/oauth/auth-url?provider=codex
Authorization: Bearer <management-key>
```

Keep any provider-specific login parameters in the query string. The login
response includes the authorization URL and a session `state`. Use that state
for status queries and cancellation.

Callbacks accept `provider`, `state`, `code`, and `error` as GET query parameters
or POST JSON fields. POST also accepts `redirect_url` containing the complete
callback URL. If `provider` is omitted, it is inferred from the pending state;
an explicit provider must match that state. Poll until the status is `ok` or
`error`; `wait` means the login is still pending. Callback acceptance alone does
not mean credential exchange and persistence have completed.

Import uses `POST /v8/management/oauth/import?provider=vertex`. The `provider`
query parameter is required; Vertex is currently the supported import provider.

## v0 compatibility

The `/v0/management` endpoints keep their original paths and payloads, including
provider-specific OAuth login URLs, callbacks, status queries, and cancellation.
For example, `GET /v0/management/codex-auth-url` and
`GET /v8/management/oauth/auth-url?provider=codex` start the same login flow.

Legacy flat configuration endpoints such as `/debug`, `/request-retry`, and
`/codex-api-key` exist under `/v0/management` only.
`/v0/management/api-keys` continues to manage client authentication keys; the v8
equivalent is `/v8/management/config/access/api-keys`.

Legacy-only configuration files keep their layout until a successful v8
configuration write. When both layouts specify a field, the new field takes
precedence and its legacy equivalent is removed. V0 setters keep legacy-only
files in their original layout; existing v8 files are saved in the latest v8
layout. `PUT /v0/management/config.yaml` still replaces the complete file and
accepts legacy, new, or mixed layouts, normalizing v8 documents on save.

Plugin OAuth uses the shared v8 login endpoint. Other plugin-defined HTTP
extensions retain their declared `/v0/management` routes.
