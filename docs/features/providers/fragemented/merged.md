# Merged Fragmented Markdown

## Source: cliproxyapi-plusplus/docs/features/providers

## Source: SPEC.md

# Technical Specification: Provider Registry & Support

## Overview

**cliproxyapi++** supports an extensive registry of LLM providers, from direct API integrations to multi-provider aggregators and proprietary protocols. This specification details the provider architecture, supported providers, and extension mechanisms.

## Provider Architecture

### Provider Types

```
Provider Registry
├── Direct Providers
│   ├── Claude (Anthropic)
│   ├── Gemini (Google)
│   ├── OpenAI
│   ├── Mistral
│   ├── Groq
│   └── DeepSeek
├── Aggregator Providers
│   ├── OpenRouter
│   ├── Together AI
│   ├── Fireworks AI
│   ├── Novita AI
│   └── SiliconFlow
└── Proprietary Providers
    ├── Kiro (AWS CodeWhisperer)
    ├── GitHub Copilot
    ├── Roo Code
    ├── Kilo AI
    └── MiniMax
```

### Provider Interface

```go
type Provider interface {
    // Provider metadata
    Name() string
    Type() ProviderType

    // Model support
    SupportsModel(model string) bool
    ListModels() []Model

    // Authentication
    AuthType() AuthType
    RequiresAuth() bool

    // Execution
    Execute(ctx context.Context, req *Request) (*Response, error)
    ExecuteStream(ctx context.Context, req *Request) (<-chan *Chunk, error)

    // Capabilities
    SupportsStreaming() bool
    SupportsFunctions() bool
    MaxTokens() int

    // Health
    HealthCheck(ctx context.Context) error
}
```

### Provider Configuration

```go
type ProviderConfig struct {
    Name        string            `yaml:"name"`
    Type        string            `yaml:"type"`
    Enabled     bool              `yaml:"enabled"`
    AuthType    string            `yaml:"auth_type"`
    Endpoint    string            `yaml:"endpoint"`
    Models      []ModelConfig     `yaml:"models"`
    Features    ProviderFeatures  `yaml:"features"`
    Limits      ProviderLimits    `yaml:"limits"`
    Cooldown    CooldownConfig    `yaml:"cooldown"`
    Priority    int               `yaml:"priority"`
}

type ModelConfig struct {
    Name              string `yaml:"name"`
    Enabled           bool   `yaml:"enabled"`
    MaxTokens         int    `yaml:"max_tokens"`
    SupportsFunctions bool   `yaml:"supports_functions"`
    SupportsStreaming bool   `yaml:"supports_streaming"`
}

type ProviderFeatures struct {
    Streaming        bool `yaml:"streaming"`
    Functions        bool `yaml:"functions"`
    Vision           bool `yaml:"vision"`
    CodeGeneration   bool `yaml:"code_generation"`
    Multimodal       bool `yaml:"multimodal"`
}

type ProviderLimits struct {
    RequestsPerMinute int `yaml:"requests_per_minute"`
    TokensPerMinute   int `yaml:"tokens_per_minute"`
    MaxTokensPerReq   int `yaml:"max_tokens_per_request"`
}
```

## Direct Providers

### Claude (Anthropic)

**Provider Type**: `claude`

**Authentication**: API Key

**Models**:
- `claude-3-5-sonnet` (max: 200K tokens)
- `claude-3-5-haiku` (max: 200K tokens)
- `claude-3-opus` (max: 200K tokens)

**Features**:
- Streaming: ✅
- Functions: ✅
- Vision: ✅
- Code generation: ✅

**Configuration**:
```yaml
providers:
  claude:
    type: "claude"
    enabled: true
    auth_type: "api_key"
    endpoint: "https://api.anthropic.com"
    models:
      - name: "claude-3-5-sonnet"
        enabled: true
        max_tokens: 200000
        supports_functions: true
        supports_streaming: true
    features:
      streaming: true
      functions: true
      vision: true
      code_generation: true
    limits:
      requests_per_minute: 60
      tokens_per_minute: 40000
```

**API Endpoint**: `https://api.anthropic.com/v1/messages`

**Request Format**:
```json
{
  "model": "claude-3-5-sonnet-20241022",
  "max_tokens": 1024,
  "messages": [
    {"role": "user", "content": "Hello!"}
  ],
  "stream": true
}
```

**Headers**:
```
x-api-key: sk-ant-xxxx
anthropic-version: 2023-06-01
content-type: application/json
```

