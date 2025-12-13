# Metrics System Implementation Guide

## Overview

This document explains the metrics collection system added to CLIProxyAPI, designed for QuantumSpring's internal monitoring needs.

## How Metrics Are Collected

### 1. Request Interception

Metrics collection happens in `internal/cmd/service.go`:
```go
// After completing proxy request to LLM API
if metricsStore := metrics.GetGlobalMetricsStore(); metricsStore != nil {
    metric := metrics.UsageMetric{
        Timestamp:        time.Now(),
        Model:            modelName,
        PromptTokens:     usage.PromptTokens,
        CompletionTokens: usage.CompletionTokens,
        TotalTokens:      usage.TotalTokens,
        RequestID:        requestID,
        Status:           fmt.Sprintf("%d", statusCode),
        LatencyMs:        latency.Milliseconds(),
        APIKeyHash:       hashAPIKey(apiKey),
    }
    
    _ = metricsStore.RecordUsage(ctx, metric)
}
```

### 2. Storage Flow
```
HTTP Request → Proxy Handler → LLM API
                    ↓
              Extract Usage
                    ↓
          metrics.RecordUsage()
                    ↓
            SQLite INSERT
```

### 3. Query Flow
```
GET /_qs/metrics → handlers.go → aggregator.go → SQLite SELECT
                                        ↓
                                  JSON Response
```

## Database Schema
```sql
CREATE TABLE usage_metrics (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp DATETIME NOT NULL,           -- When request completed
    model TEXT NOT NULL,                   -- e.g., "gpt-4"
    prompt_tokens INTEGER,                 -- Input token count
    completion_tokens INTEGER,             -- Output token count
    total_tokens INTEGER,                  -- prompt + completion
    request_id TEXT,                       -- Unique ID from LLM API
    status TEXT,                           -- HTTP status ("200", "429")
    latency_ms INTEGER,                    -- Request duration
    api_key_hash TEXT,                     -- SHA-256 of API key
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_timestamp ON usage_metrics(timestamp);
CREATE INDEX idx_model ON usage_metrics(model);
CREATE INDEX idx_api_key ON usage_metrics(api_key_hash);
```

## API Usage Examples

### Basic Queries
```bash
# Get all-time totals
curl http://localhost:8081/_qs/metrics

# Last 24 hours
FROM=$(date -u -d '24 hours ago' +%Y-%m-%dT%H:%M:%SZ)
TO=$(date -u +%Y-%m-%dT%H:%M:%SZ)
curl "http://localhost:8081/_qs/metrics?from=$FROM&to=$TO"

# Specific model
curl "http://localhost:8081/_qs/metrics?model=gpt-4"

# With Basic Auth
curl -u admin:password http://localhost:8081/_qs/metrics
```

### Response Interpretation
```json
{
  "totals": {
    "total_tokens": 1500000,     // Sum of all tokens processed
    "total_requests": 5000        // Total API calls made
  },
  "by_model": [
    {
      "model": "gpt-4",
      "tokens": 900000,            // 60% of traffic
      "requests": 2500,
      "avg_latency_ms": 1200       // Average response time
    },
    {
      "model": "claude-sonnet-4",
      "tokens": 600000,            // 40% of traffic
      "requests": 2500,
      "avg_latency_ms": 800
    }
  ],
  "timeseries": [
    {
      "bucket_start": "2024-01-15T10:00:00Z",
      "tokens": 50000,             // Tokens in 10:00-11:00 window
      "requests": 125
    }
    // ... hourly buckets
  ]
}
```

## Configuration Options

### Complete Config Block
```yaml
# Main proxy configuration
port: 8080
host: "0.0.0.0"

# Metrics system
metrics_enabled: true
metrics_storage_path: "./data/metrics.db"
metrics_bind_address: "127.0.0.1:8081"
metrics_basic_auth_user: ""      # Empty = no auth
metrics_basic_auth_pass: ""

# Other settings
usage_statistics_enabled: true   # Must be true for metrics
logging_to_file: false
```

### Environment Variable Priority

Environment variables override `config.yaml`:
```bash
METRICS_ENABLED=true              # Enables collection
METRICS_STORAGE_PATH=/data/metrics.db
METRICS_BIND_ADDRESS=0.0.0.0:8081
METRICS_BASIC_AUTH_USER=admin
METRICS_BASIC_AUTH_PASS=secret
```

## Troubleshooting

### No Data in Dashboard

**Symptom:** Dashboard shows 0 requests

**Checks:**
1. Verify `metrics_enabled: true` in config
2. Check proxy is receiving requests: `tail -f logs/proxy.log`
3. Query database directly:
```bash
   sqlite3 data/metrics.db "SELECT COUNT(*) FROM usage_metrics;"
```
4. Check metrics server is running: `curl http://localhost:8081/_qs/health`

### High Database Size

**Symptom:** `metrics.db` grows to GB scale

**Solution:** Implement periodic cleanup:
```bash
# Delete metrics older than 90 days
sqlite3 data/metrics.db "DELETE FROM usage_metrics WHERE timestamp < datetime('now', '-90 days');"

# Vacuum to reclaim space
sqlite3 data/metrics.db "VACUUM;"
```

**Automated cleanup** (add to cron):
```bash
0 2 * * * sqlite3 /app/data/metrics.db "DELETE FROM usage_metrics WHERE timestamp < datetime('now', '-90 days'); VACUUM;"
```

### Metrics Server Won't Start

**Error:** `bind: address already in use`

**Solution:** Change port in config:
```yaml
metrics_bind_address: "127.0.0.1:8082"
```

### Basic Auth Not Working

**Issue:** Still prompted for credentials after configuring

**Check:**
1. Both user AND password must be set
2. Restart proxy after config change
3. Test with curl:
```bash
   curl -v -u admin:password http://localhost:8081/_qs/metrics
```

## Performance Tuning

### For High-Traffic Deployments

1. **Batch Inserts**: Buffer metrics and insert in batches
```go
   // Accumulate 100 metrics
   if len(buffer) >= 100 {
       tx, _ := db.Begin()
       for _, m := range buffer {
           // INSERT ...
       }
       tx.Commit()
   }
```

2. **Separate Database File**: Use SSD storage
```yaml
   metrics_storage_path: "/mnt/ssd/metrics.db"
```

3. **Async Writes**: Don't block proxy requests
```go
   go func() {
       metricsStore.RecordUsage(ctx, metric)
   }()
```

## Migration to PostgreSQL

For multi-instance deployments, switch to PostgreSQL:

1. Modify `store.go` to implement `pq` driver
2. Update schema for PostgreSQL syntax
3. Add connection pooling
4. Use TimescaleDB for time-series optimization

## AI Tool Integration

### How AI Assisted This Implementation

**Architecture Phase:**
```
Prompt: "Design a Go metrics system with:
- SQLite persistence
- Hourly aggregation
- REST API
- Basic Auth security"

Claude Response: <suggested interface-based design>
```

**Implementation Phase:**
```
Prompt: "Generate SQL to group metrics by hour in SQLite"

Output: strftime('%Y-%m-%d %H:00:00', timestamp)
```

**Testing Phase:**
```
Prompt: "Create integration test for time-series aggregation with 3 metrics across 2 models"

Generated: metrics_integration_test.go (entire file)
```

### Recommended Workflow

1. **Design with AI**: Ask for architecture patterns
2. **Implement iteratively**: Generate small, testable functions
3. **Validate with tests**: AI excels at test generation
4. **Refine manually**: Review AI output for Go idioms

## License

MIT (inherited from CLIProxyAPI)