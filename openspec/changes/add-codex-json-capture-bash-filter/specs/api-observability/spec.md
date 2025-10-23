## ADDED Requirements
### Requirement: Filtered Upstream JSON Capture for Codex/Packycode (Bash/Shell Only)
系统 SHALL 在以下条件下记录最小化的上游 JSON 捕获：
- Provider 为 `codex` 或 `packycode`
- 模型为 `gpt-5-codex-low`、`gpt-5-codex-medium` 或 `gpt-5-codex-high`
- `request-log` 配置开启

捕获内容仅包含：
- `model`
- `instructions`（系统提示）
- `tools[]` 中名称包含 `bash` 或 `shell` 的函数定义（保留 `description` 与 `parameters`）
- `input[]` 中 `type=function_call` 且 `name` 包含 `bash|shell` 的调用项（仅保留 `call_id` 与 `arguments`）

捕获结果 SHALL：
- 写入 Gin Context 键：`API_JSON_CAPTURE`（[]byte JSON）、`API_JSON_CAPTURE_PROVIDER`、`API_JSON_CAPTURE_URL`
- 附加标签：`API_PROVIDER`、`API_MODEL_ID` 用于日志富化
- 以独立文件写入目录 `logs/gpt-5-codex-json-captures/`，文件名格式：`<url-path>-<provider>-<model>-<timestamp>.json`
- 不做跨请求持久化；不改变执行、翻译与 TPS 逻辑

### Configuration: `codex-json-capture-only`
当 `codex-json-capture-only=true` 时：
- 系统只执行上述 Codex 捕获并写入独立 JSON 文件
- 禁止所有其它请求日志输出（主请求/响应、流式/非流式）
- 管理端点：
  - GET `/v0/management/codex-json-capture-only` → `{ "codex-json-capture-only": <bool> }`
  - PUT/PATCH `/v0/management/codex-json-capture-only` with `{ "value": <bool> }`

行为矩阵：
| request-log | codex-json-capture-only | 主请求日志 | Codex 捕获 |
|-------------|-------------------------|------------|------------|
| false       | false                   | 关         | 关         |
| true        | false                   | 开         | 开(命中时) |
| false       | true                    | 关         | 开(仅命中) |
| true        | true                    | 关         | 开(仅命中) |

#### Scenario: Hit codex with bash tool calls
- **WHEN** provider=`codex` 且 model=`gpt-5-codex-low` 并且 input[] 含 `name=bash` 的 function_call
- **THEN** `API_JSON_CAPTURE` 存在，且 JSON 仅包含 `model`、`instructions`、`tools[bash|shell]` 与 `input[function_call bash]`

#### Scenario: Packycode alias also captures
- **WHEN** provider=`packycode` 且 model=`gpt-5-codex-medium`
- **THEN** 捕获行为与 codex 等价

#### Scenario: Non-target model or provider does not capture
- **WHEN** provider=`gemini` 或 model=`gpt-5`
- **THEN** 系统不写入 `API_JSON_CAPTURE`
