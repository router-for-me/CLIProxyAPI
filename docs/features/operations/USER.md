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
