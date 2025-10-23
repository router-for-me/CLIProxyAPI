## Why
为提升与上游“Copilot API”（下文简称 copilot）的互通与可选性，需要在系统内新增一个对外 provider `copilot`，并提供与现有体系一致的本地 OAuth 登录能力（回调端口与安全约束遵循当前项目约定）。这将允许用户在不暴露长期密钥的情况下完成登录，并将令牌以与现有提供商一致的方式持久化与管理。

## What Changes
- 新增对外 provider：`copilot`（管理端 `/v0/management/providers`、`/v0/management/models` 与 `/v1/models` 可见）。
- 新增两种登录方式：
  - GitHub Device Flow：`GET /v0/management/copilot-device-code` 返回 `device_code/user_code/verification_uri`，后台轮询 `/login/oauth/access_token` 与 `/copilot_internal/v2/token` 完成落盘；`GET /v0/management/copilot-device-status` 返回 `wait|ok|error`。
  - 回调式（保留）：`GET /v0/management/copilot-auth-url` 启动授权 + `/copilot/callback` 回调（仅本地或管理密钥保护）。
- 令牌持久化遵循现有机制（`auth-dir` 与管理端文件 API）；JSON 包含 `type=copilot` 与最小必要字段。
- 模型注册：registry 显式新增 `GetCopilotModels()`，当前与 OpenAI 模型集合同步，后续可替换为 Copilot 实际清单。
- 新增 CLI 开关：`--copilot-auth-login`（别名 `--copilot-login`），用于本地触发 Copilot 登录：默认走 Device Flow，打印 `user_code/verification_uri` 与状态轮询结果；支持回调式（结合 `--no-browser` 打印授权 URL）。

## Impact
- Affected specs:
  - `provider-integration`（新增 `copilot` 提供商曝光与模型/路由注册）
  - `auth`（新增 `copilot` 的 Device Flow + 回调式登录能力及管理端点）
  - `cli`（新增 `--copilot-auth-login` 与 `--copilot-login` 别名，行为与现有 `--claude-login/--codex-login` 一致）
- Affected code (indicative only):
  - 路由与管理端：`internal/api/server.go`、`internal/api/handlers/management/auth_files.go`
  - 认证实现：`internal/auth/copilot/*`（默认常量 + 设备流路径），`sdk/auth/copilot.go`（可选）
  - 提供商注册与模型侧：`internal/util/provider.go`、`sdk/cliproxy/providers.go`
  - CLI：`internal/cmd/login.go`（或对应 CLI 入口）增加参数与输出；
  - 配置：`internal/config/config.go` 与 `config.example.yaml`（如需添加专属基址/密钥项）
