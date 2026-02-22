# User Guide: Library-First Architecture

## What is "Library-First"?

The **Library-First** architecture means that all the core proxy logic (translation, authentication, provider communication) is packaged as a reusable Go library (`pkg/llmproxy`). This allows you to embed the proxy directly into your own applications instead of running it as a separate service.

## Why Use the Library?

### Benefits Over Standalone CLI

| Aspect | Standalone CLI | Embedded Library |
|--------|---------------|------------------|
| **Deployment** | Separate process, network calls | In-process, zero network overhead |
| **Configuration** | External config file | Programmatic config |
| **Customization** | Limited to config options | Full code access |
| **Performance** | Network latency + serialization | Direct function calls |
| **Monitoring** | External metrics/logs | Internal hooks/observability |

### When to Use Each

**Use Standalone CLI when**:
- You want a simple, drop-in proxy
- You're integrating with existing OpenAI clients
- You don't need custom logic
- You prefer configuration over code

**Use Embedded Library when**:
- You're building a Go application
- You need custom request/response processing
- You want to integrate with your auth system
- You need fine-grained control over routing

## Quick Start: Embedding in Your App

### Step 1: Install the SDK

```bash
go get github.com/KooshaPari/cliproxyapi-plusplus/sdk/cliproxy
```

### Step 2: Basic Embedding

Create `main.go`:

```go
package main

import (
    "context"
    "log"

    "github.com/KooshaPari/cliproxyapi-plusplus/pkg/llmproxy/config"
    "github.com/KooshaPari/cliproxyapi-plusplus/sdk/cliproxy"
)

func main() {
    // Load config
    cfg, err := config.LoadConfig("config.yaml")
    if err != nil {
        log.Fatalf("Failed to load config: %v", err)
    }

    // Build service
    svc, err := cliproxy.NewBuilder().
        WithConfig(cfg).
        WithConfigPath("config.yaml").
        Build()
    if err != nil {
        log.Fatalf("Failed to build service: %v", err)
    }

    // Run service
    ctx := context.Background()
    if err := svc.Run(ctx); err != nil {
        log.Fatalf("Service error: %v", err)
    }
}
```

### Step 3: Create Config File

Create `config.yaml`:

```yaml
server:
  port: 8317

providers:
  claude:
    type: "claude"
    enabled: true
    models:
      - name: "claude-3-5-sonnet"
        enabled: true

auth:
  dir: "./auths"
  providers:
    - "claude"
```

### Step 4: Run Your App

```bash
# Add your Claude API key
echo '{"type":"api_key","token":"sk-ant-xxx"}' > auths/claude.json

# Run your app
go run main.go
```

Your embedded proxy is now running on port 8317 with OpenAI-compatible endpoints!

## Advanced: Custom Translators

If you need to support a custom LLM provider, you can implement your own translator:

```go
package main

import (
    "context"

    "github.com/KooshaPari/cliproxyapi-plusplus/pkg/llmproxy/translator"
    openai "github.com/sashabaranov/go-openai"
)

// MyCustomTranslator implements the Translator interface
type MyCustomTranslator struct{}

func (t *MyCustomTranslator) TranslateRequest(
    ctx context.Context,
    req *openai.ChatCompletionRequest,
) (*translator.ProviderRequest, error) {
    // Convert OpenAI request to your provider's format
    return &translator.ProviderRequest{
        Endpoint: "https://api.myprovider.com/v1/chat",
        Headers: map[string]string{
            "Content-Type": "application/json",
        },
        Body: map[string]interface{}{
            "messages": req.Messages,
            "model":    req.Model,
        },
    }, nil
}

func (t *MyCustomTranslator) TranslateResponse(
    ctx context.Context,
    resp *translator.ProviderResponse,
) (*openai.ChatCompletionResponse, error) {
    // Convert provider response back to OpenAI format
    return &openai.ChatCompletionResponse{
        ID:      resp.ID,
        Choices: []openai.ChatCompletionChoice{
            {
                Message: openai.ChatCompletionMessage{
                    Role:    "assistant",
                    Content: resp.Content,
                },
            },
        },
    }, nil
}

// Register your translator
func main() {
    myTranslator := &MyCustomTranslator{}

    svc, err := cliproxy.NewBuilder().
        WithConfig(cfg).
        WithConfigPath("config.yaml").
        WithCustomTranslator("myprovider", myTranslator).
        Build()
    // ...
}
```

## Advanced: Custom Auth Management

Integrate with your existing auth system:

```go
package main

import (
    "context"
    "sync"

    "github.com/KooshaPari/cliproxyapi-plusplus/sdk/cliproxy"
)

// MyAuthProvider implements TokenClientProvider
type MyAuthProvider struct {
    mu    sync.RWMutex
    tokens map[string]string
}

func (p *MyAuthProvider) Load(
    ctx context.Context,
    cfg *config.Config,
) (*cliproxy.TokenClientResult, error) {
    p.mu.RLock()
    defer p.mu.RUnlock()

    var clients []cliproxy.AuthClient
    for provider, token := range p.tokens {
        clients = append(clients, cliproxy.AuthClient{
            Provider: provider,
            Type:     "api_key",
            Token:    token,
        })
    }

    return &cliproxy.TokenClientResult{
        Clients: clients,
        Count:   len(clients),
    }, nil
}

func (p *MyAuthProvider) AddToken(provider, token string) {
    p.mu.Lock()
    defer p.mu.Unlock()
    p.tokens[provider] = token
}

func main() {
    authProvider := &MyAuthProvider{
        tokens: make(map[string]string),
    }

    // Add tokens programmatically
    authProvider.AddToken("claude", "sk-ant-xxx")
    authProvider.AddToken("openai", "sk-xxx")

    svc, err := cliproxy.NewBuilder().
        WithConfig(cfg).
        WithConfigPath("config.yaml").
        WithTokenClientProvider(authProvider).
        Build()
    // ...
}
```

