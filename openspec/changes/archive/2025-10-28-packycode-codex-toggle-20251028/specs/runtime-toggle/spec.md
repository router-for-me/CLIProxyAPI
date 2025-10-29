# Capability: Runtime Codex Toggle by Packycode

## ADDED Requirements

### Requirement: Enforce Codex Toggle
系统 SHALL 在 `packycode.enabled=true` 时禁用 `provider=codex` 的认证与路由，并在 `false` 时仅恢复因该开关禁用的 Codex 认证。

#### Scenario: Enable Packycode disables Codex
- Given 已存在 `provider=codex` 的认证条目
- And `config.yaml` 中 `packycode.enabled=true`
- When 服务启动或热重载配置
- Then 所有 `provider=codex` 认证被标记为禁用（`Disabled=true`，`Status=disabled`）
- And 认证属性包含 `toggle_by=packycode`

#### Scenario: Disable Packycode restores Codex
- Given 先前因开关被禁用的 `provider=codex` 认证（`toggle_by=packycode`）
- And `config.yaml` 中 `packycode.enabled=false`
- When 服务热重载配置
- Then 上述认证被恢复（`Disabled=false`，`Status=active`，移除 `toggle_by`）

### Requirement: Model Visibility Sync
系统 SHALL 在执行互斥切换后，更新模型注册表以反映 `/v1/models` 的可见性：启用 Packycode 时仅暴露 `provider=packycode` 的 OpenAI(GPT) 集合；关闭时恢复 Codex 集合。

#### Scenario: Models reflect toggle state
- Given 切换发生
- When 注册/反注册模型集合
- Then `/v1/models` 返回的集合与当前开关状态一致

## References
- 实现：`sdk/cliproxy/service.go:enforceCodexToggle`
- 注册：`sdk/cliproxy/service.go:ensurePackycodeModelsRegistered`
- 测试：`sdk/cliproxy/service_packycode_codex_toggle_test.go`

