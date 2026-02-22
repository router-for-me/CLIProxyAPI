# Merged Fragmented Markdown

## Source: features/operations/SPEC.md

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


---

## Source: features/operations/USER.md

# User Guide: High-Scale Operations

## Understanding Operations in cliproxyapi++

cliproxyapi++ is built for production environments with intelligent operations that automatically handle rate limits, load balance requests, monitor health, and recover from failures. This guide explains how to configure and use these features.

## Quick Start: Production Deployment

### docker-compose.yml (Production)

```yaml
services:
  cliproxy:
    image: KooshaPari/cliproxyapi-plusplus:latest
    container_name: cliproxyapi++

    # Security
    security_opt:
      - no-new-privileges:true
    read_only: true
    user: "65534:65534"

    # Resources
    deploy:
      resources:
        limits:
          cpus: '4'
          memory: 2G
        reservations:
          cpus: '1'
          memory: 512M

    # Health check
    healthcheck:
      test: ["CMD", "wget", "--quiet", "--tries=1", "--spider", "http://localhost:8317/health"]
      interval: 30s
      timeout: 10s
      retries: 3
      start_period: 40s

    # Ports
    ports:
      - "8317:8317"
      - "9090:9090"  # Metrics

    # Volumes
    volumes:
      - ./config.yaml:/config/config.yaml:ro
      - ./auths:/auths:rw
      - ./logs:/logs:rw

    # Restart
    restart: unless-stopped
```

## Intelligent Cooldown

### What is Cooldown?

When a provider returns rate limit errors (429), cliproxyapi++ automatically pauses requests to that provider for a configurable cooldown period. This prevents your IP from being flagged and allows the provider to recover.

### Configure Cooldown

**config.yaml**:
```yaml
server:
  operations:
    cooldown:
      enabled: true
      detection_window: "1m"
      error_threshold: 5  # 5 errors in 1 minute triggers cooldown

providers:
  claude:
    cooldown:
      enabled: true
      default_duration: "5m"
      rate_limit_duration: "10m"  # Longer cooldown for 429
      error_duration: "2m"        # Shorter for other errors

  openai:
    cooldown:
      enabled: true
      default_duration: "3m"
      rate_limit_duration: "5m"
      error_duration: "1m"
```

### Monitor Cooldown Status

```bash
# Check cooldown status
curl http://localhost:8317/v0/operations/cooldown/status
```

Response:
```json
{
  "providers_in_cooldown": ["claude"],
  "cooldown_periods": {
    "claude": {
      "started_at": "2026-02-19T22:50:00Z",
      "ends_at": "2026-02-19T23:00:00Z",
      "remaining_seconds": 300,
      "reason": "rate_limit"
    }
  }
}
```

### Manual Cooldown Control

**Force cooldown**:
```bash
curl -X POST http://localhost:8317/v0/operations/providers/claude/cooldown \
  -H "Content-Type: application/json" \
  -d '{
    "duration": "10m",
    "reason": "manual"
  }'
```

**Force recovery**:
```bash
curl -X POST http://localhost:8317/v0/operations/providers/claude/recover
```

## Load Balancing

### Choose a Strategy

**config.yaml**:
```yaml
server:
  operations:
    load_balancing:
      strategy: "round_robin"  # Options: round_robin, quota_aware, latency, cost
```

**Strategies**:
- `round_robin`: Rotate evenly through providers (default)
- `quota_aware`: Use provider with most remaining quota
- `latency`: Use provider with lowest recent latency
- `cost`: Use provider with lowest average cost

### Round-Robin (Default)

```yaml
server:
  operations:
    load_balancing:
      strategy: "round_robin"
```

**Best for**: Simple deployments with similar providers.

### Quota-Aware

```yaml
server:
  operations:
    load_balancing:
      strategy: "quota_aware"

providers:
  claude:
    quota:
      limit: 1000000
      reset: "monthly"

  openai:
    quota:
      limit: 2000000
      reset: "monthly"
```

**Best for**: Managing API quota limits across multiple providers.

### Latency-Based

```yaml
server:
  operations:
    load_balancing:
      strategy: "latency"
      latency_window: "5m"  # Average over last 5 minutes
```

**Best for**: Performance-critical applications.

### Cost-Based

```yaml
server:
  operations:
    load_balancing:
      strategy: "cost"

providers:
  claude:
    cost_per_1k_tokens:
      input: 0.003
      output: 0.015

  openai:
    cost_per_1k_tokens:
      input: 0.005
      output: 0.015
```

**Best for**: Cost optimization.

### Provider Priority

```yaml
providers:
  claude:
    priority: 1  # Higher priority
  gemini:
    priority: 2
  openai:
    priority: 3
```

Higher priority providers are preferred (lower number = higher priority).

## Health Monitoring

### Configure Health Checks

**config.yaml**:
```yaml
server:
  operations:
    health_check:
      enabled: true
      interval: "30s"
      timeout: "10s"
      unhealthy_threshold: 3  # 3 failures = unhealthy
      healthy_threshold: 2    # 2 successes = healthy

providers:
  claude:
    health_check:
      enabled: true
      endpoint: "https://api.anthropic.com/v1/messages"
      method: "GET"
```

