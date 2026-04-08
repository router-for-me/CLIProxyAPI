# Metrics Persistence Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Persist in-memory request statistics to a JSON file so metrics survive server restarts.

**Architecture:** Create a `MetricsPersister` wrapper around `RequestStatistics` that auto-saves on a configurable interval and on graceful shutdown, loading existing data on startup. Uses atomic writes to prevent corruption.

**Tech Stack:** Go standard library (os, encoding/json, sync, time), logrus for logging

---

## Chunk 1: Core Persistence Logic

### Task 1: MetricsPersister struct and SaveToFile

**Files:**

- Create: `internal/usage/metrics_persister.go`
- Test: `internal/usage/metrics_persister_test.go`

- [ ] **Step 1: Write tests for SaveToFile and LoadFromFile**

```go
// internal/usage/metrics_persister_test.go
package usage

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "metrics.json")

	stats := NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "test-key",
		Model:       "test-model",
		RequestedAt: time.Now(),
		Detail:      coreusage.Detail{TotalTokens: 100, InputTokens: 50, OutputTokens: 50},
	})

	persister := &MetricsPersister{stats: stats, filePath: filePath}
	if err := persister.SaveToFile(); err != nil {
		t.Fatalf("SaveToFile() error = %v", err)
	}

	if _, err := os.Stat(filePath); err != nil {
		t.Fatalf("metrics file not created: %v", err)
	}

	newStats := NewRequestStatistics()
	newPersister := &MetricsPersister{stats: newStats, filePath: filePath}
	if err := newPersister.LoadFromFile(); err != nil {
		t.Fatalf("LoadFromFile() error = %v", err)
	}

	snap := newStats.Snapshot()
	if snap.TotalRequests != 1 {
		t.Errorf("expected 1 request, got %d", snap.TotalRequests)
	}
	if snap.TotalTokens != 100 {
		t.Errorf("expected 100 tokens, got %d", snap.TotalTokens)
	}
	if len(snap.APIs) != 1 {
		t.Errorf("expected 1 API key in snapshot, got %d", len(snap.APIs))
	}
	if apiSnap, ok := snap.APIs["test-key"]; !ok {
		t.Error("expected test-key in APIs")
	} else if apiSnap.TotalTokens != 100 {
		t.Errorf("expected 100 tokens for test-key, got %d", apiSnap.TotalTokens)
	}
	if len(snap.RequestsByDay) != 1 {
		t.Errorf("expected 1 day entry, got %d", len(snap.RequestsByDay))
	}
	if len(snap.RequestsByHour) != 1 {
		t.Errorf("expected 1 hour entry, got %d", len(snap.RequestsByHour))
	}
}

func TestLoadNonExistentFile(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "nonexistent.json")

	stats := NewRequestStatistics()
	persister := &MetricsPersister{stats: stats, filePath: filePath}
	err := persister.LoadFromFile()
	if err != nil {
		t.Errorf("expected no error for missing file, got %v", err)
	}
}

func TestLoadCorruptFile(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "metrics.json")
	if err := os.WriteFile(filePath, []byte("not json"), 0644); err != nil {
		t.Fatal(err)
	}

	stats := NewRequestStatistics()
	persister := &MetricsPersister{stats: stats, filePath: filePath}
	err := persister.LoadFromFile()
	if err == nil {
		t.Error("expected error for corrupt JSON")
	}
}

func TestAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "metrics.json")

	// Create initial valid file
	stats := NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "initial-key",
		Model:       "test-model",
		RequestedAt: time.Now(),
		Detail:      coreusage.Detail{TotalTokens: 42},
	})
	persister := &MetricsPersister{stats: stats, filePath: filePath}
	if err := persister.SaveToFile(); err != nil {
		t.Fatal(err)
	}

	// Read original content
	originalData, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}

	// Make dir unwritable to force SaveToFile to fail
	if err := os.Chmod(dir, 0000); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(dir, 0755) }()

	// This should fail
	err = persister.SaveToFile()
	if err == nil {
		t.Error("expected SaveToFile to fail on unwritable dir")
	}

	// Verify original file is still intact
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("original file should still exist after failed save: %v", err)
	}
	if string(data) != string(originalData) {
		t.Error("original file content changed after failed atomic write")
	}
}

func TestAutoSaveTick(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "metrics.json")

	stats := NewRequestStatistics()
	persister := NewMetricsPersister(stats, filePath)

	// Start with 50ms interval for fast test
	persister.Start(50 * time.Millisecond)

	// Record a request
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "test-key",
		Model:       "test-model",
		RequestedAt: time.Now(),
		Detail:      coreusage.Detail{TotalTokens: 50},
	})

	// Poll for file creation instead of fixed sleep
	deadline := time.After(2 * time.Second)
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	var saved bool
	for !saved {
		select {
		case <-tick.C:
			if _, err := os.Stat(filePath); err == nil {
				saved = true
			}
		case <-deadline:
			t.Fatal("metrics file not created within 2s")
		}
	}

	persister.Stop()

	snap := stats.Snapshot()
	if snap.TotalRequests != 1 {
		t.Errorf("expected 1 request, got %d", snap.TotalRequests)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/usage/ -run "TestSaveAndLoad|TestLoadNonExistentFile|TestLoadCorruptFile" -v
```

