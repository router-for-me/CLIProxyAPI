# Management API

Base path: `http://localhost:8317/v0/management`

This API manages the CLI Proxy API’s runtime configuration and authentication files. All changes are persisted to the YAML config file and hot‑reloaded by the service.

Note: The following options cannot be modified via API and must be set in the config file (restart if needed):
- `allow-remote-management`
- `remote-management-key` (if plaintext is detected at startup, it is automatically bcrypt‑hashed and written back to the config)

## Authentication

- All requests (including localhost) must provide a valid management key.
- Remote access requires enabling remote management in the config: `allow-remote-management: true`.
- Provide the management key (in plaintext) via either:
  - `Authorization: Bearer <plaintext-key>`
  - `X-Management-Key: <plaintext-key>`

Additional notes:
- If `remote-management.secret-key` is empty, the entire Management API is disabled (all `/v0/management` routes return 404).
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
      }
    }
    ```
  - Notes:
    - Statistics are recalculated for every request that reports token usage; data resets when the server restarts.
    - Hourly counters fold all days into the same hour bucket (`00`–`23`).

### Config
- GET `/config` — Get the full config
    - Request:
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' http://localhost:8317/v0/management/config
      ```
    - Response:
      ```json
      {"debug":true,"proxy-url":"","api-keys":["1...5","JS...W"],"quota-exceeded":{"switch-project":true,"switch-preview-model":true},"generative-language-api-key":["AI...01","AI...02","AI...03"],"request-log":true,"request-retry":3,"claude-api-key":[{"api-key":"cr...56","base-url":"https://example.com/api","proxy-url":"socks5://proxy.example.com:1080"},{"api-key":"cr...e3","base-url":"http://example.com:3000/api","proxy-url":""},{"api-key":"sk-...q2","base-url":"https://example.com","proxy-url":""}],"codex-api-key":[{"api-key":"sk...01","base-url":"https://example/v1","proxy-url":""}],"openai-compatibility":[{"name":"openrouter","base-url":"https://openrouter.ai/api/v1","api-key-entries":[{"api-key":"sk...01","proxy-url":""}],"models":[{"name":"moonshotai/kimi-k2:free","alias":"kimi-k2"}]},{"name":"iflow","base-url":"https://apis.iflow.cn/v1","api-key-entries":[{"api-key":"sk...7e","proxy-url":"socks5://proxy.example.com:1080"}],"models":[{"name":"deepseek-v3.1","alias":"deepseek-v3.1"},{"name":"glm-4.5","alias":"glm-4.5"},{"name":"kimi-k2","alias":"kimi-k2"}]}]}
      ```

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

### Force GPT-5 Codex
- GET `/force-gpt-5-codex` — Get current flag
  - Request:
    ```bash
    curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' http://localhost:8317/v0/management/force-gpt-5-codex
    ```
  - Response:
    ```json
    { "gpt-5-codex": false }
    ```
- PUT/PATCH `/force-gpt-5-codex` — Set boolean
  - Request:
    ```bash
    curl -X PUT -H 'Content-Type: application/json' \
    -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
      -d '{"value":true}' \
      http://localhost:8317/v0/management/force-gpt-5-codex
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

### Gemini API Key (Generative Language)
- GET `/generative-language-api-key`
  - Request:
    ```bash
    curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' http://localhost:8317/v0/management/generative-language-api-key
    ```
  - Response:
    ```json
    { "generative-language-api-key": ["AIzaSy...01","AIzaSy...02"] }
    ```
- PUT `/generative-language-api-key`
  - Request:
    ```bash
    curl -X PUT -H 'Content-Type: application/json' \
    -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
      -d '["AIzaSy-1","AIzaSy-2"]' \
      http://localhost:8317/v0/management/generative-language-api-key
    ```
  - Response:
    ```json
    { "status": "ok" }
    ```
- PATCH `/generative-language-api-key`
  - Request:
    ```bash
    curl -X PATCH -H 'Content-Type: application/json' \
    -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
      -d '{"old":"AIzaSy-1","new":"AIzaSy-1b"}' \
      http://localhost:8317/v0/management/generative-language-api-key
    ```
  - Response:
    ```json
    { "status": "ok" }
    ```
- DELETE `/generative-language-api-key`
  - Request:
    ```bash
    curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' -X DELETE 'http://localhost:8317/v0/management/generative-language-api-key?value=AIzaSy-2'
    ```
  - Response:
    ```json
    { "status": "ok" }
    ```

### Codex API KEY (object array)
- GET `/codex-api-key` — List all
    - Request:
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' http://localhost:8317/v0/management/codex-api-key
      ```
    - Response:
      ```json
      { "codex-api-key": [ { "api-key": "sk-a", "base-url": "", "proxy-url": "" } ] }
      ```
