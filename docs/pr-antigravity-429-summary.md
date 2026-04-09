# PR 总结（结构化版）

## 标题
补齐 Antigravity 超细 429 状态机，并完善积分失败多次策略

## 背景
当前官方仓库在 Antigravity 的 429 处理上，只有基础的 fallback / retryAfter / no capacity / credits fallback 逻辑，缺少 Plus 版已经具备的超细 429 状态机能力。

这会导致：
- 不同类型的 429 被混成同一种处理
- 缺少 short cooldown、soft retry、same-auth instant retry 等机制
- 积分失败多次后没有足够灵活的配置化策略
- credits 请求失败可能影响整体请求处理体验

## 目标
本次改动的目标是：
1. 将 Plus 版核心的超细 429 状态机补齐到官方仓库
2. 保留并扩展 credits 失败多次策略的配置化能力
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

### 6. 保留并扩展 credits 失败多次策略
在配置中新增字段：
- `antigravity-credits-fail-threshold`
- `antigravity-credits-fail-strategy`
- `antigravity-credits-disable-minutes`

支持两种策略：
- `temporary`：限时尝试
- `permanent`：永久禁止

### 7. 明确余额不足错误直接永久停用积分
新增对 `INSUFFICIENT_G1_CREDITS_BALANCE` 的特殊处理：
- 一旦在 429 的 `error.details[].reason` 中命中该值
- 直接视为积分余额明确不足
- 跳过 `antigravity-credits-fail-threshold / antigravity-credits-fail-strategy / antigravity-credits-disable-minutes` 配置判断
- 直接永久停用当前 auth 的积分尝试
- 清除 `preferCredits`
- 当前请求优先回退普通模式，不再继续浪费 credits 尝试

### 8. 优化 credits 失败后的处理方式
保留并强化当前逻辑：
- 普通 `RESOURCE_EXHAUSTED` 继续走现有 429 状态机和配置化策略
- 明确的 `INSUFFICIENT_G1_CREDITS_BALANCE` 由代码自动硬处理，不再依赖配置
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

## 配置示例

### 限时尝试
```yaml
quota-exceeded:
  antigravity-credits: true
  antigravity-credits-fail-threshold: 3
  antigravity-credits-fail-strategy: "temporary"
  antigravity-credits-disable-minutes: 30
```

### 永久禁止
```yaml
quota-exceeded:
  antigravity-credits: true
  antigravity-credits-fail-threshold: 3
  antigravity-credits-fail-strategy: "permanent"
```

## 风险点
- 超细 429 状态机引入后，执行路径变复杂，必须确保三条执行链行为一致
- short cooldown 与 credits 失败策略并存时，要避免相互覆盖或误判
- 编译部署时若源码目录不完整或函数结构有误，会直接导致 `go build` 失败
- 明确余额不足错误被提级为硬规则后，相关 auth 的积分通道会被直接永久停用

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
- 可配置的 credits 失败多次策略
- 对明确积分余额不足错误的自动永久停用能力
- 已验证可在服务器端完成编译、替换与运行