Expected: FAIL — MetricsPersister type does not exist yet.

- [ ] **Step 3: Implement MetricsPersister with SaveToFile and LoadFromFile**

```go
// internal/usage/metrics_persister.go
package usage

import (
	"encoding/json"
	"os"
	"path/filepath"

	log "github.com/sirupsen/logrus"
)

// MetricsPersister handles saving and loading request statistics to/from a JSON file.
type MetricsPersister struct {
	stats    *RequestStatistics
	filePath string
}

// NewMetricsPersister creates a new MetricsPersister that stores data at the given path.
func NewMetricsPersister(stats *RequestStatistics, filePath string) *MetricsPersister {
	return &MetricsPersister{
		stats:    stats,
		filePath: filePath,
	}
}

// SaveToFile writes the current statistics snapshot to disk using atomic writes.
func (p *MetricsPersister) SaveToFile() error {
	if p == nil || p.stats == nil {
		return nil
	}

	snapshot := p.stats.Snapshot()
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}

	dir := filepath.Dir(p.filePath)
	tmp, err := os.CreateTemp(dir, "metrics-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()

	_, writeErr := tmp.Write(data)
	closeErr := tmp.Close()

	if writeErr != nil || closeErr != nil {
		_ = os.Remove(tmpName)
		if writeErr != nil {
			return writeErr
		}
		return closeErr
	}

	if err := os.Rename(tmpName, p.filePath); err != nil {
		_ = os.Remove(tmpName)
		return err
	}

	return nil
}

// LoadFromFile reads a previously saved statistics snapshot from disk.
func (p *MetricsPersister) LoadFromFile() error {
	if p == nil || p.stats == nil {
		return nil
	}

	data, err := os.ReadFile(p.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			log.WithField("path", p.filePath).Debug("no metrics file found, starting fresh")
			return nil
		}
		return err
	}

	var snapshot StatisticsSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		log.WithError(err).WithField("path", p.filePath).Warn("corrupt metrics file, skipping load")
		return err
	}

	p.stats.MergeSnapshot(&snapshot)
	log.WithField("path", p.filePath).Info("loaded metrics from file")
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/usage/ -run "TestSaveAndLoad|TestLoadNonExistentFile|TestLoadCorruptFile" -v
```