- PUT `/codex-api-key` — Replace the list
    - Request:
      ```bash
      curl -X PUT -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '[{"api-key":"sk-a","proxy-url":"socks5://proxy.example.com:1080"},{"api-key":"sk-b","base-url":"https://c.example.com","proxy-url":""}]' \
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
        -d '{"index":1,"value":{"api-key":"sk-b2","base-url":"https://c.example.com","proxy-url":""}}' \
        http://localhost:8317/v0/management/codex-api-key
      ```
    - Request (by match):
      ```bash
      curl -X PATCH -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '{"match":"sk-a","value":{"api-key":"sk-a","base-url":"","proxy-url":"socks5://proxy.example.com:1080"}}' \
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

### Claude API KEY (object array)
- GET `/claude-api-key` — List all
    - Request:
      ```bash
      curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' http://localhost:8317/v0/management/claude-api-key
      ```
    - Response:
      ```json
      { "claude-api-key": [ { "api-key": "sk-a", "base-url": "", "proxy-url": "" } ] }
      ```
- PUT `/claude-api-key` — Replace the list
    - Request:
      ```bash
      curl -X PUT -H 'Content-Type: application/json' \
      -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '[{"api-key":"sk-a","proxy-url":"socks5://proxy.example.com:1080"},{"api-key":"sk-b","base-url":"https://c.example.com","proxy-url":""}]' \
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
        -d '{"index":1,"value":{"api-key":"sk-b2","base-url":"https://c.example.com","proxy-url":""}}' \
        http://localhost:8317/v0/management/claude-api-key
      ```
  - Request (by match):
    ```bash
    curl -X PATCH -H 'Content-Type: application/json' \
    -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
        -d '{"match":"sk-a","value":{"api-key":"sk-a","base-url":"","proxy-url":"socks5://proxy.example.com:1080"}}' \
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

### OpenAI Compatibility Providers (object array)
- GET `/openai-compatibility` — List all
  - Request:
    ```bash
    curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' http://localhost:8317/v0/management/openai-compatibility
    ```
  - Response:
    ```json
    { "openai-compatibility": [ { "name": "openrouter", "base-url": "https://openrouter.ai/api/v1", "api-key-entries": [ { "api-key": "sk", "proxy-url": "" } ], "models": [] } ] }
    ```
- PUT `/openai-compatibility` — Replace the list
  - Request:
    ```bash
    curl -X PUT -H 'Content-Type: application/json' \
    -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
      -d '[{"name":"openrouter","base-url":"https://openrouter.ai/api/v1","api-key-entries":[{"api-key":"sk","proxy-url":""}],"models":[{"name":"m","alias":"a"}]}]' \
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
      -d '{"name":"openrouter","value":{"name":"openrouter","base-url":"https://openrouter.ai/api/v1","api-key-entries":[{"api-key":"sk","proxy-url":""}],"models":[]}}' \
      http://localhost:8317/v0/management/openai-compatibility
    ```
  - Request (by index):
    ```bash
    curl -X PATCH -H 'Content-Type: application/json' \
    -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
      -d '{"index":0,"value":{"name":"openrouter","base-url":"https://openrouter.ai/api/v1","api-key-entries":[{"api-key":"sk","proxy-url":""}],"models":[]}}' \
      http://localhost:8317/v0/management/openai-compatibility
    ```
  - Response:
    ```json
    { "status": "ok" }
    ```

  - Notes:
    - Legacy `api-keys` input remains accepted; keys are migrated into `api-key-entries` automatically so the legacy field will eventually remain empty in responses.
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

