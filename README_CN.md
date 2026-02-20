# cliproxyapi++ 🚀

[![Go Report Card](https://goreportcard.com/badge/github.com/KooshaPari/cliproxyapi-plusplus)](https://goreportcard.com/report/github.com/KooshaPari/cliproxyapi-plusplus)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Docker Pulls](https://img.shields.io/docker/pulls/kooshapari/cliproxyapi-plusplus.svg)](https://hub.docker.com/r/kooshapari/cliproxyapi-plusplus)
[![GitHub Release](https://img.shields.io/github/v/release/KooshaPari/cliproxyapi-plusplus)](https://github.com/KooshaPari/cliproxyapi-plusplus/releases)

[English](README.md) | 中文

**cliproxyapi++** 是 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 的高性能、经过安全加固的分支版本。它为多种大模型提供商（Claude、Gemini、Codex 等）提供统一的 OpenAI 兼容代理接口，并集成了高级企业级功能和增强的第三方提供商支持。

---

## 📋 目录

- [核心功能](#-核心功能)
- [与主线版本的区别](#-与主线版本的区别)
- [快速开始](#-快速开始)
  - [先决条件](#先决条件)
  - [Docker 快速部署](#docker-快速部署)
  - [二进制安装](#二进制安装)
- [使用指南](#-使用指南)
  - [配置文件](#配置文件)
  - [API 示例](#api-示例)
- [身份认证](#-身份认证)
  - [Kiro OAuth](#kiro-oauth)
  - [GitHub Copilot](#github-copilot)
- [治理与加固](#-治理与加固)
- [贡献指南](#-贡献指南)
- [开源协议](#-开源协议)

---

## ✨ 核心功能

- 🛠 **OpenAI 兼容性**: 无缝通过标准 OpenAI SDK 使用 Claude、Gemini 等模型。
- 🔐 **OAuth 网页认证**: 为 Kiro (AWS CodeWhisperer) 等提供商提供精美的浏览器登录流程。
- ⚡ **高性能与扩展**: 内置频率限制、智能冷却管理和智能路由。
- 🔄 **后台令牌刷新**: 无需担心令牌过期；令牌在过期前 10 分钟自动刷新。
- 📊 **指标与监控**: 实时收集请求指标，用于调试和使用审计。
- 🛡 **安全加固**: 设备指纹识别和严格的转换器逻辑路径保护。
- 🌍 **多架构支持**: 官方 Docker 镜像支持 `amd64` 和 `arm64`。

---

## 🔍 与主线版本的区别

本分支 (`cliproxyapi++`) 在 CLIProxyAPI 核心基础上扩展了：
- **完整 GitHub Copilot 支持**: 集成 OAuth 登录和额度追踪。
- **Kiro (AWS CodeWhisperer) 集成**: 针对 Kiro 独特协议的专用处理程序。
- **扩展的提供商注册表**: 支持 MiniMax、Roo Code、Kilo AI、DeepSeek、Groq、Mistral 等。
- **自动化打包**: 完整的 Goreleaser 和多架构 Docker CI/CD 流水线。

---

## 🚀 快速开始

### 先决条件
- [Docker](https://docs.docker.com/get-docker/) 和 [Docker Compose](https://docs.docker.com/compose/install/) (推荐)
- 或者 [Go 1.26+](https://golang.org/dl/) 用于二进制构建。

### Docker 快速部署

```bash
# 创建部署目录
mkdir -p ~/cliproxy && cd ~/cliproxy

# 创建 docker-compose.yml
cat > docker-compose.yml << 'EOF'
services:
  cli-proxy-api:
    image: KooshaPari/cliproxyapi-plusplus:latest
    container_name: cliproxyapi++
    ports:
      - "8317:8317"
    volumes:
      - ./config.yaml:/CLIProxyAPI/config.yaml
      - ./auths:/root/.cli-proxy-api
      - ./logs:/CLIProxyAPI/logs
    restart: unless-stopped
EOF

# 下载示例配置
curl -o config.yaml https://raw.githubusercontent.com/KooshaPari/cliproxyapi-plusplus/main/config.example.yaml

# 启动代理
docker compose up -d
```

### 二进制安装

从 [发布页面](https://github.com/KooshaPari/cliproxyapi-plusplus/releases) 下载适用于您平台的最新版本。

```bash
chmod +x cliproxyapi++
./cliproxyapi++ --config config.yaml
```

---

## 📖 使用指南

### 配置文件

编辑 `config.yaml` 以添加您的 API 密钥或配置提供商设置。cliproxyapi++ 支持热重载；大多数更改无需重启即可生效。

```yaml
server:
  port: 8317
  debug: false

# 示例: Claude API Key
claude:
  - api-key: "sk-ant-..."
```

### API 示例

**列出模型:**
```bash
curl http://localhost:8317/v1/models \
  -H "Authorization: Bearer YOUR_ACCESS_KEY"
```

**聊天完成 (通过 OpenAI 格式调用 Claude):**
```bash
curl http://localhost:8317/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_ACCESS_KEY" \
  -d '{
    "model": "claude-3-5-sonnet",
    "messages": [{"role": "user", "content": "你好！"}]
  }'
```

---

## 🔑 身份认证

### Kiro OAuth
访问 Kiro 认证专用 Web 界面：
`http://your-server:8317/v0/oauth/kiro`

支持：
- AWS Builder ID
- AWS Identity Center (IDC)
- 从 Kiro IDE 迁移令牌

### GitHub Copilot
通过命令行登录：
```bash
./cliproxyapi++ --github-login
```

---

## 🛡 治理与加固

**cliproxyapi++** 秉持“纵深防御”理念构建：
1. **路径保护**: `pr-path-guard` CI 流水线防止对核心翻译逻辑的未经授权更改。
2. **资源加固**: 优化的基于 Alpine 的 Docker 镜像，具有最小的攻击面。
3. **可审计性**: 全面的日志记录和请求追踪（默认关闭以保护隐私）。
4. **打包治理**: 所有发布版本均通过 Goreleaser 进行加密签名和校验。

---

## 🤝 贡献指南

我们欢迎社区贡献！
- **第三方提供商**: 新 LLM 提供商的 PR 应直接提交到本仓库。
- **核心功能**: 对核心逻辑（非特定提供商）的更改通常应提交给 [主线项目](https://github.com/router-for-me/CLIProxyAPI)。

请参阅我们的 [CONTRIBUTING.md](CONTRIBUTING.md) (即将推出) 获取更多详细信息。

---

## 📜 开源协议

根据 MIT 许可证发行。详情请参阅 [LICENSE](LICENSE)。

---

<p align="center">
  由社区倾力打造 ❤️
</p>
