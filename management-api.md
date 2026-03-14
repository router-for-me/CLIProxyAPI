---
outline: 'deep'
---

# Management API

Base path: `http://localhost:8317/v0/management`

This API manages the CLI Proxy API’s runtime configuration and authentication files. All changes are persisted to the YAML config file and hot‑reloaded by the service.

Note: The following options cannot be modified via API and must be set in the config file (restart if needed):
- `remote-management.allow-remote`
- `remote-management.secret-key` (if plaintext is detected at startup, it is automatically bcrypt‑hashed and written back to the config)

## Authentication

- All requests (including localhost) must provide a valid management key.
- Remote access requires enabling remote management in the config: `remote-management.allow-remote: true`.
- Provide the management key (in plaintext) via either:
    - `Authorization: Bearer <plaintext-key>`
    - `X-Management-Key: <plaintext-key>`

Additional notes:
- Setting the `MANAGEMENT_PASSWORD` environment variable registers an additional plaintext management secret and forces remote management to stay enabled even when `remote-management.allow-remote` is false. The value is never persisted and must be sent via the same `Authorization`/`X-Management-Key` headers.
- When the proxy starts with `cliproxy run --password <pwd>` or via the SDK’s `WithLocalManagementPassword`, localhost clients (`127.0.0.1`/`::1`) may present that local-only password through the same headers; it only lives in memory and is not written to disk.
- The Management API returns 404 only when both `remote-management.secret-key` is empty and `MANAGEMENT_PASSWORD` is unset.
- For remote IPs, 5 consecutive authentication failures trigger a temporary ban (~30 minutes) before further attempts are allowed.

If a plaintext key is detected in the config at startup, it will be bcrypt‑hashed and written back to the config file automatically.

## Request/Response Conventions

- Content-Type: `application/json` (unless otherwise noted).
- Boolean/int/string updates: request body is `{ "value": <type> }`.
- Array PUT: either a raw array (e.g. `["a","b"]`) or `{ "items": [ ... ] }`.
- Array PATCH: supports `{ "old": "k1", "new": "k2" }` or `{ "index": 0, "value": "k2" }`.
- Object-array PATCH: supports matching by index or by key field (specified per endpoint).

## Endpoints

### Usage Statistics
- GET `/usage` — Retrieve aggregated in-memory request metrics
    - Response:
      ```json
      {
        "usage": {
          "total_requests": 24,
          "success_count": 22,
          "failure_count": 2,
          "total_tokens": 13890,
          "requests_by_day": {
            "2024-05-20": 12
          },
          "requests_by_hour": {
            "09": 4,
            "18": 8
          },
          "tokens_by_day": {
            "2024-05-20": 9876
          },
          "tokens_by_hour": {
            "09": 1234,
            "18": 865
          },
          "apis": {
            "POST /v1/chat/completions": {
              "total_requests": 12,
              "total_tokens": 9021,
              "models": {
                "gpt-4o-mini": {
                  "total_requests": 8,
                  "total_tokens": 7123,
                  "details": [
                    {
                      "timestamp": "2024-05-20T09:15:04.123456Z",
                      "source": "openai",
                      "auth_index": "1a2b3c4d5e6f7a8b",
                      "failed": false,
                      "tokens": {
                        "input_tokens": 523,
                        "output_tokens": 308,
                        "reasoning_tokens": 0,
                        "cached_tokens": 0,
                        "total_tokens": 831
                      }
                    }
                  ]
                }
              }
            }
          }
        },
        "failed_requests": 2
      }
      ```
    - Notes:
        - Statistics are recalculated for every request that reports token usage; data resets when the server restarts.
        - Hourly counters fold all days into the same hour bucket (`00`–`23`).
        - The top-level `failed_requests` repeats `usage.failure_count` for convenience when polling.
        - `details` entries include `source`, `auth_index`, and `failed` for per-request metadata.

- GET `/usage/export` — Export a usage snapshot for backup/migration
    - Response:
      ```json
      {
        "version": 1,
        "exported_at": "2025-08-31T02:34:56Z",
        "usage": {
          "total_requests": 24,
          "success_count": 22,
          "failure_count": 2,
          "total_tokens": 13890,
          "requests_by_day": {
            "2024-05-20": 12
          },
          "requests_by_hour": {
            "09": 4,
            "18": 8
          },
          "tokens_by_day": {
            "2024-05-20": 9876
          },
          "tokens_by_hour": {
            "09": 1234,
            "18": 865
          },
          "apis": {}
        }
      }
      ```
    - Notes:
        - `usage` has the same shape as `GET /usage`.
        - `version` is currently `1`.

- POST `/usage/import` — Merge a usage snapshot into memory
    - Request:
      ```bash
      curl -X POST -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d @usage-export.json \
        http://localhost:8317/v0/management/usage/import
      ```
    - Response:
      ```json
      { "added": 12, "skipped": 3, "total_requests": 100, "failed_requests": 4 }
      ```
    - Notes:
        - Accepts payloads from `/usage/export`; `version` may be `1` (or `0` for legacy exports).
        - Import merges with existing in-memory stats and skips duplicate request details.

### Config
- GET `/config` — Get the full config
    - Request:
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' http://localhost:8317/v0/management/config
      ```
    - Response:
      ```json
      {"debug":true,"proxy-url":"","api-keys":["1...5","JS...W"],"ampcode":{"upstream-url":"https://ampcode.com","restrict-management-to-localhost":true},"quota-exceeded":{"switch-project":true,"switch-preview-model":true},"gemini-api-key":[{"api-key":"AI...01","base-url":"https://generativelanguage.googleapis.com","headers":{"X-Custom-Header":"custom-value"},"proxy-url":"","excluded-models":["gemini-1.5-pro","gemini-1.5-flash"]},{"api-key":"AI...02","proxy-url":"socks5://proxy.example.com:1080","excluded-models":["gemini-pro-vision"]}],"request-log":true,"request-retry":3,"claude-api-key":[{"api-key":"cr...56","base-url":"https://example.com/api","proxy-url":"socks5://proxy.example.com:1080","models":[{"name":"claude-3-5-sonnet-20241022","alias":"claude-sonnet-latest"}],"excluded-models":["claude-3-opus"]},{"api-key":"cr...e3","base-url":"http://example.com:3000/api","proxy-url":""},{"api-key":"sk-...q2","base-url":"https://example.com","proxy-url":""}],"codex-api-key":[{"api-key":"sk...01","base-url":"https://example/v1","proxy-url":"","excluded-models":["gpt-4o-mini"]}],"openai-compatibility":[{"name":"openrouter","base-url":"https://openrouter.ai/api/v1","api-key-entries":[{"api-key":"sk...01","proxy-url":""}],"models":[{"name":"moonshotai/kimi-k2:free","alias":"kimi-k2"}]},{"name":"iflow","base-url":"https://apis.iflow.cn/v1","api-key-entries":[{"api-key":"sk...7e","proxy-url":"socks5://proxy.example.com:1080"}],"models":[{"name":"deepseek-v3.1","alias":"deepseek-v3.1"},{"name":"glm-4.5","alias":"glm-4.5"},{"name":"kimi-k2","alias":"kimi-k2"}]}]}
      ```
    - Notes:
        - Legacy `generative-language-api-key` values are migrated into `gemini-api-key`; there is no standalone management endpoint for the legacy key.
        - When no configuration is loaded yet the handler returns `{}`.

### Latest Version
- GET `/latest-version` — Fetch the latest release version string (no asset download)
    - Request:
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        http://localhost:8317/v0/management/latest-version
      ```
    - Response:
      ```json
      { "latest-version": "v1.2.3" }
      ```
    - Notes:
        - Data is retrieved from `https://api.github.com/repos/router-for-me/CLIProxyAPI/releases/latest` with `User-Agent: CLIProxyAPI`.
        - When `proxy-url` is set, the request honors that proxy; the endpoint only returns the version value and does not download release assets.