### Auth File Management

Manage JSON token files under `auth-dir`: list, download, upload, delete.

- GET `/auth-files` — List
  - Request:
    ```bash
    curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' http://localhost:8317/v0/management/auth-files
    ```
  - Response:
    ```json
    { "files": [ { "name": "acc1.json", "size": 1234, "modtime": "2025-08-30T12:34:56Z", "type": "google" } ] }
    ```

- GET `/auth-files/download?name=<file.json>` — Download a single file
  - Request:
    ```bash
    curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' -OJ 'http://localhost:8317/v0/management/auth-files/download?name=acc1.json'
    ```

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

- DELETE `/auth-files?name=<file.json>` — Delete a single file
  - Request:
    ```bash
    curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' -X DELETE 'http://localhost:8317/v0/management/auth-files?name=acc1.json'
    ```
  - Response:
    ```json
    { "status": "ok" }
    ```

- DELETE `/auth-files?all=true` — Delete all `.json` files under `auth-dir`
  - Request:
    ```bash
    curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' -X DELETE 'http://localhost:8317/v0/management/auth-files?all=true'
    ```
  - Response:
    ```json
    { "status": "ok", "deleted": 3 }
    ```

### Login/OAuth URLs

These endpoints initiate provider login flows and return a URL to open in a browser. Tokens are saved under `auths/` once the flow completes.

- GET `/anthropic-auth-url` — Start Anthropic (Claude) login
  - Request:
    ```bash
    curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
      http://localhost:8317/v0/management/anthropic-auth-url
    ```
  - Response:
    ```json
    { "status": "ok", "url": "https://..." }
    ```

- GET `/codex-auth-url` — Start Codex login
  - Request:
    ```bash
    curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
      http://localhost:8317/v0/management/codex-auth-url
    ```
  - Response:
    ```json
    { "status": "ok", "url": "https://..." }
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
    { "status": "ok", "url": "https://..." }
    ```

- GET `/qwen-auth-url` — Start Qwen login (device flow)
  - Request:
    ```bash
    curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
      http://localhost:8317/v0/management/qwen-auth-url
    ```
  - Response:
    ```json
    { "status": "ok", "url": "https://..." }
    ```

- GET `/iflow-auth-url` — Start iFlow login
  - Request:
    ```bash
    curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
      http://localhost:8317/v0/management/iflow-auth-url
    ```
  - Response:
    ```json
    { "status": "ok", "url": "https://..." }
    ```

- GET `/get-auth-status?state=<state>` — Poll OAuth flow status
  - Request:
    ```bash
    curl -H 'Authorization: Bearer <MANAGEMENT_KEY>' \
      'http://localhost:8317/v0/management/get-auth-status?state=<STATE_FROM_AUTH_URL>'
    ```
  - Response examples:
    ```json
    { "status": "wait" }
    { "status": "ok" }
    { "status": "error", "error": "Authentication failed" }
    ```

## Error Responses

Generic error format:
- 400 Bad Request: `{ "error": "invalid body" }`
- 401 Unauthorized: `{ "error": "missing management key" }` or `{ "error": "invalid management key" }`
- 403 Forbidden: `{ "error": "remote management disabled" }`
- 404 Not Found: `{ "error": "item not found" }` or `{ "error": "file not found" }`
- 500 Internal Server Error: `{ "error": "failed to save config: ..." }`

## Notes

- Changes are written back to the YAML config file and hot‑reloaded by the file watcher and clients.
- `allow-remote-management` and `remote-management-key` cannot be changed via the API; configure them in the config file.
