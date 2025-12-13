# CLI Proxy API - QuantumSpring Fork with Usage Metrics

A fork of [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) enhanced with persistent usage tracking and visualization capabilities for internal AI infrastructure monitoring at QuantumSpring.

## 🎯 Project Goals

This fork extends the original CLI Proxy to provide:

1. **Persistent Usage Statistics** - Metrics survive server restarts via SQLite storage
2. **Real-time Metrics API** - RESTful endpoints for querying aggregated usage data
3. **On-box Visualization** - Built-in web dashboard for monitoring proxy usage
4. **Security-First** - Localhost-only metrics server with optional Basic Auth

## 🚀 Quick Start

### Prerequisites

- Go 1.21+ (for building from source)
- Docker (for containerized deployment)

### Running with Docker
```bash
# Build the image
docker build -t cliproxy-metrics .

# Run with metrics enabled
docker run -d \
  -p 8080:8080 \
  -p 8081:8081 \
  -v $(pwd)/config.yaml:/app/config.yaml \
  -v $(pwd)/data:/app/data \
  -e METRICS_ENABLED=true \
  --name cliproxy \
  cliproxy-metrics

# View metrics UI
open http://localhost:8081/_qs/metrics/ui
```

### Running from Source
```bash
# Clone and build
git clone git@github.com:Ybazylbe/CLIProxyAPI-YBFork.git
cd CLIProxyAPI
go build -o cliproxy

# Configure metrics (see Configuration section)
cp config.example.yaml config.yaml
nano config.yaml

# Run
./cliproxy
```

## 📊 Metrics Configuration

Add to your `config.yaml`:
```yaml
# Enable metrics collection
metrics_enabled: true

# SQLite database path
metrics_storage_path: "./data/metrics.db"

# Metrics server bind address (localhost by default)
metrics_bind_address: "127.0.0.1:8081"

# Optional: Basic Auth protection
metrics_basic_auth_user: "admin"
metrics_basic_auth_pass: "secure_password_here"
```

### Environment Variables (Alternative)
```bash
export METRICS_ENABLED=true
export METRICS_STORAGE_PATH=/data/metrics.db
export METRICS_BIND_ADDRESS=127.0.0.1:8081
export METRICS_BASIC_AUTH_USER=admin
export METRICS_BASIC_AUTH_PASS=your_password
```

## 🔌 API Endpoints

### Health Check
```bash
GET /_qs/health
Response: {"ok": true}
```

### Metrics Data
```bash
# Get all metrics
GET /_qs/metrics

# Filter by time range
GET /_qs/metrics?from=2024-01-01T00:00:00Z&to=2024-01-31T23:59:59Z

# Filter by model
GET /_qs/metrics?model=gpt-4

# Combine filters
GET /_qs/metrics?from=2024-01-15T00:00:00Z&model=claude-sonnet-4
```

**Response Format:**
```json
{
  "totals": {
    "total_tokens": 1523450,
    "total_requests": 3847
  },
  "by_model": [
    {
      "model": "gpt-4",
      "tokens": 892340,
      "requests": 2134,
      "avg_latency_ms": 1245
    }
  ],
  "timeseries": [
    {
      "bucket_start": "2024-01-15T10:00:00Z",
      "tokens": 45230,
      "requests": 127
    }
  ]
}
```

### Metrics Dashboard
```bash
# Open web UI
GET /_qs/metrics/ui
```

## 🏗️ Architecture

### Components
```
internal/metrics/
├── metrics.go       # Core interfaces and types
├── store.go         # SQLite persistence layer
├── aggregator.go    # SQL queries for data aggregation
├── handlers.go      # HTTP API handlers
├── ui.go            # Embedded web dashboard
└── auth.go          # Basic Auth middleware
```

### Data Flow
```
Proxy Request → Service Handler → RecordUsage()
                                        ↓
                                  SQLite Store
                                        ↓
                            Metrics API ← Dashboard
```

### Collected Metrics

Each API request records:

| Field | Description | Example |
|-------|-------------|---------|
| `timestamp` | Request completion time | `2024-01-15T14:32:10Z` |
| `model` | AI model used | `gpt-4`, `claude-sonnet-4` |
| `prompt_tokens` | Input tokens | `245` |
| `completion_tokens` | Output tokens | `1823` |
| `total_tokens` | Total tokens | `2068` |
| `request_id` | Unique request ID | `req_abc123...` |
| `status` | HTTP status code | `200`, `500` |
| `latency_ms` | Request duration | `1245` |
| `api_key_hash` | SHA-256 hash of API key | `a3f2...` (anonymized) |

## 🧪 Testing
```bash
# Run all tests
go test ./internal/metrics/...

# Run with coverage
go test -cover ./internal/metrics/...

# Integration tests
go test -v ./internal/metrics/ -run TestMetricsIntegration
```

