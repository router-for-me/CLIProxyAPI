# Developer Guide: Extending Library-First Architecture

## Contributing to pkg/llmproxy

This guide is for developers who want to extend the core library functionality: adding new providers, customizing translators, implementing new authentication flows, or optimizing performance.

## Project Structure

```
pkg/llmproxy/
├── translator/       # Protocol translation layer
│   ├── base.go       # Common interfaces and utilities
│   ├── claude.go     # Anthropic Claude
│   ├── gemini.go     # Google Gemini
│   ├── openai.go     # OpenAI GPT
│   ├── kiro.go       # AWS CodeWhisperer
│   ├── copilot.go    # GitHub Copilot
│   └── aggregators.go # Multi-provider aggregators
├── provider/         # Provider execution layer
│   ├── base.go       # Provider interface and executor
│   ├── http.go       # HTTP client with retry logic
│   ├── rate_limit.go # Token bucket implementation
│   └── health.go     # Health check logic
├── auth/             # Authentication lifecycle
│   ├── manager.go    # Core auth manager
│   ├── oauth.go      # OAuth flows
│   ├── device_flow.go # Device authorization flow
│   └── refresh.go    # Token refresh worker
├── config/           # Configuration management
│   ├── loader.go     # Config file parsing
│   ├── schema.go     # Validation schema
│   └── synthesis.go  # Config merge logic
├── watcher/          # Dynamic reload orchestration
│   ├── file.go       # File system watcher
│   ├── debounce.go   # Debouncing logic
│   └── notify.go     # Change notifications
└── metrics/          # Observability
    ├── collector.go  # Metrics collection
    └── exporter.go   # Metrics export
```

## Adding a New Provider

### Step 1: Define Provider Configuration

Add provider config to `config/schema.go`:

```go
type ProviderConfig struct {
    Type        string   `yaml:"type" validate:"required,oneof=claude gemini openai kiro copilot myprovider"`
    Enabled     bool     `yaml:"enabled"`
    Models      []ModelConfig `yaml:"models"`
    AuthType    string   `yaml:"auth_type" validate:"required,oneof=api_key oauth device_flow"`
    Priority    int      `yaml:"priority"`
    Cooldown    time.Duration `yaml:"cooldown"`
    Endpoint    string   `yaml:"endpoint"`
    // Provider-specific fields
    CustomField string   `yaml:"custom_field"`
}
```

### Step 2: Implement Translator Interface

Create `pkg/llmproxy/translator/myprovider.go`:

