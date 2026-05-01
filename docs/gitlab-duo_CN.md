# GitLab Duo 使用说明

CLIProxyAPI 现在可以把 GitLab Duo 当作一等 Provider 来使用，而不是仅仅把它当成简单的文本补全封装。

当前支持：

- OAuth 登录
- personal access token 登录
- 自动刷新 GitLab `direct_access` 元数据
- 根据 GitLab 返回的元数据动态发现模型
- 针对 Anthropic 和 OpenAI/Codex 托管模型的 GitLab AI gateway 原生路由
- Claude 兼容与 OpenAI 兼容下游 API

## 这意味着什么

如果 GitLab Duo 返回的是 Anthropic 托管模型，CLIProxyAPI 会通过 GitLab AI gateway 的 Anthropic 代理转发，并复用现有的 Claude executor 路径。

如果 GitLab Duo 返回的是 OpenAI 托管模型，CLIProxyAPI 会通过 GitLab AI gateway 的 OpenAI 代理转发，并复用现有的 Codex/OpenAI executor 路径。

这让 GitLab Duo 的运行时行为更接近内置的 `codex` Provider：

- Claude 兼容客户端可以通过 `/v1/messages` 使用 GitLab Duo 模型
- OpenAI 兼容客户端可以通过 `/v1/chat/completions` 使用 GitLab Duo 模型
- OpenAI Responses 客户端可以通过 `/v1/responses` 使用 GitLab Duo 模型

模型列表不是硬编码的。CLIProxyAPI 会从 GitLab `direct_access` 中读取当前模型元数据，并注册：

- 一个稳定别名：`gitlab-duo`
- GitLab 当前发现到的托管模型名，例如 `claude-sonnet-4-5` 或 `gpt-5-codex`

## 登录

OAuth 登录：

```bash
./CLIProxyAPI -gitlab-login
```

PAT 登录：

```bash
./CLIProxyAPI -gitlab-token-login
```

也可以通过环境变量提供输入：

```bash
export GITLAB_BASE_URL=https://gitlab.com
export GITLAB_OAUTH_CLIENT_ID=your-client-id
export GITLAB_OAUTH_CLIENT_SECRET=your-client-secret
export GITLAB_PERSONAL_ACCESS_TOKEN=glpat-...
```

说明：

- OAuth 方式需要一个 GitLab OAuth application。
- PAT 登录需要一个能够调用 GitLab Duo 相关 API 的 personal access token。实践上，`api` scope 是最稳妥的基线。
- 自建 GitLab 实例可以通过 `GITLAB_BASE_URL` 接入。

## 如何使用模型

登录完成后，正常启动 CLIProxyAPI，并让客户端连接到本地代理。

你可以选择：

- `gitlab-duo`，始终使用该账号当前的 Duo 托管模型
- GitLab 当前发现到的 provider 模型名，如果你想显式固定模型

示例：

```bash
curl http://127.0.0.1:8080/v1/models
```

```bash
curl http://127.0.0.1:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "gitlab-duo",
    "messages": [
      {"role": "user", "content": "Write a Go HTTP middleware for request IDs."}
    ]
  }'
```

如果该 GitLab 账号当前绑定的是 Anthropic 模型，Claude 兼容客户端可以通过 Claude handler 路径直接使用它。如果当前绑定的是 OpenAI/Codex 模型，OpenAI 兼容客户端可以通过 `/v1/chat/completions` 或 `/v1/responses` 使用它。

## 模型如何保持最新

CLIProxyAPI 不内置固定的 GitLab Duo 模型清单。

它会刷新 GitLab `direct_access` 元数据，并使用返回的 `model_details` 以及可能存在的模型列表字段，让本地 registry 尽量与 GitLab 当前分配的托管模型保持一致。

这比硬编码模型名更符合 GitLab 当前公开 API 的实际契约。

## 当前覆盖范围

GitLab Duo Provider 目前已经具备：

- OAuth 和 PAT 登录流程
- Duo gateway 凭据的运行时刷新
- Anthropic gateway 原生路由
- OpenAI/Codex gateway 原生路由
- Claude 兼容和 OpenAI 兼容路径的 handler 级 smoke 测试

当前仍未覆盖：

- websocket 或 session 级别的完全对齐
- GitLab 公开 gateway 契约之外的 IDE 专有能力

## 参考资料

- GitLab Code Suggestions API: https://docs.gitlab.com/api/code_suggestions/
- GitLab Agent Assistant 与 managed credentials: https://docs.gitlab.com/user/duo_agent_platform/agent_assistant/
- GitLab Duo 模型选择: https://docs.gitlab.com/user/gitlab_duo/model_selection/
