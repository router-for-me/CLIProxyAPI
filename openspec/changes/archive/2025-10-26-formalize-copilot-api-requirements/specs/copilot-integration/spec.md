# copilot-integration Specification Delta

## ADDED Requirements

### Requirement: Copilot 执行器独立

The system SHALL route every Copilot request through a dedicated executor rather than the shared Codex executor.

**Rationale**: Copilot 与 Codex 拥有不同的协议细节与模型列表，解耦可以降低维护风险。

#### Scenario: Provider-based executor selection
- **GIVEN** the auth manager selects provider `copilot`
- **WHEN** invoking the execution pipeline
- **THEN** the system SHALL instantiate or reuse `CopilotExecutor`
- **AND** SHALL NOT invoke `CodexExecutor` for the same request

#### Scenario: Codex 路径保持独立
- **GIVEN** a request targeting Codex/OpenAI 模型
- **WHEN**执行执行器选择
- **THEN** the system SHALL continue调用 `CodexExecutor`
- **AND** Copilot 执行器 SHALL NOT register itself as provider for非 Copilot 模型

---

### Requirement: Copilot Chat Completions 使用流式传输

The system SHALL always negotiate streaming transport with the Copilot chat/completions endpoint (`stream=true`) and correctly consume upstream Server-Sent Events (SSE).

**Rationale**: The GitHub Copilot chat/completions API rejects `stream=false` requests. Local callers may still prefer non-streaming semantics; the proxy must bridge this gap by aggregating the SSE stream.

#### Scenario: Upstream streaming negotiated
- **GIVEN** any request routed to provider `copilot`
- **WHEN** building the upstream payload
- **THEN** the system SHALL set `stream=true`
- **AND** SHALL send the request with `Accept: text/event-stream`

#### Scenario: Non-streaming caller support
- **GIVEN** a caller request with `stream=false`
- **WHEN** the upstream SSE completes with a `response.completed` event
- **THEN** the system SHALL aggregate the stream into a single chat completion payload
- **AND** SHALL return HTTP 200 with the OpenAI-style JSON body

#### Scenario: Streaming caller propagation
- **GIVEN** a caller request with `stream=true`
- **WHEN** the upstream emits SSE events
- **THEN** the system SHALL translate each chunk into OpenAI-compatible streaming events
- **AND** SHALL forward them to the caller until `[DONE]`

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

### Requirement: Copilot 请求头语义

The system SHALL attach Copilot-specific HTTP headers that reflect caller intent and upstream requirements.

#### Scenario: Streaming response headers
- **GIVEN** a Copilot chat/completions request
- **WHEN** constructing upstream headers
- **THEN** the system SHALL set `Content-Type: application/json`
- **AND** SHALL set `Accept: text/event-stream`
- **AND** SHALL include GitHub Copilot client headers (`user-agent`, `editor-version`, `editor-plugin-version`, `openai-intent`, `x-github-api-version`, `x-request-id`)

#### Scenario: Agent initiator detection
- **GIVEN** the translated payload contains any `assistant` or `tool` role messages
- **WHEN** preparing headers
- **THEN** the system SHALL set `X-Initiator: agent`
- **ELSE** it SHALL set `X-Initiator: user`

#### Scenario: Vision payload hint
- **GIVEN** the payload includes any image content blocks
- **WHEN** preparing headers
- **THEN** the system SHALL set `copilot-vision-request: true`

---

### Requirement: Copilot Anthropic 兼容桥接

The system SHALL expose the Copilot provider through the Anthropic Messages facade so that Claude Code clients can consume Copilot-specific models such as `gpt-5-mini`.

#### Scenario: Anthropic → Copilot translation
- **GIVEN** an Anthropic Messages payload targeting `gpt-5-mini`
- **WHEN** routing through the Anthropic handler
- **THEN** the system SHALL translate messages, tool calls, and metadata into OpenAI chat/completions format without altering the model identifier
- **AND** SHALL forward the translated payload to the Copilot executor

#### Scenario: Copilot → Anthropic streaming translation
- **GIVEN** an upstream Copilot SSE stream
- **WHEN** serving an Anthropic client
- **THEN** the system SHALL translate each Copilot chunk into Anthropic-compatible SSE events
- **AND** SHALL emit a final Anthropic message once the upstream sends `[DONE]`

---

### Requirement: Copilot 请求头规范

The system SHALL include all required HTTP headers when making requests to GitHub Copilot chat/completions endpoint, as specified by the official API.

**Rationale**: Missing or incorrect headers may cause authentication failures or protocol errors.

#### Scenario: Mandatory headers for chat/completions
- **GIVEN** a Copilot chat/completions request
- **WHEN** the Copilot executor builds the upstream HTTP request
- **THEN** the system SHALL set:
  - `Authorization: Bearer <access_token>`
  - `Content-Type: application/json`
  - `Accept: application/json` (for non-streaming) or `text/event-stream` (for streaming)
  - `user-agent: GitHubCopilotChat/0.26.7`
  - `editor-version: vscode/1.0`
  - `editor-plugin-version: copilot-chat/0.26.7`
  - `copilot-integration-id: vscode-chat`
  - `openai-intent: conversation-panel`
  - `x-github-api-version: 2025-04-01`
  - `x-request-id: <UUID>`
  - `x-vscode-user-agent-library-version: electron-fetch`

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
- **WHEN** listing models via `/v1/models` 或管理端
- **THEN** the system SHALL expose upstream `/models` 返回的可用模型，至少包含：
  - `gpt-5-mini`
  - `grok-code-fast-1`
  - `gpt-5`
  - `gpt-4.1`
  - `gpt-4`
  - `gpt-4o-mini`
  - `gpt-3.5-turbo`
- **AND** 当 upstream 暴露额外预览或企业模型时，系统 SHOULD 及时同步
- **AND** 上述模型 SHALL NOT 出现在 `openai`、`codex` 或 `openai-compat` provider 下

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

### Requirement: Copilot Chat Completions Payload 兼容性

The system SHALL preserve structured message content and tool metadata when translating to Copilot chat/completions payloads.

**Rationale**: 官方 Copilot API 支持富文本、图像及工具调用格式；代理需要完整透传以保持兼容性。

#### Scenario: Structured content blocks
- **GIVEN** a caller message whose `content` 字段为数组（包含 `type: "text"` 或 `type: "image_url"`）
- **WHEN** 构建 Copilot 请求
- **THEN** the system SHALL 原样复制数组及各项字段
- **AND** SHALL 触发 vision header (`copilot-vision-request: true`) 当存在图像块时

#### Scenario: Tool call propagation
- **GIVEN** caller payload包含 `tool_calls` 或 `tool_choice`
- **WHEN** 翻译为 Copilot 请求
- **THEN** the system SHALL 复制函数名与 JSON 字符串参数
- **AND** SHALL 确保 Copilot 返回的工具调用以 OpenAI 结构映射回调用方

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
