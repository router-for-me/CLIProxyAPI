# Quickstart — 启用 Packycode 转接（CLIProxyAPI）

## 1. 前置
- 已部署 CLIProxyAPI，并可访问管理接口或挂载 `config.yaml`。
- 准备可用的上游 OpenAI API Key（按照 Packycode 要求）。

## 2. 在 config.yaml 增加 packycode 字段

```yaml
packycode:
  enabled: true
  base-url: "https://codex-api.packycode.com/v1"
  requires-openai-auth: true
  wire-api: "responses"
  privacy:
    disable-response-storage: true
  defaults:
    model: "gpt-5"
    model-reasoning-effort: "high"
  credentials:
    openai-api-key: "sk-OPENAI-XXXX..."
```

- 重启或热重载配置后生效；当 `enabled=true` 时，Claude Code 兼容入口的请求将无感转接到 Packycode。

## 3. 通过管理接口（可选）
- 获取配置：`GET /v0/management/packycode`
- 覆盖配置：`PUT /v0/management/packycode`（请求体为上面 JSON 等价结构）
- 局部更新：`PATCH /v0/management/packycode`

## 4. 验证
- 使用 Claude Code 兼容入口发起一次典型请求（如代码补全/解释），应收到正常响应。
- 若缺失上游密钥或 `base-url` 无效，保存时会提示问题来源与修复建议。

## 5. 回退
- 将 `enabled` 设为 `false`，或在管理接口中禁用；系统会停止路由至 Packycode，并保持整体稳定。
