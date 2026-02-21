# Technical Specification: High-Scale Operations

## Overview

**cliproxyapi++** is designed for high-scale production environments with intelligent operations features: automated cooldown, load balancing, health checking, and comprehensive observability.

## Operations Architecture

### Core Components

```
Operations Layer
├── Intelligent Cooldown System
│   ├── Rate Limit Detection
│   ├── Provider-Specific Cooldown
│   ├── Automatic Recovery
│   └── Load Redistribution
├── Load Balancing
│   ├── Round-Robin Strategy
│   ├── Quota-Aware Strategy
│   ├── Latency-Based Strategy
│   └── Cost-Based Strategy
├── Health Monitoring
│   ├── Provider Health Checks
│   ├── Dependency Health Checks
│   ├── Service Health Checks
│   └── Self-Healing
└── Observability
    ├── Metrics Collection
    ├── Distributed Tracing
    ├── Structured Logging
    └── Alerting
```

## Intelligent Cooldown System

### Rate Limit Detection

**Purpose**: Automatically detect when providers are rate-limited and temporarily pause requests.

**Implementation**:
```go
type RateLimitDetector struct {
    mu                sync.RWMutex
    providerStatus    map[string]ProviderStatus
    detectionWindow   time.Duration
    threshold         int
}

type ProviderStatus struct {
    InCooldown        bool
    CooldownUntil     time.Time
    RecentErrors      []time.Time
    RateLimitCount    int
}

func (d *RateLimitDetector) RecordError(provider string, statusCode int) {
    d.mu.Lock()
    defer d.mu.Unlock()

    status := d.providerStatus[provider]

    // Check for rate limit (429)
    if statusCode == 429 {
        status.RateLimitCount++
        status.RecentErrors = append(status.RecentErrors, time.Now())
    }

    // Clean old errors
    cutoff := time.Now().Add(-d.detectionWindow)
    var recent []time.Time
    for _, errTime := range status.RecentErrors {
        if errTime.After(cutoff) {
            recent = append(recent, errTime)
        }
    }
    status.RecentErrors = recent

    // Trigger cooldown if threshold exceeded
    if status.RateLimitCount >= d.threshold {
        status.InCooldown = true
        status.CooldownUntil = time.Now().Add(5 * time.Minute)
        status.RateLimitCount = 0
    }

    d.providerStatus[provider] = status
}
```

### Cooldown Duration

**Provider-specific cooldown periods**:
```yaml
providers:
  claude:
    cooldown:
      enabled: true
      default_duration: "5m"
      rate_limit_duration: "10m"
      error_duration: "2m"
  openai:
    cooldown:
      enabled: true
      default_duration: "3m"
      rate_limit_duration: "5m"
      error_duration: "1m"
```

### Automatic Recovery

**Recovery mechanisms**:
```go
type CooldownRecovery struct {
    detector *RateLimitDetector
    checker  *HealthChecker
}

func (r *CooldownRecovery) Run(ctx context.Context) {
    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            r.attemptRecovery()
        }
    }
}

func (r *CooldownRecovery) attemptRecovery() {
    for provider, status := range r.detector.providerStatus {
        if status.InCooldown && time.Now().After(status.CooldownUntil) {
            // Try health check
            if err := r.checker.Check(provider); err == nil {
                // Recovery successful
                r.detector.ExitCooldown(provider)
                log.Infof("Provider %s recovered from cooldown", provider)
            }
        }
    }
}
```

### Load Redistribution

**Redistribute requests away from cooldown providers**:
```go
type LoadRedistributor struct {
    providerRegistry map[string]ProviderExecutor
    cooldownDetector *RateLimitDetector
}

func (l *LoadRedistributor) SelectProvider(providers []string) (string, error) {
    // Filter out providers in cooldown
    available := []string{}
    for _, provider := range providers {
        if !l.cooldownDetector.IsInCooldown(provider) {
            available = append(available, provider)
        }
    }

    if len(available) == 0 {
        return "", fmt.Errorf("all providers in cooldown")
    }

    // Select from available providers
    return l.selectFromAvailable(available)
}
```

## Load Balancing Strategies

### Strategy Interface

```go
type LoadBalancingStrategy interface {
    Select(providers []string, metrics *ProviderMetrics) (string, error)
    Name() string
}
```

### Round-Robin Strategy

