# Proposal: migrate-zhipu-to-claude-agent-sdk

## Why
本变更的动机是统一通过 Claude Agent SDK Python 调用 GLM（智谱）模型，获得一致的 hooks、工具与可观测能力，并避免 Go 直连上游所带来的充值/限流/兼容性等维护成本。

## What Changes
- 将 provider="zhipu" 的执行路径迁移为通过 Python Agent Bridge（Claude Agent SDK for Python）转发至上游 GLM。
- 保持对外 OpenAI 兼容 API（/v1/chat/completions 与 /v1/models）不变，兼容流式与非流式。
- 新增配置开关 `claude-agent-sdk-for-python`（默认启用）与 `baseURL` 优先级路由；支持回退到 legacy 执行器。
- 增加诊断端点 `/debug/upstream-check` 与结构化错误日志，提升排障能力。
- 本地 Query CLI 支持 `--model` 覆盖与子进程隔离，默认模型设为 `glm-4.6`。

## Summary
将现有对 Zhipu/GLM 的直连执行链路（Go: ZhipuExecutor → HTTP 调用 open.bigmodel.cn/chat/completions）下线，替换为通过“claude agent sdk python”执行的统一代理。Go 负责将 OpenAI 兼容请求打包并转发到 SDK 暴露的 /v1/chat/completions（包含流式与非流式），保持对外 API 与调用方式不变（./cli-proxy-api 不变）。

## Goals
- 统一由 Python Agent SDK 负责对 GLM-4.5/4.6 的调用与能力扩展（流控、hooks、工具等）。
- 保持对外 API 兼容（/v1/chat/completions 与 /v1/models 不变）。
- 在配置层面提供可切换的执行后端（默认启用 Python Agent Bridge）。
- 透明支持流式与非流式响应、错误透传与使用量统计。

## Non-Goals
- 不在本提案中改变其他 Provider（Claude/Gemini/Qwen/Codex）的执行方式。
- 不改变现有 OpenAI 兼容入站协议与响应格式。

## Motivation
- 需求方要求“彻底改用 Claude Agent SDK Python 的方式调用 GLM”，并通过其提供的 hooks/工具生态获得一致的运维与可观测能力。

## High-level Design
重要澄清（与现状一致且更换执行者）：
- ./cli-proxy-api 启动与对外接口不变。仅当 provider=zhipu/模型=glm-* 时，Go 将“像调用 HTTP 一样”把 OpenAI 兼容的请求体转发给 SDK 服务。
- SDK 服务为“claude agent sdk python”基于 Python 的本地 HTTP 端点（/v1/chat/completions）。其读取凭据仅通过环境变量：
  - ANTHROPIC_BASE_URL="https://open.bigmodel.cn/api/anthropic"
  - ANTHROPIC_AUTH_TOKEN="<来自 config.yaml 的 zhipu api-key>"
- 运维要求：从 config.yaml 的 zhipu-api-key 中取 api-key，按上述变量名注入到 SDK 进程环境（Go 不直接用该 key 访问上游，以规避“需要充值”的直连路径限制）。
- 通信协议：HTTP（localhost/ClusterIP），Go 侧通过 CLAUDE_AGENT_SDK_URL 指向此服务（推荐默认 http://127.0.0.1:35331）。
- 请求映射：/v1/chat/completions → SDK:/v1/chat/completions（OpenAI 兼容），保持流/非流一致。
- 错误与用量：由 SDK 透传/聚合，Go 侧继续日志与响应输出。

## Alternatives Considered
1) 继续使用 Go 内置 HTTP 客户端直连 Zhipu：不满足“必须用 Python Agent SDK”的目标。
2) 每请求 fork Python：冷启动与并发代价较高；不取。

## Risks
- 进程管理与健康检查：通过超时/重启与退避策略缓解。
- 兼容性：保持 API 不变，并提供开关回退（仅供紧急止血，不作为默认路径）。

## Rollout Plan
- 默认采用“外部 SDK 服务”模式（不在 Go 进程内托管子进程，./cli-proxy-api 不变）。
- 运维在部署 SDK 服务时，将 config.yaml 的 zhipu api-key 映射为 SDK 所需的环境变量（ANTHROPIC_*）。
- Go 端通过 CLAUDE_AGENT_SDK_URL 指向 SDK 服务地址，可灰度切换；必要时可临时回退 legacy ZhipuExecutor（过渡期保留）。
- 监控：在 usage/metrics 中新增“backend=claude-agent-sdk-python”维度。

