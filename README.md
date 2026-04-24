# CLI Proxy API

English | [中文](README_CN.md) | [日本語](README_JA.md)

A proxy server that provides OpenAI/Gemini/Claude/Codex compatible API interfaces for CLI. Supports OAuth authentication and multiple AI providers.

## Features

### Core Features
- OpenAI/Gemini/Claude/Codex compatible API endpoints for CLI models
- Streaming and non-streaming responses
- Function calling/tools support
- Multimodal input support (text and images)

### OAuth Authentication
- OpenAI Codex support (OAuth login)
- Claude Code support (OAuth login)
- Qwen Code support (OAuth login)
- iFlow support (OAuth login)

### Multi-Account Management
- Multiple accounts with round-robin load balancing
- Gemini multi-account (AI Studio Build, Gemini CLI)
- Gemini CLI now discovers models dynamically per `auth + project`: it reads `retrieveUserQuota`, probes each candidate with a minimal `generateContent` request, and only registers probe-success models in `/v1/models`
- OpenAI Codex multi-account
- Claude Code multi-account
- Qwen Code multi-account
- iFlow multi-account

### Advanced Features
- OpenAI-compatible upstream providers via config (e.g., OpenRouter)
- Model aliases and smart routing
- Circuit breaker support for OpenAI-compatible providers
- Responses API capability auto-detection (native /responses vs chat fallback)
- previous_response_id support for incremental tool turns in chat fallback mode
- Weighted provider rotation for fair scheduling
- Anthropic API key authentication
- Request-level 404 error handling optimization

### Integration
- Amp CLI and IDE extensions support
- Provider route aliases (`/api/provider/{provider}/v1...`)
- Management proxy for OAuth authentication
- Smart model fallback with automatic routing

## Quick Start

### Installation

```bash
git clone https://github.com/router-for-me/CLIProxyAPI.git
cd CLIProxyAPI
cp config.example.yaml config.yaml
./cli-proxy-new
```

### Configuration Files

The project uses **two config files** for different environments:

| File | Purpose | `auth-dir` |
|------|---------|------------|
| `config.yaml` | Local development | `./auths` (local relative path) |
| `config-277.yaml` | Production (227 server) | `/opt/cliproxy/auths` |

- **Local development (recommended)**: `./bin/air` (managed by `.air.toml`, equivalent to running with `-config config.yaml`)
- **Fallback local run**: `go run ./cmd/server`
- **Production deployment**: `./cli-proxy-new -config config-277.yaml`

For detailed configuration options, see the [User Manual](https://help.router-for.me/cn/).

### Docker

```bash
docker run -v ./config-277.yaml:/app/config.yaml -p 8080:8080 ghcr.io/router-for-me/cliproxyapi:latest
```

## Project Structure

```
cmd/               # Entry points
internal/          # Core business code
  api/             # HTTP API server
  runtime/         # Runtime and executors
  translator/      # Protocol translation
  auth/            # Authentication modules
sdk/               # Reusable SDK
test/              # Integration tests
docs/              # Documentation
examples/          # Example code
```

## Development

See [AGENTS.md](AGENTS.md) for build/test commands and code style guidelines.

### Hot Reload (Air)

Install Air (one time):

```bash
GOBIN="$(pwd)/bin" go install github.com/air-verse/air@latest
```

Start local development with hot restart:

```bash
./bin/air
```

Behavior conventions:
- Go source changes (`*.go`, `go.mod`, `go.sum`): Air rebuilds and restarts the server.
- `config.yaml` and `auths/*` changes: no Air restart; reloaded by in-process watcher.
- Production flow is unchanged: build binary and run with production config.

Troubleshooting:
- `./bin/air: no such file or directory`: run the install command from repo root.
- App does not restart after code changes: confirm `.air.toml` is loaded from repo root.
- Config changes causing full restart: ensure YAML/auth paths are excluded in `.air.toml`.

### Build

```bash
go build -o cli-proxy-new ./cmd/server
```

### Test

```bash
go test ./...
go test -v -run TestFunctionName ./package/
```

## SDK Docs

- Usage: [docs/sdk-usage.md](docs/sdk-usage.md)
- Advanced: [docs/sdk-advanced.md](docs/sdk-advanced.md)
- Access: [docs/sdk-access.md](docs/sdk-access.md)
- Watcher: [docs/sdk-watcher.md](docs/sdk-watcher.md)

## Contributing

Contributions are welcome!

1. Fork the repository
2. Create feature branch (`git checkout -b feature/amazing-feature`)
3. Commit changes (`git commit -m 'Add some amazing feature'`)
4. Push to branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

## Third-Party Projects

- [vibeproxy](https://github.com/automazeio/vibeproxy) - macOS menu bar app
- [CCS](https://github.com/kaitranntt/ccs) - Claude account switcher CLI
- [Quotio](https://github.com/nguyenphutrong/quotio) - macOS menu bar app
- [CodMate](https://github.com/loocor/CodMate) - macOS SwiftUI app
- [ProxyPilot](https://github.com/Finesssee/ProxyPilot) - Windows CLI
- [霖君](https://github.com/wangdabaoqq/LinJun) - Cross-platform desktop app
- [CLIProxyAPI Dashboard](https://github.com/itsmylife44/cliproxyapi-dashboard) - Web admin panel

> Submit a PR to add your project to this list.

## License

MIT License - see [LICENSE](LICENSE) for details.
