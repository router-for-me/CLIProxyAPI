## MODIFIED Requirements

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