### Debug
- GET `/debug` — Get the current debug state
    - Request:
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' http://localhost:8317/v0/management/debug
      ```
    - Response:
      ```json
      { "debug": false }
      ```
- PUT/PATCH `/debug` — Set debug (boolean)
    - Request:
      ```bash
      curl -X PUT -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '{"value":true}' \
        http://localhost:8317/v0/management/debug
      ```
    - Response:
      ```json
      { "status": "ok" }
      ```

### Config YAML
- GET `/config.yaml` — Download the persisted YAML file as-is
    - Response headers:
        - `Content-Type: application/yaml; charset=utf-8`
        - `Cache-Control: no-store`
    - Response body: raw YAML stream preserving comments/formatting.
- PUT `/config.yaml` — Replace the config with a YAML document
    - Request:
      ```bash
      curl -X PUT -H 'Content-Type: application/yaml' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        --data-binary @config.yaml \
        http://localhost:8317/v0/management/config.yaml
      ```
    - Response:
      ```json
      { "ok": true, "changed": ["config"] }
      ```
    - Notes:
        - The server validates the YAML by loading it before persisting; invalid configs return `422` with `{ "error": "invalid_config", "message": "..." }`.
        - Write failures return `500` with `{ "error": "write_failed", "message": "..." }`.

### Logging to File
- GET `/logging-to-file` — Check whether file logging is enabled
    - Response:
      ```json
      { "logging-to-file": true }
      ```
- PUT/PATCH `/logging-to-file` — Enable or disable file logging
    - Request:
      ```bash
      curl -X PATCH -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '{"value":false}' \
        http://localhost:8317/v0/management/logging-to-file
      ```
    - Response:
      ```json
      { "status": "ok" }
      ```

### Log File Size Limit
- GET `/logs-max-total-size-mb` — Get the total log size limit in MB
    - Response:
      ```json
      { "logs-max-total-size-mb": 0 }
      ```
- PUT/PATCH `/logs-max-total-size-mb` — Set the total log size limit in MB
    - Request:
      ```bash
      curl -X PATCH -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '{"value":512}' \
        http://localhost:8317/v0/management/logs-max-total-size-mb
      ```
    - Response:
      ```json
      { "status": "ok" }
      ```
    - Notes:
        - Values below `0` are clamped to `0` (disabled).

### Error Log File Count Limit
- GET `/error-logs-max-files` — Get the retained error-log file count limit
    - Response:
      ```json
      { "error-logs-max-files": 10 }
      ```
- PUT/PATCH `/error-logs-max-files` — Set the retained error-log file count limit
    - Request:
      ```bash
      curl -X PATCH -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '{"value":20}' \
        http://localhost:8317/v0/management/error-logs-max-files
      ```
    - Response:
      ```json
      { "status": "ok" }
      ```
    - Notes:
        - Values below `0` are reset to `10` (default).

### Log Files
- GET `/logs` — Stream recent log lines
    - Query params:
        - `after` (optional): Unix timestamp; only lines newer than this are returned.
        - `limit` (optional): Max number of lines to return after filtering.
    - Response:
      ```json
      {
        "lines": ["2024-05-20 12:00:00 info request accepted"],
        "line-count": 125,
        "latest-timestamp": 1716206400
      }
      ```
    - Notes:
        - Requires file logging to be enabled; otherwise returns `{ "error": "logging to file disabled" }` with `400`.
        - When no log file exists yet the response contains empty `lines` and `line-count: 0`.
        - `latest-timestamp` is the largest parsed timestamp from this batch; if no timestamp is found it echoes the provided `after` (or `0`), so clients can pass it back unchanged for incremental polling.
        - `line-count` reflects the total number of lines scanned (including those filtered out by `after`) and can be used to detect whether new log data arrived.
        - `limit` must be a positive integer; when set, only the last `limit` matching lines are returned.
- DELETE `/logs` — Remove rotated log files and truncate the active log
    - Response:
      ```json
      { "success": true, "message": "Logs cleared successfully", "removed": 3 }
      ```

### Request Error Logs
- GET `/request-error-logs` — List error request log files when request logging is disabled
    - Response:
      ```json
      {
        "files": [
          {
            "name": "error-2024-05-20.log",
            "size": 12345,
            "modified": 1716206400
          }
        ]
      }
      ```
    - Notes:
        - When `request-log` is enabled, this endpoint always returns an empty list.
        - Files are discovered under the same log directory and must start with `error-` and end with `.log`.
        - `modified` is the last modification time as a Unix timestamp.
- GET `/request-error-logs/:name` — Download a specific error request log
    - Request:
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -OJ 'http://localhost:8317/v0/management/request-error-logs/error-2024-05-20.log'
      ```
    - Notes:
        - `name` must be a safe filename (no `/` or `\`) and match an existing `error-*.log` entry; otherwise the server returns a validation or not-found error.
        - The handler performs a safety check to ensure the resolved path stays inside the log directory before streaming the file.

### Request Log By ID
- GET `/request-log-by-id/:id` — Download a request log by request ID
    - Request:
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -OJ 'http://localhost:8317/v0/management/request-log-by-id/a1b2c3d4'
      ```
    - Notes:
        - Matches any log file ending with `-<id>.log` under the log directory (including `error-*.log`).
        - `id` can also be passed as `?id=`; values must not contain `/` or `\`.

### Usage Statistics Toggle
- GET `/usage-statistics-enabled` — Check whether telemetry collection is active
    - Response:
      ```json
      { "usage-statistics-enabled": true }
      ```
- PUT/PATCH `/usage-statistics-enabled` — Enable or disable collection
    - Request:
      ```bash
      curl -X PUT -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '{"value":true}' \
        http://localhost:8317/v0/management/usage-statistics-enabled
      ```
    - Response:
      ```json
      { "status": "ok" }
      ```

### Proxy Server URL
- GET `/proxy-url` — Get the proxy URL string
    - Request:
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' http://localhost:8317/v0/management/proxy-url
      ```
    - Response:
      ```json
      { "proxy-url": "socks5://user:pass@127.0.0.1:1080/" }
      ```
- PUT/PATCH `/proxy-url` — Set the proxy URL string
    - Request (PUT):
      ```bash
      curl -X PUT -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '{"value":"socks5://user:pass@127.0.0.1:1080/"}' \
        http://localhost:8317/v0/management/proxy-url
      ```
    - Request (PATCH):
      ```bash
      curl -X PATCH -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '{"value":"http://127.0.0.1:8080"}' \
        http://localhost:8317/v0/management/proxy-url
      ```
    - Response:
      ```json
      { "status": "ok" }
      ```
- DELETE `/proxy-url` — Clear the proxy URL
    - Request:
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' -X DELETE http://localhost:8317/v0/management/proxy-url
      ```
    - Response:
      ```json
      { "status": "ok" }
      ```

### API Call
- POST `/api-call` — Make a generic HTTP request (optionally with credential token/proxy)
    - Request:
      ```json
      {
        "auth_index": "<AUTH_INDEX>",
        "method": "GET",
        "url": "https://api.example.com/v1/ping",
        "header": {
          "Authorization": "Bearer $TOKEN$",
          "Accept": "application/json"
        },
        "data": ""
      }
      ```
    - Response:
      ```json
      {
        "status_code": 200,
        "header": { "Content-Type": ["application/json"] },
        "body": "{\"ok\":true}"
      }
      ```
    - Notes:
        - `auth_index` can also be sent as `authIndex` or `AuthIndex`.
        - `method` and `url` are required; `url` must be absolute (scheme + host).
        - `$TOKEN$` is substituted using the selected credential in this order: `metadata.access_token`, `attributes.api_key`, `metadata.token` / `metadata.id_token` / `metadata.cookie`.
        - If `auth_index` resolves to a credential but no token is available, the API returns `400` with `{ "error": "auth token not found" }`. Refresh failures return `{ "error": "auth token refresh failed" }`.
        - For `gemini-cli` credentials, the server refreshes OAuth access tokens using stored refresh metadata before substitution and updates the credential metadata.
        - Set `header.Host` to override the upstream Host header (applied to `req.Host`).
        - Proxy selection: credential `proxy_url`, then global `proxy-url`, otherwise direct (environment proxies are ignored).
        - Requests time out after 60 seconds; upstream failures return `502` with `{ "error": "request failed" }`.

### Quota Exceeded Behavior
- GET `/quota-exceeded/switch-project`
    - Request:
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' http://localhost:8317/v0/management/quota-exceeded/switch-project
      ```
    - Response:
      ```json
      { "switch-project": true }
      ```
- PUT/PATCH `/quota-exceeded/switch-project` — Boolean
    - Request:
      ```bash
      curl -X PUT -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '{"value":false}' \
        http://localhost:8317/v0/management/quota-exceeded/switch-project
      ```
    - Response:
      ```json
      { "status": "ok" }
      ```
- GET `/quota-exceeded/switch-preview-model`
    - Request:
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' http://localhost:8317/v0/management/quota-exceeded/switch-preview-model
      ```
    - Response:
      ```json
      { "switch-preview-model": true }
      ```
- PUT/PATCH `/quota-exceeded/switch-preview-model` — Boolean
    - Request:
      ```bash
      curl -X PATCH -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '{"value":true}' \
        http://localhost:8317/v0/management/quota-exceeded/switch-preview-model
      ```
    - Response:
      ```json
      { "status": "ok" }
      ```

### API Keys (proxy service auth)
These endpoints update the inline `config-api-key` provider inside the `auth.providers` section of the configuration. Legacy top-level `api-keys` remain in sync automatically.
- GET `/api-keys` — Return the full list
    - Request:
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' http://localhost:8317/v0/management/api-keys
      ```
    - Response:
      ```json
      { "api-keys": ["k1","k2","k3"] }
      ```
- PUT `/api-keys` — Replace the full list
    - Request:
      ```bash
      curl -X PUT -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '["k1","k2","k3"]' \
        http://localhost:8317/v0/management/api-keys
      ```
    - Response:
      ```json
      { "status": "ok" }
      ```
- PATCH `/api-keys` — Modify one item (`old/new` or `index/value`)
    - Request (by old/new):
      ```bash
      curl -X PATCH -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '{"old":"k2","new":"k2b"}' \
        http://localhost:8317/v0/management/api-keys
      ```
    - Request (by index/value):
      ```bash
      curl -X PATCH -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '{"index":0,"value":"k1b"}' \
        http://localhost:8317/v0/management/api-keys
      ```
    - Response:
      ```json
      { "status": "ok" }
      ```
- DELETE `/api-keys` — Delete one (`?value=` or `?index=`)
    - Request (by value):
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' -X DELETE 'http://localhost:8317/v0/management/api-keys?value=k1'
      ```
    - Request (by index):
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' -X DELETE 'http://localhost:8317/v0/management/api-keys?index=0'
      ```
    - Response:
      ```json
      { "status": "ok" }
      ```

### Gemini API Key
- GET `/gemini-api-key`
    - Request:
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' http://localhost:8317/v0/management/gemini-api-key
      ```
    - Response:
      ```json
      {
        "gemini-api-key": [
          {"api-key":"AIzaSy...01","priority":10,"prefix":"team-a","base-url":"https://generativelanguage.googleapis.com","headers":{"X-Custom-Header":"custom-value"},"proxy-url":"","models":[{"name":"gemini-1.5-pro","alias":"pro-main"}],"excluded-models":["gemini-1.5-pro","gemini-1.5-flash"]},
          {"api-key":"AIzaSy...02","priority":0,"proxy-url":"socks5://proxy.example.com:1080","models":[{"name":"gemini-2.0-flash","alias":"flash-fast"}],"excluded-models":["gemini-pro-vision"]}
        ]
      }
      ```
- PUT `/gemini-api-key`
    - Request (array form):
      ```bash
      curl -X PUT -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '[{"api-key":"AIzaSy-1","priority":10,"prefix":"team-a","headers":{"X-Custom-Header":"vendor-value"},"models":[{"name":"gemini-1.5-pro","alias":"pro-main"}],"excluded-models":["gemini-1.5-flash"]},{"api-key":"AIzaSy-2","base-url":"https://custom.example.com","models":[{"name":"gemini-2.0-flash","alias":"flash-fast"}],"excluded-models":["gemini-pro-vision"]}]' \
        http://localhost:8317/v0/management/gemini-api-key
      ```
    - Request (`items` wrapper form):
      ```bash
      curl -X PUT -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '{"items":[{"api-key":"AIzaSy-1","priority":5},{"api-key":"AIzaSy-2","priority":1}]}' \
        http://localhost:8317/v0/management/gemini-api-key
      ```
    - Response:
      ```json
      { "status": "ok" }
      ```
- PATCH `/gemini-api-key`
    - Request (update by index):
      ```bash
      curl -X PATCH -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '{"index":0,"value":{"api-key":"AIzaSy-1","priority":20,"prefix":"team-a","base-url":"https://custom.example.com","headers":{"X-Custom-Header":"custom-value"},"proxy-url":"","models":[{"name":"gemini-1.5-pro","alias":"pro-main"}],"excluded-models":["gemini-1.5-pro","gemini-pro-vision"]}}' \
        http://localhost:8317/v0/management/gemini-api-key
      ```
    - Request (update by api-key match):
      ```bash
      curl -X PATCH -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '{"match":"AIzaSy-1","value":{"api-key":"AIzaSy-1","priority":15,"prefix":"team-a","headers":{"X-Custom-Header":"custom-value"},"proxy-url":"socks5://proxy.example.com:1080","models":[{"name":"gemini-2.0-flash","alias":"flash-fast"}],"excluded-models":["gemini-1.5-pro-latest"]}}' \
        http://localhost:8317/v0/management/gemini-api-key
      ```
    - Response:
      ```json
      { "status": "ok" }
      ```
- DELETE `/gemini-api-key`
    - Request (by api-key):
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' -X DELETE \
        'http://localhost:8317/v0/management/gemini-api-key?api-key=AIzaSy-1'
      ```
    - Request (by index):
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' -X DELETE \
        'http://localhost:8317/v0/management/gemini-api-key?index=0'
      ```
    - Response:
      ```json
      { "status": "ok" }
      ```
    - Notes:
        - `PUT` accepts both a raw array and `{ "items": [...] }`.
        - `value` supports: `api-key`, `priority`, `prefix`, `base-url`, `proxy-url`, `headers`, `models`, `excluded-models`.
        - `priority` is optional and defaults to `0`.
        - `models` is optional and uses objects like `{ "name": "upstream-model", "alias": "display-name" }`.
        - `prefix` is optional. When set, call models as `prefix/<model>` to target this credential. The stored prefix is trimmed and must be a single segment (no `/`).
        - `PATCH` accepts partial `value` fields; omitted fields keep existing values.
        - `PATCH` can locate an item by `index` (0-based) or `match` (`api-key` exact match after trim).
        - Setting `value.api-key` to an empty string in `PATCH` removes that entry.
        - `headers` is optional; keys/values are trimmed and blank entries are removed.
        - `excluded-models` is optional; the server lowercases, trims, deduplicates, and drops blank entries before saving.

### Codex API KEY (object array)
- GET `/codex-api-key` — List all
    - Request:
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' http://localhost:8317/v0/management/codex-api-key
      ```
    - Response:
      ```json
      { "codex-api-key": [ { "api-key": "sk-a", "priority": 10, "prefix": "team-a", "base-url": "https://codex.example.com/v1", "websockets": true, "proxy-url": "socks5://proxy.example.com:1080", "models": [ { "name": "gpt-4.1", "alias": "gpt-4.1-main" } ], "headers": { "X-Team": "cli" }, "excluded-models": ["gpt-4o-mini"] } ] }
      ```
- PUT `/codex-api-key` — Replace the list
    - Request:
      ```bash
      curl -X PUT -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '[{"api-key":"sk-a","priority":10,"prefix":"team-a","base-url":"https://codex.example.com/v1","websockets":true,"proxy-url":"socks5://proxy.example.com:1080","models":[{"name":"gpt-4.1","alias":"gpt-4.1-main"}],"headers":{"X-Team":"cli"},"excluded-models":["gpt-4o-mini","gpt-4.1-mini"]},{"api-key":"sk-b","priority":1,"base-url":"https://custom.example.com","websockets":false,"proxy-url":"","models":[{"name":"gpt-4o-mini","alias":"gpt-4o-mini"}],"headers":{"X-Env":"prod"},"excluded-models":["gpt-3.5-turbo"]}]' \
        http://localhost:8317/v0/management/codex-api-key
      ```
    - Request (`items` wrapper form):
      ```bash
      curl -X PUT -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '{"items":[{"api-key":"sk-a","base-url":"https://codex.example.com/v1","priority":5},{"api-key":"sk-b","base-url":"https://custom.example.com","priority":1}]}' \
        http://localhost:8317/v0/management/codex-api-key
      ```
    - Response:
      ```json
      { "status": "ok" }
      ```
- PATCH `/codex-api-key` — Modify one (by `index` or `match`)
    - Request (by index):
      ```bash
      curl -X PATCH -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '{"index":1,"value":{"api-key":"sk-b2","prefix":"team-b","base-url":"https://c.example.com","proxy-url":"","models":[{"name":"gpt-4.1-mini","alias":"gpt-4.1-mini"}],"headers":{"X-Env":"stage"},"excluded-models":["gpt-3.5-turbo-instruct"]}}' \
        http://localhost:8317/v0/management/codex-api-key
      ```
    - Request (by match):
      ```bash
      curl -X PATCH -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '{"match":"sk-a","value":{"api-key":"sk-a","prefix":"team-a","base-url":"https://codex.example.com/v1","proxy-url":"socks5://proxy.example.com:1080","models":[{"name":"gpt-4.1","alias":"gpt-4.1-main"}],"headers":{"X-Team":"cli"},"excluded-models":["gpt-4o-mini","gpt-4.1"]}}' \
        http://localhost:8317/v0/management/codex-api-key
      ```
    - Response:
      ```json
      { "status": "ok" }
      ```
- DELETE `/codex-api-key` — Delete one (`?api-key=` or `?index=`)
    - Request (by api-key):
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' -X DELETE 'http://localhost:8317/v0/management/codex-api-key?api-key=sk-b2'
      ```
    - Request (by index):
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' -X DELETE 'http://localhost:8317/v0/management/codex-api-key?index=0'
      ```
    - Response:
      ```json
      { "status": "ok" }
      ```
    - Notes:
        - `PUT` accepts both a raw array and `{ "items": [...] }`.
        - Entry fields include: `api-key`, `priority`, `prefix`, `base-url`, `websockets`, `proxy-url`, `models`, `headers`, `excluded-models`.
        - `PATCH value` supports only: `api-key`, `prefix`, `base-url`, `proxy-url`, `models`, `headers`, `excluded-models`.
        - `priority` and `websockets` can be set via `PUT` (not `PATCH`).
        - `priority` defaults to `0`; `websockets` defaults to `false`.
        - `prefix` is optional. When set, call models as `prefix/<model>` to target this credential. Stored prefixes are trimmed and must be a single segment (no `/`).
        - `PATCH` accepts partial `value` fields; omitted fields keep existing values.
        - `PATCH` can locate an item by `index` (0-based) or `match` (`api-key` exact match after trim).
        - `base-url` is required; entries with an empty `base-url` are removed. Sending `value.base-url: ""` in `PATCH` deletes the matched item.
        - `models` is replaced as a whole list when provided.
        - `headers` lets you attach custom HTTP headers per key. Empty keys/values are stripped automatically.
        - `excluded-models` accepts model identifiers to block for this provider; the server lowercases, trims, deduplicates, and drops blank entries.

### Request Retry Count
- GET `/request-retry` — Get integer
    - Request:
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' http://localhost:8317/v0/management/request-retry
      ```
    - Response:
      ```json
      { "request-retry": 3 }
      ```
- PUT/PATCH `/request-retry` — Set integer
    - Request:
      ```bash
      curl -X PATCH -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '{"value":5}' \
        http://localhost:8317/v0/management/request-retry
      ```
    - Response:
      ```json
      { "status": "ok" }
      ```

### Max Retry Interval
- GET `/max-retry-interval` — Get the maximum retry interval in seconds
    - Request:
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        http://localhost:8317/v0/management/max-retry-interval
      ```
    - Response:
      ```json
      { "max-retry-interval": 30 }
      ```
- PUT/PATCH `/max-retry-interval` — Set the maximum retry interval in seconds
    - Request:
      ```bash
      curl -X PATCH -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '{"value":60}' \
        http://localhost:8317/v0/management/max-retry-interval
      ```
    - Response:
      ```json
      { "status": "ok" }
      ```

### Force Model Prefix
- GET `/force-model-prefix` — Get boolean
    - Request:
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        http://localhost:8317/v0/management/force-model-prefix
      ```
    - Response:
      ```json
      { "force-model-prefix": false }
      ```
- PUT/PATCH `/force-model-prefix` — Set boolean
    - Request:
      ```bash
      curl -X PATCH -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '{"value":true}' \
        http://localhost:8317/v0/management/force-model-prefix
      ```
    - Response:
      ```json
      { "status": "ok" }
      ```
    - Notes:
        - When `true`, unprefixed model requests only match credentials without a prefix.

### Routing Strategy
- GET `/routing/strategy` — Get the current routing strategy
    - Request:
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        http://localhost:8317/v0/management/routing/strategy
      ```
    - Response:
      ```json
      { "strategy": "round-robin" }
      ```
- PUT/PATCH `/routing/strategy` — Set the routing strategy
    - Request:
      ```bash
      curl -X PATCH -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '{"value":"fill-first"}' \
        http://localhost:8317/v0/management/routing/strategy
      ```
    - Response:
      ```json
      { "status": "ok" }
      ```
    - Notes:
        - Supported values: `round-robin` (default), `fill-first`.

### Request Log
- GET `/request-log` — Get boolean
    - Request:
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' http://localhost:8317/v0/management/request-log
      ```
    - Response:
      ```json
      { "request-log": false }
      ```
- PUT/PATCH `/request-log` — Set boolean
    - Request:
      ```bash
      curl -X PATCH -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '{"value":true}' \
        http://localhost:8317/v0/management/request-log
      ```
    - Response:
      ```json
      { "status": "ok" }
      ```

### WebSocket Authentication (`ws-auth`)
- GET `/ws-auth` — Check whether the WebSocket gateway enforces authentication
    - Request:
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' http://localhost:8317/v0/management/ws-auth
      ```
    - Response:
      ```json
      { "ws-auth": true }
      ```
- PUT/PATCH `/ws-auth` — Enable or disable authentication for `/ws/*` endpoints
    - Request:
      ```bash
      curl -X PATCH -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '{"value":false}' \
        http://localhost:8317/v0/management/ws-auth
      ```
    - Response:
      ```json
      { "status": "ok" }
      ```
    - Notes:
        - When toggled from `false` → `true`, the server terminates any existing WebSocket sessions so that reconnections must supply valid API credentials.
        - Disabling authentication leaves current sessions untouched but future connections will skip the auth middleware until re-enabled.

### Amp CLI Integration (`ampcode`)
Manage Amp upstream proxying, per-client upstream API-key routing, and local model rewrite rules. When `ampcode.upstream-url` is configured, Amp runtime routes are exposed under `/api/*` plus selected root paths such as `/auth`, `/threads`, `/docs`, and `/settings`.

- GET `/ampcode` — Return the full Amp configuration block
    - Response:
      ```json
      {
        "ampcode": {
          "upstream-url": "https://ampcode.com",
          "upstream-api-key": "sk-amp...",
          "upstream-api-keys": [
            { "upstream-api-key": "sk-amp-workspace-a", "api-keys": ["k1", "k2"] }
          ],
          "restrict-management-to-localhost": false,
          "model-mappings": [
            { "from": "claude-opus-4.5", "to": "claude-sonnet-4" }
          ],
          "force-model-mappings": false
        }
      }
      ```

- GET `/ampcode/upstream-url` — Fetch the upstream control-plane URL
    - Response:
      ```json
      { "upstream-url": "https://ampcode.com" }
      ```
- PUT/PATCH `/ampcode/upstream-url` — Set the upstream URL (`{"value": "<url>"}`)
    - Request:
      ```bash
      curl -X PUT -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '{"value":"https://ampcode.com"}' \
        http://localhost:8317/v0/management/ampcode/upstream-url
      ```
    - Response:
      ```json
      { "status": "ok" }
      ```
- DELETE `/ampcode/upstream-url` — Clear the upstream URL

- GET `/ampcode/upstream-api-key` — Fetch the override API key (if set)
    - Response:
      ```json
      { "upstream-api-key": "sk-amp..." }
      ```
- PUT/PATCH `/ampcode/upstream-api-key` — Set the override API key (`{"value": "<key>"}`)
    - Response:
      ```json
      { "status": "ok" }
      ```
- DELETE `/ampcode/upstream-api-key` — Remove the override key (fall back to env/file secrets)
    - Notes:
        - Secret lookup priority is: `ampcode.upstream-api-key` → environment variable `AMP_API_KEY` → `~/.local/share/amp/secrets.json` key `apiKey@https://ampcode.com/`.

- GET `/ampcode/upstream-api-keys` — List client-key to upstream-key mappings
    - Response:
      ```json
      {
        "upstream-api-keys": [
          { "upstream-api-key": "sk-amp-workspace-a", "api-keys": ["k1", "k2"] }
        ]
      }
      ```
- PUT `/ampcode/upstream-api-keys` — Replace all mappings
    - Request:
      ```bash
      curl -X PUT -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '{"value":[{"upstream-api-key":"sk-amp-workspace-a","api-keys":["k1","k2"]}]}' \
        http://localhost:8317/v0/management/ampcode/upstream-api-keys
      ```
    - Response:
      ```json
      { "status": "ok" }
      ```
- PATCH `/ampcode/upstream-api-keys` — Upsert mappings by `upstream-api-key`
    - Request:
      ```bash
      curl -X PATCH -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '{"value":[{"upstream-api-key":"sk-amp-workspace-b","api-keys":["k3"]}]}' \
        http://localhost:8317/v0/management/ampcode/upstream-api-keys
      ```
    - Response:
      ```json
      { "status": "ok" }
      ```
    - Notes:
        - Entries with empty `upstream-api-key` are ignored.
        - `api-keys` values are trimmed; blank items are removed before saving.
        - Mapping lookup uses the authenticated top-level client API key from `/api-keys`.
        - If the same client key appears in multiple entries, the first mapping wins.
- DELETE `/ampcode/upstream-api-keys` — Remove mappings by upstream key, or clear all
    - Request (remove specific mappings):
      ```bash
      curl -X DELETE -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '{"value":["sk-amp-workspace-a"]}' \
        http://localhost:8317/v0/management/ampcode/upstream-api-keys
      ```
    - Request (clear all mappings):
      ```bash
      curl -X DELETE -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '{"value":[]}' \
        http://localhost:8317/v0/management/ampcode/upstream-api-keys
      ```
    - Response:
      ```json
      { "status": "ok" }
      ```
    - Notes:
        - `value` is required; omitting it returns `400`.

- GET `/ampcode/restrict-management-to-localhost` — Check the localhost-only toggle for Amp management proxy routes
    - Response:
      ```json
      { "restrict-management-to-localhost": false }
      ```
- PUT/PATCH `/ampcode/restrict-management-to-localhost` — Set boolean
    - Response:
      ```json
      { "status": "ok" }
      ```
    - Notes:
        - When `true`, access is checked against the real TCP peer address (`RemoteAddr`), so forwarded headers do not bypass the restriction.
        - The restriction covers proxied Amp control-plane routes such as `/api/internal`, `/api/user`, `/api/auth`, `/api/meta`, `/api/telemetry`, `/api/threads`, `/api/otel`, `/api/tab`, plus root-level `/auth`, `/threads`, `/docs`, `/settings`, `/threads.rss`, and `/news.rss`.
        - The config default is `false`; enable it when Amp management traffic should stay local-only.

- GET `/ampcode/model-mappings` — List all model rewrites for Amp requests
    - Response:
      ```json
      {
        "model-mappings": [
          { "from": "claude-opus-4.5", "to": "claude-sonnet-4" },
          { "from": "^gpt-5(?:-mini)?$", "to": "gemini-2.5-pro", "regex": true }
        ]
      }
      ```
- PUT `/ampcode/model-mappings` — Replace the list
    - Request:
      ```bash
      curl -X PUT -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '{"value":[{"from":"claude-opus-4.5","to":"claude-sonnet-4"},{"from":"^gpt-5(?:-mini)?$","to":"gemini-2.5-pro","regex":true}]}' \
        http://localhost:8317/v0/management/ampcode/model-mappings
      ```
    - Response:
      ```json
      { "status": "ok" }
      ```
- PATCH `/ampcode/model-mappings` — Upsert by `from`
    - Request:
      ```bash
      curl -X PATCH -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '{"value":[{"from":"gpt-5","to":"gemini-2.5-pro"},{"from":"^claude-opus-4-5-.*$","to":"claude-sonnet-4","regex":true}]}' \
        http://localhost:8317/v0/management/ampcode/model-mappings
      ```
    - Response:
      ```json
      { "status": "ok" }
      ```
    - Notes:
        - Entries match on the trimmed `from` field; existing matches are replaced, otherwise appended.
        - Each item supports `from`, `to`, and optional `regex`.
        - Exact matches and regex matches are both case-insensitive. Exact mappings are checked first; regex mappings are evaluated in list order.
        - A mapping is applied only when its target model resolves to an available local provider.
        - Request suffixes like `model(8192)` are preserved unless the configured target already includes its own suffix.
- DELETE `/ampcode/model-mappings` — Delete mappings
    - Request (remove specific):
      ```bash
      curl -X DELETE -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '{"value":["claude-opus-4.5"]}' \
        http://localhost:8317/v0/management/ampcode/model-mappings
      ```
    - Request (empty/omitted body clears all):
      ```bash
      curl -X DELETE -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        http://localhost:8317/v0/management/ampcode/model-mappings
      ```
    - Response:
      ```json
      { "status": "ok" }
      ```

- GET `/ampcode/force-model-mappings` — Check whether mappings override local API-key availability
    - Response:
      ```json
      { "force-model-mappings": false }
      ```
- PUT/PATCH `/ampcode/force-model-mappings` — Set boolean
    - Request:
      ```bash
      curl -X PUT -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '{"value":true}' \
        http://localhost:8317/v0/management/ampcode/force-model-mappings
      ```
    - Response:
      ```json
      { "status": "ok" }
      ```
    - Notes:
        - `false` (default): try local providers first, then `model-mappings`, then the Amp upstream.
        - `true`: try `model-mappings` first, then direct local providers, then the Amp upstream.

### Claude API KEY (object array)
- GET `/claude-api-key` — List all
    - Request:
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' http://localhost:8317/v0/management/claude-api-key
      ```
    - Response:
      ```json
      { "claude-api-key": [ { "api-key": "sk-a", "priority": 10, "prefix": "team-a", "base-url": "https://example.com/api", "proxy-url": "socks5://proxy.example.com:1080", "models": [ { "name": "claude-3-5-sonnet-20241022", "alias": "claude-sonnet-latest" } ], "headers": { "X-Workspace": "team-a" }, "excluded-models": ["claude-3-opus"], "cloak": { "mode": "provider", "strict-mode": false, "sensitive-words": ["internal-project"] } } ] }
      ```
- PUT `/claude-api-key` — Replace the list
    - Request:
      ```bash
      curl -X PUT -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '[{"api-key":"sk-a","priority":10,"prefix":"team-a","base-url":"https://example.com/api","proxy-url":"socks5://proxy.example.com:1080","models":[{"name":"claude-3-5-sonnet-20241022","alias":"claude-sonnet-latest"}],"headers":{"X-Workspace":"team-a"},"excluded-models":["claude-3-opus"],"cloak":{"mode":"provider","strict-mode":false,"sensitive-words":["internal-project"]}},{"api-key":"sk-b","priority":1,"base-url":"https://c.example.com","proxy-url":"","models":[{"name":"claude-3-5-haiku-20241022","alias":"claude-haiku"}],"headers":{"X-Env":"prod"},"excluded-models":["claude-3-sonnet","claude-3-5-haiku"]}]' \
        http://localhost:8317/v0/management/claude-api-key
      ```
    - Request (`items` wrapper form):
      ```bash
      curl -X PUT -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '{"items":[{"api-key":"sk-a","priority":5},{"api-key":"sk-b","priority":1}]}' \
        http://localhost:8317/v0/management/claude-api-key
      ```
    - Response:
      ```json
      { "status": "ok" }
      ```
- PATCH `/claude-api-key` — Modify one (by `index` or `match`)
    - Request (by index):
      ```bash
      curl -X PATCH -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
          -d '{"index":1,"value":{"api-key":"sk-b2","prefix":"team-b","base-url":"https://c.example.com","proxy-url":"","models":[{"name":"claude-3-7-sonnet-20250219","alias":"claude-3.7-sonnet"}],"headers":{"X-Env":"stage"},"excluded-models":["claude-3.7-sonnet"]}}' \
          http://localhost:8317/v0/management/claude-api-key
        ```
    - Request (by match):
      ```bash
      curl -X PATCH -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
          -d '{"match":"sk-a","value":{"api-key":"sk-a","prefix":"team-a","base-url":"","proxy-url":"socks5://proxy.example.com:1080","models":[{"name":"claude-3-5-sonnet-20241022","alias":"claude-sonnet-latest"}],"headers":{"X-Workspace":"team-a"},"excluded-models":["claude-3-opus","claude-3.5-sonnet"]}}' \
          http://localhost:8317/v0/management/claude-api-key
        ```
    - Response:
      ```json
      { "status": "ok" }
      ```
- DELETE `/claude-api-key` — Delete one (`?api-key=` or `?index=`)
    - Request (by api-key):
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' -X DELETE 'http://localhost:8317/v0/management/claude-api-key?api-key=sk-b2'
      ```
    - Request (by index):
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' -X DELETE 'http://localhost:8317/v0/management/claude-api-key?index=0'
      ```
    - Response:
      ```json
      { "status": "ok" }
      ```
    - Notes:
        - `PUT` accepts both a raw array and `{ "items": [...] }`.
        - Entry fields include: `api-key`, `priority`, `prefix`, `base-url`, `proxy-url`, `models`, `headers`, `excluded-models`, `cloak`.
        - `PATCH value` supports: `api-key`, `prefix`, `base-url`, `proxy-url`, `models`, `headers`, `excluded-models`.
        - `priority` and `cloak` can be set via `PUT` (not `PATCH`).
        - `priority` defaults to `0`.
        - `prefix` is optional. When set, call models as `prefix/<model>` to target this credential. Stored prefixes are trimmed and must be a single segment (no `/`).
        - `PATCH` accepts partial `value` fields; omitted fields keep existing values.
        - `PATCH` can locate an item by `index` (0-based) or `match` (`api-key` exact match after trim).
        - `base-url` is optional for Claude. Empty `base-url` falls back to the default Claude endpoint (does not delete the entry).
        - `models` is replaced as a whole list when provided; blank model items are removed during normalization.
        - `headers` is optional; empty/blank pairs are removed automatically.
        - `excluded-models` lets you block specific Claude models for a key; the server lowercases, trims, deduplicates, and removes blank entries.

### OpenAI Compatibility Providers (object array)
- GET `/openai-compatibility` — List all
    - Request:
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' http://localhost:8317/v0/management/openai-compatibility
      ```
    - Response:
      ```json
      { "openai-compatibility": [ { "name": "openrouter", "priority": 10, "prefix": "team-a", "base-url": "https://openrouter.ai/api/v1", "api-key-entries": [ { "api-key": "sk-a", "proxy-url": "" }, { "api-key": "sk-b", "proxy-url": "socks5://proxy.example.com:1080" } ], "models": [ { "name": "moonshotai/kimi-k2:free", "alias": "kimi-k2" } ], "headers": { "X-Provider": "openrouter" } } ] }
      ```
- PUT `/openai-compatibility` — Replace the list
    - Request:
      ```bash
      curl -X PUT -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '[{"name":"openrouter","priority":10,"prefix":"team-a","base-url":"https://openrouter.ai/api/v1","api-key-entries":[{"api-key":"sk-a","proxy-url":""},{"api-key":"sk-b","proxy-url":"socks5://proxy.example.com:1080"}],"models":[{"name":"moonshotai/kimi-k2:free","alias":"kimi-k2"}],"headers":{"X-Provider":"openrouter"}}]' \
        http://localhost:8317/v0/management/openai-compatibility
      ```
    - Request (`items` wrapper form):
      ```bash
      curl -X PUT -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '{"items":[{"name":"openrouter","base-url":"https://openrouter.ai/api/v1","api-key-entries":[{"api-key":"sk-a"}]}]}' \
        http://localhost:8317/v0/management/openai-compatibility
      ```
    - Response:
      ```json
      { "status": "ok" }
      ```
- PATCH `/openai-compatibility` — Modify one (by `index` or `name`)
    - Request (by name):
      ```bash
      curl -X PATCH -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '{"name":"openrouter","value":{"name":"openrouter","prefix":"team-a","base-url":"https://openrouter.ai/api/v1","api-key-entries":[{"api-key":"sk-a","proxy-url":""},{"api-key":"sk-c","proxy-url":"socks5://proxy.example.com:1080"}],"models":[{"name":"moonshotai/kimi-k2:free","alias":"kimi-k2"}],"headers":{"X-Provider":"openrouter"}}}' \
        http://localhost:8317/v0/management/openai-compatibility
      ```
    - Request (by index):
      ```bash
      curl -X PATCH -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '{"index":0,"value":{"headers":{"X-Provider":"openrouter","X-Env":"prod"},"models":[{"name":"moonshotai/kimi-k2:free","alias":"kimi-k2"}]}}' \
        http://localhost:8317/v0/management/openai-compatibility
      ```
    - Response:
      ```json
      { "status": "ok" }
      ```

    - Notes:
        - `PUT` accepts both a raw array and `{ "items": [...] }`.
        - Provider fields include: `name`, `priority`, `prefix`, `base-url`, `api-key-entries`, `models`, `headers`.
        - `priority` is optional and defaults to `0`.
        - `prefix` is optional. When set, call models as `prefix/<model>` to target this provider. Stored prefixes are trimmed and must be a single segment (no `/`).
        - `PATCH` accepts partial `value` fields; omitted fields keep existing values.
        - `PATCH` can locate a provider by `index` (0-based) or `name` (exact match after trim).
        - `api-key-entries` and `models` are replaced as a whole list when provided in `PATCH`.
        - Each `api-key-entries` item uses `{ "api-key": "...", "proxy-url": "..." }`.
        - `headers` lets you define provider-wide HTTP headers; blank keys/values are dropped.
        - Providers without a `base-url` are removed. Sending a PATCH with `base-url` set to an empty string deletes that provider.
        - Use `api-key-entries` for credentials. Legacy `api-keys` is deprecated and should not be relied on for management updates.
- DELETE `/openai-compatibility` — Delete (`?name=` or `?index=`)
    - Request (by name):
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' -X DELETE 'http://localhost:8317/v0/management/openai-compatibility?name=openrouter'
      ```
    - Request (by index):
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' -X DELETE 'http://localhost:8317/v0/management/openai-compatibility?index=0'
      ```
    - Response:
      ```json
      { "status": "ok" }
      ```

### Vertex Compatibility API Key (object array)
Configure third-party services that expose Vertex-style `/publishers/google/models/...` endpoints but authenticate with `x-goog-api-key`. This is separate from service-account-based Vertex auth imported via `POST /vertex/import`.

- GET `/vertex-api-key` — List all
    - Request:
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' http://localhost:8317/v0/management/vertex-api-key
      ```
    - Response:
      ```json
      { "vertex-api-key": [ { "api-key": "vk-123", "priority": 10, "prefix": "team-a", "base-url": "https://example.com/api", "proxy-url": "socks5://proxy.example.com:1080", "headers": { "X-Team": "vertex" }, "models": [ { "name": "gemini-2.5-flash", "alias": "vertex-flash" } ], "excluded-models": ["imagen-3.0-generate-002"] } ] }
      ```
- PUT `/vertex-api-key` — Replace the list
    - Request:
      ```bash
      curl -X PUT -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '[{"api-key":"vk-123","priority":10,"prefix":"team-a","base-url":"https://example.com/api","proxy-url":"socks5://proxy.example.com:1080","headers":{"X-Team":"vertex"},"models":[{"name":"gemini-2.5-flash","alias":"vertex-flash"}],"excluded-models":["imagen-3.0-generate-002"]},{"api-key":"vk-456","priority":1,"base-url":"https://example.com/api","proxy-url":"","headers":{"X-Env":"prod"},"models":[{"name":"gemini-2.5-pro","alias":"vertex-pro"}],"excluded-models":["imagen-4.0-generate-preview-06-06"]}]' \
        http://localhost:8317/v0/management/vertex-api-key
      ```
    - Request (`items` wrapper form):
      ```bash
      curl -X PUT -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '{"items":[{"api-key":"vk-123","base-url":"https://example.com/api","priority":5},{"api-key":"vk-456","base-url":"https://example.com/api","priority":1}]}' \
        http://localhost:8317/v0/management/vertex-api-key
      ```
    - Response:
      ```json
      { "status": "ok" }
      ```
- PATCH `/vertex-api-key` — Modify one (by `index` or `match`)
    - Request (by index):
      ```bash
      curl -X PATCH -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '{"index":0,"value":{"api-key":"vk-123","prefix":"team-a","base-url":"https://example.com/api","proxy-url":"","headers":{"X-Team":"vertex"},"models":[{"name":"gemini-2.5-pro","alias":"vertex-pro"}],"excluded-models":["imagen-3.0-generate-002"]}}' \
        http://localhost:8317/v0/management/vertex-api-key
      ```
    - Request (by match):
      ```bash
      curl -X PATCH -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '{"match":"vk-123","value":{"api-key":"vk-123","prefix":"team-a","base-url":"https://example.com/api","proxy-url":"","headers":{"X-Team":"vertex"},"models":[{"name":"gemini-2.5-pro","alias":"vertex-pro"}],"excluded-models":["imagen-4.0-generate-preview-06-06"]}}' \
        http://localhost:8317/v0/management/vertex-api-key
      ```
    - Response:
      ```json
      { "status": "ok" }
      ```
- DELETE `/vertex-api-key` — Delete one (`?api-key=` or `?index=`)
    - Request (by api-key):
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' -X DELETE 'http://localhost:8317/v0/management/vertex-api-key?api-key=vk-123'
      ```
    - Request (by index):
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' -X DELETE 'http://localhost:8317/v0/management/vertex-api-key?index=0'
      ```
    - Response:
      ```json
      { "status": "ok" }
      ```
    - Notes:
        - `PUT` accepts both a raw array and `{ "items": [...] }`.
        - Entry fields include: `api-key`, `priority`, `prefix`, `base-url`, `proxy-url`, `headers`, `models`, `excluded-models`.
        - `PATCH value` supports: `api-key`, `prefix`, `base-url`, `proxy-url`, `headers`, `models`, `excluded-models`.
        - `priority` can be set via `PUT` (not `PATCH`) and defaults to `0`.
        - `PATCH` accepts partial `value` fields; omitted fields keep existing values.
        - `PATCH` can locate an item by `index` (0-based) or `match` (`api-key` exact match after trim).
        - `prefix` is optional. When set, call models as `prefix/<model>` to target this credential. Stored prefixes are trimmed and must be a single segment (no `/`).
        - `base-url` and `api-key` are required; sending either as empty in PUT/PATCH removes the entry.
        - Requests use the `x-goog-api-key` header, and the runtime appends `/v1/publishers/google/models/{model}:<action>` to `base-url`.
        - `models` entries must include both `name` and `alias`; blank entries are dropped. Providing `models` in PATCH replaces the whole list.
        - `headers` trims blank keys/values automatically.
        - `excluded-models` is normalized by trimming, lowercasing, deduplicating, and dropping blank items.
        - Duplicate entries with the same `api-key` + `base-url` are deduplicated during sanitization.

### OAuth Excluded Models
Configure per-provider model blocks for OAuth-based providers. Keys are provider identifiers, values are string arrays of model names to exclude.

- GET `/oauth-excluded-models` — Get the current map
    - Request:
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        http://localhost:8317/v0/management/oauth-excluded-models
      ```
    - Response:
      ```json
      {
        "oauth-excluded-models": {
          "openai": ["gpt-4.1-mini"],
          "iflow": ["deepseek-v3.1", "glm-4.5"]
        }
      }
      ```
- PUT `/oauth-excluded-models` — Replace the full map
    - Request:
      ```bash
      curl -X PUT -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '{"openai":["gpt-4.1-mini"],"iflow":["deepseek-v3.1","glm-4.5"]}' \
        http://localhost:8317/v0/management/oauth-excluded-models
      ```
    - Response:
      ```json
      { "status": "ok" }
      ```
    - Notes:
        - The body can also be wrapped as `{ "items": { ... } }`; in both cases empty/blank model names are trimmed out.
- PATCH `/oauth-excluded-models` — Upsert or delete a single provider entry
    - Request (upsert):
      ```bash
      curl -X PATCH -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '{"provider":"iflow","models":["deepseek-v3.1","glm-4.5"]}' \
        http://localhost:8317/v0/management/oauth-excluded-models
      ```
    - Request (delete provider by sending empty models):
      ```bash
      curl -X PATCH -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '{"provider":"iflow","models":[]}' \
        http://localhost:8317/v0/management/oauth-excluded-models
      ```
    - Response:
      ```json
      { "status": "ok" }
      ```
    - Notes:
        - `provider` is normalized to lowercase. Sending an empty `models` list removes that provider; if the provider does not exist, a `404` is returned.
- DELETE `/oauth-excluded-models` — Delete all models for a provider
    - Request:
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -X DELETE 'http://localhost:8317/v0/management/oauth-excluded-models?provider=iflow'
      ```
    - Response:
      ```json
      { "status": "ok" }
      ```

### OAuth Model Alias
Configure global model aliases for OAuth-backed providers. Keys are channel identifiers, values are arrays of alias objects.

- GET `/oauth-model-alias` — Get the current map
    - Request:
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        http://localhost:8317/v0/management/oauth-model-alias
      ```
    - Response:
      ```json
      {
        "oauth-model-alias": {
          "gemini-cli": [{ "name": "gemini-2.5-pro", "alias": "g2.5p", "fork": true }],
          "vertex": [{ "name": "gemini-2.5-pro", "alias": "g2.5p" }]
        }
      }
      ```
- PUT `/oauth-model-alias` — Replace the full map
    - Request:
      ```bash
      curl -X PUT -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '{"gemini-cli":[{"name":"gemini-2.5-pro","alias":"g2.5p","fork":true}],"vertex":[{"name":"gemini-2.5-pro","alias":"g2.5p"}]}' \
        http://localhost:8317/v0/management/oauth-model-alias
      ```
    - Response:
      ```json
      { "status": "ok" }
      ```
    - Notes:
        - The body can also be wrapped as `{ "items": { ... } }`; empty/invalid aliases are dropped.
- PATCH `/oauth-model-alias` — Upsert or delete a single channel
    - Request (upsert):
      ```bash
      curl -X PATCH -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '{"channel":"vertex","aliases":[{"name":"gemini-2.5-pro","alias":"g2.5p"}]}' \
        http://localhost:8317/v0/management/oauth-model-alias
      ```
    - Request (delete channel by sending empty aliases):
      ```bash
      curl -X PATCH -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '{"channel":"vertex","aliases":[]}' \
        http://localhost:8317/v0/management/oauth-model-alias
      ```
    - Response:
      ```json
      { "status": "ok" }
      ```
    - Notes:
        - `channel` (or `provider`) is normalized to lowercase. Empty `aliases` deletes the channel entry.
- DELETE `/oauth-model-alias` — Delete all aliases for a channel
    - Request:
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -X DELETE 'http://localhost:8317/v0/management/oauth-model-alias?channel=vertex'
      ```
    - Response:
      ```json
      { "status": "ok" }
      ```
    - Notes:
        - Query accepts `channel` or `provider`.
        - Aliases apply to OAuth-backed channels only (gemini-cli, vertex, aistudio, antigravity, claude, codex, qwen, iflow).
        - When `fork` is true, the alias is added in addition to the original model name.

### Auth File Management

Manage JSON token files under `auth-dir`: list, download, upload, delete.

- GET `/auth-files` — List
    - Request:
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' http://localhost:8317/v0/management/auth-files
      ```
    - Response (when the runtime auth manager is available):
      ```json
      {
        "files": [
          {
            "id": "claude-user@example.com",
            "auth_index": "1a2b3c4d5e6f7a8b",
            "name": "claude-user@example.com.json",
            "type": "claude",
            "provider": "claude",
            "label": "Claude Prod",
            "status": "ready",
            "status_message": "ok",
            "disabled": false,
            "unavailable": false,
            "runtime_only": false,
            "source": "file",
            "path": "/abs/path/auths/claude-user@example.com.json",
            "size": 2345,
            "modtime": "2025-08-30T12:34:56Z",
            "email": "user@example.com",
            "account_type": "anthropic",
            "account": "workspace-1",
            "created_at": "2025-08-30T12:00:00Z",
            "updated_at": "2025-08-31T01:23:45Z",
            "last_refresh": "2025-08-31T01:23:45Z"
          }
        ]
      }
      ```
    - Notes:
        - Entries are sorted case-insensitively by `name`. `status`, `status_message`, `disabled`, and `unavailable` mirror the runtime auth manager so you can see whether a credential is healthy.
        - `runtime_only: true` indicates the credential only exists in memory (for example Git/Postgres/ObjectStore backends); `source` switches to `memory`. When a `.json` file exists on disk, `source=file` and the response includes `path`/`size`/`modtime`.
        - `auth_index` is a stable identifier derived from the auth filename/API key and appears in usage statistics.
        - `type` mirrors `provider` when the runtime auth manager is active.
        - `email`, `account_type`, `account`, and `last_refresh` are pulled from the JSON metadata (keys such as `last_refresh`, `lastRefreshedAt`, `last_refreshed_at`, etc.).
        - If the runtime auth manager is unavailable the handler falls back to scanning `auth-dir`, returning only `name`, `size`, `modtime`, `type`, and `email`.
        - `runtime_only` entries cannot be downloaded or deleted via the file endpoints—they must be revoked from the upstream provider or a different API.

- GET `/auth-files/models?name=<file-or-id>` — List models available to a credential
    - Request:
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        'http://localhost:8317/v0/management/auth-files/models?name=claude-user@example.com.json'
      ```
    - Response:
      ```json
      {
        "models": [
          { "id": "claude-3-5-sonnet-20241022", "display_name": "Claude 3.5 Sonnet", "type": "messages", "owned_by": "anthropic" }
        ]
      }
      ```
    - Notes:
        - `name` may be a file name or auth ID; unknown values return an empty `models` list.

- GET `/model-definitions/:channel` — Return static model definitions for a channel
    - Request:
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        http://localhost:8317/v0/management/model-definitions/gemini-cli
      ```
    - Response:
      ```json
      {
        "channel": "gemini-cli",
        "models": [
          { "id": "gemini-2.5-pro", "object": "model", "owned_by": "google" }
        ]
      }
      ```
    - Notes:
        - `channel` can be provided as path param (`:channel`) or query param (`?channel=`).
        - Unknown channels return `400` with `{ "error": "unknown channel", "channel": "<input>" }`.
        - Supported channels include `claude`, `gemini`, `vertex`, `gemini-cli`, `aistudio`, `codex`, `qwen`, `iflow`, and `antigravity`.

- GET `/auth-files/download?name=<file.json>` — Download a single file
    - Request:
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' -OJ 'http://localhost:8317/v0/management/auth-files/download?name=acc1.json'
      ```
    - Notes:
        - `name` must be a `.json` filename. Only `source=file` entries have a backing file to export; `runtime_only` credentials cannot be downloaded.

- POST `/auth-files` — Upload
    - Request (multipart):
      ```bash
      curl -X POST -F 'file=@/path/to/acc1.json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        http://localhost:8317/v0/management/auth-files
      ```
    - Request (raw JSON):
      ```bash
      curl -X POST -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d @/path/to/acc1.json \
        'http://localhost:8317/v0/management/auth-files?name=acc1.json'
      ```
    - Response:
      ```json
      { "status": "ok" }
      ```
    - Notes:
        - The core auth manager must be active; otherwise the API returns `503` with `{ "error": "core auth manager unavailable" }`.
        - Both multipart and raw JSON uploads must use filenames ending in `.json`; upon success the credential is registered with the runtime auth manager immediately.

- DELETE `/auth-files?name=<file.json>` — Delete a single file
    - Request:
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' -X DELETE 'http://localhost:8317/v0/management/auth-files?name=acc1.json'
      ```
    - Response:
      ```json
      { "status": "ok" }
      ```
    - Notes:
        - Only on-disk `.json` files are removed; after a successful deletion the runtime manager is instructed to disable the corresponding credential. `runtime_only` entries are unaffected.

- DELETE `/auth-files?all=true` — Delete all `.json` files under `auth-dir`
    - Request:
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' -X DELETE 'http://localhost:8317/v0/management/auth-files?all=true'
      ```
    - Response:
      ```json
      { "status": "ok", "deleted": 3 }
      ```
    - Notes:
        - Only files on disk are counted and removed; each successful deletion also triggers a disable call into the runtime auth manager. Purely in-memory entries stay untouched.

- PATCH `/auth-files/status` — Enable or disable a credential in the runtime auth manager
    - Request:
      ```bash
      curl -X PATCH -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '{"name":"claude-user@example.com.json","disabled":true}' \
        http://localhost:8317/v0/management/auth-files/status
      ```
    - Response:
      ```json
      { "status": "ok", "disabled": true }
      ```
    - Notes:
        - `name` can be either auth file name or auth ID.
        - Requires the core auth manager; otherwise returns `503`.
        - Unknown names return `404` with `{ "error": "auth file not found" }`.

### Vertex Credential Import
Mirrors the CLI `vertex-import` helper and stores Google service account JSON as `vertex-<project>.json` files inside `auth-dir`.

- POST `/vertex/import` — Upload a Vertex service account key
    - Request (multipart):
      ```bash
      curl -X POST \
        -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -F 'file=@/path/to/my-project-sa.json' \
        -F 'location=us-central1' \
        http://localhost:8317/v0/management/vertex/import
      ```
    - Response:
      ```json
      {
        "status": "ok",
        "auth-file": "/abs/path/auths/vertex-my-project.json",
        "project_id": "my-project",
        "email": "svc@my-project.iam.gserviceaccount.com",
        "location": "us-central1"
      }
      ```
    - Notes:
        - Uploads must be sent as `multipart/form-data` using the `file` field. The payload is validated and `private_key` is normalized; malformed JSON or missing `project_id` yields `400`.
        - The optional `location` form (or query) field overrides the default `us-central1` region recorded in the credential metadata.
        - Missing `client_email` does not block import; the credential is still saved as long as `project_id` is present.
        - If `auth-dir` is not configured, the endpoint returns `503`.
        - The handler persists the credential via the same token store as other auth uploads; failures return `500` with `{ "error": "save_failed", ... }`.

### Login/OAuth URLs

These endpoints initiate provider login flows and return a URL to open in a browser. Tokens are saved under `auths/` once the flow completes.

For Anthropic, Codex, Gemini CLI, Antigravity, and iFlow you can append `?is_webui=true` to reuse the embedded callback forwarder when launching from the management UI.

Remote browser mode (no SSH port-forwarding): when the browser and server are on different machines, the OAuth provider will redirect to `http://localhost:<port>/...` on *your browser machine* (and fail). Copy the full failed redirect URL from the address bar and submit it to the server via `POST /oauth-callback`, then keep polling `/get-auth-status`.

- GET `/anthropic-auth-url` — Start Anthropic (Claude) login
    - Request:
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        http://localhost:8317/v0/management/anthropic-auth-url
      ```
    - Response:
      ```json
      { "status": "ok", "url": "https://...", "state": "anth-1716206400" }
      ```
    - Notes:
        - Add `?is_webui=true` when triggering from the built-in UI to reuse the local callback service.

- GET `/codex-auth-url` — Start Codex login
    - Request:
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        http://localhost:8317/v0/management/codex-auth-url
      ```
    - Response:
      ```json
      { "status": "ok", "url": "https://...", "state": "codex-1716206400" }
      ```

- GET `/gemini-cli-auth-url` — Start Google (Gemini CLI) login
    - Query params:
        - `project_id` (optional): Google Cloud project ID.
    - Request:
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        'http://localhost:8317/v0/management/gemini-cli-auth-url?project_id=<PROJECT_ID>'
      ```
    - Response:
      ```json
      { "status": "ok", "url": "https://...", "state": "gem-1716206400" }
      ```
    - Notes:
        - When `project_id` is omitted, the server queries Cloud Resource Manager for accessible projects, picks the first available one, and stores it in the token file (marked with `auto: true`).
        - The flow checks and, if needed, enables `cloudaicompanion.googleapis.com` via the Service Usage API; failures surface through `/get-auth-status` as errors such as `project activation required: ...`.

- GET `/antigravity-auth-url` — Start Antigravity login
    - Request:
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        http://localhost:8317/v0/management/antigravity-auth-url
      ```
    - Response:
      ```json
      { "status": "ok", "url": "https://...", "state": "ant-1716206400" }
      ```
    - Notes:
        - Add `?is_webui=true` when triggering from the built-in UI so the server starts a temporary local callback forwarder on port `51121` and reuses the main HTTP port for the final redirect.

- GET `/qwen-auth-url` — Start Qwen login (device flow)
    - Request:
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        http://localhost:8317/v0/management/qwen-auth-url
      ```
    - Response:
      ```json
      { "status": "ok", "url": "https://...", "state": "gem-1716206400" }
      ```

- GET `/kimi-auth-url` — Start Kimi login (device flow)
    - Request:
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        http://localhost:8317/v0/management/kimi-auth-url
      ```
    - Response:
      ```json
      { "status": "ok", "url": "https://...", "state": "kmi-1716206400" }
      ```

- GET `/iflow-auth-url` — Start iFlow login
    - Request:
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        http://localhost:8317/v0/management/iflow-auth-url
      ```
    - Response:
      ```json
      { "status": "ok", "url": "https://...", "state": "ifl-1716206400" }
      ```

- POST `/iflow-auth-url` — Authenticate using an existing iFlow cookie
    - Request body:
      ```bash
      curl -X POST -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '{"cookie":"<YOUR_IFLOW_COOKIE>"}' \
        http://localhost:8317/v0/management/iflow-auth-url
      ```
    - Successful response:
      ```json
      {
        "status": "ok",
        "saved_path": "/abs/path/auths/iflow-user.json",
        "email": "user@example.com",
        "expired": "2025-05-20T10:00:00Z",
        "type": "cookie"
      }
      ```
    - Notes:
        - The `cookie` field is required and must be non-empty; invalid or malformed cookies return `400` with `{ "status": "error", "error": "..." }`.
        - On success the server normalizes the cookie, exchanges it for an API token, persists it as an `iflow-*.json` auth file, and returns the saved path and basic metadata.

- POST `/oauth-callback` — Submit OAuth callback details (remote browser mode)
    - Request body (A: paste full redirect URL):
      ```bash
      curl -X POST -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -H 'Content-Type: application/json' \
        -d '{"provider":"codex","redirect_url":"http://localhost:1455/auth/callback?code=...&state=...&error="}' \
        http://localhost:8317/v0/management/oauth-callback
      ```
    - Request body (B: submit fields directly):
      ```bash
      curl -X POST -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -H 'Content-Type: application/json' \
        -d '{"provider":"codex","code":"...","state":"...","error":""}' \
        http://localhost:8317/v0/management/oauth-callback
      ```
    - Response:
      ```json
      { "status": "ok" }
      ```
    - Notes:
        - Supported providers: `anthropic`, `codex`, `gemini` (`google` alias), `iflow`, `antigravity`, `qwen`.
        - The `state` must belong to a pending OAuth session started by this server (returned from `*-auth-url`).

- GET `/get-auth-status?state=<state>` — Poll OAuth flow status
    - Request:
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        'http://localhost:8317/v0/management/get-auth-status?state=<STATE_FROM_AUTH_URL>'
      ```
    - Response examples:
      ```json
      { "status": "wait" }
      ```
      ```json
      { "status": "ok" }
      ```
      ```json
      { "status": "error", "error": "Authentication failed" }
      ```
    - Notes:
        - If `state` is missing or empty, the endpoint returns `{ "status": "ok" }` for backward compatibility.
        - When provided, `state` must match the value returned by the login endpoint.
        - `status: "ok"` means the server no longer tracks the state (flow completed or expired).
        - `status: "wait"` indicates the flow is still waiting for a callback or token exchange—continue polling as needed.

## Error Responses

Generic error format:
- 400 Bad Request: `{ "error": "invalid body" }`
- 401 Unauthorized: `{ "error": "missing management key" }` or `{ "error": "invalid management key" }`
- 403 Forbidden: `{ "error": "remote management disabled" }`
- 404 Not Found: `{ "error": "item not found" }` or `{ "error": "file not found" }`
- 422 Unprocessable Entity: `{ "error": "invalid_config", "message": "..." }`
- 500 Internal Server Error: `{ "error": "failed to save config: ..." }`
- 503 Service Unavailable: `{ "error": "core auth manager unavailable" }`

## Notes

- Changes are written back to the YAML config file and hot‑reloaded by the file watcher and clients.
- `remote-management.allow-remote` and `remote-management.secret-key` cannot be changed via the API; configure them in the config file.