```go
package translator

import (
    "context"
    "encoding/json"

    openai "github.com/sashabaranov/go-openai"
    "github.com/KooshaPari/cliproxyapi-plusplus/pkg/llmproxy"
)

type MyProviderTranslator struct {
    config *config.ProviderConfig
}

func NewMyProviderTranslator(cfg *config.ProviderConfig) *MyProviderTranslator {
    return &MyProviderTranslator{config: cfg}
}

func (t *MyProviderTranslator) TranslateRequest(
    ctx context.Context,
    req *openai.ChatCompletionRequest,
) (*llmproxy.ProviderRequest, error) {
    // Map OpenAI models to provider models
    modelMapping := map[string]string{
        "gpt-4": "myprovider-v1-large",
        "gpt-3.5-turbo": "myprovider-v1-medium",
    }
    providerModel := modelMapping[req.Model]
    if providerModel == "" {
        providerModel = req.Model
    }

    // Convert messages
    messages := make([]map[string]interface{}, len(req.Messages))
    for i, msg := range req.Messages {
        messages[i] = map[string]interface{}{
            "role":    msg.Role,
            "content": msg.Content,
        }
    }

    // Build request
    providerReq := &llmproxy.ProviderRequest{
        Method: "POST",
        Endpoint: t.config.Endpoint + "/v1/chat/completions",
        Headers: map[string]string{
            "Content-Type": "application/json",
            "Accept": "application/json",
        },
        Body: map[string]interface{}{
            "model":    providerModel,
            "messages": messages,
            "stream":   req.Stream,
        },
    }

    // Add optional parameters
    if req.Temperature != 0 {
        providerReq.Body["temperature"] = req.Temperature
    }
    if req.MaxTokens != 0 {
        providerReq.Body["max_tokens"] = req.MaxTokens
    }

    return providerReq, nil
}

func (t *MyProviderTranslator) TranslateResponse(
    ctx context.Context,
    resp *llmproxy.ProviderResponse,
) (*openai.ChatCompletionResponse, error) {
    // Parse provider response
    var providerBody struct {
        ID      string `json:"id"`
        Model   string `json:"model"`
        Choices []struct {
            Message struct {
                Role    string `json:"role"`
                Content string `json:"content"`
            } `json:"message"`
            FinishReason string `json:"finish_reason"`
        } `json:"choices"`
        Usage struct {
            PromptTokens     int `json:"prompt_tokens"`
            CompletionTokens int `json:"completion_tokens"`
            TotalTokens      int `json:"total_tokens"`
        } `json:"usage"`
    }

    if err := json.Unmarshal(resp.Body, &providerBody); err != nil {
        return nil, fmt.Errorf("failed to parse provider response: %w", err)
    }

    // Convert to OpenAI format
    choices := make([]openai.ChatCompletionChoice, len(providerBody.Choices))
    for i, choice := range providerBody.Choices {
        choices[i] = openai.ChatCompletionChoice{
            Message: openai.ChatCompletionMessage{
                Role:    openai.ChatMessageRole(choice.Message.Role),
                Content: choice.Message.Content,
            },
            FinishReason: openai.FinishReason(choice.FinishReason),
        }
    }

    return &openai.ChatCompletionResponse{
        ID:      providerBody.ID,
        Model:   resp.RequestModel,
        Choices: choices,
        Usage: openai.Usage{
            PromptTokens:     providerBody.Usage.PromptTokens,
            CompletionTokens: providerBody.Usage.CompletionTokens,
            TotalTokens:      providerBody.Usage.TotalTokens,
        },
    }, nil
}

func (t *MyProviderTranslator) TranslateStream(
    ctx context.Context,
    stream io.Reader,
) (<-chan *openai.ChatCompletionStreamResponse, error) {
    // Implement streaming translation
    ch := make(chan *openai.ChatCompletionStreamResponse)

    go func() {
        defer close(ch)

        scanner := bufio.NewScanner(stream)
        for scanner.Scan() {
            line := scanner.Text()
            if !strings.HasPrefix(line, "data: ") {
                continue
            }

            data := strings.TrimPrefix(line, "data: ")
            if data == "[DONE]" {
                return
            }

            var chunk struct {
                ID      string `json:"id"`
                Choices []struct {
                    Delta struct {
                        Content string `json:"content"`
                    } `json:"delta"`
                    FinishReason *string `json:"finish_reason"`
                } `json:"choices"`
            }

            if err := json.Unmarshal([]byte(data), &chunk); err != nil {
                continue
            }

            ch <- &openai.ChatCompletionStreamResponse{
                ID: chunk.ID,
                Choices: []openai.ChatCompletionStreamChoice{
                    {
                        Delta: openai.ChatCompletionStreamDelta{
                            Content: chunk.Choices[0].Delta.Content,
                        },
                        FinishReason: chunk.Choices[0].FinishReason,
                    },
                },
            }
        }
    }()

    return ch, nil
}

func (t *MyProviderTranslator) SupportsStreaming() bool {
    return true
}

func (t *MyProviderTranslator) SupportsFunctions() bool {
    return false
}

func (t *MyProviderTranslator) MaxTokens() int {
    return 4096
}
```

### Step 3: Implement Provider Executor

Create `pkg/llmproxy/provider/myprovider.go`:

