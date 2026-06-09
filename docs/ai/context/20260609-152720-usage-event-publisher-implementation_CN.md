# Usage Event Publisher Implementation

## 背景

本次实现 API Key 用量监控 MVP 的 CLIProxyAPI 侧能力。目标是从现有 `sdk/cliproxy/usage` 记录生成结构化 usage event，先写本地月度 JSONL，再可选同步到同机 `yui.web` 内部接口。

## 改动范围

- 新增 `internal/usage/event_types.go`
  - 定义 `UsageEvent`。
  - 生成 `api_key_hash` 和 `api_key_preview`。
  - 解析 request id、endpoint、token breakdown、latency、requested_at。
  - 对 `source` 中可能嵌入的 API key / token 进行脱敏，避免 JSONL 或 sync event 保存完整 secret。
- 新增 `internal/usage/event_writer.go`
  - 按 `requested_at` 写入 `usage-events-YYYY-MM.jsonl`。
  - 每行一个 JSON event。
  - 目录权限收紧为 `0700`，文件权限为 `0600`。
  - retention 只清理字面目录内过期的普通 `usage-events-*.jsonl` 文件，不跟随 symlink。
- 新增 `internal/usage/event_sync.go`
  - POST 到 yui.web。
  - 请求头包含 `x-internal-token`、`x-usage-timestamp`、`x-usage-signature`。
  - HMAC 口径为 `timestamp + "\n" + raw_body`。
  - 遵循项目 timeout 约束，没有额外设置 HTTP client timeout。
- 新增 `internal/usage/event_plugin.go`
  - 注册 usage plugin。
  - `USAGE_EVENTS_ENABLED=true` 时启用。
  - `USAGE_EVENTS_LOG_DIR`、`USAGE_EVENTS_RETENTION_DAYS` 控制本地账本。
  - `YUI_USAGE_EVENT_URL`、`YUI_USAGE_EVENT_TOKEN`、`YUI_USAGE_EVENT_HMAC_SECRET` 控制可选同步。
  - JSONL 写入或同步失败只记录 warning，不影响模型请求。
- 修改 `cmd/server/main.go`
  - 启动时从环境变量注册 usage event plugin。
- 修改 `config.example.yaml`
  - 追加 env-only usage event 配置说明。

## 验证

通过：

```bash
go test ./internal/usage -count=1
go build -o test-output ./cmd/server && rm test-output
```

补充运行：

```bash
go test ./...
```

该命令未全量通过，失败点在既有的非本任务测试：

- `internal/api/modules/amp` 的 `TestModifyResponse_GzipScenarios/skips_non_2xx_status`
- `internal/runtime/executor` 的 `TestEnsureQwenSystemMessage_MergeStringSystem`
- `sdk/cliproxy` 的 `TestServiceApplyCoreAuthAddOrUpdate_DeleteReAddDoesNotInheritStaleRuntimeState`

这些失败不在本次 usage event 改动文件范围内。

## 安全边界

- event、JSONL、sync body 不保存完整 client API key。
- `source` 字段如果等于或嵌入 token-like secret，会被脱敏。
- 不记录 prompt、response body、客户端 IP。
- HMAC secret 和 internal token 只通过环境变量配置，不写入代码。