Expected: All 3 tests PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w .
git add internal/usage/metrics_persister.go internal/usage/metrics_persister_test.go
git commit -m "feat: add MetricsPersister for save/load statistics to JSON"
```

---

## Chunk 2: AutoSave Lifecycle (Start/Stop/Ticker)

### Task 2: Start, Stop, and auto-save ticker

**Files:**

- Modify: `internal/usage/metrics_persister.go`
- Test: `internal/usage/metrics_persister_test.go`

- [ ] **Step 1: Write tests for Start/Stop lifecycle**

```go
// Add to metrics_persister_test.go

func TestAutoSaveTick(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "metrics.json")

	stats := NewRequestStatistics()
	persister := NewMetricsPersister(stats, filePath)

	// Start with 100ms interval for fast test
	persister.Start(100 * time.Millisecond)

	// Record a request
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "test-key",
		Model:       "test-model",
		RequestedAt: time.Now(),
		Detail:      coreusage.Detail{TotalTokens: 50},
	})

	// Wait for at least one tick to fire
	time.Sleep(250 * time.Millisecond)

	// Verify file was created
	if _, err := os.Stat(filePath); err != nil {
		t.Fatalf("expected metrics file after tick, got error: %v", err)
	}

	persister.Stop()

	// Verify final save
	snap := stats.Snapshot()
	if snap.TotalRequests != 1 {
		t.Errorf("expected 1 request, got %d", snap.TotalRequests)
	}
}

