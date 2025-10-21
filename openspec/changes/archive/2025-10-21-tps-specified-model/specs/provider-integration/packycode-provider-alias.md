# Change: Expose Packycode as external provider, mapped internally to Codex

## Why
- 对外统一将 Packycode 作为独立 provider 暴露，便于管理界面、统计与路由规则清晰表述与筛选。
- 内部继续复用 Codex 执行器与 OpenAI(GPT) 模型集合，避免重复维护，降低耦合与回归风险。

## What Changes
- watcher：合成认证改为 `Provider=packycode`，保留 `Label=packycode` 与 `base_url`、`api_key` 属性。
- service：
  - `provider=packycode` → 使用 CodexExecutor（内部映射）。
  - 模型注册时使用 `provider=packycode`，但复用 `registry.GetOpenAIModels()` 集合。
- server 启动参数：`-packycode` 注册模型改用 `provider=packycode`。
- 管理端：
  - 新增 `GET /v0/management/providers`：返回当前可用 providers（含 packycode）。
  - 新增 `GET /v0/management/models?provider=packycode`：按 provider 过滤模型，并附带 `providers` 字段。
- 日志/统计：
  - `per-request-tps` 日志与 `GET /v0/management/tps` 过滤已兼容 `provider=packycode`。

## Files Affected (non-exhaustive)
- internal/watcher/watcher.go
- sdk/cliproxy/service.go
- cmd/server/main.go
- internal/api/server.go
- internal/api/handlers/management/providers.go (new)

## Tests
- watcher：tests/internal/watcher/watcher_packycode_test.go（合成 Provider=packycode）。
- 管理端：
  - internal/api/handlers/management/providers_test.go（providers 列表与按 provider 取模型）。
  - internal/api/handlers/management/usage_packycode_test.go（TPS 过滤）。
- /v1/models：tests/internal/api/openai_models_test.go（OpenAI 列表包含 packycode 注册集）。
- 日志：internal/runtime/executor/logging_helpers_packycode_test.go（provider=packycode 出现在结构化日志）。

## Management API (for this change)
- `GET /v0/management/providers`：返回当前可用 providers 列表（含 `packycode`）。
- `GET /v0/management/models?provider=packycode`：按 provider 过滤模型，并在数据元素中附带 `providers` 字段（提供该模型的 provider 列表）。
- `GET /v0/management/tps?window=<duration>&provider=packycode[&model=<id>]`：按 provider 与可选模型过滤窗口内 TPS 聚合。

## Backward Compatibility
- 无破坏性：对外新增 provider 名称；现有 Codex 行为与模型集合未变，内部逻辑复用；默认列表/统计在不筛选时保持原语义。

## Risks & Mitigations
- 风险：Provider 名称变动可能影响依赖“codex”字面值的外部脚本。
  - 缓解：保持内部执行器为 Codex，不移除原有路径；管理端新增 providers 与 models 接口，便于迁移检查。
- 风险：注册表中同一模型同时由多个 provider 提供时的计数一致性。
  - 缓解：沿用现有 ModelRegistry 计数与 provider 分组逻辑；测试覆盖注册与过滤。

## Rollout & Observability
- 通过管理端新接口核对：`GET /v0/management/providers` 应包含 `packycode`。
- `/v1/models` 应可见 OpenAI 模型（由 packycode 注册），`owned_by=openai`。
- 结构化日志 `per-request-tps` 应包含 `provider=packycode` 与 `provider_model=packycode/<model>`。
