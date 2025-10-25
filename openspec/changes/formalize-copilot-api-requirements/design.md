# Design: Formalize GitHub Copilot API Requirements

## Context

CLIProxyAPI 当前实现对 GitHub Copilot 的支持存在多个与官方 API 规范不符的问题，其中最严重的是强制设置 `stream=false` 导致所有请求失败。本次设计旨在规范化 Copilot 集成，确保符合官方要求。

## Design Decisions

### 1. 复用 Codex Executor vs 创建独立 Copilot Executor

**决策**: 保持复用 CodexExecutor，通过 `identifier="copilot"` 区分

**理由**:
- ✅ 最小化代码变更，降低风险
- ✅ Copilot 与 Codex 共享大部分 HTTP 客户端逻辑（代理、超时、请求头）
- ✅ 通过 `if e.Identifier() == "copilot"` 条件分支已实现差异化处理
- ⚠️ 缺点：端点逻辑混合在同一执行器中，可读性略降

**替代方案考虑**:
- 创建 `CopilotChatExecutor`: 优点是清晰分离，缺点是代码重复，维护成本高

**未来优化路径**:
- 如果 Copilot 差异化需求增加（如特殊重试策略、不同认证方式），可重构为独立执行器

---

### 2. 流式响应强制策略

**决策**: 强制设置 `stream=true`，移除 `stream=false` 覆盖

**理由**:
- ✅ Copilot chat/completions 端点明确拒绝 `stream=false`（400 Bad Request）
- ✅ 日志证据显示当前实现导致所有请求失败
- ✅ 修改简单，风险低（仅修改 1 行代码）

**实现位置**:
```go
// internal/runtime/executor/codex_executor.go:88
// 旧: body, _ = sjson.SetBytes(body, "stream", false)
// 新: body, _ = sjson.SetBytes(body, "stream", true)
```

**影响分析**:
- 现有用户: 无影响（当前实现本身就失败）
- 新用户: 正常工作
- 向后兼容性: 完全兼容

---

### 3. 端点选择优先级设计

**决策**: 四级优先级链，支持灵活配置和 token 自动派生

**优先级**:
1. `auth.Attributes["base_url"]` - 用户显式配置
2. `auth.Metadata["base_url"]` - 元数据覆盖
3. Token 派生 `proxy-ep=<host>` - 自动检测
4. 默认 `https://api.githubcopilot.com` - 官方端点

**理由**:
- ✅ 支持企业自建代理（优先级 1）
- ✅ 支持动态配置（优先级 2）
- ✅ 支持个人 Copilot token（优先级 3，常见格式）
- ✅ 提供稳定后备（优先级 4）

**端点路径处理**:
- ❌ 拒绝 `/backend-api/codex` 后缀（Codex Responses API 专用）
- ✅ 统一使用 `/chat/completions` 端点（Copilot Chat API）

**实现细节**:
```go
// service.go:199-205 - 清理无效路径
if strings.HasSuffix(strings.TrimRight(raw, "/"), "/backend-api/codex") {
    delete(auth.Attributes, "base_url")  // 强制回退到默认或 token 派生
}

// codex_executor.go:65-85 - 端点选择
candidate := attributes.base_url || metadata.base_url || deriveCopilotBaseFromToken(token)
if candidate == "" { candidate = "https://api.githubcopilot.com" }
url := strings.TrimSuffix(candidate, "/") + "/chat/completions"
```

---

### 4. 认证流程设计（双层 OAuth）

**决策**: 保持现有 GitHub Device Flow + Copilot Token Exchange 架构

**流程**:
```
用户触发 --copilot-auth-login
  ↓
POST github.com/login/device/code
  ↓
显示 user_code，打开浏览器
  ↓
轮询 github.com/login/oauth/access_token
  ↓
获取 github_access_token
  ↓
GET api.github.com/copilot_internal/v2/token
  ↓
获取 copilot access_token + refresh_in
  ↓
持久化到 JSON 文件
```

**存储结构**:
```json
{
  "access_token": "<copilot_token>",
  "github_access_token": "<github_token>",
  "expires_at": 1234567890,
  "refresh_in": 28800
}
```

**Token 刷新策略**:
- 基于 `refresh_in` 字段（通常 8 小时）
- 安全边际：`RefreshSafetyMarginSeconds = 60` 秒
- 触发时机：`current_time >= last_refresh + (refresh_in - 60)`

**理由**:
- ✅ 符合 GitHub 官方认证规范
- ✅ 支持长期 session（通过 refresh_in 预刷新）
- ✅ 无需用户重复授权

---

### 5. 请求头标准化

**决策**: 强制包含所有必需的 HTTP 请求头

