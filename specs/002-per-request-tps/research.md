# Research: 每请求 TPS（Tokens Per Second）

## Decisions

1. 仅在结构化日志记录 TPS（不对外暴露）
- Rationale: 保持向后兼容与隐私最小化；消费方在外部平台做统计与告警。
- Alternatives: 在响应 JSON/headers 暴露（放弃，增加耦合与泄漏面）。

2. 字段命名采用扁平键：`tps_completion`, `tps_total`
- Rationale: 解析简单、各系统普适、避免嵌套兼容问题。
- Alternatives: 嵌套对象或命名空间前缀（放弃，查询/规约复杂）。

3. 舍入规则固定为“两位小数、四舍五入”
- Rationale: 与告警阈值、对比分析的可读性与稳定性一致。
- Alternatives: 1位/3位或不舍入（放弃，精度/噪声/一致性权衡不佳）。

## Measurement Windows

- Non-streaming total window: 请求开始→请求完成
- Streaming completion window: 首个输出 token→最后输出 token

## Error & Retry

- 失败但产生部分输出：记录完成窗口与已输出 tokens，计算 completion TPS
- 零输出：不记录 TPS 或 TPS=0.00（按规范条款执行）
- 重试：按单次请求分别记录 TPS，不跨尝试聚合

## Performance Impact

- 统一时间源采集；仅计时/计数与日志格式化，目标 ≤ 1% 或 ≤ 5ms

## Logging Contract Outline

- Fields: `request_id`, `is_streaming`, `input_tokens`, `output_tokens`, `total_tokens`, `tps_completion`, `tps_total`, `request_duration_seconds`, `stream_duration_seconds?`, `measured_at`
- Types: 数值字段为 number（秒/tokens），时间戳 ISO8601

## Alternatives Considered

- 在响应中暴露（JSON/headers）：放弃
- 仅记录 completion（无 total）：放弃（在可得时记录 total 更有用）
- 更复杂的滑动窗口/分段 TPS：放弃（当前分辨率足够，简单可靠）
