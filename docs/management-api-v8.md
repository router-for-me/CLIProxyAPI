# v8 Management API

The base path is `/v8/management`. Configuration endpoints follow the structure in
[config.example.yaml](../config.example.yaml); operational endpoints are grouped
by function. The existing `/v0/management` API remains supported. Its reference is
available in the [management API guide](https://help.router-for.me/management/api).

## Access

The v8 endpoints use the same management key and remote-access policy as v0.
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
file or adding omitted runtime defaults. A successful v8 configuration write
migrates legacy settings; reads and rejected writes leave the file unchanged.

JSON writes accept the value directly, without a `{ "value": ... }` envelope.
PUT replaces its target. PATCH merges objects and replaces lists and scalars.
DELETE removes a field. Paths identify mapping keys, not array indexes; replace
an entire list to change its entries. Optional per-key `null` overrides inherit
the corresponding group value. Legacy field names are rejected by v8 writes.

| Example path | Value or purpose |
| --- | --- |
| `/config/access/api-keys` | Client authentication keys, for example `["client-key"]`. |
| `/config/api-keys` | All upstream provider groups. |
| `/config/api-keys/codex` | Codex upstream groups. |
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

## Operational endpoints

All paths below are relative to `/v8/management`. Request and response bodies
retain the corresponding business operation's fields.

| Path | Methods | Description |
| --- | --- | --- |
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
| `/credentials/models` | GET | Get credential models. |
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

## OAuth

The login URL is shared by all providers. Set the required `provider` query
parameter to `claude`, `codex`, `antigravity`, `kimi`, `kimi-ai`, `xai`, `devin`,
`meta`, or a registered plugin provider ID. For example:

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
precedence and its legacy equivalent is removed. Individual v0 setters can
update migrated fields. `PUT /v0/management/config.yaml` still replaces the
complete file and accepts legacy, new, or mixed layouts.

Plugin OAuth uses the shared v8 login endpoint. Other plugin-defined HTTP
extensions retain their declared `/v0/management` routes.
