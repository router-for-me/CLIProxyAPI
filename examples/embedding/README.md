# CLIProxyAPI Embedding Example

This example demonstrates how to embed CLIProxyAPI as a library in your external Go application using the public `EmbedConfig` API.

## Overview

The example shows:

- ✅ Using `EmbedConfig` without internal package dependencies
- ✅ **Built-in OAuth authentication** for Claude and Gemini (no main CLI required!)
- ✅ Configuring essential server options (host, port, TLS, management API)
- ✅ Loading provider configurations from YAML
- ✅ **Response verification** using Gemini to fact-check Claude's responses
- ✅ **Auto-correction**: Failed verifications are fed back to Claude for correction
- ✅ **Readline support**: Arrow keys for history navigation and line editing
- ✅ **Shared auth directory**: Use `--auth-dir` to share tokens with main server
- ✅ **Automatic test request** to verify authentication
- ✅ Starting the service programmatically
- ✅ Graceful shutdown on SIGINT/SIGTERM

## Prerequisites

- **Go 1.21+**: Required for building the application
- **Provider Accounts** (optional): Claude Code, OpenAI Codex, Gemini CLI, etc.
- **Configuration File**: `config.yaml` with your provider settings

## Quick Start

### 1. Clone the Repository

```bash
git clone https://github.com/router-for-me/CLIProxyAPI.git
cd CLIProxyAPI/examples/embedding
```

### 2. Configure Providers

You have **three options** for providing credentials:

#### Option A: Environment Variables (Recommended)

1. Copy the example .env file:
```bash
cp .env.example .env
```

2. Edit `.env` and add your credentials:
```bash
# Claude OAuth Token (from Claude Code CLI or claude.ai/settings/developer)
CLAUDE_API_KEY=sk-ant-oat01-xxxxx...
```

3. The example will automatically load environment variables via `config.yaml`:
```yaml
claude-api-key:
  - api-key: "${CLAUDE_API_KEY}"  # Reads from .env
```

#### Option B: Direct in config.yaml

Edit `config.yaml` and add credentials directly:

```yaml
claude-api-key:
  - api-key: "sk-ant-oat01-xxxxx"  # OAuth token
  # OR
  - api-key: "sk-ant-api03-xxxxx"  # Standard API key

gemini-api-key:
  - api-key: "AIza..."
```

⚠️ **Warning**: Don't commit `config.yaml` if it contains secrets!

#### Option C: OAuth Flow (File-Based Tokens)

For file-based OAuth tokens, run the authentication flow:

**With Browser** (default):
```bash
# From this example directory
go run main.go -claude-login
```

**Browserless Mode** (for servers/headless environments):
```bash
# From this example directory
go run main.go -claude-login -no-browser

# This will print a URL - manually visit it in a browser
# The OAuth callback will still work even from a different machine
```

This creates authentication tokens in the auth directory (`./.cli-proxy-api` or `~/.cli-proxy-api`) that the embedded service will use.

### 3. Run the Example

```bash
go run main.go
```

The service will start on `http://localhost:8317`.

### 4. Test the Service

Send a request to the proxy:

```bash
curl -X POST http://localhost:8317/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer test-key" \
  -d '{
    "model": "claude-sonnet-4-20250514",
    "messages": [{"role": "user", "content": "Hello!"}]
  }'
```

Note: The `Authorization: Bearer test-key` must match an entry in `config.yaml`'s `api-keys` list.

### 5. Graceful Shutdown

Press `Ctrl+C` to gracefully stop the service.

## Code Structure

### Main Application (`main.go`)

```go
embedCfg := &cliproxy.EmbedConfig{
    Host:    "127.0.0.1",
    Port:    8317,
    AuthDir: "./auth",
    Debug:   true,
    // ... other server options
}

svc, err := cliproxy.NewBuilder().
    WithEmbedConfig(embedCfg).
    WithConfigPath("./config.yaml").
    Build()

// Run the service
ctx := context.Background()
svc.Run(ctx)
```

### Configuration Files

- **`main.go`**: Application entry point with `EmbedConfig` setup
- **`config.yaml`**: Provider-specific configurations (API keys, OAuth accounts, model mappings)
- **`go.mod`**: Go module dependencies (uses `replace` directive for local development)

