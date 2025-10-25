# copilot-integration Specification Delta

## ADDED Requirements

### Requirement: Copilot Chat Completions 必须使用流式响应

The system SHALL enforce streaming mode (`stream=true`) for all GitHub Copilot chat/completions requests, as the upstream API explicitly rejects non-streaming requests with 400 Bad Request.

**Rationale**: GitHub Copilot chat/completions endpoint does not support `stream=false`. Historical logs and error messages confirm: `{"error":{"message":"Bad request: \"stream\": false is not supported"}}`.

#### Scenario: Streaming enforced for chat/completions
- **GIVEN** a request to provider `copilot` with model `gpt-5-mini`
- **WHEN** the request is routed to CodexExecutor for Copilot
- **THEN** the system SHALL set `stream=true` in the upstream request body
- **AND** SHALL NOT override it to `stream=false`
- **AND** SHALL use `ExecuteStream()` path to handle SSE response

#### Scenario: Non-streaming explicitly rejected
- **GIVEN** upstream Copilot API requirement
- **WHEN** attempting to send `stream=false` in request body
- **THEN** upstream SHALL return 400 Bad Request
- **AND** error message SHALL contain "stream: false is not supported"

#### Scenario: SSE format handling
- **GIVEN** a streaming response from Copilot chat/completions
- **WHEN** processing SSE events
- **THEN** the system SHALL correctly parse `event:` and `data:` prefixes
- **AND** SHALL extract JSON payloads from `data:` lines
- **AND** SHALL handle `[DONE]` termination signal

---

### Requirement: Copilot 端点选择优先级

The system SHALL determine the Copilot chat/completions endpoint using a priority-based fallback mechanism, ensuring correct routing regardless of token format or configuration.

**Rationale**: Copilot tokens may embed proxy endpoint hints (`proxy-ep=<host>`), and users may override base URLs via configuration.

#### Scenario: Endpoint priority chain
- **GIVEN** a Copilot auth record
- **WHEN** determining the upstream endpoint
- **THEN** the system SHALL select in this order:
  1. `auth.Attributes["base_url"]` (explicit configuration)
  2. `auth.Metadata["base_url"]` (metadata override)
  3. Derived from `auth.Metadata["access_token"]` via `proxy-ep=<host>` parsing
  4. Default: `https://api.githubcopilot.com`
- **AND** SHALL append `/chat/completions` to the base URL

#### Scenario: Token-embedded proxy endpoint parsing
- **GIVEN** a Copilot access token containing `proxy-ep=proxy.individual.githubcopilot.com;`
- **WHEN** parsing the token via `deriveCopilotBaseFromToken()`
- **THEN** the system SHALL extract `proxy.individual.githubcopilot.com`
- **AND** SHALL construct `https://proxy.individual.githubcopilot.com/backend-api/codex` as base URL
- **AND** SHALL strip `/backend-api/codex` suffix when building chat/completions endpoint
- **AND** final endpoint SHALL be `https://proxy.individual.githubcopilot.com/chat/completions`

#### Scenario: Invalid codex path cleanup
- **GIVEN** auth.Attributes["base_url"] = `https://example.com/backend-api/codex`
- **WHEN** registering Copilot auth via `applyCoreAuthAddOrUpdate()`
- **THEN** the system SHALL delete `auth.Attributes["base_url"]`
- **AND** SHALL fall back to default endpoint or token-derived endpoint
- **RATIONALE**: `/backend-api/codex` serves Codex Responses API, not chat/completions

---

### Requirement: Copilot OAuth 认证流程规范

The system SHALL implement GitHub Device Flow OAuth to obtain Copilot access tokens, following the official GitHub authentication specification.

**Rationale**: Copilot uses a two-tier OAuth model: GitHub Device Flow grants GitHub tokens, which are then exchanged for Copilot tokens.

#### Scenario: Device Flow initiation
- **GIVEN** user invokes `--copilot-auth-login`
- **WHEN** `DoCopilotAuthLogin()` executes
- **THEN** the system SHALL:
  1. POST to `${GitHubBaseURL}/login/device/code` with `client_id` and `scope`
  2. Receive `device_code`, `user_code`, and `verification_uri`
  3. Display `user_code` and open browser to `verification_uri`
  4. Poll `${GitHubBaseURL}/login/oauth/access_token` until user authorizes
  5. Receive `github_access_token`

