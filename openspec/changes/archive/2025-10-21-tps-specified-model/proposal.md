## Why

当前每请求 TPS（Tokens Per Second）仅提供“整体”聚合视图，无法按模型区分。
在多提供商/多模型并存的环境下，运维需要按模型定位性能热点、验证路由与切换策略的实际效果。

## What Changes

- 为每条 TPS 样本关联“提供商/模型（provider/model）”复合标识：`provider` 与最终采用的 `model`。
- 扩展管理端点 `GET /v0/management/tps`：新增可选查询参数 `provider=<name>`、`model=<id>`。
  - 二者同时提供时：按精确的 provider+model 过滤窗口聚合。
  - 仅提供 `provider` 时：返回该 provider 下所有模型的窗口聚合。
  - 仅提供 `model` 时：跨 provider 聚合同名 `model` 的样本，保持与原有“按模型”语义的兼容性。
- 在结构化日志事件 `per-request-tps` 中加入 `provider` 与 `model` 字段（可同时附带 `provider_model` 拼接字段用于检索）。

## Impact

- Affected specs: `002-per-request-tps`
- Affected code:
  - `internal/runtime/executor/logging_helpers.go`（日志字段/上下文：新增 `provider`、`model`、可选 `provider_model`）
  - `internal/api/server.go`（采样记录/上下文读取：同时读写 `API_PROVIDER` 与 `API_MODEL_ID`）
  - `internal/usage/logger_plugin.go`（TPS 聚合器支持 provider/model 组合与单独过滤）
  - `internal/api/handlers/management/usage.go`（解析 `provider` 与 `model` 参数并按规则过滤返回）
- Breaking: 无（为后向兼容扩展；默认无过滤，与现状等价；仅在携带参数时切片）

