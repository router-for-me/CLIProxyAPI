# provider-integration Specification

## Purpose
TBD - created by archiving change add-zhipu-api-support. Update Purpose after archive.
## Requirements
### Requirement: Packycode Provider Exposure (Alias to Codex)
系统 SHALL 将 `packycode` 作为对外 provider 暴露，并在内部执行路径上映射到 Codex 执行器与 OpenAI 模型集合；所有对外列表与筛选（/v1/models、/v0/management/providers、/v0/management/models、/v0/management/tps）均应识别 `packycode`，同时保持既有 Codex 行为不变。

参见：openspec/changes/tps-specified-model/specs/provider-integration/packycode-provider-alias.md

#### Scenario: Management listing and filtering
- **WHEN** 管理端启用
- **THEN**
  - `GET /v0/management/providers` 返回包含 `packycode` 在内的 provider 列表
  - `GET /v0/management/models?provider=packycode` 返回由 `packycode` 提供的模型（模型元数据含 `providers` 列）
  - `GET /v0/management/tps?...&provider=packycode` 支持按 `packycode` 过滤窗口聚合

### Requirement: Zhipu Provider Integration (Direct)
系统 SHALL 在 provider registry 中注册一个 `zhipu` 提供商，占位于执行路径，不改变现有 OpenAI‑compat 行为。

#### Scenario: Provider type registered
- **WHEN** 系统启动并加载 access/sdk 配置
- **THEN** `zhipu` 作为合法提供商类型出现在 registry 中
- **AND** 未配置 `ZHIPU_API_KEY` 时不启用任何直连客户端

#### Scenario: Model mapping coexists
- **GIVEN** 模型 `glm-*` 已通过 OpenAI‑compat 上游可用
- **WHEN** 启用 `zhipu` 提供商
- **THEN** model registry 中 `glm-*` 同时显示 `openai-compat` 与 `zhipu` 两个提供者

#### Scenario: Direct executor (non-stream)
- **GIVEN** 存在 `zhipu-api-key[0]` 且含 `api-key` 与 `base-url`
- **WHEN** 请求路由到 `zhipu` 执行器分支（非流式）
- **THEN** 转换为 OpenAI-compatible chat completions 调用 `${base-url}/chat/completions`
- **AND** 使用 `Authorization: Bearer <api-key>` 与 `Content-Type: application/json`
- **AND** 成功 2xx 时返回翻译后的响应；非 2xx 返回上游错误消息与对应状态码

#### Scenario: Direct executor (stream)
- **GIVEN** 存在 `zhipu-api-key[0]` 且含 `api-key` 与 `base-url`
- **WHEN** 请求路由到 `zhipu` 执行器分支（流式）
- **THEN** 以 SSE 方式转发 `${base-url}/chat/completions` 的流响应
- **AND** 逐行传入翻译器，保留使用量统计并输出流式片段

### Requirement: Provider Model Inventory Exposure (Copilot Rules)
The system SHALL treat `copilot` as an independent provider whose model inventory is not mirrored from OpenAI.

#### Scenario: Copilot-only model visibility
- GIVEN provider `copilot`
- WHEN listing models via `/v1/models` or management API
- THEN the system SHALL expose `gpt-5-mini` and `grok-code-fast-1`
- AND that model SHALL NOT appear under providers `codex`, `openai`, or any OpenAI-compat provider

#### Scenario: Provider filtering behavior
- WHEN requesting `GET /v0/management/models?provider=copilot`
- THEN results SHALL include `gpt-5-mini` with `providers` containing `copilot`
- AND `GET /v0/management/providers` SHALL include `copilot` as a distinct provider

#### Scenario: Copilot inventory available pre-auth (seed)
- GIVEN no copilot auth has been registered
- WHEN the service starts or reloads configuration
- THEN the system SHALL register a seed inventory for provider `copilot` so `/v1/models` can advertise its models
- AND once a real copilot auth is added, the registry entry SHALL be re-registered under that auth ID, superseding the seed

