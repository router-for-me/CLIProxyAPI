# CLI Proxy API

<p align="center">
  <img src="https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go&logoColor=white" alt="Go Version">
  <img src="https://img.shields.io/badge/License-MIT-green.svg" alt="License">
  <img src="https://img.shields.io/badge/Platform-Linux%20%7C%20macOS%20%7C%20Windows-blue" alt="Platform">
</p>

<p align="center">
  <b>Universal AI API Proxy for CLI Tools</b><br>
  Seamlessly integrate OpenAI, Gemini, Claude, Codex, and more with your favorite CLI applications
</p>

<p align="center">
  <a href="README_CN.md">中文文档</a> •
  <a href="#features">Features</a> •
  <a href="#architecture">Architecture</a> •
  <a href="#installation">Installation</a> •
  <a href="#usage">Usage</a> •
  <a href="#configuration">Configuration</a>
</p>

---

## 📋 Table of Contents

- [Overview](#overview)
- [Features](#features)
- [Architecture](#architecture)
- [Tech Stack](#tech-stack)
- [Installation](#installation)
- [Quick Start](#quick-start)
- [Configuration](#configuration)
- [Usage Examples](#usage-examples)
- [Environment Variables](#environment-variables)
- [Management API](#management-api)
- [SDK Integration](#sdk-integration)
- [Contributing](#contributing)
- [License](#license)

---

## 🔭 Overview

**CLI Proxy API** is a high-performance proxy server that bridges the gap between AI service providers and CLI-based development tools. It provides standardized OpenAI-compatible API interfaces for various AI models including Gemini, Claude, Codex, Qwen, iFlow, and more.

Whether you're using Claude Code, Gemini CLI, OpenAI Codex, or any other AI coding assistant, CLIProxyAPI unifies your access patterns, handles authentication, and provides intelligent load balancing across multiple accounts.

### Why CLIProxyAPI?

- **🔄 Multi-Provider Support**: Use one tool to access many AI services
- **⚖️ Load Balancing**: Automatically distribute requests across multiple accounts
- **🔐 OAuth Integration**: Simple authentication flows without manual API key management
- **📊 Usage Tracking**: Monitor and manage your AI service consumption
- **🔌 Embeddable SDK**: Integrate the proxy directly into your Go applications

---

## ✨ Features

### Core Capabilities

- **🌐 Multi-Provider API Compatibility**
  - OpenAI-compatible endpoints (`/v1/chat/completions`, `/v1/models`)
  - Gemini API compatibility (`/v1beta/models`)
  - Claude Code native support (`/v1/messages`)
  - OpenAI Responses API support (`/v1/responses`)

- **🔐 Authentication Methods**
  - OAuth 2.0 for Google/Gemini, Claude, Codex, Qwen, iFlow, Antigravity, Kimi
  - API key authentication for all providers
  - Vertex AI service account support
  - Custom provider compatibility (e.g., OpenRouter)

- **⚖️ Intelligent Routing**
  - Round-robin load balancing across multiple accounts
  - Fill-first strategy for quota optimization
  - Automatic failover on quota exceeded
  - Model mapping and aliasing
  - Request retry with exponential backoff

- **📊 Management & Monitoring**
  - Web-based management control panel
  - RESTful Management API (`/v0/management/*`)
  - Real-time usage statistics
  - Request/response logging
  - Health check endpoints

- **🔧 Advanced Features**
  - Hot configuration reloading
  - WebSocket API support (`/v1/ws`)
  - Function calling/tools support
  - Multimodal input (text + images)
  - Streaming and non-streaming responses
  - TLS/HTTPS support

### Supported Providers

| Provider | OAuth | API Key | Models |
|----------|-------|---------|--------|
| Google Gemini | ✅ | ✅ | Gemini 2.5 Pro/Flash, etc. |
| OpenAI Codex | ✅ | ✅ | GPT-5, GPT-4, etc. |
| Anthropic Claude | ✅ | ✅ | Claude 4, Claude 3.5, etc. |
| Qwen (Alibaba) | ✅ | ❌ | Qwen 3 Coder, etc. |
| iFlow | ✅ | ❌ | GLM-4.7, etc. |
| Antigravity | ✅ | ❌ | Gemini-based models |
| Kimi (Moonshot) | ✅ | ❌ | Kimi K2.5, etc. |
| OpenRouter | ❌ | ✅ | Various |
| Vertex AI | ❌ | ✅ | Gemini on Vertex |

---

## 🏗️ Architecture

```mermaid
flowchart TB
    subgraph Clients["🖥️ Client Applications"]
        CC[Claude Code]
        GC[Gemini CLI]
        COD[Codex CLI]
        IDE[IDE Extensions]
        SDK[Custom SDKs]
    end

    subgraph Proxy["🔧 CLI Proxy API"]
        API[HTTP API Server<br/>Gin Framework]
        AUTH[Authentication Layer]
        ROUTER[Request Router]
        
        subgraph Providers["Provider Executors"]
            GEM[Google Gemini]
            CLA[Anthropic Claude]
            CODEX[OpenAI Codex]
            QW[Qwen]
            IF[iFlow]
            AG[Antigravity]
            KM[Kimi]
            OAC[OpenAI Compatible]
        end
        
        MGMT[Management API]
        WATCH[Config Watcher]
        STORE[Token Store]
    end

    subgraph Storage["💾 Storage Backends"]
        FS[File System]
        PG[(PostgreSQL)]
        GIT[Git Repository]
        S3[S3 Compatible]
    end

    subgraph External["☁️ External APIs"]
        GAPI[Google AI API]
        CAPI[Anthropic API]
        OAPI[OpenAI API]
        QAPI[Qwen API]
        IAPI[iFlow API]
    end

    Clients -->|HTTP/HTTPS| API
    API --> AUTH
    AUTH --> ROUTER
    ROUTER --> Providers
    Providers -->|Translated Requests| External
    
    API --> MGMT
    WATCH -->|Hot Reload| API
    STORE -->|Tokens/Credentials| AUTH
    
    STORE -.-> FS
    STORE -.-> PG
    STORE -.-> GIT
    STORE -.-> S3
```

### Request Flow

```mermaid
sequenceDiagram
    participant Client as Client Tool
    participant API as API Server
    participant Auth as Auth Middleware
    participant Router as Request Router
    participant Executor as Provider Executor
    participant Upstream as Upstream API

    Client->>API: Request (OpenAI/Gemini format)
    API->>Auth: Validate API Key/Token
    Auth->>Router: Get Available Credentials
    Router->>Router: Apply Routing Strategy
    Router->>Executor: Execute with Credentials
    Executor->>Executor: Translate Request Format
    Executor->>Upstream: Send Native API Request
    Upstream-->>Executor: Response (Streaming/Non-streaming)
    Executor-->>Router: Translated Response
    Router-->>API: Unified Response
    API-->>Client: OpenAI-Compatible Response
```

---

## 🛠️ Tech Stack

| Component | Technology |
|-----------|------------|
| **Language** | Go 1.26+ |
| **Web Framework** | Gin |
| **Authentication** | OAuth 2.0, JWT, API Keys |
| **Storage** | File System, PostgreSQL, Git, S3 |
| **Protocol Support** | HTTP/HTTPS, WebSocket, Server-Sent Events |
| **TLS** | Standard Go TLS (utls for advanced fingerprinting) |
| **Logging** | Logrus |
| **Configuration** | YAML with hot-reload |
| **Database** | PostgreSQL (optional) |
| **Object Storage** | MinIO-compatible S3 |

### Key Dependencies

```go
// Core
github.com/gin-gonic/gin          // HTTP web framework
github.com/sirupsen/logrus        // Structured logging
gopkg.in/yaml.v3                  // YAML configuration

// Authentication
golang.org/x/oauth2               // OAuth 2.0 support
github.com/google/uuid            // UUID generation

// Database & Storage
github.com/jackc/pgx/v5           // PostgreSQL driver
github.com/minio/minio-go/v7      // S3-compatible storage
github.com/go-git/go-git/v6       // Git operations

// Networking
github.com/gorilla/websocket      // WebSocket support
github.com/refraction-networking/utls  // TLS fingerprinting
golang.org/x/net                  // Extended networking

// Utilities
github.com/tidwall/gjson          // JSON parsing
github.com/tidwall/sjson          // JSON modification
github.com/fsnotify/fsnotify      // File system notifications
```

---

## 📦 Installation

### Prerequisites

- Go 1.26 or later (for building from source)
- Git
- (Optional) Docker for containerized deployment

### Option 1: Download Pre-built Binary

Visit the [Releases](https://github.com/luispater/CLIProxyAPI/releases) page to download the latest binary for your platform.

```bash
# Linux/macOS
curl -L -o cliproxyapi https://github.com/luispater/CLIProxyAPI/releases/latest/download/cliproxyapi-$(uname -s)-$(uname -m)
chmod +x cliproxyapi

# Windows (PowerShell)
Invoke-WebRequest -Uri "https://github.com/luispater/CLIProxyAPI/releases/latest/download/cliproxyapi-Windows-x86_64.exe" -OutFile "cliproxyapi.exe"
```

### Option 2: Build from Source

```bash
# Clone the repository
git clone https://github.com/luispater/CLIProxyAPI.git
cd CLIProxyAPI

# Build the binary
go build -o cliproxyapi ./cmd/server/

# (Optional) Install to $GOPATH/bin
go install ./cmd/server/
```

### Option 3: Docker

```bash
# Build the Docker image
docker build -t cliproxyapi .

# Run with docker-compose
docker-compose up -d
```

### Option 4: Go Install

```bash
go install github.com/router-for-me/CLIProxyAPI/v6/cmd/server@latest
```

---

## 🚀 Quick Start

### 1. Create Configuration File

```bash
# Copy the example configuration
cp config.example.yaml config.yaml

# Edit the configuration
nano config.yaml  # or use your preferred editor
```

### 2. Authenticate with Providers

```bash
# Login to Google/Gemini
./cliproxyapi -login

# Login to Claude
./cliproxyapi -claude-login

# Login to Codex
./cliproxyapi -codex-login

# Login to other providers
./cliproxyapi -qwen-login
./cliproxyapi -iflow-login
./cliproxyapi -antigravity-login
./cliproxyapi -kimi-login
```

### 3. Start the Server

```bash
# Start with default config.yaml
./cliproxyapi

# Or specify a config file
./cliproxyapi -config /path/to/config.yaml
```

The server will start on port `8317` by default.

### 4. Test the Connection

```bash
# List available models
curl http://localhost:8317/v1/models

# Test chat completion
curl -X POST http://localhost:8317/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer your-api-key" \
  -d '{
    "model": "gemini-2.5-pro",
    "messages": [{"role": "user", "content": "Hello!"}]
  }'
```

---

## ⚙️ Configuration

### Basic Configuration (config.yaml)

```yaml
# Server settings
host: ""                    # Bind address (empty = all interfaces)
port: 8317                  # Server port
tls:
  enable: false             # Enable HTTPS
cert: ""                  # TLS certificate path
  key: ""                   # TLS private key path

# Authentication directory
auth-dir: "~/.cli-proxy-api"

# API keys for client authentication
api-keys:
  - "your-secure-api-key-1"
  - "your-secure-api-key-2"

# Management API
remote-management:
  allow-remote: false       # Allow non-localhost access
  secret-key: ""            # Management API password
  disable-control-panel: false

# Debug and logging
debug: false
logging-to-file: false
logs-max-total-size-mb: 100
usage-statistics-enabled: true

# Request handling
request-retry: 3
max-retry-interval: 30
routing:
  strategy: "round-robin"   # or "fill-first"

# Quota exceeded behavior
quota-exceeded:
  switch-project: true
  switch-preview-model: true
```

### Provider Configuration

#### Gemini API Keys

```yaml
gemini-api-key:
  - api-key: "AIzaSy..."
    prefix: "prod"          # Optional: require prefix/prod/gemini-2.5-pro
    base-url: "https://generativelanguage.googleapis.com"
    models:
      - name: "gemini-2.5-pro"
        alias: "gemini-pro"
```

#### Claude API Keys

```yaml
claude-api-key:
  - api-key: "sk-ant-..."
    prefix: "team-a"
    models:
      - name: "claude-sonnet-4-5-20250929"
        alias: "claude-sonnet"
```

#### OpenAI Compatibility (e.g., OpenRouter)

```yaml
openai-compatibility:
  - name: "openrouter"
    prefix: "or"
    base-url: "https://openrouter.ai/api/v1"
    api-key-entries:
      - api-key: "sk-or-v1-..."
    models:
      - name: "moonshotai/kimi-k2:free"
        alias: "kimi-k2"
```

#### OAuth Model Aliases

```yaml
oauth-model-alias:
  gemini-cli:
    - name: "gemini-2.5-pro"
      alias: "g2.5p"
  claude:
    - name: "claude-sonnet-4-5-20250929"
      alias: "cs4.5"
```

---

## 💡 Usage Examples

### Using with Claude Code

```bash
# Set the proxy endpoint
export ANTHROPIC_BASE_URL=http://localhost:8317/v1
export ANTHROPIC_AUTH_TOKEN=your-api-key

# Run Claude Code
claude
```

### Using with Gemini CLI

```bash
# Configure Gemini CLI to use the proxy
gemini config set proxy_url http://localhost:8317

# Or use environment variable
export GEMINI_API_KEY=your-api-key
export GEMINI_BASE_URL=http://localhost:8317
```

### Using with OpenAI SDK (Python)

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://localhost:8317/v1",
    api_key="your-api-key"
)

response = client.chat.completions.create(
    model="gemini-2.5-pro",
    messages=[{"role": "user", "content": "Hello!"}]
)
print(response.choices[0].message.content)
```

### Using with cURL

```bash
# Chat completions
curl -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer your-api-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gemini-2.5-pro",
    "messages": [{"role": "user", "content": "Explain quantum computing"}],
    "stream": true
  }'

# Claude messages
curl -X POST http://localhost:8317/v1/messages \
  -H "Authorization: Bearer your-api-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-sonnet-4-5-20250929",
    "max_tokens": 1024,
    "messages": [{"role": "user", "content": "Hello Claude!"}]
  }'
```

---

## 🔧 Environment Variables

### Management

| Variable | Description | Default |
|----------|-------------|---------|
| `MANAGEMENT_PASSWORD` | Password for Management API access | - |

### Storage Backends

#### PostgreSQL

| Variable | Description | Example |
|----------|-------------|---------|
| `PGSTORE_DSN` | PostgreSQL connection string | `postgresql://user:pass@localhost/db` |
| `PGSTORE_SCHEMA` | Database schema | `public` |
| `PGSTORE_LOCAL_PATH` | Local spool directory | `/var/lib/cliproxy` |

#### Git Store

| Variable | Description | Example |
|----------|-------------|---------|
| `GITSTORE_GIT_URL` | Git repository URL | `https://github.com/org/config.git` |
| `GITSTORE_GIT_USERNAME` | Git username | `git-user` |
| `GITSTORE_GIT_TOKEN` | Git personal access token | `ghp_...` |
| `GITSTORE_LOCAL_PATH` | Local checkout path | `/data/gitstore` |

#### Object Store (S3)

| Variable | Description | Example |
|----------|-------------|---------|
| `OBJECTSTORE_ENDPOINT` | S3 endpoint | `https://s3.amazonaws.com` |
| `OBJECTSTORE_BUCKET` | Bucket name | `cliproxy-config` |
| `OBJECTSTORE_ACCESS_KEY` | Access key | `AKIA...` |
| `OBJECTSTORE_SECRET_KEY` | Secret key | `...` |
| `OBJECTSTORE_LOCAL_PATH` | Local cache path | `/data/objectstore` |

### Deployment

| Variable | Description | Values |
|----------|-------------|--------|
| `DEPLOY` | Deployment mode | `cloud` for cloud deployment |

---

## 🎛️ Management API

When enabled, the Management API provides endpoints for runtime configuration and monitoring:

### Access the Control Panel

Open `http://localhost:8317/management.html` in your browser (requires `remote-management.secret-key` to be set).

### Key Endpoints

```bash
# Get current configuration
curl http://localhost:8317/v0/management/config \
  -H "Authorization: Bearer your-management-key"

# Update configuration
curl -X PUT http://localhost:8317/v0/management/config.yaml \
  -H "Authorization: Bearer your-management-key" \
  -H "Content-Type: text/yaml" \
  --data-binary @config.yaml

# Get usage statistics
curl http://localhost:8317/v0/management/usage \
  -H "Authorization: Bearer your-management-key"

# Get auth status
curl http://localhost:8317/v0/management/get-auth-status \
  -H "Authorization: Bearer your-management-key"
```

See [MANAGEMENT_API.md](https://help.router-for.me/management/api) for complete API documentation.

---

## 📚 SDK Integration

CLIProxyAPI can be embedded directly into your Go applications:

```go
package main

import (
    "context"
    "log"
    
    "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
    "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy"
)

func main() {
    // Load configuration
    cfg, err := config.LoadConfig("config.yaml")
    if err != nil {
        log.Fatal(err)
    }
    
    // Create service
    svc, err := cliproxy.NewBuilder().
        WithConfig(cfg).
        WithConfigPath("config.yaml").
        Build()
    if err != nil {
        log.Fatal(err)
    }
    
    // Run the service
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    
    if err := svc.Run(ctx); err != nil {
        log.Fatal(err)
    }
}
```

See the `docs/` directory for detailed SDK documentation:
- [SDK Usage Guide](docs/sdk-usage.md)
- [SDK Advanced Features](docs/sdk-advanced.md)
- [SDK Access Control](docs/sdk-access.md)
- [SDK Watcher](docs/sdk-watcher.md)

---

## 🤝 Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

### Development Setup

```bash
# Clone the repository
git clone https://github.com/luispater/CLIProxyAPI.git
cd CLIProxyAPI

# Install dependencies
go mod download

# Run tests
go test ./...

# Build locally
go build -o cliproxyapi ./cmd/server/
```

### Contribution Guidelines

1. Fork the repository
2. Create your feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add some amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

Please ensure your code:
- Follows Go best practices
- Includes appropriate tests
- Updates documentation as needed
- Passes all existing tests

---

## 📄 License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

```
MIT License

Copyright (c) 2025-2005.9 Luis Pater
Copyright (c) 2025.9-present Router-For.ME

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:
```

---

## 🙏 Acknowledgments

Special thanks to all contributors and the community projects built on CLIProxyAPI:

- [vibeproxy](https://github.com/automazeio/vibeproxy) - Native macOS menu bar app
- [ProxyPal](https://github.com/heyhuynhgiabuu/proxypal) - Native macOS GUI
- [Quotio](https://github.com/nguyenphutrong/quotio) - macOS menu bar with quota tracking
- [CodMate](https://github.com/loocor/CodMate) - SwiftUI AI session manager
- [ZeroLimit](https://github.com/0xtbug/zero-limit) - Windows desktop app with Tauri
- [And many more...](https://github.com/luispater/CLIProxyAPI#who-is-with-us)

---

## 📞 Support

- 📖 Documentation: [https://help.router-for.me/](https://help.router-for.me/)
- 🐛 Issues: [GitHub Issues](https://github.com/luispater/CLIProxyAPI/issues)
- 💬 Discussions: [GitHub Discussions](https://github.com/luispater/CLIProxyAPI/discussions)

---

<p align="center">
  Made with ❤️ by the CLIProxyAPI community
</p>
