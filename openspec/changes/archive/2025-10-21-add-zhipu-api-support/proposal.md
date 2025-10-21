## Why
为 CLIProxyAPI 增加对 Zhipu GLM 平台的正式支持，使其既可通过现有 OpenAI 兼容（iFlow 等）接入，也可按需以“独立提供商”方式直连 Zhipu 官方 API（升级路径）。

## What Changes
- 新增能力：将 Zhipu 作为独立提供商纳入 provider registry 与 executor 体系（直连实现：OpenAI-compatible chat completions 非流式与流式）。
- 维持兼容：保留现有通过 OpenAI 兼容上游（iFlow）的转发能力。
- 配置扩展：定义 `ZHIPU_API_KEY` 与 `zhipu.base_url`（可选）等配置键。
- 模型映射：在 model registry 中为 `glm-*` 建立 provider 到 Zhipu 的映射（不改变既有 OpenAI-compat 路径）。
- 验证：提供严格的 OpenSpec 规范与最小任务清单。

## Impact
- 影响的规格能力：`provider-integration`（提供商注册与分发）、`openai-compat`（仅文档澄清，不修改行为）。
- 影响的代码：`sdk/access/registry.go`（注册点），`internal/registry/model_registry.go`（模型→提供商映射），`internal/runtime/executor/*`（为 Zhipu 预留执行器入口）。
- 向下兼容：默认仍通过 OpenAI 兼容路径工作；显式启用 Zhipu 提供商后才切换直连。