### Gemini (Google)

**Provider Type**: `gemini`

**Authentication**: API Key

**Models**:
- `gemini-1.5-pro` (max: 1M tokens)
- `gemini-1.5-flash` (max: 1M tokens)
- `gemini-1.0-pro` (max: 32K tokens)

**Features**:
- Streaming: ✅
- Functions: ✅
- Vision: ✅
- Multimodal: ✅

**Configuration**:
```yaml
providers:
  gemini:
    type: "gemini"
    enabled: true
    auth_type: "api_key"
    endpoint: "https://generativelanguage.googleapis.com"
    models:
      - name: "gemini-1.5-pro"
        enabled: true
        max_tokens: 1000000
    features:
      streaming: true
      functions: true
      vision: true
      multimodal: true
```

### OpenAI

**Provider Type**: `openai`

**Authentication**: API Key

**Models**:
- `gpt-4-turbo` (max: 128K tokens)
- `gpt-4` (max: 8K tokens)
- `gpt-3.5-turbo` (max: 16K tokens)

**Features**:
- Streaming: ✅
- Functions: ✅
- Vision: ✅ (GPT-4 Vision)

**Configuration**:
```yaml
providers:
  openai:
    type: "openai"
    enabled: true
    auth_type: "api_key"
    endpoint: "https://api.openai.com"
    models:
      - name: "gpt-4-turbo"
        enabled: true
        max_tokens: 128000
```

## Aggregator Providers

### OpenRouter

**Provider Type**: `openrouter`

**Authentication**: API Key

**Purpose**: Access multiple models through a single API

**Features**:
- Access to 100+ models
- Unified pricing
- Model comparison

**Configuration**:
```yaml
providers:
  openrouter:
    type: "openrouter"
    enabled: true
    auth_type: "api_key"
    endpoint: "https://openrouter.ai/api"
    models:
      - name: "anthropic/claude-3.5-sonnet"
        enabled: true
```

### Together AI

**Provider Type**: `together`

**Authentication**: API Key

**Purpose**: Open-source models at scale

**Features**:
- Open-source models (Llama, Mistral, etc.)
- Fast inference
- Cost-effective

**Configuration**:
```yaml
providers:
  together:
    type: "together"
    enabled: true
    auth_type: "api_key"
    endpoint: "https://api.together.xyz"
```

### Fireworks AI

**Provider Type**: `fireworks`

**Authentication**: API Key

**Purpose**: Fast, open-source models

**Features**:
- Sub-second latency
- Open-source models
- API-first

**Configuration**:
```yaml
providers:
  fireworks:
    type: "fireworks"
    enabled: true
    auth_type: "api_key"
    endpoint: "https://api.fireworks.ai"
```

## Proprietary Providers

### Kiro (AWS CodeWhisperer)

**Provider Type**: `kiro`

**Authentication**: OAuth Device Flow (AWS Builder ID / Identity Center)

**Purpose**: Code generation and completion

**Features**:
- Browser-based auth UI
- AWS SSO integration
- Token refresh

**Authentication Flow**:
1. User visits `/v0/oauth/kiro`
2. Selects AWS Builder ID or Identity Center
3. Completes browser-based login
4. Token stored and auto-refreshed

**Configuration**:
```yaml
providers:
  kiro:
    type: "kiro"
    enabled: true
    auth_type: "oauth_device_flow"
    endpoint: "https://codeguru.amazonaws.com"
    models:
      - name: "codeguru-codegen"
        enabled: true
    features:
      code_generation: true
```

**Web UI Implementation**:
```go
func HandleKiroAuth(c *gin.Context) {
    // Request device code
    dc, err := kiro.GetDeviceCode()
    if err != nil {
        c.JSON(500, gin.H{"error": err.Error()})
        return
    }

    // Render HTML page
    c.HTML(200, "kiro_auth.html", gin.H{
        "UserCode":           dc.UserCode,
        "VerificationURL":    dc.VerificationURL,
        "VerificationURLComplete": dc.VerificationURLComplete,
    })

    // Start background polling
    go kiro.PollForToken(dc.DeviceCode)
}
```

### GitHub Copilot

**Provider Type**: `copilot`

**Authentication**: OAuth Device Flow

**Purpose**: Code completion and generation

**Features**:
- Full OAuth device flow
- Per-credential quota tracking
- Multi-credential support
- Auto token refresh

**Authentication Flow**:
1. Request device code from GitHub
2. Display user code and verification URL
3. User authorizes via browser
4. Poll for access token
5. Store token with refresh token
6. Auto-refresh before expiration

