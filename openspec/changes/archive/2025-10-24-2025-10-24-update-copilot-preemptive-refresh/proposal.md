## Why
Copilot 上游令牌有效期较短，当前实现仅依赖通用的 ExpirationTime()/RefreshLead 机制，不会基于 `refresh_in` 主动预刷新，导致接近过期窗口内容易出现请求 401/500 或首次请求成本高。

## What Changes
- Add: 针对 provider=copilot 的“基于 refresh_in 的预刷新”能力，默认在 `refresh_in - safety_margin_seconds` 提前刷新（默认 60 秒）。【已实现】
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

---

## ADDED Requirements (provider-integration)

#### Requirement: Copilot 预刷新（refresh_in + safety margin）
- GIVEN 一个 provider=copilot 的活跃凭据，且 `metadata.refresh_in` 为正整数秒
- AND `metadata.last_refresh` 存在（RFC3339）或 `LastRefreshedAt` 非零
- AND 配置项 `copilot.refresh_safety_margin_seconds` 处于 [5,300] 区间（默认 60）
- WHEN 当前时间到达 `last_refresh + refresh_in - safety_margin`
- THEN 系统 SHALL 触发一次预刷新（无需外部请求），并调用 Copilot 刷新端点以获取新 `access_token`
- AND 更新 `metadata.access_token`、`metadata.expires_at`（秒或毫秒原样）与 `metadata.refresh_in`
- AND 成功后清空 `NextRefreshAfter` 并写入最新 `last_refresh`

#### Scenario: 触发预刷新并成功
- GIVEN `refresh_in=1500` 且 `last_refresh` 为两分钟前，`safety_margin=60`
- WHEN 现在时间已晚于 `last_refresh + 1500 - 60`
- THEN Manager 进入刷新流程，Executor 使用 `github_access_token` 调用上游，返回新 token
- AND Auth 元数据与持久化文件被更新，下一轮按照新的 refresh_in 计算

#### Scenario: 刷新失败退避
- GIVEN 上游返回 5xx 或网络错误
- WHEN 刷新失败
- THEN 系统 SHALL 将 `NextRefreshAfter` 设为当前时间 + 5 分钟（refreshFailureBackoff）
- AND 直到退避窗口结束前 SHALL NOT 重复刷新

---

## ADDED Requirements (auth)

#### Requirement: 持久化 Copilot 刷新所需字段
- WHEN 通过登录或设备流获取 Copilot token
- THEN 系统 SHALL 在存储中持久化以下字段（JSON 根级）：
  - `type`="copilot"
  - `access_token`（上游返回 token）
  - `expires_at`（上游原样：秒或毫秒）
  - `refresh_in`（秒）
  - `expired`（RFC3339，兼容已存在消费者）
  - `github_access_token`（GitHub 设备流 access token，用于后续刷新）

#### Scenario: 设备流完成后写盘
- GIVEN 完整设备流，已获得 `token/expires_at/refresh_in` 与 GitHub `access_token`
- WHEN 后端保存凭据文件
- THEN 写入上述字段，后续服务重启或热加载后即可恢复预刷新计划