```go
package provider

import (
    "context"
    "fmt"
    "net/http"

    "github.com/KooshaPari/cliproxyapi-plusplus/pkg/llmproxy"
    "github.com/KooshaPari/cliproxyapi-plusplus/pkg/llmproxy/config"
    "github.com/KooshaPari/cliproxyapi-plusplus/pkg/llmproxy/coreauth"
    "github.com/KooshaPari/cliproxyapi-plusplus/pkg/llmproxy/translator"
)

type MyProviderExecutor struct {
    config    *config.ProviderConfig
    client    *http.Client
    rateLimit *RateLimiter
    translator *translator.MyProviderTranslator
}

func NewMyProviderExecutor(
    cfg *config.ProviderConfig,
    rtProvider coreauth.RoundTripperProvider,
) *MyProviderExecutor {
    return &MyProviderExecutor{
        config:     cfg,
        client:     NewHTTPClient(rtProvider),
        rateLimit:  NewRateLimiter(cfg.RateLimit),
        translator: translator.NewMyProviderTranslator(cfg),
    }
}

func (e *MyProviderExecutor) Execute(
    ctx context.Context,
    auth coreauth.Auth,
    req *llmproxy.ProviderRequest,
) (*llmproxy.ProviderResponse, error) {
    // Rate limit check
    if err := e.rateLimit.Wait(ctx); err != nil {
        return nil, fmt.Errorf("rate limit exceeded: %w", err)
    }

    // Add auth headers
    if auth != nil {
        req.Headers["Authorization"] = fmt.Sprintf("Bearer %s", auth.Token)
    }

    // Execute request
    resp, err := e.client.Do(ctx, req)
    if err != nil {
        return nil, fmt.Errorf("request failed: %w", err)
    }

    // Check for errors
    if resp.StatusCode >= 400 {
        return nil, fmt.Errorf("provider error: %s", string(resp.Body))
    }

    return resp, nil
}

func (e *MyProviderExecutor) ExecuteStream(
    ctx context.Context,
    auth coreauth.Auth,
    req *llmproxy.ProviderRequest,
) (<-chan *llmproxy.ProviderChunk, error) {
    // Rate limit check
    if err := e.rateLimit.Wait(ctx); err != nil {
        return nil, fmt.Errorf("rate limit exceeded: %w", err)
    }

    // Add auth headers
    if auth != nil {
        req.Headers["Authorization"] = fmt.Sprintf("Bearer %s", auth.Token)
    }

    // Execute streaming request
    stream, err := e.client.DoStream(ctx, req)
    if err != nil {
        return nil, fmt.Errorf("request failed: %w", err)
    }

    return stream, nil
}

func (e *MyProviderExecutor) HealthCheck(
    ctx context.Context,
    auth coreauth.Auth,
) error {
    req := &llmproxy.ProviderRequest{
        Method:   "GET",
        Endpoint: e.config.Endpoint + "/v1/health",
    }

    resp, err := e.client.Do(ctx, req)
    if err != nil {
        return err
    }

    if resp.StatusCode != 200 {
        return fmt.Errorf("health check failed: %s", string(resp.Body))
    }

    return nil
}

func (e *MyProviderExecutor) Name() string {
    return "myprovider"
}

func (e *MyProviderExecutor) SupportsModel(model string) bool {
    for _, m := range e.config.Models {
        if m.Name == model {
            return m.Enabled
        }
    }
    return false
}
```

### Step 4: Register Provider

Update `pkg/llmproxy/provider/registry.go`:

```go
package provider

import (
    "github.com/KooshaPari/cliproxyapi-plusplus/pkg/llmproxy/config"
    "github.com/KooshaPari/cliproxyapi-plusplus/pkg/llmproxy/coreauth"
)

type ProviderFactory func(
    cfg *config.ProviderConfig,
    rtProvider coreauth.RoundTripperProvider,
) ProviderExecutor

var providers = map[string]ProviderFactory{
    "claude":      NewClaudeExecutor,
    "gemini":      NewGeminiExecutor,
    "openai":      NewOpenAIExecutor,
    "kiro":        NewKiroExecutor,
    "copilot":     NewCopilotExecutor,
    "myprovider":  NewMyProviderExecutor, // Add your provider
}

func GetExecutor(
    providerType string,
    cfg *config.ProviderConfig,
    rtProvider coreauth.RoundTripperProvider,
) (ProviderExecutor, error) {
    factory, ok := providers[providerType]
    if !ok {
        return nil, fmt.Errorf("unknown provider type: %s", providerType)
    }

    return factory(cfg, rtProvider), nil
}
```

### Step 5: Add Tests

Create `pkg/llmproxy/translator/myprovider_test.go`:

```go
package translator

import (
    "context"
    "testing"

    openai "github.com/sashabaranov/go-openai"
    "github.com/KooshaPari/cliproxyapi-plusplus/pkg/llmproxy/config"
)

func TestMyProviderTranslator(t *testing.T) {
    cfg := &config.ProviderConfig{
        Type:     "myprovider",
        Endpoint: "https://api.myprovider.com",
    }

    translator := NewMyProviderTranslator(cfg)

    t.Run("TranslateRequest", func(t *testing.T) {
        req := &openai.ChatCompletionRequest{
            Model: "gpt-4",
            Messages: []openai.ChatCompletionMessage{
                {Role: "user", Content: "Hello"},
            },
        }

        providerReq, err := translator.TranslateRequest(context.Background(), req)
        if err != nil {
            t.Fatalf("TranslateRequest failed: %v", err)
        }

        if providerReq.Endpoint != "https://api.myprovider.com/v1/chat/completions" {
            t.Errorf("unexpected endpoint: %s", providerReq.Endpoint)
        }
    })

    t.Run("TranslateResponse", func(t *testing.T) {
        providerResp := &llmproxy.ProviderResponse{
            Body: []byte(`{
                "id": "test-id",
                "model": "myprovider-v1-large",
                "choices": [{
                    "message": {"role": "assistant", "content": "Hi!"},
                    "finish_reason": "stop"
                }],
                "usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
            }`),
        }

        openaiResp, err := translator.TranslateResponse(context.Background(), providerResp)
        if err != nil {
            t.Fatalf("TranslateResponse failed: %v", err)
        }

        if openaiResp.ID != "test-id" {
            t.Errorf("unexpected id: %s", openaiResp.ID)
        }
    })
}
```