#### Scenario: Copilot token preemptive refresh (refresh_in)
- GIVEN an active copilot auth with metadata.refresh_in and metadata.github_access_token
- WHEN current_time >= (last_refresh + refresh_in - safety_margin)
- THEN the system SHALL invoke the copilot refresh path using GitHub API `/copilot_internal/v2/token`
- AND update `access_token`, `expires_at`, `refresh_in`, and `last_refresh` on success

### Requirement: Claude base_url detection for Zhipu Anthropic compatibility
系统 SHALL 基于 `provider=claude` 的认证条目 `attributes.base_url` 进行上游兼容层识别，以确保模型清单准确反映可用后端。

#### Scenario: base_url equals https://open.bigmodel.cn/api/anthropic
- WHEN `provider=claude` 的认证条目存在且 `attributes.base_url` 等于 `https://open.bigmodel.cn/api/anthropic`
- THEN 系统 SHALL 仅向注册表登记模型 `glm-4.6`
- AND 系统 SHALL 不登记任何 `claude-*` 模型

#### Scenario: base_url is empty or other endpoints
- WHEN `provider=claude` 且 `attributes.base_url` 为空或不等于上述地址
- THEN 系统 SHALL 维持既有逻辑，登记 Claude 官方模型清单（`claude-*`）。

### Requirement: Provider Model Registration
系统 SHALL 基于认证条目的实际后端特征注册可用模型列表，以保证 `/v1/models` 的可用性与准确性。

#### Scenario: Claude base_url points to Zhipu Anthropic compatibility
- WHEN `provider=claude` 的认证条目其 `attributes.base_url` 等于 `https://open.bigmodel.cn/api/anthropic`
- THEN 系统 SHALL 仅注册模型 `glm-4.6`
- AND 系统 SHALL 不注册任何 `claude-*` 模型

#### Scenario: Claude base_url points to MiniMax Anthropic compatibility
- WHEN `provider=claude` 的认证条目其 `attributes.base_url` 等于 `https://api.minimaxi.com/anthropic`
- THEN 系统 SHALL 将 Provider 识别为 `MiniMax`
- AND 系统 SHALL 仅注册模型 `MiniMax-M2`
- AND 系统 SHALL 不注册任何 `claude-*` 模型

#### Scenario: Claude base_url is other endpoints
- WHEN `provider=claude` 且 `attributes.base_url` 为空或不等于上述地址
- THEN 系统 SHALL 按既有逻辑注册 Claude 模型清单（`claude-*`）。

### Requirement: Anthropic-compatible Executor Mapping
系统 SHALL 为 Anthropic 兼容上游采用“专属执行器”映射，而非统一走 Claude 执行器：
- 官方 Claude → ClaudeExecutor（`provider=claude`）
- 智谱兼容 → GlmAnthropicExecutor（`provider=zhipu`）
- MiniMax 兼容 → MiniMaxAnthropicExecutor（`provider=minimax`）

#### Scenario: Route glm-* to zhipu executor
- GIVEN `provider=claude` 的认证条目其 `attributes.base_url` 等于 `https://open.bigmodel.cn/api/anthropic`
- WHEN 请求 `glm-4.6`
- THEN 系统 SHALL 注册/路由至 `provider=zhipu`
- AND SHALL 使用 GlmAnthropicExecutor 完成上游交互

#### Scenario: Route MiniMax-* to minimax executor
- GIVEN `provider=claude` 的认证条目其 `attributes.base_url` 等于 `https://api.minimaxi.com/anthropic`
- WHEN 请求 `MiniMax-M2`
- THEN 系统 SHALL 注册/路由至 `provider=minimax`
- AND SHALL 使用 MiniMaxAnthropicExecutor 完成上游交互

#### Scenario: Official Claude models
- GIVEN `provider=claude` 且 `attributes.base_url` 为空或为官方地址
- WHEN 请求 `claude-*`
- THEN 系统 SHALL 使用 ClaudeExecutor 完成上游交互