## Configuration Options

### Server Configuration (EmbedConfig)

Essential server options are set via `EmbedConfig` in `main.go`:

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `Host` | `string` | `""` (all interfaces) | Network interface to bind (use `127.0.0.1` for localhost-only) |
| `Port` | `int` | **Required** | Server port (1-65535) |
| `AuthDir` | `string` | `./.cli-proxy-api` or `~/.cli-proxy-api` | Directory for OAuth token storage |
| `Debug` | `bool` | `false` | Enable debug-level logging |
| `LoggingToFile` | `bool` | `false` | Write logs to files (vs. stdout) |
| `UsageStatisticsEnabled` | `bool` | `false` | Track usage statistics in memory |
| `DisableCooling` | `bool` | `false` | Disable quota cooldown scheduling |
| `RequestRetry` | `int` | `3` | Number of retry attempts on failure |
| `MaxRetryInterval` | `int` | `300` | Max retry wait time (seconds) |

#### TLS Configuration

```go
TLS: cliproxy.TLSConfig{
    Enable: true,
    Cert:   "/path/to/cert.pem",
    Key:    "/path/to/key.pem",
}
```

#### Remote Management

```go
RemoteManagement: cliproxy.RemoteManagement{
    AllowRemote:         false,  // Localhost-only by default
    SecretKey:           "...",  // Required if AllowRemote is true
    DisableControlPanel: false,  // Enable web UI
}
```

#### Quota Handling

```go
QuotaExceeded: cliproxy.QuotaExceeded{
    SwitchProject:      false,  // Auto-switch projects on quota
    SwitchPreviewModel: false,  // Auto-switch to preview models
}
```

### Provider Configuration (config.yaml)

Provider-specific settings are defined in `config.yaml`:

- **API Keys**: Direct API keys for Claude, Codex, Gemini, etc.
- **OAuth Accounts**: File-backed OAuth tokens from `-login` flows
- **Model Mappings**: Alias configurations and routing rules
- **Amp CLI Integration**: Model mappings and upstream configuration

See `config.yaml.example` in this directory for a complete template. Copy it to get started:

```bash
cp config.yaml.example config.yaml
```

## OAuth Authentication Flows

For providers that use OAuth (Claude Code, OpenAI Codex, Qwen Code, Gemini):

### 1. Authenticate

**Claude (from this example directory):**
```bash
go run main.go -claude-login
# Or browserless: go run main.go -claude-login -no-browser
```

**Gemini (for response verification):**
```bash
# Requires Google Cloud project ID
go run main.go -gemini-login -project_id YOUR_PROJECT_ID
# Or browserless: go run main.go -gemini-login -project_id YOUR_PROJECT_ID -no-browser
```
Get your project ID from https://console.cloud.google.com

**Other providers (from repository root):**
```bash
go run cmd/server/main.go -codex-login
go run cmd/server/main.go -qwen-login
```

This opens a browser for OAuth consent and stores tokens in the auth directory.

### 2. Configure AuthDir in EmbedConfig

```go
embedCfg := &cliproxy.EmbedConfig{
    AuthDir: "./auth",  // Points to OAuth token directory
    // ...
}
```

### 3. Run Your Embedded Application

The embedded service automatically loads OAuth tokens from `AuthDir`.

## Architecture

```
┌─────────────────────────────────────┐
│  Your Go Application (main.go)      │
│                                     │
│  ┌───────────────────────────────┐  │
│  │ EmbedConfig (Public API)      │  │
│  │ - Host, Port, TLS             │  │
│  │ - AuthDir, Debug              │  │
│  │ - RemoteManagement            │  │
│  └───────────────────────────────┘  │
│             ↓                       │
│  ┌───────────────────────────────┐  │
│  │ cliproxy.Service              │  │
│  │ - HTTP Server                 │  │
│  │ - OAuth Token Management      │  │
│  │ - Provider Routing            │  │
│  │ - Request Translation         │  │
│  └───────────────────────────────┘  │
│             ↓                       │
│  ┌───────────────────────────────┐  │
│  │ AI Providers                  │  │
│  │ - Claude Code (OAuth)         │  │
│  │ - OpenAI Codex (OAuth)        │  │
│  │ - Gemini CLI (API Key)        │  │
│  │ - Custom Providers            │  │
│  └───────────────────────────────┘  │
└─────────────────────────────────────┘
```

