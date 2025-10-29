## ADDED Requirements

### Requirement: MiniMax Anthropic-compatible registration and routing
系统 SHALL 基于认证条目的实际后端特征注册可用模型列表与路由执行器，以保证 `/v1/models` 的可用性与准确性。

#### Scenario: Claude base_url points to MiniMax Anthropic compatibility
- **WHEN** `provider=claude` 的认证条目其 `attributes.base_url` 等于 `https://api.minimaxi.com/anthropic`
- **THEN** 系统 SHALL 仅注册模型 `MiniMax-M2`
- **AND** 系统 SHALL 不注册任何 `claude-*` 模型
- **AND** Provider 视为 `minimax` 并由 MiniMax 专属执行器处理

#### Scenario: Route minimax-* to minimax executor (heuristic fallback)
- **GIVEN** 模型名以 `minimax-` 为前缀
- **WHEN** 动态注册表未返回 Provider
- **THEN** 系统 SHALL 将 Provider 识别为 `minimax`

#### Scenario: Pre-register MiniMax executor (no auth)
- **GIVEN** 尚未配置 MiniMax 认证
- **WHEN** 用户请求 `MiniMax-M2`
- **THEN** 系统 SHOULD 返回 `auth_not_found`（而非执行器缺失），以保证错误类型稳定

### Examples

#### Example Request (OpenAI-compatible style)
```http
POST /v1/chat/completions HTTP/1.1
Content-Type: application/json
Authorization: Bearer sk-xxxx

{
  "model": "MiniMax-M2",
  "messages": [
    {"role": "user", "content": "hi"}
  ],
  "stream": false
}
```

#### Example Response (error when no auth)
```json
{
  "error": {
    "type": "auth_error",
    "code": "auth_not_found",
    "message": "missing credentials for provider minimax"
  }
}
```

#### Example Response (models listing under Claude handler)
```json
{
  "object": "list",
  "data": [
    {"id": "MiniMax-M2", "object": "model", "owned_by": "minimax"}
  ]
}
```
