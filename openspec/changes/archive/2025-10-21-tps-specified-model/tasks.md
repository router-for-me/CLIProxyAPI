## 1. Implementation

- [x] 1.1 在结构化日志中添加 `provider`、`model` 字段（来源：最终采用的提供商与模型）；可附带 `provider_model`
- [x] 1.2 将最终提供商与模型写入 Gin 上下文键：`API_PROVIDER`、`API_MODEL_ID`
- [x] 1.3 采样记录时（`/v1` 路径完成后）读取 `API_PROVIDER` 与 `API_MODEL_ID` 并传入聚合层
- [x] 1.4 扩展聚合器以支持 provider/model 组合与单独过滤（保持默认行为不变）
- [x] 1.5 管理端点 `GET /v0/management/tps` 支持 `?provider=<name>&model=<id>`（可单独使用其一）并按规则过滤返回

## 2. Validation

- [x] 2.1 单元测试：`per-request-tps` 事件包含 `provider` 与 `model` 字段（以及可选 `provider_model`）
- [x] 2.2 单元测试：聚合器过滤逻辑（provider-only、model-only、provider+model；含边界：不存在样本、零值）
- [x] 2.3 端到端测试：`GET /v0/management/tps?window=5m&provider=zhipu&model=glm-4.6` 返回仅该组合样本；`?provider=zhipu` 与 `?model=glm-4.6` 单独也能正确过滤（参考新增 packycode/zhipu 用例）

## 3. Non-Goals / Compatibility

- 不改变现有字段语义；默认不带 `model` 查询时行为与现状一致。
- 不引入持久化，仅内存聚合；清理策略保持不变。