## Troubleshooting

### "Config file not found"

Ensure `config.yaml` exists in the example directory or update the path in `main.go`:

```go
configPath := "/absolute/path/to/config.yaml"
```

### "Port must be in range 1-65535"

Set a valid port in `EmbedConfig`:

```go
Port: 8317,  // Valid range: 1-65535
```

### "TLS cert file not found"

If TLS is enabled, ensure cert and key files exist:

```go
TLS: cliproxy.TLSConfig{
    Enable: true,
    Cert:   "/path/to/cert.pem",  // Must exist
    Key:    "/path/to/key.pem",   // Must exist
}
```

### "Remote management is enabled but secret key is empty"

Set a secret key when enabling remote management:

```go
RemoteManagement: cliproxy.RemoteManagement{
    AllowRemote: true,
    SecretKey:   "your-secret-key",  // Required when AllowRemote is true
}
```

### "No providers configured"

Add at least one provider in `config.yaml`:

```yaml
api-keys:
  - "your-api-key-here"

claude-api-key:
  - api-key: "sk-ant-api..."
```

## Production Deployment

For production use:

### 1. Update go.mod

Replace the `replace` directive with the actual version:

```go
module your-app

go 1.24

require github.com/router-for-me/CLIProxyAPI/v6 v6.x.x
```

### 2. Enable TLS

```go
TLS: cliproxy.TLSConfig{
    Enable: true,
    Cert:   "/etc/ssl/certs/server.crt",
    Key:    "/etc/ssl/private/server.key",
}
```

### 3. Secure Management API

```go
RemoteManagement: cliproxy.RemoteManagement{
    AllowRemote: false,  // Localhost-only recommended
    SecretKey:   os.Getenv("MANAGEMENT_SECRET"),
}
```

### 4. Use Environment Variables

```go
embedCfg := &cliproxy.EmbedConfig{
    Host:    os.Getenv("CLIPROXY_HOST"),
    Port:    getEnvInt("CLIPROXY_PORT", 8317),
    AuthDir: os.Getenv("CLIPROXY_AUTH_DIR"),
    // ...
}
```

## SDK Integration Testing

The example includes built-in integration testing using the Anthropic SDK for Go.

### Test Request Mode

Without the `-chat` flag, the example sends a test request to verify authentication:

```bash
go run main.go
# Output: Sending test message to Claude...
# Claude says: Hello! How can I help you today?
```

### Interactive Chat Mode

With `-chat`, the example demonstrates full streaming integration:

```bash
go run main.go -chat
# Starts interactive chat with streaming responses
```

## Response Verification with Gemini

The example supports **automatic response verification** using Gemini to fact-check Claude's responses. This feature uses Gemini's web search/grounding capabilities to verify factual claims.

### Setup

1. **Authenticate with Gemini:**
   ```bash
   go run main.go -gemini-login -project_id YOUR_PROJECT_ID
   ```

2. **Start chat (verification auto-enabled):**
   ```bash
   go run main.go -chat
   ```

### How It Works

- After each Claude response, Gemini verifies factual claims
- Results display with status emojis: ✅ Verified, ⚠️ Partially Verified, ❌ Inaccurate, ℹ️ Unable to Verify
- **Auto-correction**: If verification fails (❌ or ⚠️), feedback is sent to Claude for correction
- Verification streams in real-time below Claude's response
- Results are cached (5 min TTL) to avoid redundant API calls

### Chat Commands

| Command | Description |
|---------|-------------|
| `verify` | Show current verification status |
| `verify on` | Enable verification |
| `verify off` | Disable verification |
| `help` | Show all commands |

### Command-Line Options

```bash
# Disable verification even when Gemini is configured
go run main.go -chat -verify=false

# Use a different Claude model (default: claude-opus-4-5-20251101)
go run main.go -chat -model claude-sonnet-4-20250514

# Use a different Gemini model for verification (default: gemini-2.5-flash)
go run main.go -chat -verify-model gemini-2.5-pro

# Use a custom auth directory (default: ./.cli-proxy-api or ~/.cli-proxy-api)
go run main.go -chat -auth-dir ~/.my-auth

# Set inactivity timeout (default: 15 minutes, 0 to disable)
go run main.go -chat -timeout 30
```

