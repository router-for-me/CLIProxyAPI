# cliproxyapi++ 🚀

[![Go Report Card](https://goreportcard.com/badge/github.com/KooshaPari/cliproxyapi-plusplus)](https://goreportcard.com/report/github.com/KooshaPari/cliproxyapi-plusplus)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Docker Pulls](https://img.shields.io/docker/pulls/kooshapari/cliproxyapi-plusplus.svg)](https://hub.docker.com/r/kooshapari/cliproxyapi-plusplus)
[![GitHub Release](https://img.shields.io/github/v/release/KooshaPari/cliproxyapi-plusplus)](https://github.com/KooshaPari/cliproxyapi-plusplus/releases)

English | [中文](README_CN.md)

**cliproxyapi++** is a high-performance, hardened fork of [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI). It provides a unified, OpenAI-compatible proxy interface for various LLM providers (Claude, Gemini, Codex, etc.) with advanced enterprise-grade features and enhanced third-party provider support.

---

## 📋 Table of Contents

- [Key Features](#-key-features)
- [Differences from Mainline](#-differences-from-mainline)
- [Getting Started](#-getting-started)
  - [Prerequisites](#prerequisites)
  - [Docker Quick Start](#docker-quick-start)
  - [Binary Installation](#binary-installation)
- [Usage](#-usage)
  - [Configuration](#configuration)
  - [API Examples](#api-examples)
- [Authentication](#-authentication)
  - [Kiro OAuth](#kiro-oauth)
  - [GitHub Copilot](#github-copilot)
- [Governance & Hardening](#-governance--hardening)
- [Contributing](#-contributing)
- [License](#-license)

---

## ✨ Key Features

- 🛠 **OpenAI Compatibility**: Seamlessly use Claude, Gemini, and others through standard OpenAI SDKs.
- 🔐 **OAuth Web Authentication**: Beautiful, browser-based login flow for providers like Kiro (AWS CodeWhisperer).
- ⚡ **Performance & Scaling**: Built-in rate limiting, intelligent cooldown management, and smart routing.
- 🔄 **Background Token Refresh**: Never worry about token expiration; tokens refresh automatically 10 minutes before they expire.
- 📊 **Metrics & Monitoring**: Real-time request metrics collection for debugging and usage auditing.
- 🛡 **Security Hardened**: Device fingerprinting and strict path guards for translator logic.
- 🌍 **Multi-arch Support**: Official Docker images for both `amd64` and `arm64`.

---

## 🔍 Differences from Mainline

This fork (`cliproxyapi++`) extends the core CLIProxyAPI with:
- **Full GitHub Copilot Support**: Integrated OAuth login and quota tracking.
- **Kiro (AWS CodeWhisperer) Integration**: Specialized handlers for Kiro's unique protocol.
- **Extended Provider Registry**: Support for MiniMax, Roo Code, Kilo AI, DeepSeek, Groq, Mistral, and more.
- **Automated Packaging**: Full Goreleaser and multi-arch Docker CI/CD pipeline.

---

## 🚀 Getting Started

### Prerequisites
- [Docker](https://docs.docker.com/get-docker/) and [Docker Compose](https://docs.docker.com/compose/install/) (recommended)
- OR [Go 1.26+](https://golang.org/dl/) for binary builds.

### Docker Quick Start

```bash
# Create deployment directory
mkdir -p ~/cliproxy && cd ~/cliproxy

# Create docker-compose.yml
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

# Download example config
curl -o config.yaml https://raw.githubusercontent.com/KooshaPari/cliproxyapi-plusplus/main/config.example.yaml

# Start the proxy
docker compose up -d
```

### Binary Installation

Download the latest release for your platform from the [Releases page](https://github.com/KooshaPari/cliproxyapi-plusplus/releases).

```bash
chmod +x cliproxyapi++
./cliproxyapi++ --config config.yaml
```

---

## 📖 Usage

### Configuration

Edit `config.yaml` to add your API keys or configure provider settings. cliproxyapi++ supports hot-reloading; most changes take effect immediately without restart.

```yaml
server:
  port: 8317
  debug: false

# Example: Claude API Key
claude:
  - api-key: "sk-ant-..."
```

### API Examples

**List Models:**
```bash
curl http://localhost:8317/v1/models \
  -H "Authorization: Bearer YOUR_ACCESS_KEY"
```

**Chat Completion (Claude via OpenAI format):**
```bash
curl http://localhost:8317/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_ACCESS_KEY" \
  -d '{
    "model": "claude-3-5-sonnet",
    "messages": [{"role": "user", "content": "Hello!"}]
  }'
```

---

## 🔑 Authentication

### Kiro OAuth
Access the dedicated web UI for Kiro authentication:
`http://your-server:8317/v0/oauth/kiro`

Supports:
- AWS Builder ID
- AWS Identity Center (IDC)
- Token migration from Kiro IDE

### GitHub Copilot
Login via the command line:
```bash
./cliproxyapi++ --github-login
```

---

## 🛡 Governance & Hardening

**cliproxyapi++** is built with "Defense in Depth" in mind:
1. **Path Protection**: The `pr-path-guard` CI workflow prevents unauthorized changes to core translation logic.
2. **Resource Hardening**: Optimized Alpine-based Docker images with minimal attack surface.
3. **Auditability**: Comprehensive logging and request tracking (disabled by default for privacy).
4. **Packaging Governance**: All releases are cryptographically signed and checksummed using Goreleaser.

---

## 🤝 Contributing

We welcome community contributions! 
- **Third-party Providers**: PRs for new LLM providers should be submitted directly to this repository.
- **Core Features**: Changes to core logic (not provider-specific) should generally be proposed to the [mainline project](https://github.com/router-for-me/CLIProxyAPI).

Please see our [CONTRIBUTING.md](CONTRIBUTING.md) (coming soon) for more details.

---

## 📜 License

Distributed under the MIT License. See [LICENSE](LICENSE) for more information.

---

<p align="center">
  Built with ❤️ by the community
</p>
