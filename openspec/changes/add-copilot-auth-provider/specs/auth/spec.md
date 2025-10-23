## ADDED Requirements
### Requirement: Copilot OAuth Login Flow
系统 SHALL 为 `copilot` 提供本地 OAuth 登录能力，暴露启动授权与回调端点；回调仅限本地或受管理密钥保护；令牌与元数据以现有机制落盘并可通过管理端文件 API 管理。

#### Scenario: Start authorization URL
- **WHEN** 管理端启用
- **THEN** `GET /v0/management/copilot-auth-url` 返回可用于启动授权的 URL

#### Scenario: OAuth callback handling
- **GIVEN** 用户完成授权并被重定向至回调
- **WHEN** `GET /copilot/callback?code=...&state=...`
- **THEN** 服务端完成令牌交换并将凭证写入 `auth-dir`，对管理端可见

#### Scenario: Local-only or secret-protected
- **WHEN** 远程管理未启用
- **THEN** 回调接口仅允许本地回环访问；启用远程管理时需管理密钥保护

