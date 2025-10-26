# Implementation Tasks

## Phase 1: 架构拆分与执行器抽离

- [x] **实现独立 CopilotExecutor**
  - 新建 `internal/runtime/executor/copilot_executor.go`（或等效模块）
  - 提取现有 Codex Executor 中 Copilot 专属逻辑（请求头、SSE 聚合、vision 检测、X-Initiator）
  - 保留 Codex Executor 中 Codex/OpenAI 逻辑不受影响
  - Test: 单元测试覆盖 Copilot 执行器的非流式与流式路径
- [x] **对齐 Copilot 请求头与 payload 兼容性**
  - 参考 `copilot-api` 实现补充 `copilot-integration-id`、`x-vscode-user-agent-library-version` 等 headers
  - 确保 messages 支持字符串与多段结构（text/image）、工具调用 (`tool_calls`/`tool_choice`)
  - Test: 针对富文本与 tool call 场景的执行器测试

- [x] **重构认证与执行器注册流程**
  - `sdk/cliproxy/auth/manager.go`：为 CopilotExecutor 注册独立 provider id（如 `copilot`）
  - `sdk/cliproxy/service.go`：调整 `ensureExecutorsForAuth` 与 `registerModelsForAuth`，确保 Copilot auth 仅绑定新执行器并同步 `grok-code-fast-1` 等模型
  - 验证 OAuth 刷新逻辑仍然可用（Copilot refresh evaluator）

## Phase 2: 模型注册与路由解耦

- [x] **独立 Copilot 模型注册器**
  - 在 `internal/registry` 下新增 Copilot 专属模型表
  - `util.GetProviderName` 仅返回 Copilot provider，对应模型从 Codex provider 移除
  - 管理端 `/v0/management/models`、`/v1/models` 返回的 provider 信息保持准确
- [x] **同步 Copilot `/models` 清单**
  - Seed 数据包含 `gpt-5-mini`、`grok-code-fast-1`、`gpt-5`、`gpt-4.1`、`gpt-4`、`gpt-4o-mini`、`gpt-3.5-turbo`
  - 设计可扩展机制以添加 upstream 新模型（preview/enterprise）

- [x] **API Handler 路由调整**
  - `sdk/api/handlers/openai`：在 `ExecuteWithAuthManager` 前保留模型→provider 映射，但确保 Copilot 模型走独立 provider
  - `sdk/api/handlers/claude`/`messages` 等路径复用 CopilotExecutor 输出（必要时更新翻译层）
  - Test: handler 级别测试覆盖 `provider=copilot` 请求

## Phase 3: OpenSpec & 文档同步

- [x] **更新 copilot-integration 规范**
  - 描述独立执行器、模型注册与请求路由的边界
  - 明确 Copilot 与 Codex 使用不同 provider id、端点与重试策略

- [x] **整理配置与管理文档**
  - README/config.example.yaml：新增 CopilotExecutor 部署说明、限制与故障排查
  - 管理端文档强调 `provider=copilot` 独立性

## Phase 4: 验证与回归

- [x] **Run openspec validate**
  - `openspec validate formalize-copilot-api-requirements --strict`
  - 修复新增规范文件的校验问题

- [x] **集成 & 回归测试**
  - OAuth Device Flow、token 刷新
  - `/v1/chat/completions`、`/v1/messages`（流式/非流式）对 Copilot 模型的路径
  - 验证 Codex/OpenAI 模型未被回归
  - 日志：确认敏感字段遮蔽仍然生效

## Validation Checklist

每个阶段完成后验证：
- [x] Go 代码通过 gofmt/goimports
- [x] 独立 Copilot 执行器与 Codex 执行器互不影响
- [x] 流式/非流式测试覆盖 Copilot 关键路径
- [x] 管理端与开放接口返回的 provider/id 正确
- [x] OpenSpec 校验通过

## Dependencies

- 无新增外部依赖
- 需要对现有 Copilot 认证、执行器和注册流程进行重构

## Estimated Effort

- Phase 1: 2 小时（执行器拆分 + 测试）
- Phase 2: 1.5 小时（模型注册与路由）
- Phase 3: 1 小时（规范与文档）
- Phase 4: 1 小时（验证与回归）
- **总计**: 约 5.5 小时
