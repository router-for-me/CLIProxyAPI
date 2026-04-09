# PR 总结（结构化版）

## 标题

补齐 Antigravity 超细 429 状态机，并改成 credits 全自动处理

## 背景

当前官方仓库在 Antigravity 的 429 处理上，只有基础的 fallback / retryAfter / no capacity / credits fallback 逻辑，缺少 Plus 版已经具备的超细 429 状态机能力。

这会导致：

- 不同类型的 429 被混成同一种处理
- 缺少 short cooldown、soft retry、same-auth instant retry 等机制
- 积分失败多次后仍有配置残留，行为不够收敛
- credits 请求失败可能影响整体请求处理体验

## 目标

本次改动的目标是：

1. 将 Plus 版核心的超细 429 状态机补齐到官方仓库
2. 删除 credits 失败策略配置，改成代码全自动处理
3. 避免 credits 失败直接污染主失败链
4. 修复服务器本地编译与部署过程中暴露的语法问题
5. 对明确余额不足错误进行更强硬、更准确的自动处理

## 主要改动

### 1. 补齐 Antigravity 超细 429 状态机

在 `internal/runtime/executor/antigravity_executor.go` 中新增并接入：

- `decideAntigravity429(...)`
- `classifyAntigravity429(...)`

支持以下状态分类：

- `soft_retry`
- `instant_retry_same_auth`
- `short_cooldown_switch_auth`
- `full_quota_exhausted`

### 2. 增加 same-auth instant retry

当 429 被识别为极短限流时：

- 不立即失败
- 不立即切换 auth
- 而是同 auth 短等待后重试

### 3. 增加 short cooldown 机制

新增：

- `antigravityShortCooldownKey(...)`
- `antigravityIsInShortCooldown(...)`
- `markAntigravityShortCooldown(...)`

当某个 auth + model 命中短冷却时，会提前返回带 `retryAfter` 的 429，便于上层切换账号或等待。

### 4. 增加 soft rate limit retry

新增：

- `antigravityShouldRetrySoftRateLimit(...)`
- `antigravitySoftRateLimitDelay(...)`

当 429 属于 soft rate limit 时，会走短退避重试，而不是直接进入硬失败路径。

### 5. 三条执行链统一接入

以下执行路径都已统一接入超细 429 状态机：

- `Execute(...)`
- `executeClaudeNonStream(...)`
- `ExecuteStream(...)`

### 6. credits 失败策略改成全自动

移除配置字段：

- `antigravity-credits-fail-threshold`
- `antigravity-credits-fail-strategy`
- `antigravity-credits-disable-minutes`

改为固定自动策略：

- 明确的 `INSUFFICIENT_G1_CREDITS_BALANCE`：直接永久停用当前 auth 的积分尝试
- 其他 credits 失败：自动进入固定时长停用窗口，避免持续浪费 credits 尝试
- credits 成功后会清理失败状态，恢复正常偏好逻辑

### 7. 明确余额不足错误直接永久停用积分

新增对 `INSUFFICIENT_G1_CREDITS_BALANCE` 的特殊处理：

- 一旦在 429 的 `error.details[].reason` 中命中该值
- 直接视为积分余额明确不足
- 不再依赖任何 credits 失败配置字段
- 直接永久停用当前 auth 的积分尝试
- 清除 `preferCredits`
- 当前请求优先回退普通模式，不再继续浪费 credits 尝试

### 8. 优化 credits 失败后的处理方式

保留并强化当前逻辑：

- 普通 `RESOURCE_EXHAUSTED` 继续走现有 429 状态机和自动 credits 策略
- 明确的 `INSUFFICIENT_G1_CREDITS_BALANCE` 由代码自动硬处理
- credits 失败不直接作为主请求最终失败
- 优先回退普通请求路径
- 避免 credits 失败直接扩大成整体调度失败

### 9. 修复服务器编译报错

修复了 `internal/runtime/executor/antigravity_executor.go` 中函数结构闭合问题，解决了服务器本地 `go build` 时报出的语法错误。

## 涉及文件

- `internal/runtime/executor/antigravity_executor.go`
- `internal/config/config.go`
- `config.example.yaml`
- `docs/pr-antigravity-429-summary.md`

## 自动处理说明

```yaml
quota-exceeded:
  antigravity-credits: true
```

说明：

- 不再提供 temporary / permanent 的配置切换
- 明确余额不足走永久停用
- 其他 credits 失败由代码自动按固定策略处理

## 风险点

- 超细 429 状态机引入后，执行路径变复杂，必须确保三条执行链行为一致
- short cooldown 与自动 credits 策略并存时，要避免相互覆盖或误判
- 编译部署时若源码目录不完整或函数结构有误，会直接导致 `go build` 失败
- 明确余额不足错误被提级为硬规则后，相关 auth 的积分通道会被直接永久停用

### 10. 秒级 instant retry 等待规则继续收紧

针对实际线上出现的秒级 `RATE_LIMIT_EXCEEDED` 边界抖动问题，进一步调整 instant retry 的等待策略：

- 不再使用模糊缓冲描述
- 明确以 `RetryInfo.retryDelay` 的原始解析值作为基准
- 当前实际等待时间为：`retryDelay + 800ms`
- 例如：
  - `retryDelay = 0.467174873s` -> 实际等待 `1.267174873s`
  - `retryDelay = 0.606037544s` -> 实际等待 `1.406037544s`

这样做的原因是：

- `error.message` 中的 `after 0s / 1s` 只是展示文案，不能作为准确冷却依据
- 真正可靠的冷却值应取 `RetryInfo.retryDelay`
- 秒级窗口边界存在轻微抖动，按原值裸等或只加较小缓冲时，仍可能偶发再次撞上 429
- 提升到 `+800ms` 后，更适合当前线上波动场景

## 验证结果

已完成：

- 服务器安装 Go 编译环境
- 完整源码重新上传到服务器
- 服务器本地成功执行 `go build -o /tmp/CLIProxyAPI.build ./cmd/server`
- 替换 `/opt/cliproxyapi/CLIProxyAPI`
- 重启 `cliproxyapi.service`
- 健康检查通过：`/healthz -> {"status":"ok"}`

## 最终效果

本次 PR 合并后，官方仓库将具备：

- Plus 版核心的超细 429 状态判断能力
- 更合理的 short cooldown / soft retry / instant retry 行为
- credits 全自动处理，不再暴露额外配置项
- 对明确积分余额不足错误的自动永久停用能力
- 秒级 instant retry 已改为严格按 `retryDelay + 800ms` 等待，进一步压制秒级边界 429 外抛

- 已验证可在服务器端完成编译、替换与运行