## Custom Authentication Flows

### Implementing OAuth

If your provider uses OAuth, implement the `AuthFlow` interface:

```go
package auth

import (
    "context"
    "time"

    "github.com/KooshaPari/cliproxyapi-plusplus/pkg/llmproxy/config"
)

type MyProviderOAuthFlow struct {
    clientID     string
    clientSecret string
    redirectURL  string
    tokenURL     string
    authURL      string
}

func (f *MyProviderOAuthFlow) Start(ctx context.Context) (*AuthResult, error) {
    // Generate authorization URL
    state := generateState()
    authURL := fmt.Sprintf("%s?client_id=%s&redirect_uri=%s&state=%s",
        f.authURL, f.clientID, f.redirectURL, state)

    return &AuthResult{
        Method:    "oauth",
        AuthURL:   authURL,
        State:     state,
        ExpiresAt: time.Now().Add(10 * time.Minute),
    }, nil
}

func (f *MyProviderOAuthFlow) Exchange(ctx context.Context, code string) (*AuthToken, error) {
    // Exchange authorization code for token
    req := map[string]string{
        "client_id":     f.clientID,
        "client_secret": f.clientSecret,
        "code":          code,
        "redirect_uri":  f.redirectURL,
        "grant_type":    "authorization_code",
    }

    resp, err := http.PostForm(f.tokenURL, req)
    if err != nil {
        return nil, err
    }

    var token struct {
        AccessToken  string `json:"access_token"`
        RefreshToken string `json:"refresh_token"`
        ExpiresIn    int    `json:"expires_in"`
    }

    if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
        return nil, err
    }

    return &AuthToken{
        AccessToken:  token.AccessToken,
        RefreshToken: token.RefreshToken,
        ExpiresAt:    time.Now().Add(time.Duration(token.ExpiresIn) * time.Second),
    }, nil
}

func (f *MyProviderOAuthFlow) Refresh(ctx context.Context, refreshToken string) (*AuthToken, error) {
    // Refresh token
    req := map[string]string{
        "client_id":     f.clientID,
        "client_secret": f.clientSecret,
        "refresh_token": refreshToken,
        "grant_type":    "refresh_token",
    }

    resp, err := http.PostForm(f.tokenURL, req)
    if err != nil {
        return nil, err
    }

    var token struct {
        AccessToken  string `json:"access_token"`
        RefreshToken string `json:"refresh_token"`
        ExpiresIn    int    `json:"expires_in"`
    }

    if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
        return nil, err
    }

    return &AuthToken{
        AccessToken:  token.AccessToken,
        RefreshToken: token.RefreshToken,
        ExpiresAt:    time.Now().Add(time.Duration(token.ExpiresIn) * time.Second),
    }, nil
}
```

### Implementing Device Flow

