## Context

Copilot 集成涉及三处：
1) 设备码登录与 token 交换（CLI 与管理端）
2) 模型注册表（Copilot 独占模型清单）
3) 执行器路由（仍复用 CodexExecutor，但不做 gpt-5-mini→minimal 别名重写）

## Goals / Non-Goals

- Goals
  - 明确 Copilot 端请求头与响应格式契约
  - 将 Copilot 模型清单与 OpenAI 解耦，独占 `gpt-5-mini`
  - 保持最小变更风险，先复用现有执行器链路

- Non-Goals
  - 不引入 Copilot 专属执行器（后续可独立提案）
  - 不改变 OpenAI / Codex 既有模型与路由

## Decisions

1) 设备码请求添加 `Accept: application/json`，避免表单编码导致解析失败。
2) Copilot token 交换请求添加必需头（User-Agent/OpenAI-Intent/Editor-* 等）。
3) `GetCopilotModels()` 仅返回 `gpt-5-mini`，OwnedBy/Type= `copilot`。
4) 从 OpenAI 模型清单移除 `gpt-5-mini`，避免跨提供商误解析。
5) CodexExecutor 中移除将 `gpt-5-mini` 作为 minimal 的别名重写，避免语义混淆。

## Alternatives Considered

- A: 继续镜像 OpenAI 模型集（放弃）。
  - 造成 provider 语义不清晰，且与 Copilot 实际库存不符。

- B: 立即新增 CopilotExecutor（暂缓）。
  - 优点：能力隔离、可单独演进；
  - 风险：新增复杂度与测试成本；当前未发现与 CodexExecutor 不兼容的上游契约。

## Risks / Trade-offs

- 风险：下游依赖 Copilot 显示 OpenAI 全量模型 → 变更后模型缺失。
  - 缓解：文档与变更记录，提供回滚方案；建议改用 `codex/openai` provider。

- 风险：Copilot 上游头部契约演进。
  - 缓解：头部集中设置，测试覆盖，后续以补丁快速跟进。

## Migration Plan

1) 合并本变更 → 发布小版本说明 Copilot 模型清单变更（BREAKING）。
2) 提供回滚脚本（恢复镜像与别名重写）。
3) 如后续需要 CopilotExecutor：以独立变更提案补充设计与落地计划。

## Open Questions

- Copilot 未来是否提供多模型与上下文/参数能力？
- 是否需要将 Copilot token 交换头部外化为可配置项？