**必需头部**:
```go
Authorization: Bearer <access_token>
Content-Type: application/json
Accept: text/event-stream          // 流式
user-agent: GitHubCopilotChat/0.26.7
editor-version: vscode/1.0
editor-plugin-version: copilot-chat/0.26.7
openai-intent: conversation-panel
x-github-api-version: 2025-04-01
x-request-id: <UUID>
```

**理由**:
- ✅ 符合官方客户端行为（通过逆向分析 VS Code 扩展）
- ✅ 避免认证失败或协议错误
- ✅ 提供可追踪的请求 ID

**版本策略**:
- 硬编码版本号（`GitHubCopilotChat/0.26.7`）
- 可配置化（后续优化，如 GitHub 更新要求）

---

### 6. 错误处理策略

**决策**: 统一使用 `statusErr{code, msg}` 包装器，原样传递上游错误

**错误映射**:
| 状态码 | 处理方式 | 重试策略 |
|--------|---------|---------|
| 400 | 返回原始错误消息 | 不重试（客户端错误） |
| 401 | 返回 "Invalid API key" | 触发 token 刷新（如启用） |
| 429 | 返回原始错误消息 | 不重试（交由客户端处理） |
| 500 | 返回原始错误消息 | 不重试（上游问题） |

**理由**:
- ✅ 保持一致性（所有 executor 统一行为）
- ✅ 保留上游错误详情（便于调试）
- ✅ 避免过度封装（透明传递错误）

**特殊处理**:
- 流中断: `408 Timeout` + "stream disconnected before completion"
- 认证失败: 可选触发 token 刷新（当前未实现，后续优化）

---

### 7. 模型清单管理

**决策**: Copilot 模型独立于 OpenAI/Codex，使用 seed 预注册机制

**模型列表**:
- `gpt-5-mini`
- `grok-code-fast-1`

**Seed 注册流程**:
```
服务启动
  ↓
ensureCopilotModelsRegistered()
  ↓
GlobalModelRegistry().Register(
  clientID: "copilot:models:seed",
  models: [gpt-5-mini, grok-code-fast-1]
)
  ↓
用户添加 Copilot auth
  ↓
applyCoreAuthAddOrUpdate()
  ↓
GlobalModelRegistry().UnregisterClient("copilot:models:seed")
GlobalModelRegistry().Register(
  clientID: auth.ID,
  models: [gpt-5-mini, grok-code-fast-1]
)
```

**理由**:
- ✅ 未授权时也能展示 Copilot 模型（`/v1/models` 可见）
- ✅ 授权后自动关联到真实 auth ID
- ✅ 避免与 OpenAI 模型混淆

---

## Architecture Diagram

```
┌─────────────────────────────────────────────────────────────────┐
│                        Client Request                            │
│  POST /v1/chat/completions                                       │
│  Authorization: Bearer <copilot_token>                           │
│  Body: {"model": "gpt-5-mini", "messages": [...]}                │
└─────────────────────────────────────────────────────────────────┘
                              ↓
┌─────────────────────────────────────────────────────────────────┐
│                    AuthMiddleware                                │
│  - Extract Bearer token                                          │
│  - Match to provider="copilot"                                   │
│  - Set context: apiKey, accessProvider                           │
└─────────────────────────────────────────────────────────────────┘
                              ↓
┌─────────────────────────────────────────────────────────────────┐
│                    Route to OpenAI Handler                       │
│  /v1/chat/completions → openaiHandlers.ChatCompletions          │
└─────────────────────────────────────────────────────────────────┘
                              ↓
┌─────────────────────────────────────────────────────────────────┐
│                  Core Auth Manager                               │
│  pickNext(provider="copilot", model="gpt-5-mini")                │
│  → 返回 Auth{Provider: copilot, Metadata: {access_token, ...}}  │
└─────────────────────────────────────────────────────────────────┘
                              ↓
┌─────────────────────────────────────────────────────────────────┐
│                 CodexExecutor (Copilot Branch)                   │
│  if e.Identifier() == "copilot" {                                │
│    1. Endpoint Selection (4-tier priority)                       │
│    2. Set stream=true                                            │
│    3. Add Copilot headers                                        │
│    4. ExecuteStream() → SSE handling                             │
│  }                                                               │
└─────────────────────────────────────────────────────────────────┘
                              ↓
┌─────────────────────────────────────────────────────────────────┐
│              Upstream Copilot API                                │
│  POST https://api.githubcopilot.com/chat/completions             │
│  Headers: Authorization, user-agent, x-github-api-version, ...   │
│  Body: {"model": "gpt-5-mini", "stream": true, ...}              │
└─────────────────────────────────────────────────────────────────┘
                              ↓
┌─────────────────────────────────────────────────────────────────┐
│                    SSE Response Stream                           │
│  event: message_start                                            │
│  data: {"type": "message_start", ...}                            │
│                                                                  │
│  event: content_block_delta                                      │
│  data: {"type": "delta", "delta": {"text": "Hello"}}            │
│                                                                  │
│  data: [DONE]                                                    │
└─────────────────────────────────────────────────────────────────┘
                              ↓
┌─────────────────────────────────────────────────────────────────┐
│               SSE Parsing & Translation                          │
│  bufio.Scanner → jsonPayload() → TranslateStream()               │
│  → 转换为 OpenAI 格式（如需要）→ 返回客户端                       │
└─────────────────────────────────────────────────────────────────┘
```

