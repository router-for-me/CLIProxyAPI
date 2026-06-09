# Usage Event Empty API Key Boundary

## 背景

API Key 用量监控 MVP 的账本目标是按 client API key 统计 token 和请求量。自审时发现如果某条 `usage.Record` 没有 `APIKey`，旧实现会对空字符串计算 SHA-256，并在 JSONL / yui.web 中形成一个空 key 的未托管用量项。

## 决策

`usageEventPlugin.HandleUsage` 遇到空白 `record.APIKey` 时直接跳过，不写本地 JSONL，也不同步到 yui.web。

## 原因

- 没有 client API key 就无法归属到用户、Shop 订单或本地 key。
- 空字符串 hash 会把所有缺失 key 的事件聚合成同一个伪 key，污染管理员看板。
- 跳过缺失 key 的 record 不影响已有 `UsageReporter` 的内存统计能力，只影响新增的 per-key 账本。

## 验证

新增测试：

```bash
go test ./internal/usage -run TestUsageEventPluginSkipsRecordWithoutAPIKey -count=1
```

结果通过，确认缺失 API key 时不会写 JSONL，也不会触发 sync。
