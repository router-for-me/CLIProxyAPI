# Merged Fragmented Markdown

## Source: features/architecture/DEV.md

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


---

## Source: features/architecture/SPEC.md

# Technical Specification: Library-First Architecture (pkg/llmproxy)

## Overview

**cliproxyapi++** implements a "Library-First" architectural pattern by extracting all core proxy logic from the traditional `internal/` package into a public, reusable `pkg/llmproxy` module. This transformation enables external Go applications to import and embed the entire translation, authentication, and communication engine without depending on the CLI binary.

## Architecture Migration

### Before: Mainline Structure
```
CLIProxyAPI/
├── internal/
│   ├── translator/      # Core translation logic (NOT IMPORTABLE)
│   ├── provider/        # Provider executors (NOT IMPORTABLE)
│   └── auth/            # Auth management (NOT IMPORTABLE)
└── cmd/server/
```

### After: cliproxyapi++ Structure
```
cliproxyapi++/
├── pkg/llmproxy/         # PUBLIC LIBRARY (IMPORTABLE)
│   ├── translator/       # Translation engine
│   ├── provider/         # Provider implementations
│   ├── config/           # Configuration synthesis
│   ├── watcher/          # Dynamic reload orchestration
│   └── auth/             # Auth lifecycle management
├── cmd/server/          # CLI entry point (uses pkg/llmproxy)
└── sdk/cliproxy/        # High-level embedding SDK
```

## Core Components

### 1. Translation Engine (`pkg/llmproxy/translator`)

**Purpose**: Handles bidirectional protocol conversion between OpenAI-compatible requests and proprietary LLM APIs.

**Key Interfaces**:
```go
type Translator interface {
    // Convert OpenAI format to provider format
    TranslateRequest(ctx context.Context, req *openai.ChatRequest) (*ProviderRequest, error)

    // Convert provider response back to OpenAI format
    TranslateResponse(ctx context.Context, resp *ProviderResponse) (*openai.ChatResponse, error)

    // Stream translation for SSE
    TranslateStream(ctx context.Context, stream io.Reader) (<-chan *openai.ChatChunk, error)

    // Provider-specific capabilities
    SupportsStreaming() bool
    SupportsFunctions() bool
    MaxTokens() int
}
```

**Implemented Translators**:
- `claude.go` - Anthropic Claude API
- `gemini.go` - Google Gemini API
- `openai.go` - OpenAI GPT API
- `kiro.go` - AWS CodeWhisperer (custom protocol)
- `copilot.go` - GitHub Copilot (custom protocol)
- `aggregators.go` - OpenRouter, Together, Fireworks

**Translation Strategy**:
1. **Request Normalization**: Parse OpenAI-format request, extract:
   - Messages (system, user, assistant)
   - Tools/functions
   - Generation parameters (temp, top_p, max_tokens)
   - Streaming flag

2. **Provider Mapping**: Map OpenAI models to provider endpoints:
   ```
   claude-3-5-sonnet -> claude-3-5-sonnet-20241022 (Anthropic)
   gpt-4 -> gpt-4-turbo-preview (OpenAI)
   gemini-1.5-pro -> gemini-1.5-pro-preview-0514 (Gemini)
   ```

3. **Response Normalization**: Convert provider responses to OpenAI format:
   - Standardize usage statistics (prompt_tokens, completion_tokens)
   - Normalize finish reasons (stop, length, content_filter)
   - Map provider-specific error codes to OpenAI error types

### 2. Provider Execution (`pkg/llmproxy/provider`)

**Purpose**: Orchestrates HTTP communication with LLM providers, handling authentication, retry logic, and error recovery.

