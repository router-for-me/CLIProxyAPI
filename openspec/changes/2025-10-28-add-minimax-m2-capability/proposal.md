## Why
将 MiniMax 的 Anthropic 兼容接入（MiniMax-M2）从混合变更中拆分为独立的变更单，明确其注册与路由规则，避免在 `provider=claude` 但 `base_url` 指向 MiniMax 兼容端点时继续暴露 `claude-*` 模型，导致 `/v1/models` 与实际后端不一致。

## What Changes
- 检测 `provider=claude` 的认证条目上 `attributes.base_url`：
  - 当为 `https://api.minimaxi.com/anthropic`（MiniMax Anthropic 兼容端点）：仅登记 `MiniMax-M2`；不登记任何 `claude-*` 模型；Provider 视为 `minimax`。
- 路由规则：
  - `MiniMax-M2` SHALL 路由至 `provider=minimax` 并由 MiniMax 专属执行器处理。
  - 启发式回退：`minimax-*` 前缀 SHALL 推断 Provider 为 `minimax`。
- 执行器：
  - 预注册 MiniMax Anthropic 兼容执行器，避免在未配置认证时出现“执行器缺失”错误。

## Impact
- Affected specs: `specs/provider-integration/spec.md`
- Affected code:
  - `sdk/cliproxy/service.go`（`registerModelsForAuth` MiniMax 兼容端点检测与仅登记 `MiniMax-M2`）
  - `internal/util/provider.go`（`minimax-*` 前缀 → `minimax` 启发式）
  - `sdk/cliproxy/builder.go`（预注册 Anthropic 兼容执行器）
- Tests: 路由/可见性/注册用例已覆盖（见 tasks.md）

## ADDED Requirements

### Requirement: MiniMax Anthropic-compatible registration and routing
- 系统 SHALL 基于 `provider=claude` + `attributes.base_url=https://api.minimaxi.com/anthropic` 的认证条目，仅登记 `MiniMax-M2`，并视为 `provider=minimax`。
- 系统 SHALL 不登记任何 `claude-*` 模型。
- 系统 SHALL 将 `minimax-*` 前缀模型路由至 `provider=minimax`（启发式回退）。

#### Scenario: Claude base_url points to MiniMax Anthropic compatibility
- **WHEN** `provider=claude` 且 `attributes.base_url` 为 `https://api.minimaxi.com/anthropic`
- **THEN** `/v1/models` 列表中包含 `MiniMax-M2` 且不包含任何 `claude-*`
- **AND** 对 `MiniMax-M2` 的请求由 `minimax` 执行器处理
