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
