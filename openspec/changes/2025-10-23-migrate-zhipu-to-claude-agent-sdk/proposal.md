# Proposal: migrate-zhipu-to-claude-agent-sdk

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