```go
package auth

import (
    "context"
    "fmt"
    "time"

    "github.com/KooshaPari/cliproxyapi-plusplus/pkg/llmproxy/config"
)

type MyProviderDeviceFlow struct {
    deviceCodeURL string
    tokenURL      string
    clientID      string
}

func (f *MyProviderDeviceFlow) Start(ctx context.Context) (*AuthResult, error) {
    // Request device code
    resp, err := http.PostForm(f.deviceCodeURL, map[string]string{
        "client_id": f.clientID,
    })
    if err != nil {
        return nil, err
    }

    var dc struct {
        DeviceCode              string `json:"device_code"`
        UserCode               string `json:"user_code"`
        VerificationURI        string `json:"verification_uri"`
        VerificationURIComplete string `json:"verification_uri_complete"`
        ExpiresIn              int    `json:"expires_in"`
        Interval               int    `json:"interval"`
    }

    if err := json.NewDecoder(resp.Body).Decode(&dc); err != nil {
        return nil, err
    }

    return &AuthResult{
        Method:           "device_flow",
        UserCode:         dc.UserCode,
        VerificationURL:  dc.VerificationURI,
        VerificationURLComplete: dc.VerificationURIComplete,
        DeviceCode:       dc.DeviceCode,
        Interval:         dc.Interval,
        ExpiresAt:        time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second),
    }, nil
}

func (f *MyProviderDeviceFlow) Poll(ctx context.Context, deviceCode string) (*AuthToken, error) {
    // Poll for token
    ticker := time.NewTicker(5 * time.Second)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return nil, ctx.Err()
        case <-ticker.C:
            resp, err := http.PostForm(f.tokenURL, map[string]string{
                "client_id":   f.clientID,
                "grant_type":  "urn:ietf:params:oauth:grant-type:device_code",
                "device_code": deviceCode,
            })
            if err != nil {
                return nil, err
            }

            var token struct {
                AccessToken string `json:"access_token"`
                ExpiresIn   int    `json:"expires_in"`
                Error       string `json:"error"`
            }

            if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
                return nil, err
            }

            if token.Error == "" {
                return &AuthToken{
                    AccessToken: token.AccessToken,
                    ExpiresAt:   time.Now().Add(time.Duration(token.ExpiresIn) * time.Second),
                }, nil
            }

            if token.Error != "authorization_pending" {
                return nil, fmt.Errorf("device flow error: %s", token.Error)
            }
        }
    }
}
```

## Performance Optimization

### Connection Pooling

```go
package provider

import (
    "net/http"
    "time"
)

func NewHTTPClient(rtProvider coreauth.RoundTripperProvider) *http.Client {
    transport := &http.Transport{
        MaxIdleConns:        100,
        MaxIdleConnsPerHost: 10,
        IdleConnTimeout:     90 * time.Second,
        TLSHandshakeTimeout: 10 * time.Second,
    }

    return &http.Client{
        Transport: transport,
        Timeout:   60 * time.Second,
    }
}
```

### Rate Limiting Optimization

```go
package provider

import (
    "golang.org/x/time/rate"
)

type RateLimiter struct {
    limiter *rate.Limiter
}

func NewRateLimiter(reqPerSec float64) *RateLimiter {
    return &RateLimiter{
        limiter: rate.NewLimiter(rate.Limit(reqPerSec), 10), // Burst of 10
    }
}

func (r *RateLimiter) Wait(ctx context.Context) error {
    return r.limiter.Wait(ctx)
}
```

### Caching Strategy

```go
package provider

import (
    "sync"
    "time"
)

type Cache struct {
    mu    sync.RWMutex
    data  map[string]cacheEntry
    ttl   time.Duration
}

type cacheEntry struct {
    value      interface{}
    expiresAt  time.Time
}

func NewCache(ttl time.Duration) *Cache {
    c := &Cache{
        data: make(map[string]cacheEntry),
        ttl:  ttl,
    }

    // Start cleanup goroutine
    go c.cleanup()

    return c
}

func (c *Cache) Get(key string) (interface{}, bool) {
    c.mu.RLock()
    defer c.mu.RUnlock()

    entry, ok := c.data[key]
    if !ok || time.Now().After(entry.expiresAt) {
        return nil, false
    }

    return entry.value, true
}

func (c *Cache) Set(key string, value interface{}) {
    c.mu.Lock()
    defer c.mu.Unlock()

    c.data[key] = cacheEntry{
        value:     value,
        expiresAt: time.Now().Add(c.ttl),
    }
}

func (c *Cache) cleanup() {
    ticker := time.NewTicker(time.Minute)
    defer ticker.Stop()

    for range ticker.C {
        c.mu.Lock()
        for key, entry := range c.data {
            if time.Now().After(entry.expiresAt) {
                delete(c.data, key)
            }
        }
        c.mu.Unlock()
    }
}
```

## Testing Guidelines

### Unit Tests

- Test all translator methods
- Mock HTTP responses
- Cover error paths

### Integration Tests

- Test against real provider APIs (use test keys)
- Test authentication flows
- Test streaming responses

### Contract Tests

- Verify OpenAI API compatibility
- Test model mapping
- Validate error handling

## Submitting Changes

1. **Add tests** for new functionality
2. **Run linter**: `make lint`
3. **Run tests**: `make test`
4. **Update documentation** if API changes
5. **Submit PR** with description of changes

## API Stability

All exported APIs in `pkg/llmproxy` follow semantic versioning:
- **Major version bump** (v7, v8): Breaking changes
- **Minor version bump**: New features (backwards compatible)
- **Patch version**: Bug fixes

Deprecated APIs remain for 2 major versions before removal.