**Configuration**:
```yaml
providers:
  copilot:
    type: "copilot"
    enabled: true
    auth_type: "oauth_device_flow"
    endpoint: "https://api.githubcopilot.com"
    models:
      - name: "copilot-codegen"
        enabled: true
    features:
      code_generation: true
```

**Token Storage**:
```json
{
  "type": "oauth_device_flow",
  "access_token": "ghu_xxx",
  "refresh_token": "ghr_xxx",
  "expires_at": "2026-02-20T00:00:00Z",
  "quota": {
    "limit": 10000,
    "used": 100,
    "remaining": 9900
  }
}
```

### Roo Code

**Provider Type**: "roocode"

**Authentication**: API Key

**Purpose**: AI coding assistant

**Features**:
- Code generation
- Code explanation
- Refactoring

**Configuration**:
```yaml
providers:
  roocode:
    type: "roocode"
    enabled: true
    auth_type: "api_key"
    endpoint: "https://api.roocode.ai"
```

### Kilo AI

**Provider Type**: "kiloai"

**Authentication**: API Key

**Purpose**: Custom AI solutions

**Features**:
- Custom models
- Enterprise deployments

**Configuration**:
```yaml
providers:
  kiloai:
    type: "kiloai"
    enabled: true
    auth_type: "api_key"
    endpoint: "https://api.kiloai.io"
```

### MiniMax

**Provider Type**: "minimax"

**Authentication**: API Key

**Purpose**: Chinese LLM provider

**Features**:
- Bilingual support
- Fast inference
- Cost-effective

**Configuration**:
```yaml
providers:
  minimax:
    type: "minimax"
    enabled: true
    auth_type: "api_key"
    endpoint: "https://api.minimax.chat"
```

## Provider Registry

### Registry Interface

```go
type ProviderRegistry struct {
    mu         sync.RWMutex
    providers  map[string]Provider
    byType     map[ProviderType][]Provider
}

func NewRegistry() *ProviderRegistry {
    return &ProviderRegistry{
        providers: make(map[string]Provider),
        byType:    make(map[ProviderType][]Provider),
    }
}

func (r *ProviderRegistry) Register(provider Provider) error {
    r.mu.Lock()
    defer r.mu.Unlock()

    if _, exists := r.providers[provider.Name()]; exists {
        return fmt.Errorf("provider already registered: %s", provider.Name())
    }

    r.providers[provider.Name()] = provider
    r.byType[provider.Type()] = append(r.byType[provider.Type()], provider)

    return nil
}

func (r *ProviderRegistry) Get(name string) (Provider, error) {
    r.mu.RLock()
    defer r.mu.RUnlock()

    provider, ok := r.providers[name]
    if !ok {
        return nil, fmt.Errorf("provider not found: %s", name)
    }

    return provider, nil
}

func (r *ProviderRegistry) ListByType(t ProviderType) []Provider {
    r.mu.RLock()
    defer r.mu.RUnlock()

    return r.byType[t]
}

func (r *ProviderRegistry) ListAll() []Provider {
    r.mu.RLock()
    defer r.mu.RUnlock()

    providers := make([]Provider, 0, len(r.providers))
    for _, p := range r.providers {
        providers = append(providers, p)
    }

    return providers
}
```

### Auto-Registration

```go
func RegisterBuiltinProviders(registry *ProviderRegistry) {
    // Direct providers
    registry.Register(NewClaudeProvider())
    registry.Register(NewGeminiProvider())
    registry.Register(NewOpenAIProvider())
    registry.Register(NewMistralProvider())
    registry.Register(NewGroqProvider())
    registry.Register(NewDeepSeekProvider())

    // Aggregators
    registry.Register(NewOpenRouterProvider())
    registry.Register(NewTogetherProvider())
    registry.Register(NewFireworksProvider())
    registry.Register(NewNovitaProvider())
    registry.Register(NewSiliconFlowProvider())

    // Proprietary
    registry.Register(NewKiroProvider())
    registry.Register(NewCopilotProvider())
    registry.Register(NewRooCodeProvider())
    registry.Register(NewKiloAIProvider())
    registry.Register(NewMiniMaxProvider())
}
```

## Model Mapping

### OpenAI to Provider Model Mapping

