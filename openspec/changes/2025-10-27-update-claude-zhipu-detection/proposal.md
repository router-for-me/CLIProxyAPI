## Why
当用户通过 `claude-api-key` 将 `base-url` 指向 Zhipu 的 Anthropic 兼容端点（`https://open.bigmodel.cn/api/anthropic`）时，
继续注册 Claude 官方模型会造成 `/v1/models` 列表与实际可用后端的不一致，影响用户选择与路由期望。

## What Changes
- 检测 `provider=claude` 的认证条目上 `attributes.base_url`：
  - 当为 `https://open.bigmodel.cn/api/anthropic`（智谱 Anthropic 兼容端点）：仅在模型注册表登记 `glm-4.6`，且不登记任何 `claude-*` 模型。
  - 当为 `https://api.minimaxi.com/anthropic`（MiniMax Anthropic 兼容端点）：登记 Provider `MiniMax` 与模型 `MiniMax-M2`，且不登记任何 `claude-*` 模型。
  - 其它情形：保持既有 Claude 模型注册行为不变（登记 `claude-*` 清单）。

- 执行器策略（新增）：对于任何 Claude API（`provider=claude`）下识别到的上游 provider（含官方/智谱/MiniMax），系统 SHALL 默认使用 Claude 执行器处理请求（非回退机制，而是默认映射）。

## Impact
- 受影响代码：
  - `sdk/cliproxy/service.go::registerModelsForAuth`（模型注册保持不变）
  - `sdk/cliproxy/service.go::ensureExecutorsForAuth` 与 provider→executor 路由（明确约束为 Claude 执行器默认处理 Claude API 兼容端点）
  - `internal/util/provider.go`（如有需要，撤销“回退”语义，改为“默认选择 Claude 执行器”）
- 受影响接口：`/v1/models`（在 User-Agent 为 `claude-cli` 时使用 Claude handler 列表）。