```go
type RoundRobinStrategy struct {
    counters map[string]int
    mu       sync.Mutex
}

func (s *RoundRobinStrategy) Select(providers []string, metrics *ProviderMetrics) (string, error) {
    s.mu.Lock()
    defer s.mu.Unlock()

    if len(providers) == 0 {
        return "", fmt.Errorf("no providers available")
    }

    // Get counter for first provider (all share counter)
    counter := s.counters["roundrobin"]
    selected := providers[counter%len(providers)]

    s.counters["roundrobin"] = counter + 1

    return selected, nil
}
```

### Quota-Aware Strategy

```go
type QuotaAwareStrategy struct{}

func (s *QuotaAwareStrategy) Select(providers []string, metrics *ProviderMetrics) (string, error) {
    var bestProvider string
    var bestQuota float64

    for _, provider := range providers {
        quota := metrics.GetQuotaRemaining(provider)
        if quota > bestQuota {
            bestQuota = quota
            bestProvider = provider
        }
    }

    if bestProvider == "" {
        return "", fmt.Errorf("no providers available")
    }

    return bestProvider, nil
}
```

### Latency-Based Strategy

```go
type LatencyStrategy struct {
    window time.Duration
}

func (s *LatencyStrategy) Select(providers []string, metrics *ProviderMetrics) (string, error) {
    var bestProvider string
    var bestLatency time.Duration

    for _, provider := range providers {
        latency := metrics.GetAverageLatency(provider, s.window)
        if bestProvider == "" || latency < bestLatency {
            bestLatency = latency
            bestProvider = provider
        }
    }

    if bestProvider == "" {
        return "", fmt.Errorf("no providers available")
    }

    return bestProvider, nil
}
```

### Cost-Based Strategy

```go
type CostStrategy struct{}

func (s *CostStrategy) Select(providers []string, metrics *ProviderMetrics) (string, error) {
    var bestProvider string
    var bestCost float64

    for _, provider := range providers {
        cost := metrics.GetAverageCost(provider)
        if bestProvider == "" || cost < bestCost {
            bestCost = cost
            bestProvider = provider
        }
    }

    if bestProvider == "" {
        return "", fmt.Errorf("no providers available")
    }

    return bestProvider, nil
}
```

## Health Monitoring

### Provider Health Checks

```go
type ProviderHealthChecker struct {
    executors map[string]ProviderExecutor
    interval  time.Duration
    timeout   time.Duration
}

func (h *ProviderHealthChecker) Check(provider string) error {
    executor, ok := h.executors[provider]
    if !ok {
        return fmt.Errorf("provider not found: %s", provider)
    }

    ctx, cancel := context.WithTimeout(context.Background(), h.timeout)
    defer cancel()

    return executor.HealthCheck(ctx, nil)
}

func (h *ProviderHealthChecker) Run(ctx context.Context) {
    ticker := time.NewTicker(h.interval)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            h.checkAllProviders()
        }
    }
}

func (h *ProviderHealthChecker) checkAllProviders() {
    for provider := range h.executors {
        if err := h.Check(provider); err != nil {
            log.Warnf("Provider %s health check failed: %v", provider, err)
        } else {
            log.Debugf("Provider %s healthy", provider)
        }
    }
}
```

### Health Status

```go
type HealthStatus struct {
    Provider    string    `json:"provider"`
    Status      string    `json:"status"`
    LastCheck   time.Time `json:"last_check"`
    LastSuccess time.Time `json:"last_success"`
    ErrorCount  int       `json:"error_count"`
}

type HealthStatus struct {
    Providers   map[string]ProviderHealthStatus `json:"providers"`
    Overall     string                         `json:"overall"`
    Timestamp   time.Time                      `json:"timestamp"`
}
```

### Self-Healing

```go
type SelfHealing struct {
    healthChecker *ProviderHealthChecker
    strategy      LoadBalancingStrategy
}

func (s *SelfHealing) Run(ctx context.Context) {
    ticker := time.NewTicker(1 * time.Minute)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            s.heal()
        }
    }
}

func (s *SelfHealing) heal() {
    status := s.healthChecker.GetStatus()

    for provider, providerStatus := range status.Providers {
        if providerStatus.Status == "unhealthy" {
            log.Warnf("Provider %s unhealthy, attempting recovery", provider)

            // Try recovery
            if err := s.healthChecker.Check(provider); err == nil {
                log.Infof("Provider %s recovered", provider)
            } else {
                log.Errorf("Provider %s recovery failed: %v", provider, err)
            }
        }
    }
}
```

## Observability

### Metrics Collection

**Metrics types**:
- Counter: Total requests, errors, tokens
- Gauge: Current connections, queue size
- Histogram: Request latency, response size
- Summary: Response time percentiles