### Test Coverage

- ✅ Basic CRUD operations (`store_test.go`)
- ✅ Multi-model aggregation (`metrics_integration_test.go`)
- ✅ Time-based filtering
- ✅ Model-specific queries

## 🔒 Security

### API Key Anonymization

API keys are **never stored in plaintext**. The system uses SHA-256 hashing:
```go
// From main.go
func hashAPIKey(key string) string {
    if key == "" {
        return ""
    }
    h := sha256.Sum256([]byte(key))
    return hex.EncodeToString(h[:])
}
```

### Basic Authentication

Metrics endpoints support optional Basic Auth using constant-time comparison to prevent timing attacks:
```go
// From internal/metrics/auth.go
subtle.ConstantTimeCompare([]byte(username), []byte(configUser))
```

### Localhost Binding

By default, metrics server binds to `127.0.0.1` - accessible only from the same machine:
```yaml
metrics_bind_address: "127.0.0.1:8081"  # Local only
# metrics_bind_address: "0.0.0.0:8081"  # ⚠️ Expose to network
```

## 🤖 AI-Assisted Development

This implementation leveraged AI coding tools throughout the development process:

### Tools Used

1. **Claude Code (Sonnet 4)** - Primary development assistant
   - Architecture design and Go idioms
   - SQL query optimization
   - Test case generation

2. **GitHub Copilot** - Code completion
   - Boilerplate HTTP handlers
   - Struct field mappings

### Development Workflow
```bash
# 1. Initial architecture consultation with Claude
"Design a metrics system for Go proxy with SQLite persistence"

# 2. Iterative implementation
- Generated store.go skeleton
- Refined aggregation queries
- Added security middleware

# 3. Test generation
"Create integration tests covering time-series aggregation"

# 4. Documentation
"Write API examples for metrics endpoints"
```

### AI-Generated Components

- **SQLite Schema** (`store.go`) - Claude suggested indexes for performance
- **Time-Series Bucketing** (`aggregator.go`) - SQL `strftime()` grouping logic
- **Chart.js Integration** (`ui.go`) - Dashboard implementation with auto-refresh
- **Integration Tests** (`metrics_integration_test.go`) - Multi-scenario coverage

### Prompt Examples

<details>
<summary>View sample prompts used during development</summary>
```
1. "Create a Go interface for metrics storage with methods:
   - RecordUsage(metric)
   - GetTotals(query)
   - GetByModel(query)
   - GetTimeSeries(query)"

2. "Write SQL to aggregate tokens by hourly buckets in SQLite"

3. "Implement Basic Auth middleware using crypto/subtle for timing safety"

4. "Generate integration test that records 3 metrics across 2 models 
   and validates aggregation results"
```

</details>

## 📈 Dashboard Features

The built-in UI (`/_qs/metrics/ui`) provides:

- **KPI Cards**: Total requests, tokens, active models, 24h activity
- **Time-Series Chart**: Hourly request volume with Chart.js
- **Model Breakdown Table**: Per-model statistics with latency
- **Auto-Refresh**: Updates every 30 seconds

![Metrics Dashboard](docs/screenshots/metrics-ui.png)

## 🛠️ Implementation Notes

### Why SQLite?

- ✅ Zero-configuration persistence
- ✅ ACID compliance for data integrity
- ✅ Embedded - no separate database process
- ✅ Sufficient for single-node deployments
- ⚠️ Not suitable for distributed systems (consider PostgreSQL for multi-instance)

### Performance Considerations

1. **Indexes**: Created on `timestamp`, `model`, `api_key_hash` for fast queries
2. **Batch Writes**: Consider buffering metrics for high-throughput scenarios
3. **Data Retention**: Implement cleanup for metrics older than N days

### Future Enhancements

- [ ] Prometheus metrics export
- [ ] Per-API-key quota enforcement
- [ ] Anomaly detection (spike in errors, unusual latency)
- [ ] Export to CSV/JSON
- [ ] Cost estimation based on token prices

## 📝 Original Project

This is a fork of [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI).

For original features (OAuth flows, model routing, multi-account support), see:
- [Original README](README_ORIGINAL.md)
- [Upstream Docs](https://help.router-for.me/)

## 🤝 Contributing

Improvements welcome! Focus areas:

1. **Metric Collection Hooks** - Integration with additional proxy handlers
2. **Dashboard Enhancements** - Filters, date pickers, export buttons
3. **Storage Backends** - PostgreSQL, ClickHouse adapters
4. **Alerting** - Webhook notifications for quota thresholds

## 📄 License

MIT License (inherited from upstream CLIProxyAPI)

---

**Built for QuantumSpring's internal AI infrastructure monitoring**

Questions? Check [METRICS_GUIDE.md](docs/METRICS_GUIDE.md) or open an issue