**Key Interfaces**:
```go
type ProviderExecutor interface {
    // Execute a single request (non-streaming)
    Execute(ctx context.Context, auth coreauth.Auth, req *ProviderRequest) (*ProviderResponse, error)

    // Execute streaming request
    ExecuteStream(ctx context.Context, auth coreauth.Auth, req *ProviderRequest) (<-chan *ProviderChunk, error)

    // Health check provider
    HealthCheck(ctx context.Context, auth coreauth.Auth) error

    // Provider metadata
    Name() string
    SupportsModel(model string) bool
}
```

**Executor Lifecycle**:
```
Request -> RateLimitCheck -> AuthValidate -> ProviderExecute ->
    -> Success -> Response
    -> RetryableError -> Backoff -> Retry
    -> NonRetryableError -> Error
```

**Rate Limiting**:
- Per-provider token bucket
- Per-credential quota tracking
- Intelligent cooldown on 429 responses

### 3. Configuration Management (`pkg/llmproxy/config`)

**Purpose**: Loads, validates, and synthesizes configuration from multiple sources.

**Configuration Hierarchy**:
```
1. Base config (config.yaml)
2. Environment overrides (CLI_PROXY_*)
3. Runtime synthesis (watcher merges changes)
4. Per-request overrides (query params)
```

**Key Structures**:
```go
type Config struct {
    Server      ServerConfig
    Providers   map[string]ProviderConfig
    Auth        AuthConfig
    Management  ManagementConfig
    Logging     LoggingConfig
}

type ProviderConfig struct {
    Type        string  // "claude", "gemini", "openai", etc.
    Enabled     bool
    Models      []ModelConfig
    AuthType    string  // "api_key", "oauth", "device_flow"
    Priority    int     // Routing priority
    Cooldown    time.Duration
}
```

**Hot-Reload Mechanism**:
- File watcher on `config.yaml` and `auths/` directory
- Debounced reload (500ms delay)
- Atomic config swapping (no request interruption)
- Validation before activation (reject invalid configs)

### 4. Watcher & Synthesis (`pkg/llmproxy/watcher`)

**Purpose**: Orchestrates dynamic configuration updates and background lifecycle management.

**Watcher Architecture**:
```go
type Watcher struct {
    configPath     string
    authDir        string
    reloadChan     chan struct{}
    currentConfig  atomic.Value // *Config
    currentAuths   atomic.Value // []coreauth.Auth
}

// Run starts the watcher goroutine
func (w *Watcher) Run(ctx context.Context) error {
    // 1. Initial load
    w.loadAll()

    // 2. Watch files
    go w.watchConfig(ctx)
    go w.watchAuths(ctx)

    // 3. Handle reloads
    for {
        select {
        case <-w.reloadChan:
            w.loadAll()
        case <-ctx.Done():
            return ctx.Err()
        }
    }
}
```

**Synthesis Pipeline**:
```
Config File Changed -> Parse YAML -> Validate Schema ->
    Merge with Existing -> Check Conflicts -> Atomic Swap
```

**Background Workers**:
1. **Token Refresh Worker**: Checks every 5 minutes, refreshes tokens expiring within 10 minutes
2. **Health Check Worker**: Pings providers every 30 seconds, marks unhealthy providers
3. **Metrics Collector**: Aggregates request latency, error rates, token usage

## Data Flow

### Request Processing Flow
```
HTTP Request (OpenAI format)
    ↓
Middleware (CORS, auth, logging)
    ↓
Handler (Parse request, select provider)
    ↓
Provider Executor (Rate limit check)
    ↓
Translator (Convert to provider format)
    ↓
HTTP Client (Execute provider API)
    ↓
Translator (Convert response)
    ↓
Handler (Send response)
    ↓
Middleware (Log metrics)
    ↓
HTTP Response (OpenAI format)
```

### Configuration Reload Flow
```
File System Event (config.yaml changed)
    ↓
Watcher (Detect change)
    ↓
Debounce (500ms)
    ↓
Config Loader (Parse and validate)
    ↓
Synthesizer (Merge with existing)
    ↓
Atomic Swap (Update runtime config)
    ↓
Notification (Trigger background workers)
```

