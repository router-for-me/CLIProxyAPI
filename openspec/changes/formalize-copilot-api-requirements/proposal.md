# Formalize GitHub Copilot API Requirements

## Problem Statement

当前 Copilot 集成仍然强绑定 Codex Executor 与 OpenAI 路径，存在以下问题：

1. **职责混杂**：Copilot 专属逻辑（SSE 聚合、vision header、X-Initiator、token 刷新）深嵌在 `codex_executor.go` 中，影响 Codex/OpenAI 维护。
2. **模型注册不清晰**：Copilot 模型通过 Codex provider 暴露，在 `/v1/models` 与管理端查询时难以区分，provider 轮询逻辑也受影响。
3. **扩展受限**：后续若 Copilot 引入新的认证、公有模型或错误语义，将与 Codex 路径互相牵制，难以独立演进。
4. **测试覆盖不足**：复用路径导致 Copilot 与 Codex 难以分别验证，回归风险高。

## Proposed Solution

将 Copilot 集成解耦为独立执行路径，包含：

1. **创建 CopilotExecutor**：抽出 Copilot HTTP/流式处理逻辑，保留 Codex Executor 专注 Codex/OpenAI。
2. **独立 provider/model 注册**：Copilot 模型仅绑定 Copilot provider，管理端与 `/v1/models` 清楚显示所属。
3. **认证与路由重构**：`sdk/cliproxy/auth` 基于 provider 选择执行器，Copilot auth 不再经过 Codex 逻辑分支。
4. **规范同步**：更新 `copilot-integration` 规范，明确独立执行器、请求头、模型清单、错误语义。
5. **测试与文档**：补齐 Copilot 专属单元/集成测试，更新 README 与 config 示例。

## Scope

### In Scope
- 创建独立的 `CopilotExecutor` 与相关路由，确保 Copilot 与 Codex 执行路径解耦。
- 拆分模型注册、provider 轮询、管理端查询，确保 `provider=copilot` 单独存在。
- 更新 `copilot-integration` 规范、配置与 README，描述独立组件及其接口要求。
- 补齐 Copilot 执行器的单元测试与集成测试，覆盖流式/非流式、OAuth 刷新、模型查询。

### Out of Scope
- 新增 Copilot 模型种类（遵循上游返回即可）。
- 重构 OAuth Device Flow 交互界面（保留现有 CLI 流程）。
- 额外的安全增强（例如 metadata 全量遮蔽）在后续改进中处理。

## Impact Analysis

### Benefits
- ✅ Copilot 与 Codex 代码路径分离，降低互相干扰的风险。
- ✅ 管理端与模型列表更清晰，便于调试与权限排查。
- ✅ 未来可针对 Copilot 定制重试、错误处理或模型能力，而不影响 Codex。
- ✅ 测试与调试粒度提高，便于独立验证。

### Risks
- ⚠️ 拆分过程中需确保 OAuth/刷新逻辑无回归。
- ⚠️ 现有调用方若依赖旧的 provider 名称，需要额外兼容或迁移说明。

### Migration Path
- 保留历史配置字段（如 `provider=copilot`）但更新内部路由。
- 必要时提供一次性迁移脚本/说明，确保管理端与日志能看到新的 provider。
- 验证 Copilot 模型在 `/v1/models` 中依旧可见，并在管理端查询时显式标注。

## Success Criteria

1. ✅ Copilot 请求全部经过独立执行器，Codex 路径不再依赖 Copilot 分支。
2. ✅ `/v1/models` 与管理端返回中，Copilot 模型仅归属 `copilot` provider。
3. ✅ OAuth Device Flow、token 刷新、流式/非流式调用通过自动化测试。
4. ✅ `copilot-integration` 规范更新并通过 `openspec validate`。
5. ✅ README/config 示例描述新的架构与运维要点。

## Related Work
- 参考现有 `provider-integration` 规范中的 Copilot 相关要求
- 与 Codex Responses API 保持清晰边界
