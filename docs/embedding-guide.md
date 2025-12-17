# Embedding CLIProxyAPI in External Go Applications

This guide explains how to embed CLIProxyAPI as a library in your Go applications using the public `EmbedConfig` API, which bypasses Go's internal package restrictions.

## Why Use EmbedConfig?

The standard SDK (`sdk/cliproxy`) requires types from `internal/config`, which Go blocks from external imports:

```
use of internal package github.com/router-for-me/CLIProxyAPI/v6/internal/config not allowed
```

The `EmbedConfig` API provides a public alternative that:

- **Works in external projects** - No internal package dependencies
- **Type-safe configuration** - Full compile-time validation
- **Minimal surface area** - Only essential fields exposed
- **Backwards compatible** - Existing internal API unchanged

## Quick Start

### 1. Install the SDK

```bash
go get github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy
```

### 2. Create Your Application

```go
package main

import (
    "context"
    "log"
    "os"
    "os/signal"
    "syscall"

    "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy"
)

func main() {
    // Create public configuration
    embedCfg := &cliproxy.EmbedConfig{
        Host:    "127.0.0.1",
        Port:    8317,
        AuthDir: "./auth",
        Debug:   true,
    }

    // Build the service
    svc, err := cliproxy.NewBuilder().
        WithEmbedConfig(embedCfg).
        WithConfigPath("./config.yaml").
        Build()
    if err != nil {
        log.Fatalf("Failed to build service: %v", err)
    }

    // Setup graceful shutdown
    ctx, cancel := context.WithCancel(context.Background())
    sigCh := make(chan os.Signal, 1)
    signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

    go func() {
        <-sigCh
        log.Println("Shutting down...")
        cancel()
    }()

    // Run the service
    if err := svc.Run(ctx); err != nil && err != context.Canceled {
        log.Fatalf("Service error: %v", err)
    }
}
```

### 3. Create config.yaml

```yaml
# Provider configurations (API keys, OAuth accounts)
api-keys:
  - "your-api-key"

claude-api-key:
  - api-key: "${CLAUDE_API_KEY}"  # Environment variable support
```

### 4. Run

```bash
go run main.go
```

## EmbedConfig Reference

### Core Fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `Host` | `string` | `""` | Network interface (`127.0.0.1` for localhost-only) |
| `Port` | `int` | **Required** | Server port (1-65535) |
| `AuthDir` | `string` | `"./auth"` | OAuth token storage directory |
| `Debug` | `bool` | `false` | Enable debug logging |
| `LoggingToFile` | `bool` | `false` | Write logs to file |

### Resilience Fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `RequestRetry` | `int` | `3` | Retry attempts on failure |
| `MaxRetryInterval` | `int` | `300` | Max retry wait (seconds) |
| `DisableCooling` | `bool` | `false` | Disable quota cooldown |
| `UsageStatisticsEnabled` | `bool` | `false` | Track usage metrics |

### TLS Configuration

```go
TLS: cliproxy.TLSConfig{
    Enable: true,
    Cert:   "/path/to/cert.pem",
    Key:    "/path/to/key.pem",
}
```

### Remote Management

```go
RemoteManagement: cliproxy.RemoteManagement{
    AllowRemote:         false,  // Localhost-only by default
    SecretKey:           "your-secret-key",
    DisableControlPanel: false,
}
```

### Quota Handling

```go
QuotaExceeded: cliproxy.QuotaExceeded{
    SwitchProject:      false,
    SwitchPreviewModel: false,
}
```

## OAuth Authentication

### Option 1: Built-in SDK Auth Manager

For applications that need to run OAuth flows programmatically:

```go
import (
    "github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
)

func runOAuthLogin(authDir string, noBrowser bool) error {
    // Create token store
    tokenStore := auth.GetTokenStore(authDir)

    // Create Claude authenticator
    authenticator := auth.NewClaudeAuthenticator(tokenStore)

    // Run OAuth flow
    opts := auth.LoginOptions{NoBrowser: noBrowser}
    return authenticator.Login(context.Background(), opts)
}
```

### Option 2: CLI Authentication

Run OAuth flows using the main CLI, then use the tokens in your embedded app:

```bash
# Authenticate (creates tokens in ./auth/)
go run cmd/server/main.go -claude-login

# Browserless mode for servers
go run cmd/server/main.go -claude-login -no-browser
```

### Option 3: Direct API Keys

Use standard API keys in config.yaml without OAuth:

```yaml
claude-api-key:
  - api-key: "sk-ant-api03-..."  # Standard API key
```

## Architecture

```
External Application
        │
        ├──────────────────────────────────────────────────────────┐
        │                                                          │
        ▼                                                          ▼
  sdk/auth.Manager                                      sdk/cliproxy.Builder
        │                                                          │
  .Login("claude", nil, opts)                    .WithEmbedConfig(&EmbedConfig{})
        │                                        .WithConfigPath("config.yaml")
        ▼                                                          │
  [OAuth Flow]                                                     ▼
        │                                               [Conversion Layer]
        ▼                                                          │
  Auth File (./auth/*.json)                                        ▼
        │                                            internal/config.Config
        │                                                          │
        └──────────────────────────────────────────────────────────┤
                                                                   ▼
                                                      CLIProxyAPI Service
                                                             │
                                                             ▼
                                                    API Clients (Anthropic SDK)
                                                    (via localhost proxy)
```

### Component Responsibilities

