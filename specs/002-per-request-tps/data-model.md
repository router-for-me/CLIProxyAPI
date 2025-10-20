# Data Model: 每请求 TPS（Tokens Per Second）

## Entity: PerRequestMetrics (log-only)

- `request_id`: string — 请求唯一标识（与现有日志一致）
- `is_streaming`: boolean — 是否为流式
- `request_duration_seconds`: number — 请求开始→完成（非流式用于 total TPS）
- `stream_duration_seconds`: number? — 首/末输出时间间隔（流式用于 completion TPS）
- `input_tokens`: number — 输入 tokens 数
- `output_tokens`: number — 输出 tokens 数
- `total_tokens`: number — 输入+输出 tokens
- `tps_completion`: number — 输出 tokens / 输出时间窗（两位小数）
- `tps_total`: number? — 输入+输出 tokens / 请求时间窗（两位小数）
- `measured_at`: string — ISO8601 时间戳

## Validation Rules

- `tps_completion` 四舍五入两位小数；分母≤0时，值=0.00 或字段缺省（按规范）
- `tps_total` 同上；仅当时间窗与计数可得时记录
- 数值非负；时间窗与 tokens 一致性校验（例如 total_tokens = input+output）

## Relationships

- 与业务实体无直接关系；仅为观测日志实体
