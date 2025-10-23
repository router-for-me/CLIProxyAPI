## ADDED Requirements
### Requirement: Copilot Provider Exposure (Direct or Mapped)
系统 SHALL 将 `copilot` 作为对外 provider 暴露，并在模型与路由注册中加入相应分支；允许与 OpenAI‑compat 映射并存，且不得改变既有 provider 行为。

#### Scenario: Management listing and filtering
- **WHEN** 管理端启用
- **THEN**
  - `GET /v0/management/providers` 返回包含 `copilot` 的 provider 列表
  - `GET /v0/management/models?provider=copilot` 返回由 `copilot` 提供的模型（可为空集合）

#### Scenario: Model registry visibility
- **WHEN** 初始化或热更新完成
- **THEN** model registry 中可见 provider=`copilot` 的条目；与其他 provider 并存