```go
type ModelMapper struct {
    mappings map[string]map[string]string  // openai_model -> provider -> provider_model
}

var defaultMappings = map[string]map[string]string{
    "claude-3-5-sonnet": {
        "claude": "claude-3-5-sonnet-20241022",
        "openrouter": "anthropic/claude-3.5-sonnet",
    },
    "gpt-4-turbo": {
        "openai": "gpt-4-turbo-preview",
        "openrouter": "openai/gpt-4-turbo",
    },
    "gemini-1.5-pro": {
        "gemini": "gemini-1.5-pro-preview-0514",
        "openrouter": "google/gemini-pro-1.5",
    },
}

func (m *ModelMapper) MapModel(openaiModel, provider string) (string, error) {
    if providerMapping, ok := m.mappings[openaiModel]; ok {
        if providerModel, ok := providerMapping[provider]; ok {
            return providerModel, nil
        }
    }

    // Default: return original model name
    return openaiModel, nil
}
```

### Custom Model Mappings

```yaml
providers:
  custom:
    type: "custom"
    model_mappings:
      "gpt-4": "my-provider-v1-large"
      "gpt-3.5-turbo": "my-provider-v1-medium"
```

## Provider Capabilities

### Capability Detection

```go
type CapabilityDetector struct {
    registry *ProviderRegistry
}

func (d *CapabilityDetector) DetectCapabilities(provider string) (*ProviderCapabilities, error) {
    p, err := d.registry.Get(provider)
    if err != nil {
        return nil, err
    }

    caps := &ProviderCapabilities{
        Streaming:      p.SupportsStreaming(),
        Functions:      p.SupportsFunctions(),
        Vision:         p.SupportsVision(),
        CodeGeneration: p.SupportsCodeGeneration(),
        MaxTokens:      p.MaxTokens(),
    }

    return caps, nil
}

type ProviderCapabilities struct {
    Streaming      bool `json:"streaming"`
    Functions      bool `json:"functions"`
    Vision         bool `json:"vision"`
    CodeGeneration bool `json:"code_generation"`
    MaxTokens      int  `json:"max_tokens"`
}
```

### Capability Matrix

| Provider | Streaming | Functions | Vision | Code | Max Tokens |
|----------|-----------|-----------|--------|------|------------|
| Claude | ✅ | ✅ | ✅ | ✅ | 200K |
| Gemini | ✅ | ✅ | ✅ | ❌ | 1M |
| OpenAI | ✅ | ✅ | ✅ | ❌ | 128K |
| Kiro | ❌ | ❌ | ❌ | ✅ | N/A |
| Copilot | ✅ | ❌ | ❌ | ✅ | N/A |

## Provider Selection

### Selection Strategies

```go
type ProviderSelector interface {
    Select(request *Request, available []Provider) (Provider, error)
}

type RoundRobinSelector struct {
    counter int
}

func (s *RoundRobinSelector) Select(request *Request, available []Provider) (Provider, error) {
    if len(available) == 0 {
        return nil, fmt.Errorf("no providers available")
    }

    selected := available[s.counter%len(available)]
    s.counter++

    return selected, nil
}

type CapabilityBasedSelector struct{}

func (s *CapabilityBasedSelector) Select(request *Request, available []Provider) (Provider, error) {
    // Filter providers that support required capabilities
    var capable []Provider
    for _, p := range available {
        if request.RequiresStreaming && !p.SupportsStreaming() {
            continue
        }
        if request.RequiresFunctions && !p.SupportsFunctions() {
            continue
        }
        capable = append(capable, p)
    }

    if len(capable) == 0 {
        return nil, fmt.Errorf("no providers support required capabilities")
    }

    // Select first capable provider
    return capable[0], nil
}
```

### Request Routing

```go
type RequestRouter struct {
    registry *ProviderRegistry
    selector ProviderSelector
}

func (r *RequestRouter) Route(request *Request) (Provider, error) {
    // Get enabled providers
    providers := r.registry.ListEnabled()

    // Filter by model support
    var capable []Provider
    for _, p := range providers {
        if p.SupportsModel(request.Model) {
            capable = append(capable, p)
        }
    }

    if len(capable) == 0 {
        return nil, fmt.Errorf("no providers support model: %s", request.Model)
    }

    // Select provider
    return r.selector.Select(request, capable)
}
```

## Adding a New Provider

### Step 1: Define Provider

