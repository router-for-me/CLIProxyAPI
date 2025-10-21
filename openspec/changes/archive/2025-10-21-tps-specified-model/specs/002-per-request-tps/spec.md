## ADDED Requirements

### Requirement: Per-provider/Model TPS Filtering and Attribution
系统 SHALL 为每条 TPS 样本记录并可查询“提供商/模型（provider/model）”复合标识（最终采用的提供商与模型），以支持同名模型在不同 provider 下的独立观测与对比；默认行为保持后向兼容。

#### Scenario: Log event includes provider and model fields
- **WHEN** 请求完成并触发 `per-request-tps` 结构化日志事件
- **THEN** 日志字段包含 `provider: <provider>` 与 `model: <model-id>`（最终采用的提供商与模型）；可附带 `provider_model: <provider>/<model-id>` 便于检索

#### Scenario: Context carries final provider and model
- **WHEN** 请求处理完成
- **THEN** Gin 上下文包含 `API_PROVIDER` 与 `API_MODEL_ID` 键，值分别为最终采用的提供商与模型，供下游采样读取

#### Scenario: Aggregation filters by provider and/or model
- **GIVEN** 管理端开启且存在历史 TPS 样本
- **WHEN** 客户端请求 `GET /v0/management/tps?window=5m&provider=<provider>&model=<model-id>`（两者可单独使用）
- **THEN**
  - 同时指定时：仅返回 `<provider>/<model-id>` 组合的样本聚合统计（avg/median/count）
  - 仅 provider 时：返回该 provider 下所有模型的聚合
  - 仅 model 时：跨 provider 聚合同名 model 的样本
  - 窗口过滤与默认清理策略保持一致

#### Scenario: Backward compatible default
- **WHEN** 客户端未提供 `model` 查询参数
- **THEN** 响应与现有行为一致（不区分模型的总体聚合），无须客户端变更