func TestStopSavesOnShutdown(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "metrics.json")

	stats := NewRequestStatistics()
	persister := NewMetricsPersister(stats, filePath)

	persister.Start(1 * time.Hour) // Long interval, won't fire

	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "test-key",
		Model:       "test-model",
		RequestedAt: time.Now(),
		Detail:      coreusage.Detail{TotalTokens: 200},
	})

	persister.Stop()

	// Stop should trigger final save
	if _, err := os.Stat(filePath); err != nil {
		t.Fatalf("expected metrics file after Stop(), got error: %v", err)
	}

	// Verify content
	newStats := NewRequestStatistics()
	newPersister := NewMetricsPersister(newStats, filePath)
	if err := newPersister.LoadFromFile(); err != nil {
		t.Fatalf("LoadFromFile() error = %v", err)
	}

	snap := newStats.Snapshot()
	if snap.TotalRequests != 1 {
		t.Errorf("expected 1 request after reload, got %d", snap.TotalRequests)
	}
	if snap.TotalTokens != 200 {
		t.Errorf("expected 200 tokens after reload, got %d", snap.TotalTokens)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/usage/ -run "TestAutoSaveTick|TestStopSavesOnShutdown" -v
```

Expected: FAIL — Start/Stop methods don't exist yet.

- [ ] **Step 3: Implement Start and Stop methods**

Add to `internal/usage/metrics_persister.go`:

```go
import (
	"sync"
	"time"
	// ... existing imports
)

// MetricsPersister handles saving and loading request statistics to/from a JSON file.
type MetricsPersister struct {
	stats      *RequestStatistics
	filePath   string
	mu         sync.Mutex
	stopCh     chan struct{}
	saveTicker *time.Ticker
}

// Start loads existing metrics from disk and begins periodic auto-save.
func (p *MetricsPersister) Start(interval time.Duration) {
	if p == nil {
		return
	}

	// Load existing data
	if err := p.LoadFromFile(); err != nil {
		log.WithError(err).Warn("failed to load metrics on startup")
	}

	p.stopCh = make(chan struct{})
	p.saveTicker = time.NewTicker(interval)

	go p.runAutoSave()
}

// Stop halts the periodic auto-save and performs a final flush to disk.
func (p *MetricsPersister) Stop() {
	if p == nil {
		return
	}

	p.mu.Lock()
	if p.saveTicker != nil {
		p.saveTicker.Stop()
		p.saveTicker = nil
	}
	if p.stopCh != nil {
		close(p.stopCh)
		p.stopCh = nil
	}
	p.mu.Unlock()

	// Final flush
	if err := p.SaveToFile(); err != nil {
		log.WithError(err).Error("failed to save metrics on shutdown")
	} else {
		log.Info("metrics saved on shutdown")
	}
}

func (p *MetricsPersister) runAutoSave() {
	defer func() {
		if r := recover(); r != nil {
			log.WithField("panic", r).Error("metrics auto-save panicked")
		}
	}()

	for {
		select {
		case <-p.saveTicker.C:
			if err := p.SaveToFile(); err != nil {
				log.WithError(err).Warn("failed to auto-save metrics")
			}
		case <-p.stopCh:
			return
		}
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/usage/ -run "TestAutoSaveTick|TestStopSavesOnShutdown" -v
```

Expected: All tests PASS.

- [ ] **Step 5: Run all usage tests**

```bash
go test ./internal/usage/ -v
```

Expected: All tests PASS (existing + new).

- [ ] **Step 6: Commit**

```bash
gofmt -w .
git add internal/usage/metrics_persister.go internal/usage/metrics_persister_test.go
git commit -m "feat: add Start/Stop auto-save lifecycle to MetricsPersister"
```

---

## Chunk 3: Config and Wire-Up

### Task 3: Add MetricsPersistenceConfig to Config struct

**Files:**

- Modify: `internal/config/config.go` (add struct + field to Config)
- Modify: `config.example.yaml` (add example section)

- [ ] **Step 1: Add MetricsPersistenceConfig struct and field**

In `internal/config/config.go`, find `UsageStatisticsEnabled` field (around line 66) and add after it:

```go
// MetricsPersistence controls automatic persistence of usage statistics.
MetricsPersistence MetricsPersistenceConfig `yaml:"metrics-persistence" json:"metrics-persistence"`
```

Add the struct definition near the bottom of the file, alongside other config structs:

```go
// MetricsPersistenceConfig controls automatic persistence of usage statistics.
type MetricsPersistenceConfig struct {
	Enabled             bool `yaml:"enabled" json:"enabled"`
	SaveIntervalSeconds int  `yaml:"save-interval-seconds" json:"save-interval-seconds"`
}
```

- [ ] **Step 2: Add to config.example.yaml**

Find `usage-statistics-enabled:` line (line 67) and add after it:

```yaml
# Metrics persistence automatically saves usage statistics to disk.
# When enabled, metrics are saved periodically and on graceful shutdown.
metrics-persistence:
  enabled: true
  save-interval-seconds: 300
```

- [ ] **Step 3: Run build to verify no compile errors**

```bash
go build -o /dev/null ./cmd/server
```

Expected: Clean build, no errors.

- [ ] **Step 4: Commit**

```bash
gofmt -w .
git add internal/config/config.go config.example.yaml
git commit -m "feat: add metrics-persistence config options"
```

---

## Chunk 4: Wire Up in Server Lifecycle

### Task 4: Integrate MetricsPersister into cmd/server/main.go and internal/cmd/run.go

**Files:**

- Modify: `internal/cmd/run.go` (wire persister into StartService and StartServiceBackground)

- [ ] **Step 1: Wire persister into StartService**

In `internal/cmd/run.go`, modify `StartService`:

```go
func StartService(cfg *config.Config, configPath string, localPassword string) {
	builder := cliproxy.NewBuilder().
		WithConfig(cfg).
		WithConfigPath(configPath).
		WithLocalManagementPassword(localPassword)

	ctxSignal, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Setup metrics persistence
	persister := setupMetricsPersistence(cfg, configPath)
	if persister != nil {
		defer persister.Stop()
	}

	runCtx := ctxSignal
	if localPassword != "" {
		var keepAliveCancel context.CancelFunc
		runCtx, keepAliveCancel = context.WithCancel(ctxSignal)
		builder = builder.WithServerOptions(api.WithKeepAliveEndpoint(10*time.Second, func() {
			log.Warn("keep-alive endpoint idle for 10s, shutting down")
			keepAliveCancel()
		}))
	}

	service, err := builder.Build()
	if err != nil {
		log.Errorf("failed to build proxy service: %v", err)
		return
	}

	err = service.Run(runCtx)
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Errorf("proxy service exited with error: %v", err)
	}
}
```

- [ ] **Step 2: Wire persister into StartServiceBackground**

```go
func StartServiceBackground(cfg *config.Config, configPath string, localPassword string) (cancel func(), done <-chan struct{}) {
	builder := cliproxy.NewBuilder().
		WithConfig(cfg).
		WithConfigPath(configPath).
		WithLocalManagementPassword(localPassword)

	ctx, cancelFn := context.WithCancel(context.Background())
	doneCh := make(chan struct{})

	service, err := builder.Build()
	if err != nil {
		log.Errorf("failed to build proxy service: %v", err)
		close(doneCh)
		return cancelFn, doneCh
	}

	// Setup metrics persistence only after successful build
	persister := setupMetricsPersistence(cfg, configPath)

	go func() {
		defer func() {
			if persister != nil {
				persister.Stop()
			}
			close(doneCh)
		}()
		if err := service.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Errorf("proxy service exited with error: %v", err)
		}
	}()

	return cancelFn, doneCh
}
```

- [ ] **Step 3: Add setupMetricsPersistence helper**

Add at the bottom of `internal/cmd/run.go`:

```go
func setupMetricsPersistence(cfg *config.Config, configPath string) *usage.MetricsPersister {
	if cfg == nil {
		return nil
	}

	mp := cfg.MetricsPersistence
	if !mp.Enabled {
		log.Info("metrics persistence disabled")
		return nil
	}

	configDir := filepath.Dir(configPath)
	metricsPath := filepath.Join(configDir, "metrics.json")

	interval := time.Duration(mp.SaveIntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 5 * time.Minute
	}

	persister := usage.NewMetricsPersister(usage.GetRequestStatistics(), metricsPath)
	persister.Start(interval)

	log.WithField("interval", interval).WithField("path", metricsPath).Info("metrics persistence enabled")
	return persister
}
```

- [ ] **Step 4: Add missing imports**

Ensure `internal/cmd/run.go` imports include:

```go
import (
	"path/filepath"
	// ... existing imports
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
)
```

- [ ] **Step 5: Build and verify**

```bash
go build -o /dev/null ./cmd/server
```

Expected: Clean build.

- [ ] **Step 6: Run full test suite**

```bash
go test ./...
```

Expected: All tests pass.

- [ ] **Step 7: Commit**

```bash
gofmt -w .
git add internal/cmd/run.go
git commit -m "feat: wire MetricsPersister into server lifecycle"
```

---

## Chunk 5: Verification

### Task 5: End-to-end manual verification

- [ ] **Step 1: Build the server**

```bash
go build -o cli-proxy-api ./cmd/server
```

- [ ] **Step 2: Start server with a config**

```bash
./cli-proxy-api --config config.yaml &
SERVER_PID=$!
```

- [ ] **Step 3: Make a few requests (or wait for existing traffic)**

- [ ] **Step 4: Check metrics.json exists**

```bash
ls -la metrics.json
cat metrics.json | head -20
```

- [ ] **Step 5: Stop server gracefully**

```bash
kill -INT $SERVER_PID
wait $SERVER_PID 2>/dev/null
```

- [ ] **Step 6: Verify metrics.json has data**

```bash
cat metrics.json
```

- [ ] **Step 7: Restart server and verify metrics loaded**

```bash
./cli-proxy-api --config config.yaml &
# Check TUI or management API shows previous metrics
curl http://localhost:8317/v0/management/usage -H "Authorization: Bearer <key>"
```

- [ ] **Step 8: Clean up**

```bash
kill -INT $SERVER_PID 2>/dev/null
rm -f cli-proxy-api
```

- [ ] **Step 9: Final format check**

```bash
gofmt -w .
go build -o /dev/null ./cmd/server
```