---

## Trade-offs and Alternatives

### Trade-off 1: Stream=true 强制 vs 参数透传
- **选择**: 强制 stream=true
- **替代**: 透传客户端参数，失败时返回 400
- **理由**: 避免用户困惑，确保 Copilot 请求始终成功

### Trade-off 2: 复用 Codex Executor vs 独立执行器
- **选择**: 复用
- **替代**: 创建 CopilotChatExecutor
- **理由**: 减少代码重复，降低维护成本（当前差异化需求少）

### Trade-off 3: 端点硬编码 vs 全配置化
- **选择**: 默认硬编码 + 配置覆盖
- **替代**: 全部从配置读取
- **理由**: 降低配置复杂度，大部分用户使用默认值

---

## Security Considerations

### Token 存储
- ✅ JSON 文件权限：仅当前用户可读写（0600）
- ⚠️ 日志遮蔽：当前仅遮蔽 Authorization header，未遮蔽 metadata.access_token
- 🔜 改进：扩展 maskAuthorizationHeader 到 metadata 字段

### HTTPS 强制
- ✅ 所有 Copilot API 调用使用 HTTPS
- ✅ Token 派生逻辑自动添加 `https://` 前缀

### 认证令牌生命周期
- ✅ 预刷新机制避免过期 token 使用
- ✅ 安全边际（60 秒）防止边界情况

---

## Migration Path

### 从当前实现迁移
1. **无配置变更**: 用户无需修改 YAML
2. **无数据迁移**: 现有 auth JSON 文件完全兼容
3. **行为变更**: 从失败（400）→ 成功（200），纯改进

### 向后兼容性
- ✅ 配置文件格式不变
- ✅ API 接口不变
- ✅ 认证流程不变
- ✅ 日志格式不变（仅内容变化：stream=true）

---

## Testing Strategy

### 单元测试
- [ ] `deriveCopilotBaseFromToken()` - 各种 token 格式
- [ ] `shouldRefresh()` - 刷新时机计算
- [ ] `sanitizeCopilotOAuth()` - 配置默认值

### 集成测试
- [ ] OAuth Device Flow - Mock GitHub API
- [ ] Token 刷新 - Mock GitHub API
- [ ] 端点选择 - 各优先级场景

### E2E 测试
- [ ] Chat/completions 请求 - Mock Copilot API
- [ ] SSE 流式响应 - 完整生命周期
- [ ] 错误处理 - 各 HTTP 状态码

---

## Performance Considerations

### Token 刷新性能
- **频率**: 每 8 小时 1 次
- **影响**: 可忽略（单次 HTTP 请求）
- **优化**: 异步刷新，不阻塞请求

### SSE 缓冲区
- **大小**: 20MB (`bufio.Scanner` 缓冲)
- **影响**: 支持大模型响应，内存占用合理

---

## Future Enhancements

1. **独立 Copilot 执行器**: 如差异化需求增加，重构为独立组件
2. **参数验证告警**: 对不支持参数发出警告（如 `logprobs`）
3. **日志遮蔽增强**: 扩展到 metadata 敏感字段
4. **Token 格式验证**: 检查 `sk-*` 前缀，区分 Copilot vs OpenAI token
5. **配置化 User-Agent**: 支持自定义版本号（如 GitHub 更新要求）
6. **重试策略**: 401 错误时自动触发 token 刷新并重试

---

## References

- GitHub Copilot CLI 架构分析: https://medium.com/@shubh7/github-copilot-cli-architecture-features-and-operational-protocols-f230b8b3789f
- GitHub Device Flow OAuth: https://docs.github.com/en/developers/apps/building-oauth-apps/authorizing-oauth-apps#device-flow
- SSE 规范: https://html.spec.whatwg.org/multipage/server-sent-events.html
- CLIProxyAPI 现有实现: `internal/runtime/executor/codex_executor.go`, `sdk/cliproxy/auth/manager.go`