## Diagnostics & Observability

为便于在生产环境快速定位上游连通/路径/权限/模型问题，本变更同步引入以下诊断与可观测能力（仅用于排障，不影响对外 API）：

- 暴露服务端诊断端点：`POST /debug/upstream-check`
  - 运行于 Python Agent Bridge（PAB）内部，以 90s 超时在服务器侧直接请求 `${ANTHROPIC_BASE_URL}` 的上游接口。
  - 尝试路径（按序）：`/chat/completions`、`/v1/chat/completions`、`/v1/messages`（Anthropic 风格，携带 `anthropic-version`）。
  - 返回字段：`url`、`status`、`body` 或结构化 `error`（分类含 `DNS`、`ECONNREFUSED`、`ETIMEDOUT`、`SSL`、`HTTP_4xx/5xx` 等）。

- 结构化错误日志（PAB）：
  - 字段：`category`、`url`、`auth_preview`（掩码）、`model`、`env_keys`、`traceback`。

- 流式错误处理：
  - 当流式路径出错时，以 SSE 错误事件输出并以 `[DONE]` 收尾，避免直接 HTTP 500 破坏客户端消费。

## Status
- [x] Implemented in Go: provider=zhipu requests are forwarded to Python Agent SDK `/v1/chat/completions` (stream/non-stream).
- [x] Config surfaced: Claude Agent SDK for Python (config key `claude-agent-sdk-for-python`): `enabled` (default true), `baseURL` (default `http://127.0.0.1:35331`), and env `CLAUDE_AGENT_SDK_URL` override; `config.example.yaml` docs updated.
- [x] Bridge URL selection priority: config.claude-agent-sdk-for-python.baseURL → env `CLAUDE_AGENT_SDK_URL` → ensureClaudePythonBridge default; rollback to legacy when `claude-agent-sdk-for-python.enabled=false`.
- [x] Tests: unit tests added for config parsing, executor rollback (enabled=false), and positive paths to Python Bridge (non-stream/stream) with SSE `[DONE]` handling.
- [ ] Optional E2E: requires running Python SDK with `ANTHROPIC_*` envs.

## Success Metrics
- E2E 用例（GLM-4.6）通过；流式/非流式覆盖。
- 管理接口列模、调用链日志与使用统计与旧实现一致。

## Appendix
- Python 侧建议：优先使用 ClaudeSDKClient（会话/流式/中断/钩子可用）。

## Change: Query CLI defaults and isolation

为便于本地/CI 直接验证 Python Agent SDK，对 repo 内 `python/claude_agent_sdk_python/query_cli.py` 增强如下：

- 新增 `--model` 参数：高优先级覆盖默认与环境变量。
- 默认模型：在未显式提供时，使用 `glm-4.6`。
- 子进程隔离：默认以子进程重入执行，避免复用已运行的 Claude Code 实例与旧会话。

示例：

```bash
# 指定模型（优先级最高）
ANTHROPIC_BASE_URL="https://open.bigmodel.cn/api/anthropic" \
ANTHROPIC_AUTH_TOKEN="<token>" \
PYTHONPATH=python \
python python/claude_agent_sdk_python/query_cli.py --model glm-4.6 "Hello"

# 使用默认模型（不设置 ANTHROPIC_MODEL、不加 --model）
ANTHROPIC_BASE_URL="https://open.bigmodel.cn/api/anthropic" \
ANTHROPIC_AUTH_TOKEN="<token>" \
PYTHONPATH=python \
python python/claude_agent_sdk_python/query_cli.py "Hello"
```

关键片段（节选）：

```python
# 默认模型（导入 SDK 前）
if not os.getenv("ANTHROPIC_MODEL", "").strip():
    os.environ["ANTHROPIC_MODEL"] = "glm-4.6"

# --model 参数与子进程隔离
parser.add_argument("--model", dest="model", help="Override model (high precedence)")
parser.add_argument("--no-subproc", dest="no_subproc", action="store_true", help="Do not force subprocess isolation")
...
if args.model and args.model.strip():
    os.environ["ANTHROPIC_MODEL"] = args.model.strip()
...
if not args.no_subproc:
    cmd = [sys.executable, str(Path(__file__).resolve()), "--no-subproc", *extra]
    rc = subprocess.call(cmd, env=os.environ.copy())
    raise SystemExit(rc)
```
