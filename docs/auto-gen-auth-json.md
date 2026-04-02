# `auths/*.json` 与 OpenAI OAuth 刷新链路排查记录

日期：2026-04-02

## 背景

本次排查的目标是确认两件事：

1. 项目中是否存在与下面这条 `curl` 完全对应的代码。
2. `auths/` 目录里的 JSON 文件，是否就是这条 `curl` 所使用的那套凭据来源。

参考 `curl`：

```bash
curl --location --request POST 'https://auth.openai.com/oauth/token' \
--header 'Content-Type: application/json' \
--data-raw '{
  "client_id": "app_LlGpXReQgckcGGUo2JrYvtJK",
  "grant_type": "refresh_token",
  "redirect_uri": "com.openai.chat://auth0.openai.com/ios/com.openai.chat/callback",
  "refresh_token": "rt_xxxxxxxxxxxxx"
}'
```

## 结论

结论分成两层：

1. 项目里可以找到同一个接口 `POST https://auth.openai.com/oauth/token` 的刷新逻辑。
2. 但找不到和上面这条 `curl` 完全一致的实现；尤其是 `client_id`、`redirect_uri` 和请求编码方式都不一致。

更具体地说：

- 项目中的 Codex OAuth 刷新逻辑，确实会请求 `https://auth.openai.com/oauth/token`
- 当前实现使用的 `client_id` 是 `app_EMoamEEZ73f0CkXaXp7hrann`
- 当前实现的默认 `redirect_uri` 是 `http://localhost:1455/auth/callback`
- 刷新请求使用 `application/x-www-form-urlencoded`
- 刷新请求本身不携带 `redirect_uri`
- 全仓未发现 `app_LlGpXReQgckcGGUo2JrYvtJK`
- 全仓未发现 `com.openai.chat://auth0.openai.com/ios/com.openai.chat/callback`

因此，项目当前代码和 `auths/*.json` 中保存的凭据，更像是本项目自己的 Codex Web/CLI OAuth 流，而不是参考 `curl` 所代表的 iOS ChatGPT App OAuth 流。

## 代码定位

### 1. OpenAI Codex OAuth 常量

文件：`internal/auth/codex/openai_auth.go`

- `TokenURL = "https://auth.openai.com/oauth/token"`
- `ClientID = "app_EMoamEEZ73f0CkXaXp7hrann"`
- `RedirectURI = "http://localhost:1455/auth/callback"`

### 2. 刷新 token 的核心实现

同文件中的 `RefreshTokens(...)` 会构造如下表单参数：

- `client_id`
- `grant_type=refresh_token`
- `refresh_token`
- `scope=openid profile email`

随后向 `TokenURL` 发起 `POST` 请求，并使用：

- `Content-Type: application/x-www-form-urlencoded`
- `Accept: application/json`

### 3. 实际调用入口

文件：`internal/runtime/executor/codex_executor.go`

`CodexExecutor.Refresh(...)` 会：

1. 从 `auth.Metadata["refresh_token"]` 读取 `refresh_token`
2. 调用 `codexauth.NewCodexAuth(...).RefreshTokensWithRetry(...)`
3. 用返回的新 token 更新 `auth.Metadata`

也就是说，运行期真正触发刷新时，走的是 `executor -> codex auth -> OpenAI /oauth/token` 这条链路。

## `auths/` 目录检查结果

排查时，`auths/` 目录内实际只有 2 个 JSON 文件，且都属于 `codex` 类型。

这些文件的共同特征：

- 顶层包含 `access_token`
- 顶层包含 `refresh_token`
- 顶层包含 `id_token`
- 顶层包含 `account_id`
- 顶层包含 `email`
- 顶层包含 `expired`
- 顶层包含 `last_refresh`
- 顶层包含 `type = "codex"`

对 `access_token` / `id_token` 的非敏感 claim 进行解码后，可以看到：

- `iss` 为 `https://auth.openai.com`
- `aud` 指向 OpenAI API / 当前客户端
- `scp` 包含 `openid`、`email`、`profile`、`offline_access`
- `client_id` 为 `app_EMoamEEZ73f0CkXaXp7hrann`

这说明：

1. `auths/*.json` 确实保存了可用于续期的 `refresh_token`
2. 这些文件对应的是项目当前 Codex OAuth 客户端
3. 这些文件没有体现出参考 `curl` 中那组 iOS 专用 `client_id` / `redirect_uri`

## 与参考 `curl` 的关键差异

### 1. `client_id` 不同

参考 `curl`：

- `app_LlGpXReQgckcGGUo2JrYvtJK`

项目当前实现与 `auths/*.json` 解码结果：

- `app_EMoamEEZ73f0CkXaXp7hrann`

### 2. `redirect_uri` 不同

参考 `curl`：

- `com.openai.chat://auth0.openai.com/ios/com.openai.chat/callback`

项目当前实现：

- `http://localhost:1455/auth/callback`

并且需要注意，项目里的 refresh 请求本身并不会带 `redirect_uri`。

### 3. 请求体编码方式不同

参考 `curl` 使用：

- `Content-Type: application/json`

项目当前实现使用：

- `Content-Type: application/x-www-form-urlencoded`

### 4. 仓库内未发现参考 `curl` 的特征值

全仓搜索结果显示，以下内容均未命中：

- `app_LlGpXReQgckcGGUo2JrYvtJK`
- `com.openai.chat://auth0.openai.com/ios/com.openai.chat/callback`
- `com.openai.chat`

因此，仓库中不存在对该 iOS `curl` 的直接字面实现。

## 推断

基于当前代码和 `auths/*.json` 的内容，可以做出以下推断：

- 项目已经实现了 OpenAI OAuth token 刷新
- 刷新接口与参考 `curl` 的目标地址相同
- 但项目使用的是另一套 OAuth 客户端配置
- `auths/*.json` 保存的是项目当前登录流拿到的 token，而不是参考 `curl` 对应的 iOS App 凭据

## 安全注意

`auths/*.json` 中包含真实的：

- `access_token`
- `refresh_token`
- `id_token`

这些内容属于高敏感凭据，不应直接贴到文档、Issue、聊天记录或提交到公开仓库。
