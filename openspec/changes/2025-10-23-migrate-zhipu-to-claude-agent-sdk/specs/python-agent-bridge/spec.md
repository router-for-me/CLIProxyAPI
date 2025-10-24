## MODIFIED Requirements: Zhipu Provider Execution

#### Requirement: Replace direct Zhipu HTTP executor with Python Agent SDK bridge
- The system MUST route all provider="zhipu" executions to a Python Agent Bridge (PAB) that uses Claude Agent SDK (Python) to call GLM models.
- The Go service MUST expose the same OpenAI-compatible endpoints (/v1/chat/completions, streaming and non-streaming) unchanged.
- The PAB MUST support both streaming and non-streaming responses and error pass-through semantics.

#### Requirement: bridge URL selection priority
- When Claude Agent SDK for Python is enabled (claude-agent-sdk-for-python.enabled=true), the bridge base URL MUST be selected with the following priority:
  1) config.claude-agent-sdk-for-python.baseURL (if non-empty)
  2) environment variable CLAUDE_AGENT_SDK_URL (if non-empty)
  3) runtime fallback ensureClaudePythonBridge() default
- When claude-agent-sdk-for-python.enabled=false, provider="zhipu" MUST fallback to legacy Go ZhipuExecutor (OpenAI-compatible direct path) for both streaming and non-streaming.

#### Scenario: Non-streaming chat completions
Given provider="zhipu" and model="glm-4.6"
When client POSTs /v1/chat/completions without stream=true
Then Go forwards the translated request to PAB and returns the JSON result unchanged in OpenAI format.

#### Change: Local Query CLI behavior
- 新增 `python/claude_agent_sdk_python/query_cli.py` 的约定：
  - 当未设置 `ANTHROPIC_MODEL` 且未使用 `--model` 参数时，默认使用 `glm-4.6`。
  - 提供 `--model` 参数以高优先级覆盖默认与环境变量。
  - 默认启用子进程隔离，避免复用已运行的 Claude Code 实例。

示例：
```bash
# 默认使用 glm-4.6（不设置 ANTHROPIC_MODEL）
ANTHROPIC_BASE_URL="https://open.bigmodel.cn/api/anthropic" \
ANTHROPIC_AUTH_TOKEN="<token>" \
PYTHONPATH=python \
python python/claude_agent_sdk_python/query_cli.py "Hello"

# 显式覆盖模型
PYTHONPATH=python python python/claude_agent_sdk_python/query_cli.py --model glm-4.6 "Hello"
```

#### Scenario: Streaming chat completions
Given provider="zhipu" and model="glm-4.6" with stream=true
When client POSTs /v1/chat/completions
Then Go forwards to PAB using an SSE-compatible stream and relays chunks to client, ending with [DONE].

#### Scenario: Error propagation
When PAB returns an HTTP error (>=400)
Then Go MUST return the same status and a JSON error body preserving message/code when present.

#### Requirement: Diagnostic and Observability
- The Python Agent Bridge (PAB) SHALL expose a diagnostic endpoint `POST /debug/upstream-check` that performs a server-side upstream request to `${ANTHROPIC_BASE_URL}/v1/chat/completions` with a 90s timeout and returns:
  - `status`: upstream HTTP status code (when available)
  - `body`: upstream JSON body or text (truncated if needed)
  - `error`: structured object when transport errors occur with a `type` classifier in {`DNS`, `ECONNREFUSED`, `ETIMEDOUT`, `SSL`, `HTTP_4xx/5xx`, other}
- The diagnostic endpoint MUST also attempt the following paths in order: `/chat/completions`, `/v1/chat/completions`, `/v1/messages` (Anthropic style; include `anthropic-version` header), to accommodate different upstream gateways.
- The PAB SHALL emit structured error logs for SDK and upstream errors with fields:
  - `category` (one of classifiers above), `url`, `auth_preview` (masked token), `model`, `env_keys`, `traceback`
- For streaming mode, the PAB SHALL emit an SSE error event and end with `[DONE]` instead of returning HTTP 500 directly.

#### Scenario: Rollback by configuration
Given claude-agent-sdk-for-python.enabled=false
When client requests provider="zhipu"
Then system MUST route via legacy Go ZhipuExecutor (OpenAI-compatible direct execution), not the Python Agent Bridge.

## ADDED Requirements: Configuration & Rollout

#### Requirement: Claude Agent SDK for Python config (key: claude-agent-sdk-for-python)
- Config MUST include (under key `claude-agent-sdk-for-python`):
  - enabled (bool, default true)
  - baseURL (string, default http://127.0.0.1:35331)
  - env map for exporting Zhipu credentials to PAB process when managed as sidecar (optional)

#### Scenario: Rollback
When Claude Agent SDK for Python is disabled (claude-agent-sdk-for-python.enabled=false)
Then provider="zhipu" MUST fallback to legacy Go ZhipuExecutor.

## Test Coverage (reference)
- Unit: config parsing for claude-agent-sdk-for-python.enabled/baseURL defaults and trimming
  - tests/internal/config/python_agent_config_test.go
- Unit: executor fallback when claude-agent-sdk-for-python.enabled=false (non-stream/stream)
  - tests/internal/executor/zhipu_executor_test.go::TestZhipuExecutor_FallbackWhenPythonAgentDisabled_*
- Unit: executor positive paths using claude-agent-sdk-for-python.baseURL (non-stream/stream)
  - tests/internal/executor/zhipu_executor_test.go::TestZhipuExecutor_UsePythonAgentBaseURL_*
