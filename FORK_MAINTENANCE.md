# Fork Maintenance Guide: LiteLLM Integration

This document describes the conflict-free architecture for maintaining custom LiteLLM features while staying in sync with upstream [router-for-me/CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI).

## Table of Contents

1. [Architecture Overview](#architecture-overview)
2. [File Structure](#file-structure)
3. [Implementation Details](#implementation-details)
4. [Hook Locations](#hook-locations)
5. [Configuration Schema](#configuration-schema)
6. [Merge Procedure](#merge-procedure)
7. [Testing Requirements](#testing-requirements)
8. [Troubleshooting](#troubleshooting)

---

## Architecture Overview

### Design Principles

| Principle | Rationale |
|-----------|-----------|
| **Separate Files** | New files never conflict during `git merge` |
| **Minimal Hooks** | Only 1 touch point in upstream code = predictable conflicts |
| **Gin Middleware** | Idiomatic pattern, intercepts before upstream handlers |
| **No Internal Coupling** | Don't depend on upstream's internal structs/signatures |

### Feature Scope

| Feature | Status | Implementation |
|---------|--------|----------------|
| Explicit LiteLLM routing (`litellm-models`) | ✅ Included | Middleware intercept |
| Passthrough mode (`litellm-passthrough-mode`) | ✅ Included | Middleware flag check |
| Enhanced error logging | ✅ Included | Separate helpers file |
| Request ID propagation | ✅ Included | Shared helper in error_helpers.go |
| Fallback on OAuth error | ✅ Included | litellm_fallback.go middleware |

### Why This Architecture?

Previous implementations modified upstream's `FallbackHandler`, `routes.go`, and `proxy.go` directly. This caused conflicts on every upstream merge because:

1. Upstream refactored `FallbackHandler` signature multiple times
2. Upstream added hot-reload support changing function signatures
3. Our response recording logic was interleaved with upstream's handler flow

The new architecture uses **Gin middleware** to intercept requests **before** they reach upstream's handlers, making our code completely independent.

---

## File Structure

```
internal/api/modules/amp/
│
├── # UPSTREAM FILES (minimal modifications with >>> FORK markers)
├── amp.go                    # Hook: LiteLLM init + middleware registration
├── fallback_handlers.go      # no changes
├── routes.go                 # Hook: LiteLLM middleware in chain
├── proxy.go                  # Minimal: request ID + error handler + handleProxyAbort
├── model_mapper.go           # no changes
│
├── # OUR FILES (never conflict with upstream)
├── litellm_middleware.go     # LiteLLM explicit routing middleware
├── litellm_proxy.go          # LiteLLM reverse proxy creation
├── litellm_fallback.go       # LiteLLM fallback on quota errors
├── litellm_config.go         # Config helpers and validation
└── error_helpers.go          # Error classification + NewProxyErrorHandler factory
```

---

## Implementation Details

### 1. error_helpers.go

Generic error classification utilities. Can potentially be contributed upstream.

```go
// error_helpers.go
package amp

import (
    "context"
    "errors"
    "fmt"
    "net/http"
    "net/url"
    "time"

    log "github.com/sirupsen/logrus"
)

// ErrorType classifies proxy errors for appropriate logging and handling
type ErrorType string

const (
    ErrorTypeClientDisconnect ErrorType = "client_disconnect"
    ErrorTypeTimeout          ErrorType = "timeout"
    ErrorTypeNetworkTimeout   ErrorType = "network_timeout"
    ErrorTypeNetworkError     ErrorType = "network_error"
    ErrorTypeProxyError       ErrorType = "proxy_error"
    ErrorTypeUnknown          ErrorType = "unknown"
)

// ClassifyProxyError determines the category of a proxy error
func ClassifyProxyError(err error) ErrorType {
    if err == nil {
        return ErrorTypeUnknown
    }

    // Client disconnected (context canceled)
    if errors.Is(err, context.Canceled) {
        return ErrorTypeClientDisconnect
    }

    // Timeout errors
    if errors.Is(err, context.DeadlineExceeded) {
        return ErrorTypeTimeout
    }

    // URL/network errors
    var urlErr *url.Error
    if errors.As(err, &urlErr) {
        if urlErr.Timeout() {
            return ErrorTypeNetworkTimeout
        }
        return ErrorTypeNetworkError
    }

    return ErrorTypeProxyError
}

// RequestContext contains debugging metadata extracted from a request
type RequestContext struct {
    RequestID   string
    ClientIP    string
    UserAgent   string
    ContentType string
    Method      string
    Path        string
}

// ExtractRequestContext extracts useful debugging context from an HTTP request
func ExtractRequestContext(req *http.Request) RequestContext {
    return RequestContext{
        RequestID:   req.Header.Get("X-Request-ID"),
        ClientIP:    req.RemoteAddr,
        UserAgent:   req.Header.Get("User-Agent"),
        ContentType: req.Header.Get("Content-Type"),
        Method:      req.Method,
        Path:        req.URL.Path,
    }
}

// GetOrGenerateRequestID returns existing request ID or generates a new one
func GetOrGenerateRequestID(req *http.Request) string {
    if id := req.Header.Get("X-Request-ID"); id != "" {
        return id
    }
    return fmt.Sprintf("req-%d", time.Now().UnixNano())
}

// LogProxyError logs a proxy error with appropriate level and context
func LogProxyError(proxyName string, req *http.Request, err error) {
    errorType := ClassifyProxyError(err)
    ctx := ExtractRequestContext(req)

    fields := log.Fields{
        "error_type": errorType,
        "method":     ctx.Method,
        "path":       ctx.Path,
        "client_ip":  ctx.ClientIP,
    }

    if ctx.RequestID != "" {
        fields["request_id"] = ctx.RequestID
    }

    // Use WARN for client-side issues, ERROR for upstream failures
    if errorType == ErrorTypeClientDisconnect {
        log.WithFields(fields).Warnf("%s: client disconnected during %s %s",
            proxyName, ctx.Method, ctx.Path)
    } else {
        fields["error"] = err.Error()
        log.WithFields(fields).Errorf("%s proxy error for %s %s: %v",
            proxyName, ctx.Method, ctx.Path, err)
    }
}
```

### 2. litellm_proxy.go

Self-contained LiteLLM proxy with enhanced error handling.

```go
// litellm_proxy.go
package amp

import (
    "fmt"
    "net/http"
    "net/http/httputil"
    "net/url"
    "strings"

    "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
    log "github.com/sirupsen/logrus"
)

// CreateLiteLLMProxy creates a reverse proxy for LiteLLM with enhanced error handling
func CreateLiteLLMProxy(cfg *config.Config) (*httputil.ReverseProxy, error) {
    if cfg.LiteLLMBaseURL == "" {
        return nil, fmt.Errorf("litellm-base-url not configured")
    }

    parsed, err := url.Parse(cfg.LiteLLMBaseURL)
    if err != nil {
        return nil, fmt.Errorf("invalid litellm-base-url: %w", err)
    }

    proxy := httputil.NewSingleHostReverseProxy(parsed)
    originalDirector := proxy.Director

    proxy.Director = func(req *http.Request) {
        originalDirector(req)
        req.Host = parsed.Host

        // Generate/propagate request ID for distributed tracing
        requestID := GetOrGenerateRequestID(req)
        req.Header.Set("X-Request-ID", requestID)

        // Strip /api/provider/{provider} prefix if present
        // Example: /api/provider/openai/v1/chat/completions → /v1/chat/completions
        path := req.URL.Path
        if strings.HasPrefix(path, "/api/provider/") {
            parts := strings.SplitN(path, "/", 5)
            if len(parts) >= 5 {
                req.URL.Path = "/" + parts[4]
            } else if len(parts) == 4 {
                req.URL.Path = "/"
            }
        }

        // Inject LiteLLM API key if configured
        if cfg.LiteLLMAPIKey != "" {
            req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", cfg.LiteLLMAPIKey))
        }

        log.Debugf("litellm proxy: forwarding %s %s (original: %s)",
            req.Method, req.URL.Path, path)
    }

    // Enhanced error handler with classification
    proxy.ErrorHandler = func(rw http.ResponseWriter, req *http.Request, err error) {
        LogProxyError("litellm", req, err)

        rw.Header().Set("Content-Type", "application/json")
        rw.WriteHeader(http.StatusBadGateway)
        rw.Write([]byte(`{"error":"litellm_proxy_error","message":"Failed to reach LiteLLM proxy"}`))
    }

    return proxy, nil
}
```

### 3. litellm_config.go

Configuration helpers and validation.

```go
// litellm_config.go
package amp

import (
    "strings"
    "sync"

    "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// LiteLLMConfig holds runtime LiteLLM configuration with thread-safe access
type LiteLLMConfig struct {
    mu            sync.RWMutex
    enabled       bool
    passthroughMode bool
    models        map[string]bool
}

// NewLiteLLMConfig creates a new LiteLLM configuration from app config
func NewLiteLLMConfig(cfg *config.Config) *LiteLLMConfig {
    lc := &LiteLLMConfig{
        models: make(map[string]bool),
    }
    lc.Update(cfg)
    return lc
}

// Update refreshes the configuration (hot-reload support)
func (lc *LiteLLMConfig) Update(cfg *config.Config) {
    lc.mu.Lock()
    defer lc.mu.Unlock()

    lc.enabled = cfg.LiteLLMHybridMode && cfg.LiteLLMBaseURL != ""
    lc.passthroughMode = cfg.LiteLLMPassthroughMode

    // Rebuild models map
    lc.models = make(map[string]bool)
    for _, model := range cfg.LiteLLMModels {
        lc.models[strings.ToLower(model)] = true
    }
}

// IsEnabled returns whether LiteLLM routing is enabled
func (lc *LiteLLMConfig) IsEnabled() bool {
    lc.mu.RLock()
    defer lc.mu.RUnlock()
    return lc.enabled
}

// IsPassthroughMode returns whether all traffic should go to LiteLLM
func (lc *LiteLLMConfig) IsPassthroughMode() bool {
    lc.mu.RLock()
    defer lc.mu.RUnlock()
    return lc.passthroughMode
}

// ShouldRouteToLiteLLM checks if a model should be routed to LiteLLM
func (lc *LiteLLMConfig) ShouldRouteToLiteLLM(model string) bool {
    lc.mu.RLock()
    defer lc.mu.RUnlock()

    if !lc.enabled {
        return false
    }

    // Passthrough mode routes everything to LiteLLM
    if lc.passthroughMode {
        return true
    }

    // Check explicit model list
    return lc.models[strings.ToLower(model)]
}
```

### 4. litellm_middleware.go

The main middleware that intercepts and routes requests.

```go
// litellm_middleware.go
package amp

import (
    "bytes"
    "encoding/json"
    "io"
    "net/http/httputil"
    "strings"

    "github.com/gin-gonic/gin"
    log "github.com/sirupsen/logrus"
)

// LiteLLMMiddleware creates a Gin middleware for LiteLLM routing
// This middleware intercepts requests and routes matching models to LiteLLM
// before they reach upstream handlers.
func LiteLLMMiddleware(liteLLMCfg *LiteLLMConfig, proxy *httputil.ReverseProxy) gin.HandlerFunc {
    return func(c *gin.Context) {
        // Skip if LiteLLM not enabled or no proxy
        if !liteLLMCfg.IsEnabled() || proxy == nil {
            c.Next()
            return
        }

        // Skip non-POST requests (GET /models, OPTIONS, etc.)
        if c.Request.Method != "POST" {
            c.Next()
            return
        }

        // Read and restore request body
        bodyBytes, err := io.ReadAll(c.Request.Body)
        if err != nil {
            log.Debugf("litellm middleware: failed to read body: %v", err)
            c.Next()
            return
        }
        c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))

        // Extract model from request
        model := extractModelFromBody(bodyBytes)
        if model == "" {
            // Try extracting from URL path (Gemini-style)
            model = extractModelFromPath(c.Request.URL.Path)
        }

        if model == "" {
            // Can't determine model, let upstream handle
            c.Next()
            return
        }

        // Check if should route to LiteLLM
        if liteLLMCfg.ShouldRouteToLiteLLM(model) {
            log.Infof("litellm routing: %s → LiteLLM", model)

            // Restore body for proxy
            c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))

            // Route to LiteLLM and abort chain
            proxy.ServeHTTP(c.Writer, c.Request)
            c.Abort()
            return
        }

        // Not a LiteLLM model, continue to upstream handlers
        c.Next()
    }
}

// extractModelFromBody extracts the model name from a JSON request body
func extractModelFromBody(body []byte) string {
    var payload struct {
        Model string `json:"model"`
    }
    if err := json.Unmarshal(body, &payload); err != nil {
        return ""
    }
    return payload.Model
}

// extractModelFromPath extracts model from Gemini-style URL paths
// e.g., /v1beta1/models/gemini-pro:generateContent → gemini-pro
func extractModelFromPath(path string) string {
    // Handle /models/{model}:{action} pattern
    if idx := strings.Index(path, "/models/"); idx >= 0 {
        modelAction := path[idx+len("/models/"):]
        if colonIdx := strings.Index(modelAction, ":"); colonIdx > 0 {
            return modelAction[:colonIdx]
        }
        // No colon, check for slash
        if slashIdx := strings.Index(modelAction, "/"); slashIdx > 0 {
            return modelAction[:slashIdx]
        }
        return modelAction
    }
    return ""
}
```

---

## Hook Locations

### Single Hook Point in amp.go

Location: `internal/api/modules/amp/amp.go` in the `Register()` function.

**Search for this comment to find the hook location:**
```go
// Always register provider aliases
```

**Add these lines BEFORE that comment:**

```go
// >>> LITELLM_HOOK_START
// LiteLLM middleware integration (see FORK_MAINTENANCE.md)
var liteLLMProxy *httputil.ReverseProxy
liteLLMCfg := NewLiteLLMConfig(ctx.Config)
if liteLLMCfg.IsEnabled() {
    var err error
    liteLLMProxy, err = CreateLiteLLMProxy(ctx.Config)
    if err != nil {
        log.Errorf("Failed to create LiteLLM proxy: %v", err)
    } else {
        log.Infof("LiteLLM routing enabled for: %s", ctx.Config.LiteLLMBaseURL)
    }
}
// <<< LITELLM_HOOK_END
```

**Then modify the provider aliases registration to include middleware:**

Find the line registering provider routes (varies by upstream version) and add the middleware:

```go
// >>> LITELLM_MIDDLEWARE_HOOK
if liteLLMProxy != nil {
    ampProviders.Use(LiteLLMMiddleware(liteLLMCfg, liteLLMProxy))
}
// <<< LITELLM_MIDDLEWARE_HOOK
```

### Hook Re-application After Merge

After each upstream merge:

1. Search for `LITELLM_HOOK` markers - if missing, re-add them
2. Run `go build` to verify compilation
3. Test with a model in your `litellm-models` list

---

## Configuration Schema

### Current Config Fields (in config.go)

These fields should already exist from previous implementation:

```go
// LiteLLM Hybrid Routing
LiteLLMHybridMode      bool     `yaml:"litellm-hybrid-mode"`
LiteLLMBaseURL         string   `yaml:"litellm-base-url"`
LiteLLMAPIKey          string   `yaml:"litellm-api-key"`
LiteLLMModels          []string `yaml:"litellm-models"`
LiteLLMPassthroughMode bool     `yaml:"litellm-passthrough-mode"`
```

### Example config.yaml

```yaml
# LiteLLM Integration
litellm-hybrid-mode: true
litellm-base-url: "http://localhost:4000"
litellm-api-key: "sk-your-litellm-key"
litellm-models:
  - "gpt-5"
  - "gpt-5-codex"
  - "gpt-5.1"
  - "deepseek-chat"
litellm-passthrough-mode: false  # Set true to route ALL traffic to LiteLLM
```

### Future: Namespaced Config (Recommended)

To avoid config schema drift with upstream, consider migrating to:

```yaml
extensions:
  litellm:
    enabled: true
    base-url: "http://localhost:4000"
    api-key: "sk-xxx"
    models:
      - "gpt-5"
      - "gpt-5-codex"
    passthrough-mode: false
```

---

## Merge Procedure

### Standard Merge Workflow

```bash
# 1. Fetch upstream
git fetch upstream

# 2. Check what's new
git log --oneline main..upstream/main

# 3. Merge upstream
git merge upstream/main

# 4. If conflicts in amp.go:
#    - Accept upstream's version for the conflicting section
#    - Re-add LITELLM_HOOK markers (see Hook Locations above)

# 5. Verify our files are intact
ls internal/api/modules/amp/litellm_*.go
ls internal/api/modules/amp/error_helpers.go

# 6. Build and test
go build -o ./bin/ai-cli-proxy-api ./cmd/server
./bin/ai-cli-proxy-api &
curl -X POST http://localhost:8317/api/provider/openai/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "gpt-5", "messages": [{"role": "user", "content": "test"}]}'

# 7. Commit and push
git add .
git commit -m "Merge upstream: [description]"
git push origin main
```

### Conflict Resolution Checklist

| File | Action |
|------|--------|
| `litellm_*.go` | Should NEVER conflict (our files) |
| `error_helpers.go` | Should NEVER conflict (our file) |
| `amp.go` | Re-apply LITELLM_HOOK markers if removed |
| `fallback_handlers.go` | Accept upstream (we don't modify) |
| `routes.go` | Re-apply LITELLM_HOOK_MIDDLEWARE markers if removed |
| `proxy.go` | Accept upstream, then re-apply >>> FORK sections (request ID, error handler, handleProxyAbort) |
| `config.go` | Check our LiteLLM fields still exist |

---

## Testing Requirements

### Unit Tests

Create `litellm_middleware_test.go`:

```go
func TestExtractModelFromBody(t *testing.T) {
    tests := []struct {
        body     string
        expected string
    }{
        {`{"model": "gpt-5"}`, "gpt-5"},
        {`{"model": "claude-3-opus"}`, "claude-3-opus"},
        {`{}`, ""},
        {`invalid`, ""},
    }
    // ...
}

func TestShouldRouteToLiteLLM(t *testing.T) {
    // Test explicit model list
    // Test passthrough mode
    // Test disabled state
}
```

### Integration Tests

```bash
# Test LiteLLM routing
curl -X POST http://localhost:8317/api/provider/openai/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "gpt-5", "messages": [{"role": "user", "content": "Hello"}]}'
# Should see: "litellm routing: gpt-5 → LiteLLM" in logs

# Test OAuth routing (model NOT in litellm-models)
curl -X POST http://localhost:8317/api/provider/anthropic/v1/messages \
  -H "Content-Type: application/json" \
  -d '{"model": "claude-3-opus", "messages": [{"role": "user", "content": "Hello"}]}'
# Should NOT see LiteLLM routing, should go to OAuth
```

### CI Verification

Add to CI pipeline:

```yaml
- name: Verify LiteLLM files exist
  run: |
    test -f internal/api/modules/amp/litellm_middleware.go
    test -f internal/api/modules/amp/litellm_proxy.go
    test -f internal/api/modules/amp/litellm_config.go
    test -f internal/api/modules/amp/error_helpers.go

- name: Verify hooks present
  run: |
    grep -q "LITELLM_HOOK" internal/api/modules/amp/amp.go
```

---

## Troubleshooting

### LiteLLM routing not working

1. Check `litellm-hybrid-mode: true` in config
2. Check `litellm-base-url` is set
3. Check model is in `litellm-models` list
4. Check logs for "LiteLLM routing enabled"

### Requests going to wrong backend

1. Verify model name matches exactly (case-insensitive)
2. Check middleware is registered (search logs for "LiteLLM")
3. Ensure middleware runs BEFORE upstream handlers

### After merge, LiteLLM stopped working

1. Check if `amp.go` was modified by upstream
2. Search for `LITELLM_HOOK` markers
3. Re-apply hooks if missing (see Hook Locations)
4. Rebuild and test

### Config fields missing after merge

1. Check `internal/config/config.go` still has LiteLLM fields
2. If upstream restructured config, add fields back
3. Consider using namespaced config to avoid future issues

---

## Changelog

| Date | Change |
|------|--------|
| 2024-12-05 | Initial architecture design |
| 2024-12-05 | Dropped fallback-on-error feature (too coupled) |
| 2024-12-05 | Adopted middleware pattern for conflict-free merges |

---

## References

- [Gin Middleware Documentation](https://gin-gonic.com/docs/examples/custom-middleware/)
- [httputil.ReverseProxy](https://pkg.go.dev/net/http/httputil#ReverseProxy)
- [Upstream CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)