### Monitor Provider Health

```bash
# Check all providers
curl http://localhost:8317/v0/operations/providers/status
```

Response:
```json
{
  "providers": {
    "claude": {
      "status": "healthy",
      "in_cooldown": false,
      "last_check": "2026-02-19T23:00:00Z",
      "uptime_percent": 99.9,
      "requests_last_minute": 100,
      "errors_last_minute": 0,
      "average_latency_ms": 450
    },
    "openai": {
      "status": "unhealthy",
      "in_cooldown": true,
      "last_check": "2026-02-19T23:00:00Z",
      "uptime_percent": 95.0,
      "requests_last_minute": 0,
      "errors_last_minute": 10,
      "average_latency_ms": 0
    }
  }
}
```

### Self-Healing

Enable automatic recovery of unhealthy providers:

```yaml
server:
  operations:
    self_healing:
      enabled: true
      check_interval: "1m"
      max_attempts: 3
      backoff_duration: "30s"
```

## Observability

### Enable Metrics

**config.yaml**:
```yaml
metrics:
  enabled: true
  port: 9090
  path: "/metrics"
```

**View metrics**:
```bash
curl http://localhost:9090/metrics
```

**Key metrics**:
```
# Request count
cliproxy_requests_total{provider="claude",model="claude-3-5-sonnet",status="success"} 1000

# Error count
cliproxy_errors_total{provider="claude",error_type="rate_limit"} 5

# Token usage
cliproxy_tokens_total{provider="claude",model="claude-3-5-sonnet",type="input"} 50000
cliproxy_tokens_total{provider="claude",model="claude-3-5-sonnet",type="output"} 25000

# Request latency
cliproxy_request_duration_seconds_bucket{provider="claude",le="0.5"} 800
cliproxy_request_duration_seconds_bucket{provider="claude",le="1"} 950
cliproxy_request_duration_seconds_bucket{provider="claude",le="+Inf"} 1000
```

### Prometheus Integration

**prometheus.yml**:
```yaml
scrape_configs:
  - job_name: 'cliproxyapi'
    static_configs:
      - targets: ['localhost:9090']
    scrape_interval: 15s
```

### Grafana Dashboards

Import the cliproxyapi++ dashboard for:
- Request rate by provider
- Error rate tracking
- P95/P99 latency
- Token usage over time
- Cooldown events
- Provider health status

### Structured Logging

**config.yaml**:
```yaml
logging:
  level: "info"  # debug, info, warn, error
  format: "json"
  output: "/logs/cliproxy.log"
  rotation:
    enabled: true
    max_size: "100M"
    max_age: "30d"
    max_backups: 10
```

**View logs**:
```bash
# Follow logs
tail -f logs/cliproxy.log

# Filter for errors
grep "level=error" logs/cliproxy.log

# Pretty print JSON logs
cat logs/cliproxy.log | jq '.'
```

**Log entry example**:
```json
{
  "timestamp": "2026-02-19T23:00:00Z",
  "level": "info",
  "msg": "request_success",
  "provider": "claude",
  "model": "claude-3-5-sonnet",
  "request_id": "req-123",
  "latency_ms": 450,
  "tokens": {
    "input": 100,
    "output": 50
  }
}
```

### Distributed Tracing (Optional)

Enable OpenTelemetry tracing:

```yaml
tracing:
  enabled: true
  exporter: "jaeger"  # Options: jaeger, zipkin, otlp
  endpoint: "http://localhost:14268/api/traces"
  service_name: "cliproxyapi++"
  sample_rate: 0.1  # Sample 10% of traces
```

**View traces**:
- Jaeger UI: http://localhost:1668
- Zipkin UI: http://localhost:9411

## Alerting

### Configure Alerts

**config.yaml**:
```yaml
alerts:
  enabled: true
  rules:
    - name: High error rate
      condition: error_rate > 0.05
      duration: "5m"
      severity: warning
      notifications:
        - slack
        - email

    - name: Provider down
      condition: provider_health == "unhealthy"
      duration: "2m"
      severity: critical
      notifications:
        - pagerduty

    - name: Rate limit hit
      condition: rate_limit_count > 10
      duration: "1m"
      severity: warning
      notifications:
        - slack

    - name: High latency
      condition: p95_latency > 5s
      duration: "10m"
      severity: warning
      notifications:
        - email
```

### Notification Channels

**Slack**:
```yaml
notifications:
  slack:
    enabled: true
    webhook_url: "${SLACK_WEBHOOK_URL}"
    channel: "#alerts"
```

**Email**:
```yaml
notifications:
  email:
    enabled: true
    smtp_server: "smtp.example.com:587"
    from: "alerts@example.com"
    to: ["ops@example.com"]
```

**PagerDuty**:
```yaml
notifications:
  pagerduty:
    enabled: true
    api_key: "${PAGERDUTY_API_KEY}"
    service_key: "your-service-key"
```

