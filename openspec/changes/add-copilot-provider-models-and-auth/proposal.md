## Why

- 现状问题：
  - Copilot 设备码登录流程在未声明 `Accept: application/json` 时，GitHub 端返回 `application/x-www-form-urlencoded` 导致 JSON 解析失败；
  - Copilot token 交换缺少上游常见必需头，导致 403；
  - 模型注册表将 Copilot 模型镜像为 OpenAI 模型集，且缺少独占模型 `gpt-5-mini` 的明确归属，造成“unknown provider for model gpt-5-mini”。

- 机会：明确 Copilot 的专属模型清单并完善登录与 token 交换契约，消除易错点，提升可用性与可观测性。

## What Changes

- 认证（CLI 与管理端）：
  - 为 GitHub 设备码端点添加 `Accept: application/json`（避免表单编码导致解码失败）。
  - 为 Copilot token 交换请求添加常见必需头（示例）：
    - `User-Agent: cli-proxy-copilot`
    - `OpenAI-Intent: copilot-cli-login`
    - `Editor-Plugin-Name: cli-proxy`
    - `Editor-Plugin-Version: 1.0.0`
    - `Editor-Version: cli/1.0`
    - `X-GitHub-Api-Version: 2023-07-07`

- 模型注册表（BREAKING）：
  - 将 Copilot 的模型清单改为独占，不再镜像 OpenAI；
  - 目前 Copilot 仅暴露一个模型：`gpt-5-mini`；
  - 从 OpenAI 模型列表中移除 `gpt-5-mini`，避免跨提供商误解析。

- 执行器路由：
  - 取消将 `gpt-5-mini` 视作 `gpt-5-minimal` 的别名重写；
  - 为 provider=copilot 增加特判：改为调用 `https://api.githubcopilot.com/chat/completions`（或团队域名），以 Authorization: Bearer 出站，并补齐必要头（`user-agent`/`editor-*`/`openai-intent`/`x-github-api-version`/`x-request-id`）；
  - 其余 provider 仍走 Codex Responses API；后续如需再引入 Copilot 专属执行器，可作为增量提案。

## Impact

- Affected specs/capabilities：
  - auth（copilot 设备流 + token 交换 头部契约）
  - model-registry（提供商→模型清单，Copilot 独占）
  - executor-routing（去除 gpt-5-mini → minimal 重写）

- Affected code（参考实现位置）：
  - 设备码与 token 交换（CLI）：`internal/cmd/copilot_login.go`
  - 设备码与轮询（管理端）：`internal/api/handlers/management/auth_files.go`
  - 模型清单：`internal/registry/model_definitions.go`
  - 执行器映射：`internal/runtime/executor/codex_executor.go`
  - 注册与执行器绑定（上下文）：`sdk/cliproxy/service.go`

- Breaking 说明：
  - Copilot 不再镜像 OpenAI 模型；仅提供 `gpt-5-mini`。
  - 如调用方依赖 Copilot 暴露 OpenAI 模型集合，需要改造至使用 `codex/openai` 或其他 provider。

## Rollback Plan

- 回滚为镜像策略：`GetCopilotModels()` 恢复返回 `GetOpenAIModels()`；
- 在执行器中恢复 `gpt-5-mini` → minimal 的别名映射（如业务确有依赖）。

## Risks / Mitigations

- 风险：下游假设 Copilot 暴露 OpenAI 全量模型集 → 变更后报错或模型缺失。
  - 缓解：在管理端与 `/v1/models` 明确列举；发布变更日志；保留回滚策略。
- 风险：GitHub/Copilot 端头部要求演进。
  - 缓解：将头部集中在单处设置，便于后续更新；增加最小测试覆盖。

## Open Questions

- 是否需要 Copilot 专属执行器（与 CodexExecutor 解耦）？
- Copilot 端未来模型清单如何扩展（版本、上下文长度、参数支持）？

