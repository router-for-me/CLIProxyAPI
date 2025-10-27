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

### Requirement: Executor Mapping for Claude API
系统 SHALL 对于任何由 `provider=claude` 派生的兼容端点（含官方/智谱/MiniMax），默认使用 Claude 执行器对外提供服务（非回退机制）。

#### Scenario: Use Claude executor for Zhipu-compatible endpoint
- GIVEN `provider=claude` 的认证条目指向 `https://open.bigmodel.cn/api/anthropic`
- WHEN 通过 Claude 或 OpenAI 兼容入口请求对应模型（如 `glm-4.6`）
- THEN 系统 SHALL 使用 Claude 执行器处理请求
- AND 请求通过该 base_url 完成上游交互

#### Scenario: Use Claude executor for MiniMax-compatible endpoint
- GIVEN `provider=claude` 的认证条目指向 `https://api.minimaxi.com/anthropic`
- WHEN 通过 Claude 或 OpenAI 兼容入口请求对应模型（如 `MiniMax-M2`）
- THEN 系统 SHALL 使用 Claude 执行器处理请求
- AND 请求通过该 base_url 完成上游交互