## Performance Optimization

### Connection Pooling

Configure connection pools:

```yaml
server:
  operations:
    connection_pool:
      max_idle_conns: 100
      max_idle_conns_per_host: 10
      idle_conn_timeout: "90s"
```

### Request Batching

Enable batch processing:

```yaml
server:
  operations:
    batch_processing:
      enabled: true
      max_batch_size: 10
      timeout: "100ms"
```

### Response Caching

Cache responses for identical requests:

```yaml
server:
  operations:
    cache:
      enabled: true
      size: 1000  # Number of cached responses
      ttl: "5m"   # Time to live
```

## Disaster Recovery

### Backup Configuration

Automated backup script:

```bash
#!/bin/bash
# backup.sh

BACKUP_DIR="/backups/cliproxy"
TIMESTAMP=$(date +%Y%m%d_%H%M%S)

# Create backup directory
mkdir -p "$BACKUP_DIR"

# Backup config
cp config.yaml "$BACKUP_DIR/config_$TIMESTAMP.yaml"

# Backup auths
tar -czf "$BACKUP_DIR/auths_$TIMESTAMP.tar.gz" auths/

# Backup logs
tar -czf "$BACKUP_DIR/logs_$TIMESTAMP.tar.gz" logs/

# Remove old backups (keep last 30)
find "$BACKUP_DIR" -name "*.tar.gz" -mtime +30 -delete

echo "Backup complete: $BACKUP_DIR/cliproxy_$TIMESTAMP"
```

Schedule with cron:
```bash
# Run daily at 2 AM
0 2 * * * /path/to/backup.sh
```

### Restore Configuration

```bash
#!/bin/bash
# restore.sh

BACKUP_FILE="$1"

# Stop service
docker compose down

# Extract config
tar -xzf "$BACKUP_FILE" --wildcards "config_*.yaml"

# Extract auths
tar -xzf "$BACKUP_FILE" --wildcards "auths_*.tar.gz"

# Start service
docker compose up -d
```

### Failover Configuration

**Active-Passive**:
```yaml
server:
  failover:
    enabled: true
    mode: "active_passive"
    health_check_interval: "10s"
    failover_timeout: "30s"
    backup_url: "http://backup-proxy:8317"
```

**Active-Active**:
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

## Troubleshooting

### High Error Rate

**Problem**: Error rate > 5%

**Solutions**:
1. Check provider status: `GET /v0/operations/providers/status`
2. Review cooldown status: `GET /v0/operations/cooldown/status`
3. Check logs for error patterns
4. Verify credentials are valid
5. Check provider status page for outages

### Provider Always in Cooldown

**Problem**: Provider stuck in cooldown

**Solutions**:
1. Manually recover: `POST /v0/operations/providers/{provider}/recover`
2. Adjust cooldown thresholds
3. Check rate limits from provider
4. Reduce request rate
5. Use multiple providers for load distribution

### High Latency

**Problem**: Requests taking > 5 seconds

**Solutions**:
1. Check connection pool settings
2. Enable latency-based load balancing
3. Check provider status for issues
4. Review network connectivity
5. Consider caching responses

### Memory Usage High

**Problem**: Container using > 2GB memory

**Solutions**:
1. Check connection pool size
2. Limit cache size
3. Reduce worker pool size
4. Check for memory leaks in logs
5. Restart container

### Health Checks Failing

**Problem**: Provider marked unhealthy

**Solutions**:
1. Check health check endpoint is correct
2. Verify network connectivity to provider
3. Check credentials are valid
4. Review provider status page
5. Adjust health check timeout

## Best Practices

### Deployment

- [ ] Use docker-compose for easy management
- [ ] Enable health checks
- [ ] Set appropriate resource limits
- [ ] Configure logging rotation
- [ ] Enable metrics collection
- [ ] Set up alerting

### Monitoring

- [ ] Monitor error rate (target < 1%)
- [ ] Monitor P95 latency (target < 2s)
- [ ] Monitor token usage
- [ ] Track cooldown events
- [ ] Review audit logs daily
- [ ] Set up Grafana dashboards

### Scaling

- [ ] Use multiple providers for redundancy
- [ ] Enable load balancing
- [ ] Configure connection pooling
- [ ] Set up active-active failover
- [ ] Monitor resource usage
- [ ] Scale horizontally as needed

### Backup

- [ ] Daily automated backups
- [ ] Test restore procedure
- [ ] Store backups off-site
- [ ] Encrypt sensitive data
- [ ] Document recovery process
- [ ] Regular disaster recovery drills

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

**Cooldown Status**
```http
GET /v0/operations/cooldown/status
```

**Force Cooldown**
```http
POST /v0/operations/providers/{provider}/cooldown
```

**Force Recovery**
```http
POST /v0/operations/providers/{provider}/recover
```

**Load Balancing Status**
```http
GET /v0/operations/load_balancing/status
```

## Next Steps

- See [SPEC.md](./SPEC.md) for technical operations details
- See [../security/](../security/) for security operations
- See [../../api/](../../api/) for API documentation


---