```go
package provider

type MyProvider struct {
    config *ProviderConfig
}

func NewMyProvider(cfg *ProviderConfig) *MyProvider {
    return &MyProvider{config: cfg}
}

func (p *MyProvider) Name() string {
    return p.config.Name
}

func (p *MyProvider) Type() ProviderType {
    return ProviderTypeDirect
}

func (p *MyProvider) SupportsModel(model string) bool {
    for _, m := range p.config.Models {
        if m.Name == model && m.Enabled {
            return true
        }
    }
    return false
}

func (p *MyProvider) Execute(ctx context.Context, req *Request) (*Response, error) {
    // Implement execution
    return nil, nil
}

func (p *MyProvider) ExecuteStream(ctx context.Context, req *Request) (<-chan *Chunk, error) {
    // Implement streaming
    return nil, nil
}

func (p *MyProvider) SupportsStreaming() bool {
    for _, m := range p.config.Models {
        if m.SupportsStreaming {
            return true
        }
    }
    return false
}

func (p *MyProvider) SupportsFunctions() bool {
    for _, m := range p.config.Models {
        if m.SupportsFunctions {
            return true
        }
    }
    return false
}

func (p *MyProvider) MaxTokens() int {
    max := 0
    for _, m := range p.config.Models {
        if m.MaxTokens > max {
            max = m.MaxTokens
        }
    }
    return max
}

func (p *MyProvider) HealthCheck(ctx context.Context) error {
    // Implement health check
    return nil
}
```

### Step 2: Register Provider

```go
func init() {
    registry.Register(NewMyProvider(&ProviderConfig{
        Name:    "myprovider",
        Type:    "direct",
        Enabled: false,
    }))
}
```

### Step 3: Add Configuration

```yaml
providers:
  myprovider:
    type: "myprovider"
    enabled: false
    auth_type: "api_key"
    endpoint: "https://api.myprovider.com"
    models:
      - name: "my-model-v1"
        enabled: true
        max_tokens: 4096
```

## API Reference

### Provider Management

**List All Providers**
```http
GET /v1/providers
```

**Get Provider Details**
```http
GET /v1/providers/{name}
```

**Enable/Disable Provider**
```http
PUT /v1/providers/{name}/enabled
```

**Get Provider Models**
```http
GET /v1/providers/{name}/models
```

**Get Provider Capabilities**
```http
GET /v1/providers/{name}/capabilities
```

**Get Provider Status**
```http
GET /v1/providers/{name}/status
```

### Model Management

**List Models**
```http
GET /v1/models
```

**List Models by Provider**
```http
GET /v1/models?provider=claude
```

**Get Model Details**
```http
GET /v1/models/{model}
```

### Capability Query

**Check Model Support**
```http
GET /v1/capabilities?model=claude-3-5-sonnet&feature=streaming
```

**Get Provider Capabilities**
```http
GET /v1/providers/{name}/capabilities
```

---

## Source: USER.md

# User Guide: Providers

This guide explains provider configuration using the current `cliproxyapi++` config schema.

## Core Model

- Client sends requests to OpenAI-compatible endpoints (`/v1/*`).
- `cliproxyapi++` resolves model -> provider/credential based on prefix + aliases.
- Provider blocks in `config.yaml` define auth, base URL, and model exposure.

## Current Provider Configuration Patterns

### Direct provider key

```yaml
claude-api-key:
  - api-key: "sk-ant-..."
    prefix: "claude-prod"
```

### Aggregator provider

```yaml
openrouter:
  - api-key: "sk-or-v1-..."
    base-url: "https://openrouter.ai/api/v1"
    prefix: "or"
```

### OpenAI-compatible provider registry

```yaml
openai-compatibility:
  - name: "openrouter"
    prefix: "or"
    base-url: "https://openrouter.ai/api/v1"
    api-key-entries:
      - api-key: "sk-or-v1-..."
```

### OAuth/session provider

```yaml
kiro:
  - token-file: "~/.aws/sso/cache/kiro-auth-token.json"
```

## Operational Best Practices

- Use `force-model-prefix: true` to enforce explicit routing boundaries.
- Keep at least one fallback provider for each critical workload.
- Use `models` + `alias` to keep client model names stable.
- Use `excluded-models` to hide risky/high-cost models from consumers.

## Validation Commands

```bash
curl -sS http://localhost:8317/v1/models \
  -H "Authorization: Bearer <api-key>" | jq '.data[:10]'

curl -sS http://localhost:8317/v1/metrics/providers | jq
```

## Deep Dives

- [Provider Usage](/provider-usage)
- [Provider Catalog](/provider-catalog)
- [Provider Operations](/provider-operations)
- [Routing and Models Reference](/routing-reference)

---

Copied count: 2
