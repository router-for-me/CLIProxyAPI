## Why
Copilot 上游令牌有效期较短，当前实现仅依赖通用的 ExpirationTime()/RefreshLead 机制，不会基于 `refresh_in` 主动预刷新，导致接近过期窗口内容易出现请求 401/500 或首次请求成本高。

## What Changes
- Add: 针对 provider=copilot 的“基于 refresh_in 的预刷新”能力，默认在 `refresh_in - safety_margin_seconds` 提前刷新（默认 60 秒）。
- Add: 当 `refresh_in` 缺失时回退到 `expires_at/expired` 与现有 ProviderRefreshLead；两者皆无时维持现状。
- Add: 刷新失败的退避与状态上报逻辑（沿用现有 Manager.MarkResult 语义），并记录 `NextRefreshAfter`，避免紧密重试。
- Add: 可配置项 `copilot.refresh_safety_margin_seconds`（SDKConfig 下），默认 60，范围校验 5–300。
- Add: 统一持久化字段规范：保存 `expires_at`（或 RFC3339 `expired`）、可选 `refresh_in`，确保重启后可恢复计划。
- No breaking change: 仅增加预刷新与配置，保持现有接口与行为兼容。

## Impact
- Specs: auth, provider-integration
- Code:
  - internal/cmd/copilot_login.go（写入 refresh_in/expires_at）
  - sdk/cliproxy/auth/manager.go（调度：基于 refresh_in 的预刷新）
  - internal/runtime/executor/codex_executor.go（仅若需要暴露 refresh_in 到 Metadata）
  - internal/config（新增配置项解析与默认值）
- Ops: 无需迁移；旧凭据在无 refresh_in 时自动回退到原逻辑。