#### Scenario: Copilot token exchange
- **GIVEN** a valid `github_access_token`
- **WHEN** exchanging for Copilot token
- **THEN** the system SHALL:
  1. GET `${GitHubAPIBaseURL}/copilot_internal/v2/token`
  2. Set header `Authorization: token <github_access_token>`
  3. Receive JSON with `token`, `expires_at`, `refresh_in`
  4. Store as `auth.Metadata["access_token"]`, `auth.Metadata["expires_at"]`, `auth.Metadata["refresh_in"]`

#### Scenario: Token persistence
- **GIVEN** a successful Copilot token exchange
- **WHEN** persisting auth record
- **THEN** the system SHALL serialize to JSON file with structure:
  ```json
  {
    "access_token": "<copilot_token>",
    "github_access_token": "<github_token>",
    "expires_at": 1234567890,
    "refresh_in": 28800
  }
  ```
- **AND** file SHALL be named `copilot-<timestamp>.json`

---

### Requirement: Copilot Token 预刷新策略

The system SHALL proactively refresh Copilot tokens before expiration using the `refresh_in` metadata field provided by GitHub API.

**Rationale**: Copilot tokens have short lifetimes (typically 8 hours) and require preemptive refresh to avoid auth failures.

#### Scenario: Refresh timing calculation
- **GIVEN** auth with `metadata.refresh_in = 28800` (8 hours) and `RefreshSafetyMarginSeconds = 60`
- **WHEN** checking `shouldRefresh()` at time `T`
- **THEN** refresh SHALL be triggered when `T >= last_refresh + (28800 - 60) seconds`
- **AND** new token SHALL be fetched via GitHub API

#### Scenario: Refresh execution
- **GIVEN** a Copilot auth requiring refresh
- **WHEN** `Refresh()` is invoked
- **THEN** the system SHALL:
  1. Extract `github_access_token` from `auth.Metadata`
  2. GET `${GitHubAPIBaseURL}/copilot_internal/v2/token` with GitHub token
  3. Update `auth.Metadata["access_token"]`, `auth.Metadata["refresh_in"]`, `auth.Metadata["expires_at"]`
  4. Persist updated auth record

#### Scenario: Refresh failure handling
- **GIVEN** a refresh request that fails (e.g., GitHub token expired)
- **WHEN** `Refresh()` returns error
- **THEN** the system SHALL log the error
- **AND** SHALL mark auth as disabled or retry based on error type
- **AND** SHALL NOT invalidate existing access token immediately (allow graceful degradation)

---

### Requirement: Copilot 请求头规范

The system SHALL include all required HTTP headers when making requests to GitHub Copilot chat/completions endpoint, as specified by the official API.

**Rationale**: Missing or incorrect headers may cause authentication failures or protocol errors.

#### Scenario: Mandatory headers for chat/completions
- **GIVEN** a Copilot chat/completions request
- **WHEN** building HTTP request via `applyCodexHeaders()`
- **THEN** the system SHALL set:
  - `Authorization: Bearer <access_token>`
  - `Content-Type: application/json`
  - `Accept: application/json` (for non-streaming) or `text/event-stream` (for streaming)
  - `user-agent: GitHubCopilotChat/0.26.7`
  - `editor-version: vscode/1.0`
  - `editor-plugin-version: copilot-chat/0.26.7`
  - `openai-intent: conversation-panel`
  - `x-github-api-version: 2025-04-01`
  - `x-request-id: <UUID>`

#### Scenario: User-Agent version consistency
- **GIVEN** upstream Copilot API version requirements
- **WHEN** updating user-agent header
- **THEN** version string SHALL match observed working versions from official clients
- **AND** SHALL be configurable if GitHub updates requirements

---

### Requirement: Copilot 支持的模型清单

The system SHALL maintain an accurate inventory of Copilot-specific models that are not available through other providers.

**Rationale**: Copilot models are independent from OpenAI/Codex model pools and should not be mirrored.

#### Scenario: Copilot-only models
- **GIVEN** provider `copilot`
- **WHEN** listing models via `/v1/models` or management API
- **THEN** the system SHALL expose:
  - `gpt-5-mini`
  - `grok-code-fast-1`
- **AND** these models SHALL NOT appear under `openai`, `codex`, or `openai-compat` providers

#### Scenario: Model registry seeding
- **GIVEN** no copilot auth registered
- **WHEN** service starts
- **THEN** the system SHALL register seed models for provider `copilot`
- **AND** SHALL use placeholder `copilot:models:seed` as client ID
- **AND** once real auth is added, SHALL re-register with actual auth ID

---

### Requirement: Copilot 错误处理规范

The system SHALL handle Copilot-specific error responses according to HTTP status codes and provide meaningful error messages.

**Rationale**: Proper error handling improves debugging and user experience.

