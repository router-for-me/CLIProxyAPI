# Quickstart: 每请求 TPS（仅日志）

## 验证步骤

1. 运行服务（本地或容器）
2. 开启 request-log 与 tps-log（任一方式）：
   - 配置：在 config.yaml 设置
     - request-log: true
     - tps-log: true
   - 管理 API：
     - 查看状态：GET /management/request-log, GET /management/tps-log
     - 开启：
       - PUT /management/request-log {"request-log": true}
       - PUT /management/tps-log {"tps-log": true}
3. 触发一个非流式请求与一个流式请求
4. 在日志中查找字段：`tps_completion`（必有）、`tps_total`（如可用）
5. 手工计算校验：
   - completion TPS = 输出 tokens / 输出时间窗（秒）
   - total TPS = （输入 + 输出 tokens）/ 请求时间窗（秒）
6. 校验两位小数、四舍五入；误差 ≤ 5%

## 管理端聚合查询（US4）

> 该端点用于查询自服务器启动以来（或指定窗口内）的 TPS 聚合统计，与是否打印 TPS 日志无关。

- 端点：`GET /v0/management/tps`
- 认证：与其他管理端点一致，需要配置管理密钥或 `MANAGEMENT_PASSWORD`

示例：

```bash
# 全量（等价于保留期内：默认近 24h）
curl -H "Authorization: Bearer <mgmt_key>" http://localhost:<port>/v0/management/tps

# 指定窗口（Go Duration 格式）：30s/5m/1h 等
curl -H "Authorization: Bearer <mgmt_key>" "http://localhost:<port>/v0/management/tps?window=5m"
```

响应示例：

```json
{
  "tps": {
    "since": "2025-10-21T12:00:00Z",
    "completion": { "count": 1234, "avg": 87.12, "median": 85.50 },
    "total":      { "count": 1200, "avg": 65.30, "median": 64.90 }
  }
}
```

### 行为说明

- 每个请求“仅记录一次”TPS 样本（非流式与流式一致）。
- 聚合与日志开关解耦：即使 `tps-log` 关闭，聚合样本仍会记录，可通过管理端查询。
- 窗口查询：`window` 参数用于仅统计最近窗口内的样本；非法值回退为全量聚合。

### 自动清理策略（内存受控）

- 定时清理：每 1 分钟清理早于 24 小时的样本；至少保留最近 10 个样本。
- 写时惰性清理：每新增约 1000 个样本触发一次清理。
- 仅清理 TPS 样本与其累加和，不影响请求日志与使用统计聚合。

## 示例日志（示意）

```json
{
  "request_id": "...",
  "is_streaming": true,
  "request_duration_seconds": 2.70,
  "stream_duration_seconds": 2.50,
  "input_tokens": 120,
  "output_tokens": 250,
  "total_tokens": 370,
  "tps_completion": 100.00,
  "tps_total": 137.04,
  "measured_at": "2025-10-20T12:00:00Z"
}
```

## 常见问题

- 零输出如何处理？→ `tps_completion` = 0.00 或字段缺省（按实现选择，规范已定义）。
- 失败重试如何计？→ 按单次请求/尝试分别记录，不跨尝试汇总。
- 性能开销如何评估？→ 使用基准测试对照采集前后延迟，目标 ≤ 1% 或 ≤ 5ms。

### 管理端聚合相关

- 查询不到数据？→ 可能暂无样本：先发起 /v1 请求；或处于极低 QPS，保留期内样本很少。
- `window` 大于保留期？→ 等同于在保留期内聚合（默认近 24h）。
- 日志关闭是否影响聚合？→ 不影响；聚合与日志输出解耦。

## 仪表盘与告警示例

- 过滤：`tps_completion < 20`
- 分段统计：按 `model`、`provider` 维度聚合 `tps_completion` P50/P90/P99
- 告警：`tps_total < 10` 持续 5 分钟触发，恢复阈值 12
- 趋势：按小时窗口绘制 `tps_completion` 平均值
