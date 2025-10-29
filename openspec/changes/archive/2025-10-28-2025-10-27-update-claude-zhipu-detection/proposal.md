## Why
当用户通过 `claude-api-key` 将 `base-url` 指向 Zhipu 的 Anthropic 兼容端点（`https://open.bigmodel.cn/api/anthropic`）时，
继续注册 Claude 官方模型会造成 `/v1/models` 列表与实际可用后端的不一致，影响用户选择与路由期望。

## What Changes
- 检测 `provider=claude` 的认证条目上 `attributes.base_url`：
  - 当为 `https://open.bigmodel.cn/api/anthropic`（智谱 Anthropic 兼容端点）：登记 `glm-4.6`；不登记任何 `claude-*` 模型。
  - 当为 `https://api.minimaxi.com/anthropic`（MiniMax Anthropic 兼容端点）：登记 `MiniMax-M2`；不登记任何 `claude-*` 模型。
  - 其它情形：保持既有 Claude 模型注册行为不变（登记 `claude-*` 清单）。

- 执行器架构（更新）：引入 AnthropicCompatExecutor 基类，并将 Anthropic 兼容上游分离为专属执行器：
  - 官方 Claude：ClaudeExecutor（identifier=`claude`）
  - 智谱兼容：GlmAnthropicExecutor（identifier=`zhipu`）
  - MiniMax 兼容：MiniMaxAnthropicExecutor（identifier=`minimax`）
  系统 SHALL 依据上游归属路由到对应执行器（非“默认 Claude”，亦非“回退”）。

## Impact
- 受影响代码：
  - `internal/runtime/executor/` 新增 AnthropicCompatExecutor 及两个具体执行器（zhipu/minimax）。
  - `sdk/cliproxy/service.go::registerModelsForAuth`：Zhipu/MiniMax 模型注册回归到各自 provider（`zhipu`/`minimax`）。
  - `sdk/cliproxy/service.go::ensureExecutorsForAuth`：当 `provider=claude` 但 `base_url` 指向兼容端点时，合成运行时 auth（provider 改写为 `zhipu`/`minimax`）并注册对应执行器。
  - `internal/util/provider.go`：provider 选择与模型前缀严格映射（`glm-*`→`zhipu`，`minimax-*`→`minimax`，`claude-*`→`claude`）。
- 受影响接口：`/v1/models`（展示不受影响；Claude/OpenAI handler 均从注册表读取可用清单）。