#### Scenario: 400 Bad Request (stream parameter error)
- **GIVEN** upstream returns 400 with body `{"error":{"message":"Bad request: \"stream\": false is not supported"}}`
- **WHEN** processing error response
- **THEN** the system SHALL return `statusErr{code: 400, msg: <upstream body>}`
- **AND** SHALL log error with context

#### Scenario: 401 Unauthorized (invalid token)
- **GIVEN** upstream returns 401
- **WHEN** processing error response
- **THEN** the system SHALL return `statusErr{code: 401, msg: "Invalid API key"}`
- **AND** SHALL NOT retry immediately
- **AND** MAY trigger token refresh if configured

#### Scenario: 429 Too Many Requests (rate limit)
- **GIVEN** upstream returns 429
- **WHEN** processing error response
- **THEN** the system SHALL return `statusErr{code: 429, msg: <upstream body>}`
- **AND** SHALL NOT implement automatic retry (leave to client)

#### Scenario: 500 Internal Server Error
- **GIVEN** upstream returns 5xx
- **WHEN** processing error response
- **THEN** the system SHALL return `statusErr{code: <actual code>, msg: <upstream body>}`
- **AND** SHALL log full error details for debugging

---

### Requirement: Copilot 配置结构规范

The system SHALL define clear YAML configuration structure for Copilot OAuth settings.

**Rationale**: Consistent configuration reduces setup errors.

#### Scenario: CopilotOAuth config structure
- **GIVEN** user configures Copilot in YAML
- **WHEN** loading config
- **THEN** the system SHALL recognize fields:
  ```yaml
  copilot-oauth:
    auth-url: "https://github.com/login/oauth/authorize"
    token-url: "https://github.com/login/oauth/access_token"
    client-id: "<GitHub OAuth App Client ID>"
    redirect-port: 54556
    scope: "openid email profile offline_access"
    github-base-url: "https://github.com"
    github-api-base-url: "https://api.github.com"
    github-client-id: "<GitHub Device Flow Client ID>"
  ```

#### Scenario: Configuration defaults
- **GIVEN** missing optional fields
- **WHEN** `sanitizeCopilotOAuth()` is called
- **THEN** the system SHALL apply defaults:
  - `redirect-port: 54556`
  - `scope: "openid email profile offline_access"`
  - `github-base-url: "https://github.com"`
  - `github-api-base-url: "https://api.github.com"`

#### Scenario: RefreshSafetyMarginSeconds
- **GIVEN** config field `copilot.refresh-safety-margin-seconds`
- **WHEN** calculating refresh timing
- **THEN** the system SHALL subtract this value from `refresh_in`
- **AND** default SHALL be 60 seconds

---

## MODIFIED Requirements

### Requirement: Provider Model Inventory Exposure (Copilot Rules)

**Updated**: Clarified that Copilot chat/completions endpoint requires streaming.

The system SHALL treat `copilot` as an independent provider whose model inventory is not mirrored from OpenAI, **and enforce streaming mode for all chat/completions requests**.

#### Scenario: Copilot streaming enforcement (ADDED)
- **GIVEN** a request to provider `copilot`
- **WHEN** executing via CodexExecutor
- **THEN** the system SHALL set `stream=true`
- **AND** SHALL use `ExecuteStream()` pathway
- **AND** SHALL NOT force `stream=false`

*(Other scenarios from provider-integration spec remain unchanged)*

---

## Implementation Notes

### Code Locations
- **Stream fix**: `internal/runtime/executor/codex_executor.go:88`
- **Endpoint selection**: `internal/runtime/executor/codex_executor.go:65-85`
- **Token parsing**: `internal/runtime/executor/codex_executor.go:541-571`
- **OAuth flow**: `cmd/server/copilot_login.go:26-208`
- **Token refresh**: `sdk/cliproxy/auth/manager.go` (shouldRefresh, Refresh)
- **Headers**: `internal/runtime/executor/codex_executor.go:85-101`

### Testing Recommendations
1. Unit tests for `deriveCopilotBaseFromToken()` with various token formats
2. Integration tests for OAuth Device Flow (mock GitHub API)
3. E2E tests for chat/completions streaming (mock Copilot API)
4. Regression tests for token refresh timing
5. Error handling tests for all HTTP status codes

### Security Considerations
- Token storage: Ensure file permissions restrict access to auth JSON files
- Log masking: Extend to `metadata.access_token` and `metadata.github_access_token`
- HTTPS enforcement: All Copilot API calls must use HTTPS

### Migration Notes
- Existing configurations: No breaking changes
- Existing auth records: Compatible with new logic
- Existing logs: May show corrected behavior (stream=true instead of stream=false)