### Features

- **Auto-Correction**: Failed verifications trigger Claude to correct its response
- **Readline Support**: Arrow keys for history navigation, Ctrl+R for search
- **Caching**: Identical responses return cached verification (shows 📋 Cached indicator)
- **Rate Limiting**: Automatic 60-second cooldown on rate limit errors
- **Graceful Fallback**: Chat continues normally if verification fails
- **Visual Separation**: Yellow header distinguishes verification from Claude's response
- **Shared Auth**: Use `--auth-dir` to share tokens with main CLIProxyAPI server

### Custom SDK Client Code

Here's how to create your own test client:

```go
import (
    "github.com/anthropics/anthropic-sdk-go"
    "github.com/anthropics/anthropic-sdk-go/option"
)

// Create client pointing to embedded proxy
client := anthropic.NewClient(
    option.WithBaseURL("http://localhost:8317"),
    option.WithAPIKey("test-key"),  // Matches api-keys in config.yaml
)

// Send a message
resp, err := client.Messages.New(ctx, anthropic.MessageNewParams{
    Model:     anthropic.F("claude-sonnet-4-20250514"),
    MaxTokens: anthropic.Int(1024),
    Messages: anthropic.F([]anthropic.MessageParam{
        anthropic.NewUserMessage(anthropic.NewTextBlock("Hello!")),
    }),
})

// Stream responses
stream := client.Messages.NewStreaming(ctx, params)
for stream.Next() {
    event := stream.Current()
    // Process streaming events...
}
```

### Testing Tips

1. **Verify OAuth First**: Run `go run main.go -claude-login` before testing API calls
2. **Check API Key**: Ensure `api-keys` in config.yaml matches your client's Authorization header
3. **Use Debug Mode**: Set `Debug: true` in EmbedConfig (in `createEmbedConfig()`) for detailed logs
4. **Model Names**: Use full model names like `claude-sonnet-4-20250514` (not SDK constants)
5. **Check Server Logs**: In chat mode, logs go to `./logs/server.log`

## Performance Notes

- **Startup Time**: ~100-200ms for service initialization
- **Memory Usage**: ~50-100MB base + provider overhead
- **Request Latency**: <10ms proxy overhead + upstream latency
- **Concurrent Requests**: Supports thousands of concurrent connections

## FAQ

### Q: Can I use this without config.yaml?

No. The builder requires `WithConfigPath()` for provider configurations. You can create a minimal config.yaml:

```yaml
api-keys:
  - "default-key"
```

### Q: How do I add custom providers?

Add them to `config.yaml` under `openai-compatibility`:

```yaml
openai-compatibility:
  - name: "custom-provider"
    base-url: "https://api.custom.com/v1"
    api-key-entries:
      - api-key: "your-key"
    models:
      - name: "model-name"
        alias: "custom-model"
```

### Q: What's the difference between EmbedConfig and WithConfig?

- **`WithEmbedConfig`**: Public API for external applications (no internal package dependencies)
- **`WithConfig`**: Internal API for advanced use cases (requires `internal/config` access)

Use `WithEmbedConfig` for embedding in external projects.

### Q: How do I handle multiple instances?

Use shared storage backends (PostgreSQL, Git, Object Storage) for token/config persistence:

```go
// See docs/sdk-advanced.md for multi-instance deployment patterns
```

## Related Documentation

- [SDK Usage Guide](../../docs/sdk-usage.md)
- [Embedding Guide](../../docs/embedding-guide.md)
- [SDK Advanced](../../docs/sdk-advanced.md)
- [Amp CLI Integration](../../docs/amp-cli-integration.md)

## License

This example is part of the CLIProxyAPI project and is licensed under the same terms.

## Support

For issues, questions, or contributions:
- **Issues**: https://github.com/router-for-me/CLIProxyAPI/issues
- **Discussions**: https://github.com/router-for-me/CLIProxyAPI/discussions
