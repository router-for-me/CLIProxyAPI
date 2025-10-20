# Phase 1 — Data Model（Packycode 配置）

## 实体：PackycodeConfig

- enabled: boolean（默认 false）
- base-url: string（必填；例如 https://codex-api.packycode.com/v1）
- requires-openai-auth: boolean（默认 true）
- wire-api: string（固定 "responses"）
- privacy:
  - disable-response-storage: boolean（默认 true）
- defaults:
  - model: string（默认 "gpt-5"）
  - model-reasoning-effort: string（枚举：low|medium|high；默认 "high"）
- credentials:
  - openai-api-key: string（当 requires-openai-auth=true 时必填）
- effective-source: string（只读；生效配置来源：config.yaml | env | codex-cli）

## 关系与派生

- 与系统“路由规则”存在使用关系：当 `enabled=true` 时，Claude Code 兼容入口的请求将按策略转发至 Packycode 上游。
- `effective-source` 由加载顺序派生，不可由用户直接设置。

## 校验规则（Validation）

1) base-url 必须为非空有效 URL（HTTP/HTTPS）。
2) requires-openai-auth=true 时，credentials.openai-api-key 必须为非空字符串。
3) wire-api 必须为 "responses"。
4) model-reasoning-effort 必须为 {low, medium, high} 之一。
5) 隐私默认不持久化：privacy.disable-response-storage 默认为 true。

## 状态与过渡（State）

- enable → disable：允许随时切换；disable 后不再路由至 Packycode。
- update-config：更新上述字段时需进行校验，不通过则拒绝保存并返回可执行修复提示。
