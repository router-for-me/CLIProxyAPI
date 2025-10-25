# Formalize GitHub Copilot API Requirements

## Problem Statement

CLIProxyAPI 当前对 GitHub Copilot 的支持存在多个与官方 API 规范不符的实现：

1. **严重缺陷**：`codex_executor.go` 强制设置 `stream=false`，导致所有 Copilot chat/completions 请求失败（400 Bad Request: "stream: false is not supported"）
2. **端点混淆**：Copilot 复用 Codex 执行器，但端点路径在 `/chat/completions` 与 `/backend-api/codex` 之间不清晰
3. **认证规范缺失**：Token 格式验证不明确，仅依赖 `provider="copilot"` 配置匹配
4. **日志安全风险**：`access_token` 可能完整记录在 metadata 中，仅 Authorization header 被遮蔽
5. **参数验证缺失**：不支持的参数被静默丢弃，无告警

## Proposed Solution

通过规范化 GitHub Copilot 的集成要求，明确以下方面：

1. **流式响应要求**：强制 Copilot chat/completions 使用 `stream=true`
2. **端点规范**：明确 Copilot 使用 `/chat/completions` 端点，与 Codex Responses API 分离
3. **认证机制**：规范 OAuth Device Flow、Token 格式、Bearer 认证
4. **请求头规范**：明确必需的请求头（user-agent, editor-version, x-github-api-version 等）
5. **错误处理**：规范化错误响应和重试策略
6. **模型参数**：定义支持的模型和参数集合

## Scope

### In Scope
- 创建 `copilot-integration` 规范，明确所有 GitHub Copilot API 要求
- 修复 `stream=false` 强制覆盖缺陷
- 明确端点选择优先级（attributes.base_url → metadata.base_url → token 派生 → 默认）
- 规范化 Copilot 认证流程（OAuth Device Flow + Bearer Token）
- 定义必需的 HTTP 请求头
- 明确支持的模型列表（gpt-5-mini, grok-code-fast-1 等）

### Out of Scope
- 创建独立的 Copilot 执行器（保留复用 Codex 执行器的现状，通过 identifier 区分）
- 参数验证告警机制（后续优化）
- 日志遮蔽增强（后续安全改进）

## Impact Analysis

### Benefits
- ✅ 修复所有 Copilot 请求失败的严重缺陷
- ✅ 提供明确的 Copilot 集成规范，便于维护和验证
- ✅ 符合 GitHub Copilot 官方 API 要求
- ✅ 改善开发者体验，减少配置错误

### Risks
- ⚠️ 修改流式参数可能影响现有 Copilot 用户（但当前实现本身就是失败的）
- ⚠️ 端点配置变更需要验证与现有 token 派生逻辑的兼容性

### Migration Path
- 无需数据迁移
- 代码修改向后兼容（仅修复缺陷）
- 配置文件无需更新

## Success Criteria

1. ✅ 所有 Copilot chat/completions 请求成功（非 400 错误）
2. ✅ 流式响应正常工作（SSE 格式正确）
3. ✅ OAuth Device Flow 认证流程符合官方规范
4. ✅ 请求头完整性符合官方要求
5. ✅ `openspec validate` 通过，无冲突

## Related Work
- 参考现有 `provider-integration` 规范中的 Copilot 相关要求
- 与 Codex Responses API 保持清晰边界
