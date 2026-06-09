# CLIProxyAPI Usage Event 发布设计

## 背景

`CLIProxyAPI` 已有 `UsageReporter` 和 `sdk/cliproxy/usage` 插件机制。当前内存 usage 统计适合运行期看板，但不适合作为 `yui.web` Shop 的月度用量账本。

第一阶段需要让 `CLIProxyAPI` 在不影响模型请求可用性的前提下，把每个 client API Key 的 usage record 写成本地结构化 JSONL，并实时同步到同机 `yui.web`。

## 目标

- 复用现有 usage record，不解析普通 request-log。
- 为每个 usage record 生成结构化 usage event。
- 本地按月写 JSONL，记录所有 client API Key，包括 `LOCAL` key。
- 实时 POST 到 `yui.web`，由 `yui.web` 关联 Shop 订单和未托管 key。
- 不记录 prompt、response body、完整 API Key、客户端 IP。
- 同步失败或本地写入失败不影响用户请求。

## 非目标

- 不替换现有 `/v0/management/usage`。
- 不用普通 request-log 做账本。
- 不在 CLIProxyAPI 侧做金额计算。
- 不在 CLIProxyAPI 侧判断是否 Shop key。
- 不因为 usage event 同步失败而拒绝模型请求。

## 现有可复用能力

- `sdk/cliproxy/usage/manager.go` 已支持注册 usage plugin。
- `internal/runtime/executor/helps/usage_helpers.go` 已从 Gin context 中读取 `apiKey`，并构造 `usage.Record`。
- 各 executor 已在 streaming/non-streaming 路径解析 usage 并调用 `reporter.Publish`。
- `UsageReporter.EnsurePublished` 已能在上游没有 usage 时保证请求计数。

## 事件生成位置

新增一个 usage plugin，监听 `sdk/cliproxy/usage.Record`。

推荐位置：

```text
internal/usage/event_plugin.go
```

职责：

- 从 `usage.Record` 和 Gin context 中提取事件字段。
- 生成或读取 `request_id`。
- 生成 `api_key_hash` 和 `api_key_preview`。
- 规范化 token、时间、成功/失败状态。
- 发送给本地 JSONL writer。
- 发送给 yui.web sync client。

## Usage Event 字段

```json
{
  "version": 1,
  "request_id": "req_...",
  "api_key_hash": "sha256_hex",
  "api_key_preview": "sk-...abcd",
  "provider": "codex",
  "model": "gpt-5.4",
  "endpoint": "/v1/responses",
  "source": "upstream account label or email",
  "auth_index": "0",
  "success": true,
  "failed": false,
  "input_tokens": 0,
  "output_tokens": 0,
  "reasoning_tokens": 0,
  "cached_tokens": 0,
  "total_tokens": 0,
  "latency_ms": 0,
  "requested_at": "2026-06-09T14:21:33+09:00"
}
```

字段来源：

- `provider`：`usage.Record.Provider`。
- `model`：`usage.Record.Model`，为空时使用 `unknown`。
- `source`：`usage.Record.Source`。
- `auth_index`：`usage.Record.AuthIndex`。
- token 字段：`usage.Record.Detail`。
- `requested_at`：`usage.Record.RequestedAt`，为空时使用当前时间。
- `latency_ms`：`usage.Record.Latency`。
- `endpoint`：从 Gin context 的 `FullPath()` 或 `Request.URL.Path` 读取。
- `api_key_hash` / `api_key_preview`：从 `usage.Record.APIKey` 生成。
- `request_id`：优先使用现有 request id；没有时生成随机唯一 ID。

## API Key 处理

- 完整 API Key 只在内存中用于 hash 和 preview。
- JSONL、同步事件、yui.web SQLite 都不保存完整 API Key。
- hash 使用稳定 SHA-256 十六进制值，方便 yui.web 对已有 `api_keys.api_key` 回填同样 hash。
- preview 沿用现有风格，例如前缀 + 后 6 位；preview 只用于展示，不能作为关联键。

## 本地 JSONL

文件路径：

```text
<usage-log-dir>/usage-events-YYYY-MM.jsonl
```

默认建议：

```text
logs/usage/usage-events-YYYY-MM.jsonl
```

规则：

- 每行一个完整 usage event JSON。
- 文件按 `requested_at` 所在月份切分。
- 写入采用 append。
- 写入失败只记录应用错误日志，不影响模型请求。
- 清理 90 天之前的 usage JSONL。
- 清理逻辑只处理 `usage-events-*.jsonl`，不碰普通 request log 和 error log。

## yui.web 同步

同步 URL：

```text
http://127.0.0.1:4173/api/internal/usage-events
```

请求头：

```text
x-internal-token: <token>
x-usage-timestamp: <unix seconds>
x-usage-signature: <hex hmac>
content-type: application/json
```

签名：

```text
HMAC_SHA256(secret, timestamp + "\n" + raw_body)
```

行为：

- 先写本地 JSONL，再尝试同步 yui.web。
- 同步失败不重试阻塞当前请求。
- 同步失败写应用错误日志，并保留本地 JSONL 供后续导入。
- yui.web 用 `request_id` 幂等去重，重复同步不会重复计数。

## 配置建议

MVP 可用环境变量，避免改动过多配置结构：

```env
USAGE_EVENTS_ENABLED=true
USAGE_EVENTS_LOG_DIR=logs/usage
USAGE_EVENTS_RETENTION_DAYS=90
YUI_USAGE_EVENT_URL=http://127.0.0.1:4173/api/internal/usage-events
YUI_USAGE_EVENT_TOKEN=<same-as-yui-internal-token>
YUI_USAGE_EVENT_HMAC_SECRET=<strong-random-secret>
```

后续如果要通过管理面板修改，再把这些配置迁入 `config.yaml`。

## 失败语义

- usage event 生成失败：记录错误，不影响请求。
- JSONL 写失败：记录错误，不影响请求。
- yui.web POST 失败：记录错误，不影响请求。
- HMAC 配置缺失：本地 JSONL 仍可写；同步禁用并记录配置错误。
- URL 未配置：只写本地 JSONL。

## 与现有 usage 统计关系

- 保留现有 `internal/usage/logger_plugin.go` 内存统计。
- 新 usage event plugin 与现有 logger plugin 并行注册。
- `usage-statistics-enabled` 只控制内存统计，不应阻止 JSONL 账本记录。
- 如果需要单独开关，使用 `USAGE_EVENTS_ENABLED`。

## 测试范围

- event 不包含完整 API Key。
- API Key hash 对同一 key 稳定。
- preview 不为空且不能恢复完整 key。
- request_id 缺失时生成唯一 ID。
- token total 为 0 时按 input + output + reasoning 兜底。
- failed record 没有 usage 时仍写入失败请求事件。
- 月度 JSONL 文件名正确。
- JSONL append 格式为一行一个 JSON。
- 90 天清理只删除 usage JSONL。
- 同步请求 HMAC 正确。
- 同步失败不返回错误给 usage manager。
- URL 或 secret 缺失时只写本地 JSONL。
- streaming/non-streaming 路径都能触发 event。

## 与 yui.web 的分工

- CLIProxyAPI：只负责真实请求 usage 事件。
- yui.web：负责订单关联、SQLite 账本、管理员展示、手动导入。
- CLIProxyAPI 不判断一个 key 是否属于 Shop。
- yui.web 未匹配到 Shop 订单时，把 key 显示为本地/未托管。
