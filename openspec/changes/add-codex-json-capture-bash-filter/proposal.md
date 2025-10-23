## Why
为了解决在使用 gpt-5-codex 系列模型时，上游实际传输 JSON 内容不透明、且在客户端容易误解 BashOutput 行为导致重复轮询的问题，需要在服务端实现一个“最小且受控”的上游 JSON 捕获能力，用于调试审计与问题定位。同时必须严格限流与脱敏，仅抓取与 bash/shell 工具相关的必要字段，避免泄露无关敏感内容。

## What Changes
- 新增：当 provider 为 `codex` 或 `packycode` 且 model 为 `gpt-5-codex-{low,medium,high}` 时，启用受控的上游 JSON 捕获。
- 仅捕获以下最小字段：
  - `model`
  - `instructions`（系统提示）
  - `tools[]` 中名称包含 `bash` 或 `shell` 的函数定义（保留 `description` 与 `parameters`）
  - `input[]` 中 `type=function_call` 且 `name` 包含 `bash|shell` 的调用项（仅保留 `call_id` 与 `arguments`）
- 写入 Gin Context：`API_JSON_CAPTURE`、`API_JSON_CAPTURE_PROVIDER`、`API_JSON_CAPTURE_URL`，并补充 `API_PROVIDER`、`API_MODEL_ID` 以便 TPS/日志富化。
- 将过滤后的最小 JSON 以独立文件写入目录 `logs/gpt-5-codex-json-captures/`，文件名格式：`<url-path>-<provider>-<model>-<timestamp>.json`；避免与主请求/响应日志混排，提升可读性。
- 新增配置开关 `codex-json-capture-only`：为 true 时仅写入上述 Codex 捕获 JSON，且禁用其它任何请求日志（主请求/响应、流式/非流式）。
- 该能力受 `Config.RequestLog` 门控，关闭时不生效；不做跨请求持久化；不改变现有执行/翻译与 TPS 行为。

## Impact
- Affected specs: `api-observability`
- Affected code:
  - `internal/runtime/executor/logging_helpers.go`
  - `internal/api/middleware/response_writer.go`
  - `internal/logging/request_logger.go`
  - `internal/api/server.go`
  - `internal/api/handlers/management/config_basic.go`
- 安全与隐私：仅抓取与 bash/shell 相关的最小必要字段；不包含完整消息与非相关工具；遵循现有日志脱敏策略。