```go
type MetricsCollector struct {
    registry prometheus.Registry

    // Counters
    requestCount    *prometheus.CounterVec
    errorCount      *prometheus.CounterVec
    tokenCount      *prometheus.CounterVec

    // Gauges
    activeRequests  *prometheus.GaugeVec
    queueSize       prometheus.Gauge

    // Histograms
    requestLatency  *prometheus.HistogramVec
    responseSize    *prometheus.HistogramVec
}

func NewMetricsCollector() *MetricsCollector {
    registry := prometheus.NewRegistry()

    c := &MetricsCollector{
        registry: registry,
        requestCount: prometheus.NewCounterVec(
            prometheus.CounterOpts{
                Name: "cliproxy_requests_total",
                Help: "Total number of requests",
            },
            []string{"provider", "model", "status"},
        ),
        errorCount: prometheus.NewCounterVec(
            prometheus.CounterOpts{
                Name: "cliproxy_errors_total",
                Help: "Total number of errors",
            },
            []string{"provider", "error_type"},
        ),
        tokenCount: prometheus.NewCounterVec(
            prometheus.CounterOpts{
                Name: "cliproxy_tokens_total",
                Help: "Total number of tokens processed",
            },
            []string{"provider", "model", "type"},
        ),
    }

    registry.MustRegister(c.requestCount, c.errorCount, c.tokenCount)

    return c
}
```

### Distributed Tracing

**OpenTelemetry integration**:
```go
import (
    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/exporters/jaeger"
    "go.opentelemetry.io/otel/sdk/trace"
)

func InitTracing(serviceName string) error {
    exporter, err := jaeger.New(jaeger.WithCollectorEndpoint(
        jaeger.WithEndpoint("http://localhost:14268/api/traces"),
    ))
    if err != nil {
        return err
    }

    tp := trace.NewTracerProvider(
        trace.WithBatcher(exporter),
    )

    otel.SetTracerProvider(tp)

    return nil
}
```

**Trace requests**:
```go
func (h *Handler) HandleRequest(c *gin.Context) {
    ctx := c.Request.Context()
    span := trace.SpanFromContext(ctx)

    span.SetAttributes(
        attribute.String("provider", provider),
        attribute.String("model", model),
    )

    // Process request
    resp, err := h.executeRequest(ctx, req)

    if err != nil {
        span.RecordError(err)
        span.SetStatus(codes.Error, err.Error())
    } else {
        span.SetStatus(codes.Ok, "success")
    }
}
```

### Structured Logging

**Log levels**:
- DEBUG: Detailed request/response data
- INFO: General operations
- WARN: Recoverable errors (rate limits, retries)
- ERROR: Failed requests

```go
import "log/slog"

type RequestLogger struct {
    logger *slog.Logger
}

func (l *RequestLogger) LogRequest(req *openai.ChatCompletionRequest, resp *openai.ChatCompletionResponse, err error) {
    attrs := []slog.Attr{
        slog.String("provider", req.Provider),
        slog.String("model", req.Model),
        slog.Int("message_count", len(req.Messages)),
        slog.Duration("latency", time.Since(req.StartTime)),
    }

    if resp != nil {
        attrs = append(attrs,
            slog.Int("prompt_tokens", resp.Usage.PromptTokens),
            slog.Int("completion_tokens", resp.Usage.CompletionTokens),
        )
    }

    if err != nil {
        l.logger.LogAttrs(context.Background(), slog.LevelError, "request_failed", attrs...)
    } else {
        l.logger.LogAttrs(context.Background(), slog.LevelInfo, "request_success", attrs...)
    }
}
```

### Alerting

**Alert conditions**:
```yaml
alerts:
  - name: High error rate
    condition: error_rate > 0.05
    duration: 5m
    severity: warning
    action: notify_slack

  - name: Provider down
    condition: provider_health == "unhealthy"
    duration: 2m
    severity: critical
    action: notify_pagerduty

  - name: Rate limit hit
    condition: rate_limit_count > 10
    duration: 1m
    severity: warning
    action: notify_slack

  - name: High latency
    condition: p95_latency > 5s
    duration: 10m
    severity: warning
    action: notify_email
```

## Performance Optimization

### Connection Pooling