1. **sdk/auth.Manager** - Handles OAuth authentication flows
   - Creates auth files without internal config dependency
   - Supports Claude, Codex, and other providers
   - Configurable browser behavior (`NoBrowser` option)

2. **sdk/cliproxy.Builder** - Constructs the proxy service
   - Accepts public `EmbedConfig` for server settings
   - Loads provider configs from YAML via `WithConfigPath()`
   - Merges configurations and validates

3. **Auth Files** - Persistent OAuth tokens
   - Stored in configurable `AuthDir`
   - Auto-loaded on service startup
   - Auto-refreshed during runtime

## Using with Anthropic SDK

Once the embedded proxy is running, connect using the Anthropic SDK:

```go
import (
    "github.com/anthropics/anthropic-sdk-go"
    "github.com/anthropics/anthropic-sdk-go/option"
)

func createClient() *anthropic.Client {
    return anthropic.NewClient(
        option.WithBaseURL("http://localhost:8317"),
        option.WithAPIKey("your-api-key"),  // From config.yaml api-keys
    )
}

// Send a message
resp, err := client.Messages.New(ctx, anthropic.MessageNewParams{
    Model:     anthropic.F("claude-sonnet-4-20250514"),
    MaxTokens: anthropic.Int(1024),
    Messages: anthropic.F([]anthropic.MessageParam{
        anthropic.NewUserMessage(anthropic.NewTextBlock("Hello!")),
    }),
})
```

### Streaming Responses

```go
stream := client.Messages.NewStreaming(ctx, anthropic.MessageNewParams{
    Model:     anthropic.F("claude-sonnet-4-20250514"),
    MaxTokens: anthropic.Int(1024),
    Messages: anthropic.F([]anthropic.MessageParam{
        anthropic.NewUserMessage(anthropic.NewTextBlock("Tell me a story")),
    }),
})

for stream.Next() {
    event := stream.Current()
    switch delta := event.Delta.(type) {
    case anthropic.ContentBlockDeltaEventDelta:
        if delta.Text != "" {
            fmt.Print(delta.Text)
        }
    }
}
```

## Configuration Patterns

### Development Configuration

```go
embedCfg := &cliproxy.EmbedConfig{
    Host:    "127.0.0.1",
    Port:    8317,
    AuthDir: "./auth",
    Debug:   true,
    LoggingToFile: false,  // Console output
}
```

### Production Configuration

```go
embedCfg := &cliproxy.EmbedConfig{
    Host:    "0.0.0.0",
    Port:    443,
    AuthDir: "/var/lib/cliproxy/auth",
    Debug:   false,
    LoggingToFile: true,
    TLS: cliproxy.TLSConfig{
        Enable: true,
        Cert:   "/etc/ssl/certs/server.crt",
        Key:    "/etc/ssl/private/server.key",
    },
    RemoteManagement: cliproxy.RemoteManagement{
        AllowRemote: true,
        SecretKey:   os.Getenv("MANAGEMENT_SECRET"),
    },
}
```

### Testing Configuration

```go
embedCfg := &cliproxy.EmbedConfig{
    Host:    "127.0.0.1",
    Port:    0,  // Let OS assign port
    AuthDir: t.TempDir(),
    Debug:   true,
}
```

## When to Use Each API

| Use Case | API | Reason |
|----------|-----|--------|
| External Go application | `WithEmbedConfig()` | No internal package dependencies |
| Internal tool/CLI | `WithConfig()` | Full access to internal types |
| Simple deployment | `WithConfigPath()` only | File-based configuration |
| Complex provider setup | Both APIs | EmbedConfig for server, YAML for providers |

## Complete Example

See `examples/embedding/` for a full working example that includes:

- OAuth authentication flow (`-claude-login` flag)
- Interactive chat mode with streaming (`-chat` flag)
- Inactivity timeout and auto-shutdown (`-timeout` flag)
- Model selection (`-model` flag)
- Environment variable support via `.env`

```bash
cd examples/embedding

# First-time: authenticate
go run main.go -claude-login

# Start interactive chat
go run main.go -chat

# Chat with specific model
go run main.go -chat -model claude-opus-4-5-20251101

# Server mode with 30-minute timeout
go run main.go -timeout 30
```

## Troubleshooting

### "Port must be in range 1-65535"

Set a valid port in EmbedConfig:

```go
Port: 8317,  // Required, range 1-65535
```

### "TLS cert file not found"

Ensure TLS files exist when TLS is enabled:

```go
TLS: cliproxy.TLSConfig{
    Enable: true,
    Cert:   "/path/to/cert.pem",  // Must exist
    Key:    "/path/to/key.pem",   // Must exist
}
```

### "Remote management enabled but secret key empty"

Set a secret key when allowing remote access:

```go
RemoteManagement: cliproxy.RemoteManagement{
    AllowRemote: true,
    SecretKey:   "your-secret-key",  // Required
}
```

### OAuth tokens not loading

Ensure `AuthDir` points to the correct directory:

```go
AuthDir: "./auth",  // Must match where tokens were created
```

## Related Documentation

- [SDK Usage Guide](./sdk-usage.md) - Full SDK reference (internal API)
- [SDK Advanced](./sdk-advanced.md) - Custom providers, storage backends
- [Amp CLI Integration](./amp-cli-integration.md) - IDE extension support
- [Examples](../examples/embedding/) - Working example with chat mode
