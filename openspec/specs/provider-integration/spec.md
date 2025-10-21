# provider-integration Specification

## Purpose
TBD - created by archiving change add-zhipu-api-support. Update Purpose after archive.
## Requirements
### Requirement: Zhipu Provider Integration (Direct)
系统 SHALL 在 provider registry 中注册一个 `zhipu` 提供商，占位于执行路径，不改变现有 OpenAI‑compat 行为。

#### Scenario: Provider type registered
- **WHEN** 系统启动并加载 access/sdk 配置
- **THEN** `zhipu` 作为合法提供商类型出现在 registry 中
- **AND** 未配置 `ZHIPU_API_KEY` 时不启用任何直连客户端

#### Scenario: Model mapping coexists
- **GIVEN** 模型 `glm-*` 已通过 OpenAI‑compat 上游可用
- **WHEN** 启用 `zhipu` 提供商
- **THEN** model registry 中 `glm-*` 同时显示 `openai-compat` 与 `zhipu` 两个提供者

#### Scenario: Direct executor (non-stream)
- **GIVEN** 存在 `zhipu-api-key[0]` 且含 `api-key` 与 `base-url`
- **WHEN** 请求路由到 `zhipu` 执行器分支（非流式）
- **THEN** 转换为 OpenAI-compatible chat completions 调用 `${base-url}/chat/completions`
- **AND** 使用 `Authorization: Bearer <api-key>` 与 `Content-Type: application/json`
- **AND** 成功 2xx 时返回翻译后的响应；非 2xx 返回上游错误消息与对应状态码

#### Scenario: Direct executor (stream)
- **GIVEN** 存在 `zhipu-api-key[0]` 且含 `api-key` 与 `base-url`
- **WHEN** 请求路由到 `zhipu` 执行器分支（流式）
- **THEN** 以 SSE 方式转发 `${base-url}/chat/completions` 的流响应
- **AND** 逐行传入翻译器，保留使用量统计并输出流式片段