### Token Refresh Flow
```
Background Worker (Every 5 min)
    ↓
Scan All Auths
    ↓
Check Expiry (token.ExpiresAt < now + 10min)
    ↓
Execute Refresh Flow
    ↓
Update Storage (auths/{provider}.json)
    ↓
Notify Watcher
    ↓
Atomic Swap (Update runtime auths)
```

## Reusability Patterns

### Embedding as Library
```go
import "github.com/KooshaPari/cliproxyapi-plusplus/pkg/llmproxy"

// Create translator
translator := llmproxy.NewClaudeTranslator()

// Translate request
providerReq, err := translator.TranslateRequest(ctx, openaiReq)

// Create executor
executor := llmproxy.NewClaudeExecutor()

// Execute
resp, err := executor.Execute(ctx, auth, providerReq)

// Translate response
openaiResp, err := translator.TranslateResponse(ctx, resp)
```

### Custom Provider Integration
```go
// Implement Translator interface
type MyCustomTranslator struct{}

func (t *MyCustomTranslator) TranslateRequest(ctx context.Context, req *openai.ChatRequest) (*llmproxy.ProviderRequest, error) {
    // Custom translation logic
    return &llmproxy.ProviderRequest{}, nil
}

// Register with executor
executor := llmproxy.NewExecutor(
    llmproxy.WithTranslator(&MyCustomTranslator{}),
)
```

### Extending Configuration
```go
// Custom config synthesizer
type MySynthesizer struct{}

func (s *MySynthesizer) Synthesize(base *llmproxy.Config, overrides map[string]interface{}) (*llmproxy.Config, error) {
    // Custom merge logic
    return base, nil
}

// Use in watcher
watcher := llmproxy.NewWatcher(
    llmproxy.WithSynthesizer(&MySynthesizer{}),
)
```

## Performance Characteristics

### Memory Footprint
- Base package: ~15MB (includes all translators)
- Per-request allocation: <1MB
- Config reload overhead: <10ms

### Concurrency Model
- Request handling: Goroutine-per-request (bounded by worker pool)
- Config reloading: Single goroutine (serialized)
- Token refresh: Single goroutine (serialized per provider)
- Health checks: Per-provider goroutines

### Throughput
- Single instance: ~1000 requests/second (varies by provider)
- Hot reload impact: <5ms latency blip during swap
- Background workers: <1% CPU utilization

## Security Considerations

### Public API Stability
- All exported APIs follow semantic versioning
- Breaking changes require major version bump (v7, v8, etc.)
- Deprecated APIs remain for 2 major versions

### Input Validation
- All translator inputs validated before provider execution
- Config validation on load (reject malformed configs)
- Auth credential validation before storage

### Error Propagation
- Internal errors sanitized before API response
- Provider errors mapped to OpenAI error types
- Detailed logging for debugging (configurable verbosity)

## Migration Guide

### From Mainline internal/
```go
// Before (mainline)
import "github.com/router-for-me/CLIProxyAPI/v6/internal/translator"

// After (cliproxyapi++)
import "github.com/KooshaPari/cliproxyapi-plusplus/pkg/llmproxy/translator"
```

### Function Compatibility
Most internal functions have public equivalents:
- `internal/translator.NewClaude()` → `llmproxy/translator.NewClaude()`
- `internal/provider.NewExecutor()` → `llmproxy/provider.NewExecutor()`
- `internal/config.Load()` → `llmproxy/config.LoadConfig()`

## Testing Strategy

### Unit Tests
- Each translator: Mock provider responses
- Each executor: Mock HTTP transport
- Config validation: Test schema violations

### Integration Tests
- End-to-end proxy: Real provider APIs (test keys)
- Hot reload: File system changes
- Token refresh: Expiring credentials

### Contract Tests
- OpenAI API compatibility: Verify response format
- Provider contract: Verify translator mapping


---

## Source: features/architecture/USER.md

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


---
