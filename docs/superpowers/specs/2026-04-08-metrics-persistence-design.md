# Metrics Persistence Design

**Date:** 2026-04-08
**Status:** Draft

## Problem

Metrics are stored in-memory via `RequestStatistics` (`internal/usage/logger_plugin.go`). All accumulated data (request counts, token counts, time-series, per-request details) is lost on every restart. Manual export/import API exists but requires operator intervention.

## Requirements

- Persist metrics to JSON file in same directory as `config.yaml`
- Auto-load on startup (if file exists)
- Auto-save on graceful shutdown
- Periodic save every 5 minutes (configurable) to protect against crashes
- Thread-safe, no impact on request performance
- Config toggle to enable/disable

## Architecture

### New File: `internal/usage/metrics_persister.go`

```go
type MetricsPersister struct {
    stats      *RequestStatistics
    filePath   string
    mu         sync.Mutex
    stopCh     chan struct{}
    saveTicker *time.Ticker
    enabled    bool
}
```

### Methods

| Method | Purpose |
|---|---|
| `NewMetricsPersister(stats *RequestStatistics, configDir string)` | Create persister, compute file path (`configDir/metrics.json`) |
| `Start(interval time.Duration)` | Load existing file, start ticker goroutine |
| `Stop()` | Stop ticker, final flush |
| `SaveToFile() error` | Snapshot → Marshal → Atomic write |
| `LoadFromFile() error` | Read → Unmarshal → MergeSnapshot |

### Config (`config.yaml`)

```yaml
metrics-persistence:
  enabled: true
  save-interval-seconds: 300
```

Config struct in `internal/config/config.go`:

```go
type MetricsPersistenceConfig struct {
    Enabled             bool `yaml:"enabled"`
    SaveIntervalSeconds int  `yaml:"save-interval-seconds"`
}
```

Defaults: `enabled: true`, `save-interval-seconds: 300`

## Data Flow

### Startup

```
cmd/server/main.go
  ├── Load config
  ├── Init RequestStatistics
  ├── NewMetricsPersister(stats, configDir)
  └── persister.Start(interval)
        ├── Load metrics.json (if exists)
        │     └── stats.MergeSnapshot(imported)
        └── Start ticker goroutine
```

### Running

```
Usage record → stats.Record() (in-memory, unchanged)
Ticker fires → SaveToFile()
  ├── stats.Snapshot()
  ├── Marshal JSON
  └── Atomic write (write .tmp → rename → metrics.json)
```

### Shutdown

```
persister.Stop()
  ├── Stop ticker
  └── SaveToFile() (final flush)
```

## Error Handling

| Scenario | Behavior |
|---|---|
| File not found on load | Log warning, skip, treat as first run |
| Write fails | Log error, don't panic, retry next tick |
| Corrupt JSON file | Log error, skip merge, keep empty stats |
| Ticker goroutine panic | Recover, log, stop gracefully |

No `log.Fatal` or `panic` — follow project convention.

## Atomic Write Pattern

Use `os.CreateTemp` + `os.Rename` to prevent corruption on crash mid-write:

```go
tmp, err := os.CreateTemp(dir, "metrics-*.tmp")
// write JSON to tmp
tmp.Close()
os.Rename(tmp.Name(), m.filePath)
```

## Testing (`internal/usage/metrics_persister_test.go`)

1. `TestSaveAndLoad` — Save stats with data → Load into new stats → Verify equality
2. `TestLoadNonExistentFile` — Load missing file → No panic, empty stats
3. `TestLoadCorruptFile` — Write invalid JSON → Load → Log error, skip, empty stats
4. `TestAtomicWrite` — Simulate crash mid-write → Old file unchanged
5. `TestAutoSaveTick` — Mock ticker → Verify SaveToFile called

## Integration (`cmd/server/main.go`)

```go
var persister *usage.MetricsPersister
if cfg.MetricsPersistence.Enabled {
    configDir := filepath.Dir(configPath)
    persister = usage.NewMetricsPersister(stats, configDir)
    interval := time.Duration(cfg.MetricsPersistence.SaveIntervalSeconds) * time.Second
    go persister.Start(interval)
    defer persister.Stop()
}
```

## Files Changed

| File | Change |
|---|---|
| `internal/usage/metrics_persister.go` | New |
| `internal/usage/metrics_persister_test.go` | New |
| `internal/config/config.go` | Add `MetricsPersistenceConfig` struct and field |
| `config.example.yaml` | Add `metrics_persistence` section |
| `cmd/server/main.go` | Wire up persister lifecycle |
| `docs/superpowers/specs/2026-04-08-metrics-persistence-design.md` | This file |