## Advanced: Request Interception

Add custom logic before/after requests:

```go
svc, err := cliproxy.NewBuilder().
    WithConfig(cfg).
    WithConfigPath("config.yaml").
    WithServerOptions(
        cliproxy.WithMiddleware(func(c *gin.Context) {
            // Log request before processing
            log.Printf("Request: %s %s", c.Request.Method, c.Request.URL.Path)
            c.Next()

            // Log response after processing
            log.Printf("Response status: %d", c.Writer.Status())
        }),
        cliproxy.WithRouterConfigurator(func(e *gin.Engine, h *handlers.BaseAPIHandler, cfg *config.Config) {
            // Add custom routes
            e.GET("/my-custom-endpoint", func(c *gin.Context) {
                c.JSON(200, gin.H{"message": "custom endpoint"})
            })
        }),
    ).
    Build()
```

## Advanced: Lifecycle Hooks

Respond to service lifecycle events:

```go
hooks := cliproxy.Hooks{
    OnBeforeStart: func(cfg *config.Config) {
        log.Println("Initializing database connections...")
        // Your custom init logic
    },
    OnAfterStart: func(s *cliproxy.Service) {
        log.Println("Service ready, starting health checks...")
        // Your custom startup logic
    },
    OnBeforeShutdown: func(s *cliproxy.Service) {
        log.Println("Graceful shutdown started...")
        // Your custom shutdown logic
    },
}

svc, err := cliproxy.NewBuilder().
    WithConfig(cfg).
    WithConfigPath("config.yaml").
    WithHooks(hooks).
    Build()
```

## Configuration: Hot Reload

The embedded library automatically reloads config when files change:

```yaml
# config.yaml
server:
  port: 8317
  hot-reload: true  # Enable hot reload (default: true)

providers:
  claude:
    type: "claude"
    enabled: true
```

When you modify `config.yaml` or add/remove files in `auths/`, the library:
1. Detects the change (file system watcher)
2. Validates the new config
3. Atomically swaps the runtime config
4. Notifies background workers (token refresh, health checks)

No restart required!

## Configuration: Custom Sources

Load config from anywhere:

```go
// From environment variables
type EnvConfigLoader struct{}

func (l *EnvConfigLoader) Load() (*config.Config, error) {
    cfg := &config.Config{}

    cfg.Server.Port = getEnvInt("PROXY_PORT", 8317)
    cfg.Providers["claude"].Enabled = getEnvBool("ENABLE_CLAUDE", true)

    return cfg, nil
}

svc, err := cliproxy.NewBuilder().
    WithConfigLoader(&EnvConfigLoader{}).
    Build()
```

## Monitoring: Metrics

Access provider metrics:

```go
svc, err := cliproxy.NewBuilder().
    WithConfig(cfg).
    WithConfigPath("config.yaml").
    WithRouterConfigurator(func(e *gin.Engine, h *handlers.BaseAPIHandler, cfg *config.Config) {
        // Metrics endpoint
        e.GET("/metrics", func(c *gin.Context) {
            metrics := h.GetProviderMetrics()
            c.JSON(200, metrics)
        })
    }).
    Build()
```

Metrics include:
- Request count per provider
- Average latency
- Error rate
- Token usage
- Quota remaining

## Monitoring: Logging

Customize logging:

```go
import "log/slog"

svc, err := cliproxy.NewBuilder().
    WithConfig(cfg).
    WithConfigPath("config.yaml").
    WithLogger(slog.New(slog.NewJSONHandler(os.Stdout, nil))).
    Build()
```

Log levels:
- `DEBUG`: Detailed request/response data
- `INFO`: General operations (default)
- `WARN`: Recoverable errors (rate limits, retries)
- `ERROR`: Failed requests

## Troubleshooting

### Service Won't Start

**Problem**: `Failed to build service`

**Solutions**:
1. Check config.yaml syntax: `go run github.com/KooshaPari/cliproxyapi-plusplus/pkg/llmproxy/config@latest validate config.yaml`
2. Verify auth files exist and are valid JSON
3. Check port is not in use

### Config Changes Not Applied

**Problem**: Modified config.yaml but no effect

**Solutions**:
1. Ensure hot-reload is enabled
2. Wait 500ms for debouncing
3. Check file permissions (readable by process)
4. Verify config is valid (errors logged)

### Custom Translator Not Working

**Problem**: Custom provider returns errors

**Solutions**:
1. Implement all required interface methods
2. Validate request/response formats
3. Check error handling in TranslateRequest/TranslateResponse
4. Add debug logging

### Performance Issues

**Problem**: High latency or CPU usage

**Solutions**:
1. Enable connection pooling in HTTP client
2. Use streaming for long responses
3. Tune worker pool size
4. Profile with `pprof`

## Next Steps

- See [DEV.md](./DEV.md) for extending the library
- See [../auth/](../auth/) for authentication features
- See [../security/](../security/) for security features
- See [../../api/](../../api/) for API documentation