```go
type ConnectionPool struct {
    clients map[string]*http.Client
    mu      sync.RWMutex
}

func NewConnectionPool() *ConnectionPool {
    return &ConnectionPool{
        clients: make(map[string]*http.Client),
    }
}

func (p *ConnectionPool) GetClient(provider string) *http.Client {
    p.mu.RLock()
    client, ok := p.clients[provider]
    p.mu.RUnlock()

    if ok {
        return client
    }

    p.mu.Lock()
    defer p.mu.Unlock()

    // Create new client
    client = &http.Client{
        Transport: &http.Transport{
            MaxIdleConns:        100,
            MaxIdleConnsPerHost: 10,
            IdleConnTimeout:     90 * time.Second,
        },
        Timeout: 60 * time.Second,
    }

    p.clients[provider] = client
    return client
}
```

### Request Batching

**Batch multiple requests**:
```go
type RequestBatcher struct {
    batch      []*openai.ChatCompletionRequest
    maxBatch   int
    timeout    time.Duration
    resultChan chan *BatchResult
}

func (b *RequestBatcher) Add(req *openai.ChatCompletionRequest) {
    b.batch = append(b.batch, req)

    if len(b.batch) >= b.maxBatch {
        b.flush()
    }
}

func (b *RequestBatcher) flush() {
    if len(b.batch) == 0 {
        return
    }

    // Execute batch
    results := b.executeBatch(b.batch)

    // Send results
    for _, result := range results {
        b.resultChan <- result
    }

    b.batch = nil
}
```

### Response Caching

**Cache responses**:
```go
type ResponseCache struct {
    cache  *lru.Cache
    ttl    time.Duration
}

func NewResponseCache(size int, ttl time.Duration) *ResponseCache {
    return &ResponseCache{
        cache: lru.New(size),
        ttl:   ttl,
    }
}

func (c *ResponseCache) Get(key string) (*openai.ChatCompletionResponse, bool) {
    item, ok := c.cache.Get(key)
    if !ok {
        return nil, false
    }

    cached := item.(*CacheEntry)
    if time.Since(cached.Timestamp) > c.ttl {
        c.cache.Remove(key)
        return nil, false
    }

    return cached.Response, true
}

func (c *ResponseCache) Set(key string, resp *openai.ChatCompletionResponse) {
    c.cache.Add(key, &CacheEntry{
        Response:  resp,
        Timestamp: time.Now(),
    })
}
```

## Disaster Recovery

### Backup and Restore

**Backup configuration**:
```bash
#!/bin/bash
# backup.sh

BACKUP_DIR="/backups/cliproxy"
TIMESTAMP=$(date +%Y%m%d_%H%M%S)

# Backup config
cp config.yaml "$BACKUP_DIR/config_$TIMESTAMP.yaml"

# Backup auths
tar -czf "$BACKUP_DIR/auths_$TIMESTAMP.tar.gz" auths/

# Backup logs
tar -czf "$BACKUP_DIR/logs_$TIMESTAMP.tar.gz" logs/

echo "Backup complete: $BACKUP_DIR/cliproxy_$TIMESTAMP"
```

**Restore configuration**:
```bash
#!/bin/bash
# restore.sh

BACKUP_FILE="$1"

# Extract config
tar -xzf "$BACKUP_FILE" --wildcards "config_*.yaml"

# Extract auths
tar -xzf "$BACKUP_FILE" --wildcards "auths_*.tar.gz"

# Restart service
docker compose restart
```

### Failover

**Active-passive failover**:
```yaml
server:
  failover:
    enabled: true
    mode: "active_passive"
    health_check_interval: "10s"
    failover_timeout: "30s"
    backup_url: "http://backup-proxy:8317"
```

**Active-active failover**:
```yaml
server:
  failover:
    enabled: true
    mode: "active_active"
    load_balancing: "consistent_hash"
    health_check_interval: "10s"
    peers:
      - "http://proxy1:8317"
      - "http://proxy2:8317"
      - "http://proxy3:8317"
```

## API Reference

### Operations Endpoints

**Health Check**
```http
GET /health
```

**Metrics**
```http
GET /metrics
```

**Provider Status**
```http
GET /v0/operations/providers/status
```

Response:
```json
{
  "providers": {
    "claude": {
      "status": "healthy",
      "in_cooldown": false,
      "last_check": "2026-02-19T23:00:00Z",
      "requests_last_minute": 100,
      "errors_last_minute": 2,
      "average_latency_ms": 500
    }
  }
}
```

**Cooldown Status**
```http
GET /v0/operations/cooldown/status
```

Response:
```json
{
  "providers_in_cooldown": ["claude"],
  "cooldown_periods": {
    "claude": {
      "started_at": "2026-02-19T22:50:00Z",
      "ends_at": "2026-02-19T22:55:00Z",
      "reason": "rate_limit"
    }
  }
}
```

**Force Recovery**
```http
POST /v0/operations/providers/{provider}/recover
```
