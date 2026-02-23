# cliproxyapi++ 🚀

[![Go Report Card](https://goreportcard.com/badge/github.com/KooshaPari/cliproxyapi-plusplus)](https://goreportcard.com/report/github.com/KooshaPari/cliproxyapi-plusplus)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Docker Pulls](https://img.shields.io/docker/pulls/kooshapari/cliproxyapi-plusplus.svg)](https://hub.docker.com/r/kooshapari/cliproxyapi-plusplus)
[![GitHub Release](https://img.shields.io/github/v/release/KooshaPari/cliproxyapi-plusplus)](https://github.com/KooshaPari/cliproxyapi-plusplus/releases)

[English](README.md) | 中文

**cliproxyapi++** 是 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 的高性能、经过安全加固的终极分支版本。它秉持“纵深防御”的开发理念和“库优先”的架构设计，为多种主流及私有大模型提供 OpenAI 兼容接口，并具备企业级稳定性。

---

## 🏆 深度对比：`++` 版本的优势

为什么选择 **cliproxyapi++** 而不是主线版本？虽然主线版本专注于开源社区的稳定性，但 `++` 版本则是为高并发、生产级环境而设计的，在安全性、自动化生命周期管理和广泛的提供商支持方面具有显著优势。

### 📊 功能对比矩阵

| 功能特性 | 主线版本 | CLIProxyAPI+ | **cliproxyapi++** |
| :--- | :---: | :---: | :---: |
| **核心代理逻辑** | ✅ | ✅ | ✅ |
| **基础模型支持** | ✅ | ✅ | ✅ |
| **标准 Web UI** | ❌ | ✅ | ✅ |
| **高级认证 (Kiro/Copilot)** | ❌ | ⚠️ | ✅ **(完整支持)** |
| **后台令牌自动刷新** | ❌ | ❌ | ✅ **(自动刷新)** |
| **安全加固** | 基础 | 基础 | ✅ **(企业级)** |
| **频率限制与冷却** | ❌ | ❌ | ✅ **(智能路由)** |
| **核心逻辑复用** | `internal/` | `internal/` | ✅ **(`pkg/llmproxy`)** |
| **CI/CD 流水线** | 基础 | 基础 | ✅ **(签名/多架构)** |

---

## 🔍 技术差异与安全加固

### 1. 架构演进：`pkg/llmproxy`
主线版本将核心逻辑保留在 `internal/` 目录下（这会导致外部 Go 项目无法直接导入），而 **cliproxyapi++** 已将整个翻译和代理引擎重构为清晰、公开的 `pkg/llmproxy` 库。
*   **可复用性**: 您可以直接在自己的 Go 应用程序中导入代理逻辑。
*   **解耦**: 实现了配置管理与执行逻辑的严格分离。

### 2. 企业级身份认证与生命周期管理
*   **完整 GitHub Copilot 集成**: 不仅仅是 API 包装。`++` 包含完整的 OAuth 设备流登录、每个凭据的额度追踪以及智能会话管理。
*   **Kiro (AWS CodeWhisperer) 2.0**: 提供定制化的 Web 界面 (`/v0/oauth/kiro`)，支持通过浏览器进行 AWS Builder ID 和 Identity Center 登录。
*   **后台令牌刷新**: 专门的后台服务实时监控令牌状态，并在过期前 10 分钟自动刷新，确保智能体任务零停机。

### 3. 安全加固（“纵深防御”）
*   **路径保护 (Path Guard)**: 定制的 GitHub Action 工作流 (`pr-path-guard`)，防止在 PR 过程中对关键的 `internal/translator/` 逻辑进行任何未经授权的修改。
*   **设备指纹**: 生成唯一且不可变的设备标识符，以满足严格的提供商安全检查，防止账号被标记。
*   **加固的 Docker 基础镜像**: 基于经过审计的 Alpine 3.22.0 层构建，仅包含最少软件包，显著降低了潜在的攻击面。

### 4. 高规模运营支持
*   **智能冷却机制**: 自动化的“冷却”系统可检测提供商端的频率限制，并智能地暂停对特定供应商的请求，同时将流量路由至其他可用节点。
*   **统一模型转换器**: 复杂的映射层，允许您请求 `claude-3-5-sonnet`，而由代理自动处理目标供应商（如 Vertex、AWS、Anthropic 等）的具体协议要求。

---

## 🚀 快速开始

### 先决条件
- 已安装 [Docker](https://docs.docker.com/get-docker/) 和 [Docker Compose](https://docs.docker.com/compose/install/)
- 或安装 [Go 1.26+](https://golang.org/dl/)

### 一键部署 (Docker)

```bash
# 设置部署目录
mkdir -p ~/cliproxy && cd ~/cliproxy
curl -o config.yaml https://raw.githubusercontent.com/KooshaPari/cliproxyapi-plusplus/main/config.example.yaml

# 创建 compose 文件
cat > docker-compose.yml << 'EOF'
services:
  cliproxy:
    image: KooshaPari/cliproxyapi-plusplus:latest
    container_name: cliproxyapi++
    ports: ["8317:8317"]
    volumes:
      - ./config.yaml:/CLIProxyAPI/config.yaml
      - ./auths:/root/.cli-proxy-api
      - ./logs:/CLIProxyAPI/logs
    restart: unless-stopped
EOF

docker compose up -d
```

---

## 🛠️ 高级用法

### 扩展的供应商支持
`cliproxyapi++` 开箱即用地支持海量模型注册：
*   **直接接入**: Claude, Gemini, OpenAI, Mistral, Groq, DeepSeek.
*   **聚合器**: OpenRouter, Together AI, Fireworks AI, Novita AI, SiliconFlow.
*   **私有协议**: Kiro (AWS), GitHub Copilot, Roo Code, Kilo AI, MiniMax.

### API 规范
代理提供两个主要的 API 表面：
1.  **OpenAI 兼容接口**: `/v1/chat/completions` 和 `/v1/models`。
2.  **管理接口**:
    *   `GET /v0/config`: 查看当前（支持热重载）的配置。
    *   `GET /v0/oauth/kiro`: 交互式 Kiro 认证界面。
    *   `GET /v0/logs`: 实时日志查看。

---

## 🤝 贡献指南

我们维持严格的质量门禁，以保持项目的“加固”状态：
1.  **代码风格**: 必须通过 `golangci-lint` 检查，且无任何警告。
2.  **测试覆盖**: 所有的翻译器逻辑必须包含单元测试。
3.  **治理**: 对 `pkg/` 核心逻辑的修改需要先在 Issue 中进行讨论。

请参阅 **[CONTRIBUTING.md](CONTRIBUTING.md)** 了解更多详情。

---

## 📜 开源协议

本项目根据 MIT 许可证发行。详情请参阅 [LICENSE](LICENSE) 文件。

---

<p align="center">
  <b>为现代智能体技术栈打造的加固级 AI 基础设施。</b><br>
  由社区倾力打造 ❤️
</p>
