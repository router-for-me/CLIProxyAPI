# Amp CLI 集成指南

本指南说明如何在 Amp CLI 和 Amp IDE 扩展中使用 CLIProxyAPI，通过 OAuth 让你能够把已有的 Google/ChatGPT/Claude 订阅与 Amp 的 CLI 一起使用。

## 目录

- [概述](#概述)
  - [应该认证哪些服务提供商？](#应该认证哪些服务提供商)
- [架构](#架构)
- [配置](#配置)
- [设置](#设置)
- [用法](#用法)
- [故障排查](#故障排查)

## 概述

Amp CLI 集成为 Amp 的 API 模式添加了专用路由，同时保持与现有 CLIProxyAPI 功能的完全兼容。这样你可以在同一个代理服务器上同时使用传统 CLIProxyAPI 功能和 Amp CLI。

### 主要特性

- **提供者路由别名**：将 Amp 的 `/api/provider/{provider}/v1...` 路径映射到 CLIProxyAPI 处理器
- **管理代理**：将 OAuth 和账号管理请求转发到 Amp 控制平面
- **智能回退**：自动将未配置的模型路由到 ampcode.com
- **密钥管理**：可配置优先级（配置 > 环境变量 > 文件），缓存 5 分钟
- **安全优先**：管理路由默认限制为 localhost
- **自动 gzip 处理**：自动解压来自 Amp 上游的响应

### 你可以做什么

- 使用 Amp CLI 搭配你的 Google 账号（Gemini 3 Pro Preview、Gemini 2.5 Pro、Gemini 2.5 Flash）
- 使用 Amp CLI 搭配你的 ChatGPT Plus/Pro 订阅（GPT-5、GPT-5 Codex 模型）
- 使用 Amp CLI 搭配你的 Claude Pro/Max 订阅（Claude Sonnet 4.5、Opus 4.1）
- 将 Amp IDE 扩展（VS Code、Cursor、Windsurf 等）与同一个代理一起使用
- 通过一个代理同时运行多个 CLI 工具（Factory + Amp）
- 将未配置的模型自动路由到 ampcode.com

### 应该认证哪些服务提供商？

**重要**：需要认证的提供商取决于你安装的 Amp 版本当前使用的模型和功能。Amp 的不同智能模式和子代理会使用不同的提供商：

- **Smart 模式**：使用 Google/Gemini 模型（Gemini 3 Pro）
- **Rush 模式**：使用 Anthropic/Claude 模型（Claude Haiku 4.5）
- **Oracle 子代理**：使用 OpenAI/GPT 模型（GPT-5 medium reasoning）
- **Librarian 子代理**：使用 Anthropic/Claude 模型（Claude Sonnet 4.5）
- **Search 子代理**：使用 Anthropic/Claude 模型（Claude Haiku 4.5）
- **Review 功能**：使用 Google/Gemini 模型（Gemini 2.5 Flash-Lite）

有关 Amp 当前使用哪些模型的最新信息，请参阅 **[Amp 模型文档](https://ampcode.com/models)**。

#### 回退行为

CLIProxyAPI 采用智能回退机制：

1. **本地已认证提供商**（`--login`、`--codex-login`、`--claude-login`）：
   - 请求使用**你的 OAuth 订阅**（ChatGPT Plus/Pro、Claude Pro/Max、Google 账号）
   - 享受订阅自带的额度
   - 不消耗 Amp 额度

2. **本地未认证提供商**：
   - 请求自动转发到 **ampcode.com**
   - 使用 Amp 的后端提供商连接
   - 如果提供商是付费的（OpenAI、Anthropic 付费档），**需要消耗 Amp 额度**
   - 若 Amp 额度不足，可能产生错误

**建议**：对你有订阅的所有提供商都进行认证，以最大化价值并尽量减少 Amp 额度消耗。如果没有覆盖 Amp 使用的全部提供商，请确保为回退请求准备足够的 Amp 额度。

## 架构

### 请求流

```
Amp CLI/IDE
  ↓
  ├─ Provider API requests (/api/provider/{provider}/v1/...)
  │   ↓
  │   ├─ Model configured locally?
  │   │   YES → Use local OAuth tokens (OpenAI/Claude/Gemini handlers)
  │   │   NO  → Forward to ampcode.com (reverse proxy)
  │   ↓
  │   Response
  │
  └─ Management requests (/api/auth, /api/user, /api/threads, ...)
      ↓
      ├─ Localhost check (security)
      ↓
      └─ Reverse proxy to ampcode.com
          ↓
          Response (auto-decompressed if gzipped)
```

### 组件

Amp 集成以模块化路由模块（`internal/api/modules/amp/`）实现，包含以下组件：

1. **路由别名**（`routes.go`）：将 Amp 风格的路径映射到标准处理器
2. **反向代理**（`proxy.go`）：将管理请求转发到 ampcode.com
3. **回退处理器**（`fallback_handlers.go`）：将未配置的模型路由到 ampcode.com
4. **密钥管理**（`secret.go`）：多来源 API 密钥解析并带缓存
5. **主模块**（`amp.go`）：负责注册和配置

## 配置

### 基础配置

在 `config.yaml` 中新增以下字段：

```yaml
# Amp 上游控制平面（管理路由必需）
amp-upstream-url: "https://ampcode.com"

# 可选：覆盖 API key（否则使用环境变量或文件）
# amp-upstream-api-key: "your-amp-api-key"

# 安全性：将管理路由限制为 localhost（推荐）
amp-restrict-management-to-localhost: true
```

### 密钥解析优先级

Amp 模块以如下优先级解析 API key：

| 来源 | 键名 | 优先级 | 缓存 |
|------|------|--------|------|
| 配置文件 | `amp-upstream-api-key` | 高 | 无 |
| 环境变量 | `AMP_API_KEY` | 中 | 无 |
| Amp 密钥文件 | `~/.local/share/amp/secrets.json` | 低 | 5 分钟 |

**建议**：日常使用时采用 Amp 密钥文件（最低优先级）。该文件由 `amp login` 自动管理。

### 安全设置

**`amp-restrict-management-to-localhost`**（默认：`true`）

启用后，管理路由（`/api/auth`、`/api/user`、`/api/threads` 等）只接受来自 localhost（127.0.0.1、::1）的连接，可防止：
- 浏览器探测式攻击
- 对管理端点的远程访问
- 基于 CORS 的攻击
- 伪造头攻击（例如 `X-Forwarded-For: 127.0.0.1`）

#### 工作原理

此限制使用**实际的 TCP 连接地址**（`RemoteAddr`），而非 `X-Forwarded-For` 等 HTTP 头，能防止头部伪造，但有重要影响：

- ✅ **直接连接可用**：在本机或服务器直接运行 CLIProxyAPI 时适用
- ⚠️ **可能不适用于反向代理场景**：部署在 nginx、Cloudflare 等代理后，请求源会显示为代理 IP 而非 localhost

#### 反向代理部署

若需要在反向代理（nginx、Caddy、Cloudflare Tunnel 等）后运行 CLIProxyAPI：

1. **关闭 localhost 限制**：
   ```yaml
   amp-restrict-management-to-localhost: false
   ```

2. **使用替代安全措施**：
   - 防火墙规则限制管理路由访问
   - 代理层认证（HTTP Basic Auth、OAuth）
   - 网络隔离（VPN、Tailscale、Cloudflare Access）
   - 将 CLIProxyAPI 仅绑定 `127.0.0.1`，并通过 SSH 隧道访问

3. **nginx 示例配置**（阻止外部访问管理路由）：
   ```nginx
   location /api/auth { deny all; }
   location /api/user { deny all; }
   location /api/threads { deny all; }
   location /api/internal { deny all; }
   ```

**重要**：只有在理解安全影响并已采取其他防护措施时，才关闭 `amp-restrict-management-to-localhost`。

## 设置

### 1. 配置 CLIProxyAPI

创建或编辑 `config.yaml`：

```yaml
port: 8317
auth-dir: "~/.cli-proxy-api"

# Amp 集成
amp-upstream-url: "https://ampcode.com"
amp-restrict-management-to-localhost: true

# 其他常规设置...
debug: false
logging-to-file: true
```

### 2. 认证提供商

为要使用的提供商执行 OAuth 登录：

**Google 账号（Gemini 2.5 Pro、Gemini 2.5 Flash、Gemini 3 Pro Preview）：**
```bash
./cli-proxy-api --login
```

**ChatGPT Plus/Pro（GPT-5、GPT-5 Codex）：**
```bash
./cli-proxy-api --codex-login
```

**Claude Pro/Max（Claude Sonnet 4.5、Opus 4.1）：**
```bash
./cli-proxy-api --claude-login
```

令牌会保存到：
- Gemini: `~/.cli-proxy-api/gemini-<email>.json`
- OpenAI Codex: `~/.cli-proxy-api/codex-<email>.json`
- Claude: `~/.cli-proxy-api/claude-<email>.json`

### 3. 启动代理

```bash
./cli-proxy-api --config config.yaml
```

或使用 tmux 在后台运行（推荐用于远程服务器）：

```bash
tmux new-session -d -s proxy "./cli-proxy-api --config config.yaml"
```

### 4. 配置 Amp CLI

#### 方案 A：配置文件

编辑 `~/.config/amp/settings.json`：

```json
{
  "amp.url": "http://localhost:8317"
}
```

#### 方案 B：环境变量

```bash
export AMP_URL=http://localhost:8317
```

### 5. 登录并使用 Amp

通过代理登录（请求会被代理到 ampcode.com）：

```bash
amp login
```

像平常一样使用 Amp：

```bash
amp "Write a hello world program in Python"
```

### 6. （可选）配置 Amp IDE 扩展

该代理同样适用于 VS Code、Cursor、Windsurf 等 Amp IDE 扩展。

1. 在 IDE 中打开 Amp 扩展设置
2. 将 **Amp URL** 设置为 `http://localhost:8317`
3. 用你的 Amp 账号登录
4. 在 IDE 中开始使用 Amp

CLI 和 IDE 可同时使用该代理。

## 用法

### 支持的路由

#### 提供商别名（始终可用）

这些路由即使未配置 `amp-upstream-url` 也可使用：

- `/api/provider/openai/v1/chat/completions`
- `/api/provider/openai/v1/responses`
- `/api/provider/anthropic/v1/messages`
- `/api/provider/google/v1beta/models/:action`

Amp CLI 会使用你在 CLIProxyAPI 中通过 OAuth 认证的模型来调用这些路由。

#### 管理路由（需要 `amp-upstream-url`）

这些路由会被代理到 ampcode.com：

- `/api/auth` - 认证
- `/api/user` - 用户资料
- `/api/meta` - 元数据
- `/api/threads` - 会话线程
- `/api/telemetry` - 使用遥测
- `/api/internal` - 内部 API

**安全性**：默认限制为 localhost。

### 模型回退行为

当 Amp 请求模型时：

1. **检查本地配置**：CLIProxyAPI 是否有该模型提供商的 OAuth 令牌？
2. **如果有**：路由到本地处理器（使用你的 OAuth 订阅）
3. **如果没有**：转发到 ampcode.com（使用 Amp 的默认路由）

这实现了无缝混用：
- 你已配置的模型（Gemini、ChatGPT、Claude）→ 你的 OAuth 订阅
- 未配置的模型 → Amp 的默认提供商

### 示例 API 调用

**使用本地 OAuth 的聊天补全：**
```bash
curl http://localhost:8317/api/provider/openai/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-5",
    "messages": [{"role": "user", "content": "Hello"}]
  }'
```

**管理端点（仅限 localhost）：**
```bash
curl http://localhost:8317/api/user
```

## 故障排查

### 常见问题

| 症状 | 可能原因 | 解决方案 |
|------|----------|----------|
| `/api/provider/...` 返回 404 | 路径错误 | 确保路径准确：`/api/provider/{provider}/v1...` |
| `/api/user` 返回 403 | 非 localhost 请求 | 在同一机器上访问，或关闭 `amp-restrict-management-to-localhost`（不推荐） |
| 提供商返回 401/403 | OAuth 缺失或过期 | 重新运行 `--codex-login` 或 `--claude-login` |
| Amp gzip 错误 | 响应解压问题 | 更新到最新构建；自动解压应能处理 |
| 模型未走代理 | Amp URL 设置错误 | 检查 `amp.url` 设置或 `AMP_URL` 环境变量 |
| CORS 错误 | 受保护的管理端点 | 使用 CLI/终端而非浏览器 |

### 诊断

**查看代理日志：**
```bash
# 若 logging-to-file: true
tail -f logs/requests.log

# 若运行在 tmux 中
tmux attach-session -t proxy
```

**临时开启调试模式：**
```yaml
debug: true
```

**测试基础连通性：**
```bash
# 检查代理是否运行
curl http://localhost:8317/v1/models

# 检查 Amp 特定路由
curl http://localhost:8317/api/provider/openai/v1/models
```

**验证 Amp 配置：**
```bash
# 检查 Amp 是否使用代理
amp config get amp.url

# 或检查环境变量
echo $AMP_URL
```

### 安全清单

- ✅ 保持 `amp-restrict-management-to-localhost: true`（默认）
- ✅ 不要将代理暴露到公共网络（绑定到 localhost 或使用防火墙/VPN）
- ✅ 使用 `amp login` 管理的 Amp 密钥文件（`~/.local/share/amp/secrets.json`）
- ✅ 定期重新登录轮换 OAuth 令牌
- ✅ 若处理敏感数据，使用加密磁盘存储配置和 auth-dir
- ✅ 保持代理二进制为最新版本以获取安全修复

## 其他资源

- [CLIProxyAPI 主文档](https://help.router-for.me/)
- [Amp CLI 官方手册](https://ampcode.com/manual)
- [管理 API 参考](https://help.router-for.me/management/api)
- [SDK 文档](sdk-usage.md)

## 免责声明

此集成仅用于个人或教育用途。使用反向代理或替代 API 基址可能违反提供商的服务条款。你需要对自己的使用方式负责。账号可能会被限速、锁定或封禁。软件不附带任何保证，使用风险自负。
