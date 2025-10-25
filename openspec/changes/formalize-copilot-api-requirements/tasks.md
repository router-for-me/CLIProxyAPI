# Implementation Tasks

## Phase 1: Critical Bug Fix (P0)

- [ ] **Fix stream=false override in codex_executor.go**
  - Location: `internal/runtime/executor/codex_executor.go:88`
  - Change: `sjson.SetBytes(body, "stream", false)` → `sjson.SetBytes(body, "stream", true)`
  - Validation: 运行 Copilot chat/completions 请求，验证返回 200 而非 400
  - Test: 创建测试用例验证 Copilot 流式响应

- [ ] **Verify streaming response handling**
  - 确认 `ExecuteStream()` 路径正确处理 SSE 格式
  - 验证 `jsonPayload()` 正确解析 `event:` 和 `data:` 前缀
  - Test: 验证完整的流式响应周期（开始 → 增量 → 完成）

## Phase 2: Specification Documentation

- [ ] **Create copilot-integration spec delta**
  - 文件: `openspec/changes/formalize-copilot-api-requirements/specs/copilot-integration/spec.md`
  - 内容: 添加所有 Copilot API 要求（见下方详细规范）

- [ ] **Document endpoint selection rules**
  - 明确优先级: attributes.base_url → metadata.base_url → token 派生 → 默认
  - 默认端点: `https://api.githubcopilot.com/chat/completions`
  - Token 派生逻辑: 解析 `proxy-ep=<host>` 提示

- [ ] **Document authentication requirements**
  - OAuth Device Flow: GitHub Device Code → GitHub Token → Copilot Token
  - Token 刷新: 基于 `metadata.refresh_in` 预刷新策略
  - Bearer Token: `Authorization: Bearer <access_token>`

- [ ] **Document required HTTP headers**
  - `Authorization: Bearer <token>`
  - `user-agent: GitHubCopilotChat/0.26.7`
  - `editor-version: vscode/1.0`
  - `editor-plugin-version: copilot-chat/0.26.7`
  - `openai-intent: conversation-panel`
  - `x-github-api-version: 2025-04-01`
  - `x-request-id: <UUID>`

- [ ] **Document supported models**
  - `gpt-5-mini`
  - `grok-code-fast-1`
  - 其他 Copilot 专属模型

- [ ] **Document error handling**
  - 400 Bad Request: 流式参数错误
  - 401 Unauthorized: Token 无效或过期
  - 429 Too Many Requests: 速率限制
  - 500 Internal Server Error: 上游错误

## Phase 3: Validation & Testing

- [ ] **Run openspec validate**
  - `openspec validate formalize-copilot-api-requirements --strict`
  - 解决所有验证错误

- [ ] **Integration testing**
  - 测试 OAuth Device Flow 登录
  - 测试 chat/completions 请求（流式 + 非流式）
  - 测试 token 刷新机制
  - 测试端点选择优先级

- [ ] **Log verification**
  - 验证请求日志格式正确
  - 确认敏感信息被正确遮蔽

## Phase 4: Documentation Update

- [ ] **Update README.md**
  - 添加 Copilot 配置说明
  - 添加已知限制（必须使用流式）

- [ ] **Update config.example.yaml**
  - 添加 Copilot OAuth 配置示例
  - 注释说明各字段含义

## Validation Checklist

每个任务完成后验证：
- [ ] 代码符合 Go 代码规范（gofmt/goimports）
- [ ] 日志记录适当（使用 logrus 分级）
- [ ] 错误处理完整（不吞错）
- [ ] 测试覆盖关键路径
- [ ] OpenSpec 验证通过

## Dependencies

- 无外部依赖
- 修改限于现有 Copilot 实现，不引入新组件

## Estimated Effort

- Phase 1: 30 分钟（代码修改 + 测试）
- Phase 2: 2 小时（编写规范文档）
- Phase 3: 1 小时（验证测试）
- Phase 4: 30 分钟（文档更新）
- **总计**: 约 4 小时
